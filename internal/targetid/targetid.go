// Package targetid 为「扫描条目」与「计划条目」生成稳定标识。
//
// 为什么需要它：④ 的决策（歧义怎么处置、PATH 待确认项落到哪）必须引用一个
// **稳定**的键。显示名会重复（真实机器上同名不同版本比比皆是），数组下标会错位
// （决策本身就会让 plan 重算，重算后顺序可能变）。只有从条目内容算出来的哈希
// 同时满足：换机不变、重跑不变、换顺序不变。
//
// 这个 id **不落盘、不进 schema**——它完全可以从条目内容重算出来，存进配置
// 反而会多一个会过期的事实源。
package targetid

import (
	"crypto/sha1"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/Elysia4319/envlift/internal/config"
)

// 条目类别。
const (
	KindSoftware = "software"
	KindPath     = "path"
	// KindRuntime 是运行时的类别。
	//
	// B2 原表只写了 software / path，但运行时条目（python / node …）同样会落到
	// manual 或待确认，④ 的决策也要能引用它们。因此扩一类，公式不变——
	// 扩展点在这里明确记一笔，避免以后有人以为漏了。
	KindRuntime = "runtime"
)

// idLen 是截断长度。12 个十六进制字符（48 bit）对上万个条目的场景绰绰有余，
// 而完整 40 位在界面和日志里都太吵。
const idLen = 12

// Key 是参与哈希的全部字段。
//
// 字段顺序固定为 kind|publisher|name|version|scope|value，缺哪个就传空串——
// 拼接方式变了 id 就全变，所以这个顺序是契约，不是实现细节。
//
// 「缺失」与「空串」必须等价（A1）：publisher / install_location 在不同来源下
// 会缺失（winget 源常有 publisher 没有 install_location，App Paths 有路径没版本），
// 若 nil 与 "" 算出不同的 id，同一条目换个来源就会变成另一个条目，决策立刻失效。
// 这里统一先 deref 成 "" 再参与哈希。
type Key struct {
	Kind      string
	Publisher string
	Name      string
	Version   string
	// Scope 区分同名同版本但不同范围的条目：软件用 hive/arch_view，PATH 用 user/machine。
	Scope string
	// Value 是「落点」：软件用 install_location，PATH 用路径值本身。
	Value string
}

// ID 返回 sha1(encode(kind|publisher|name|version|scope|value))[:12]。
func (k Key) ID() string {
	joined := encodeFields([]string{
		normalize(k.Kind),
		normalize(k.Publisher),
		normalize(k.Name),
		normalize(k.Version),
		normalize(k.Scope),
		normalizeValue(k.Value),
	})
	sum := sha1.Sum([]byte(joined))
	return hex.EncodeToString(sum[:])[:idLen]
}

// encodeFields 用「长度:值」逐段编码后再以 | 连接。
//
// 只把字段用 | 拼起来是不够的：name="a|b"、version="" 与 name="a"、version="b"
// 都会拼成 "a|b"，两个不同条目撞成同一个 id（A4 明确要求这一组反例不碰撞）。
// 长度前缀让字段边界不再依赖分隔符本身，分隔符只负责可读性。
func encodeFields(fields []string) string {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return b.String()
}

// Software 按 config.Software 生成 id。
//
// scope 用 hive/arch_view、value 用 install_location：同名的两个条目若装在不同根键
// 或不同架构视图下，就是两条不同的东西，必须得到不同 id。
func Software(sw config.Software) string {
	return Key{
		Kind:      KindSoftware,
		Publisher: deref(sw.Publisher),
		Name:      sw.Name,
		Version:   deref(sw.Version),
		Scope:     normalize(deref(sw.Hive)) + "/" + normalize(deref(sw.ArchView)),
		Value:     deref(sw.InstallLocation),
	}.ID()
}

// Path 按 PATH 条目生成 id。section 是 user / machine（展开前后值可能相同，
// 所以段别必须进哈希）。
func Path(section, value string) string {
	return Key{
		Kind:  KindPath,
		Scope: section,
		Value: value,
	}.ID()
}

// Runtime 按 config.Runtime 生成 id。
//
// A2：runtime 没有 publisher / install_location，这两格恒为空，于是「fnm 托管的
// node 21」与「系统安装的 node 21」在 kind/name/version 上完全相同——只用这三项
// 会算出同一个 id，而它们是两个不同的安装动作（前者该走 fnm，后者该走 winget），
// 决策会串台。因此 scope 这一格必须带上「谁在管」：
//
//	managed:<manager>（如 managed:fnm、managed:conda）  —— 由版本管理器托管
//	system                                            —— 非托管，系统/包管理器装的
//
// value 仍放实际解析到的可执行文件：同一个管理器下的两份同版本安装（不同路径）
// 也应算两个条目。
func Runtime(rt config.Runtime) string {
	return Key{
		Kind:    KindRuntime,
		Name:    rt.Name,
		Version: deref(rt.Version),
		Scope:   RuntimeScope(rt),
		Value:   deref(rt.Executable),
	}.ID()
}

// RuntimeScope 返回 runtime 的 scope 取值域：managed:<manager> 或 system。
//
// 取值域是契约：写成别的前缀（如裸的 manager 名）会让历史决策静默失配。
func RuntimeScope(rt config.Runtime) string {
	if m := normalize(deref(rt.Manager)); m != "" {
		return "managed:" + m
	}
	return "system"
}

// normalize 只做「显然等价」的折叠：去空白、小写、合并连续空格。
//
// 刻意不复用 match.Normalize：那一套会剥版本号与年份（为了匹配 DisplayName），
// 用在这里会让 1.2.3 与 1.2.4 算出同一个 id。
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// normalizeValue 额外去掉尾部反斜杠：`C:\Tools` 与 `C:\Tools\` 是同一条。
func normalizeValue(s string) string {
	return strings.TrimRight(normalize(s), `\/`)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
