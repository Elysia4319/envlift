package scan

import (
	"strings"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/winreg"
)

// PATH 的注册表位置（B1：只读这两处，不取当前会话 PATH）。
const (
	userPathKey    = `Environment`
	machinePathKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
)

// PATH 条目上可能出现的标记。只在预览里展示，不写进配置文件，也不改动条目本身。
const (
	FlagEmpty             = "empty"
	FlagDuplicate         = "duplicate"
	FlagQuoted            = "quoted"
	FlagTrailingBackslash = "trailing-backslash"
	FlagInvalid           = "invalid"
)

// Note 标注某条 PATH 条目有问题，按索引对应配置里的条目。
type Note struct {
	Index int
	Flags []string
}

// normalizeForDup 生成用于判重的键：大小写不敏感，忽略包裹引号与尾部反斜杠。
func normalizeForDup(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"`)
	s = strings.TrimRight(s, `\`)
	return strings.ToLower(s)
}

// parsePath 把一条注册表 PATH 值拆成条目，并给出需要提示的标注。
//
// 保留原始顺序、原始字面值、重复项与空项——只标不改，最终形态由人确认（B1）。
func parsePath(v winreg.Value, exists func(string) bool) ([]config.PathEntry, []Note) {
	parts := strings.Split(v.String, ";")
	entries := make([]config.PathEntry, 0, len(parts))
	notes := make([]Note, 0)
	seen := make(map[string]bool, len(parts))

	for i, p := range parts {
		var flags []string
		trimmed := strings.TrimSpace(p)

		if trimmed == "" {
			flags = append(flags, FlagEmpty)
		}
		if strings.Contains(trimmed, `"`) {
			flags = append(flags, FlagQuoted)
		}
		if len(trimmed) > 3 && strings.HasSuffix(trimmed, `\`) {
			flags = append(flags, FlagTrailingBackslash)
		}

		key := normalizeForDup(trimmed)
		if key != "" {
			if seen[key] {
				flags = append(flags, FlagDuplicate)
			}
			seen[key] = true
		}
		if trimmed != "" && !exists(trimmed) {
			flags = append(flags, FlagInvalid)
		}

		entries = append(entries, config.PathEntry{Value: p, Expand: v.Expand})
		if len(flags) > 0 {
			notes = append(notes, Note{Index: i, Flags: flags})
		}
	}
	return entries, notes
}
