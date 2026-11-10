// Package pathenv 按 Windows 真实行为把命令名解析为绝对路径。
//
// 为什么不直接用 exec.LookPath：LookPath 读的是**当前进程**的 PATH 环境变量。
// 本工具常被别的宿主程序启动（IDE、终端包装器），会话 PATH 里可能混入宿主注入的
// 目录，导致「配置里记的是 A 的路径，跑出来的是 B 的版本」。这里改为按注册表
// PATH 解析，会话 PATH 只作最后兜底。
//
// 段顺序依据（实测，非假设）：
//
//	在 cmd.exe 里执行 `echo %PATH%`，与注册表两处 Path 值逐段比对，得到
//	当前进程 PATH 共 40 段 = 4 段宿主注入 + 20 段系统 + 16 段用户；
//	系统段首条位于 index 4，用户段首条位于 index 24（= 4 + 20）。
//	即 Windows 的拼接顺序是「系统段在前，用户段在后」，宿主注入的部分在最前面。
package pathenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var reWinVar = regexp.MustCompile(`%([^%]+)%`)

// ExpandVars 展开 %VAR% 形式。
//
// 仅用于文件系统查找与存在性检查，**绝不写入配置**——把 %SystemRoot% 展开成
// C://WINDOWS 会把机器相关值固化进配置，还原时也失去判断依据（B1）。
func ExpandVars(s string) string {
	return reWinVar.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1 : len(m)-1]
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return m
	})
}

// 目录来源。
const (
	SourceMachine = "machine"
	SourceUser    = "user"
	SourceSession = "session"
	SourceLiteral = "literal"
)

// DefaultPathExt 是 PATHEXT 两处都读不到时的兜底（A4）。
const DefaultPathExt = ".COM;.EXE;.BAT;.CMD"

// Dir 是一个可搜索目录及其来源。
type Dir struct {
	Path   string
	Source string
	// Index 是该目录在其来源段内的序号，从 0 开始。
	Index int
}

// Resolved 是解析结果，带来源信息（A5）。
//
// Source + Index 回答「为什么解析到这个」：是系统段第 18 条，还是会话兜底。
type Resolved struct {
	Path   string
	Source string
	Index  int
}

// SplitList 按 ';' 拆分并丢弃空段，保持原顺序。
func SplitList(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SplitPathExt 把 PATHEXT 规范化成大写、带前导点的扩展名列表。
// 读不到时返回 DefaultPathExt。
func SplitPathExt(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultPathExt
	}
	parts := SplitList(raw)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, ".") {
			p = "." + p
		}
		out = append(out, strings.ToUpper(p))
	}
	if len(out) == 0 {
		return SplitList(DefaultPathExt)
	}
	return out
}

// Lookup 判断路径是否存在，命中时返回磁盘上的真实文件名。
//
// 返回真实文件名是为了修正大小写：PATHEXT 里是 .EXE，而磁盘上通常是 .exe，
// 直接把 PATHEXT 拼进路径会得到 python.EXE —— Windows 能跑，但配置里看着像 bug。
// 由调用方注入，便于测试用假文件系统。
type Lookup func(path string) (string, bool)

// Resolve 按「目录外层、扩展名内层」的顺序查找可执行文件（A4）。
//
// name 已带扩展名时按字面查找；带路径分隔符时直接按字面判断，不进目录搜索。
func Resolve(name string, dirs []Dir, exts []string, lookup Lookup) (Resolved, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Resolved{}, false
	}

	if strings.ContainsAny(name, `\/`) {
		if found, ok := lookup(name); ok {
			return Resolved{Path: found, Source: SourceLiteral, Index: -1}, true
		}
		return Resolved{}, false
	}

	hasExt := filepath.Ext(name) != ""

	for _, d := range dirs {
		if d.Path == "" {
			continue
		}
		if hasExt {
			if found, ok := lookup(filepath.Join(d.Path, name)); ok {
				return Resolved{Path: found, Source: d.Source, Index: d.Index}, true
			}
			continue
		}
		for _, ext := range exts {
			if found, ok := lookup(filepath.Join(d.Path, name+ext)); ok {
				return Resolved{Path: found, Source: d.Source, Index: d.Index}, true
			}
		}
	}
	return Resolved{}, false
}

// BuildDirs 按「系统段 → 用户段 → 会话兜底」组装搜索目录。
//
// 会话兜底只补充前两段没有覆盖到的目录，避免同一目录被搜两遍。
func BuildDirs(machine, user, session []string) []Dir {
	out := make([]Dir, 0, len(machine)+len(user)+len(session))
	seen := make(map[string]bool, len(machine)+len(user)+len(session))

	add := func(list []string, source string) {
		idx := 0
		for _, p := range list {
			key := normalize(p)
			if key == "" {
				continue
			}
			out = append(out, Dir{Path: p, Source: source, Index: idx})
			seen[key] = true
			idx++
		}
	}

	add(machine, SourceMachine)
	add(user, SourceUser)

	idx := 0
	for _, p := range session {
		key := normalize(p)
		if key == "" || seen[key] {
			continue
		}
		out = append(out, Dir{Path: p, Source: SourceSession, Index: idx})
		seen[key] = true
		idx++
	}
	return out
}

func normalize(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"`)
	p = strings.TrimRight(p, `\`)
	return strings.ToLower(p)
}

// DiskLookup 是访问真实文件系统的 Lookup 实现。
func DiskLookup(path string) (string, bool) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return "", false
	}
	// 用目录项的真实名字修正大小写（PATHEXT 是大写，磁盘上通常是小写）。
	dir, base := filepath.Split(path)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if strings.EqualFold(e.Name(), base) {
				return filepath.Join(dir, e.Name()), true
			}
		}
	}
	return path, true
}
