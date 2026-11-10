package config

import (
	"errors"
	"reflect"
	"regexp"
	"strings"
)

// 脱敏占位符（D5-R1：只有用户名与主机名，没有 ${DRIVE}）。
const (
	UserPlaceholder = "${USER}"
	HostPlaceholder = "${HOST}"
)

// 内部替换令牌。用 NUL 包裹，保证不会与真实内容碰撞，也避免在正则替换串里
// 手写 $ 转义（$ 在 Go 的替换模板里是分组引用）。
const (
	userToken = "\x00envlift-user\x00"
	hostToken = "\x00envlift-host\x00"
)

// Redactor 负责两个方向的字符串改写：导出时脱敏、还原时展开。
type Redactor struct {
	Username string
	Hostname string
	// Raw 为真时 Apply 不做任何改写（对应 scan --raw）。
	Raw bool

	reUsersPath *regexp.Regexp
	reUserSeg   *regexp.Regexp
	reHost      *regexp.Regexp
}

// NewRedactor 用本机的用户名与主机名构造脱敏器。
func NewRedactor(username, hostname string) Redactor {
	r := Redactor{Username: username, Hostname: hostname}
	if username != "" {
		q := regexp.QuoteMeta(username)
		// 主路径形态：...\Users\<name>\... 或结尾
		r.reUsersPath = regexp.MustCompile(`(?i)\\Users\\` + q + `(\\|$)`)
		// 兜底：作为独立路径段出现的用户名
		r.reUserSeg = regexp.MustCompile(`(?i)(^|[\\/"'\s])` + q + `([\\/"'\s]|$)`)
	}
	if hostname != "" {
		r.reHost = regexp.MustCompile(`(?i)(^|[^0-9A-Za-z_-])` +
			regexp.QuoteMeta(hostname) + `([^0-9A-Za-z_-]|$)`)
	}
	return r
}

// WithIdentity 用给定身份重建匹配规则，Raw 保持不变。
//
// 匹配规则是在构造时预编译的，所以换了用户名/主机名必须重建而不是改字段。
func (r Redactor) WithIdentity(username, hostname string) Redactor {
	n := NewRedactor(username, hostname)
	n.Raw = r.Raw
	return n
}

// Apply 就地脱敏整份配置。
func (r Redactor) Apply(cfg *Config) {
	if r.Raw || cfg == nil {
		return
	}
	walkStrings(reflect.ValueOf(cfg), r.redactString)
}

// RedactStrings 就地脱敏任意结构里的字符串字段（计划、报告等）。
// v 必须是结构体指针。
func (r Redactor) RedactStrings(v any) {
	if r.Raw || v == nil {
		return
	}
	walkStrings(reflect.ValueOf(v), r.redactString)
}

// Restore 就地把占位符展开为本机的真实值。
//
// 配置里存在占位符但当前环境无法确定对应值时返回错误——静默留下 ${USER}
// 会让还原出来的路径变成字面量，属于「看起来成功实际失败」，必须报错。
func (r Redactor) Restore(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if r.Username == "" && hasPlaceholder(cfg, UserPlaceholder) {
		return errors.New("配置含 " + UserPlaceholder + " 占位符，但无法确定当前用户名")
	}
	if r.Hostname == "" && hasPlaceholder(cfg, HostPlaceholder) {
		return errors.New("配置含 " + HostPlaceholder + " 占位符，但无法确定当前主机名")
	}
	walkStrings(reflect.ValueOf(cfg), r.expandString)
	return nil
}

// HasPlaceholders 报告配置中是否还存在脱敏占位符。
//
// 返回 false 说明这是一份 --raw 导出（C8）：里面是原机器的字面值，没有占位符
// 可代入，还原时不能静默按字面值装到新机器上。
func HasPlaceholders(cfg *Config) bool {
	return hasPlaceholder(cfg, UserPlaceholder) || hasPlaceholder(cfg, HostPlaceholder)
}

// ExpandString 把占位符替换为真实值，供构造命令行等场景使用。
func (r Redactor) ExpandString(s string) string { return r.expandString(s) }

func (r Redactor) redactString(s string) string {
	if s == "" {
		return s
	}
	out := s
	if r.reUsersPath != nil {
		out = r.reUsersPath.ReplaceAllString(out, `\Users\`+userToken+`${1}`)
	}
	if r.reUserSeg != nil {
		out = r.reUserSeg.ReplaceAllString(out, `${1}`+userToken+`${2}`)
	}
	if r.reHost != nil {
		out = r.reHost.ReplaceAllString(out, `${1}`+hostToken+`${2}`)
	}
	out = strings.ReplaceAll(out, userToken, UserPlaceholder)
	out = strings.ReplaceAll(out, hostToken, HostPlaceholder)
	return out
}

func (r Redactor) expandString(s string) string {
	if s == "" {
		return s
	}
	if r.Username != "" {
		s = strings.ReplaceAll(s, UserPlaceholder, r.Username)
	}
	if r.Hostname != "" {
		s = strings.ReplaceAll(s, HostPlaceholder, r.Hostname)
	}
	return s
}

func hasPlaceholder(cfg *Config, ph string) bool {
	found := false
	walkStrings(reflect.ValueOf(cfg), func(s string) string {
		if strings.Contains(s, ph) {
			found = true
		}
		return s
	})
	return found
}

// walkStrings 深度遍历任意结构里的所有字符串字段并就地改写。
//
// 用反射而不是逐字段手写，是为了让 schema 以后新增字段时脱敏自动覆盖到，
// 不会出现「新加的路径字段忘了脱敏」这类静默漏洞。map 的 key 不改写。
func walkStrings(v reflect.Value, fn func(string) string) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		walkStrings(v.Elem(), fn)

	case reflect.Interface:
		if v.IsNil() {
			return
		}
		inner := reflect.New(v.Elem().Type()).Elem()
		inner.Set(v.Elem())
		walkStrings(inner, fn)
		if v.CanSet() {
			v.Set(inner)
		}

	case reflect.String:
		if v.CanSet() {
			v.SetString(fn(v.String()))
		}

	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			// 只走可导出的字段：未导出字段本来也改不了，硬递归到 map 分支会在
			// SetMapIndex 上 panic（plan 里的 baseByID / recipeResolvers 就是这种）。
			if !v.Field(i).CanSet() {
				continue
			}
			walkStrings(v.Field(i), fn)
		}

	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkStrings(v.Index(i), fn)
		}

	case reflect.Map:
		// 不能写的 map（未导出字段、或来自不可寻址的值）直接跳过，否则
		// SetMapIndex 会 panic：脱敏是"尽力而为"，不该把程序带崩。
		if !v.CanSet() {
			return
		}
		if v.Type().Key().Kind() != reflect.String {
			return
		}
		for _, k := range v.MapKeys() {
			mv := v.MapIndex(k)
			cp := reflect.New(mv.Type()).Elem()
			cp.Set(mv)
			walkStrings(cp, fn)
			v.SetMapIndex(k, cp)
		}
	}
}
