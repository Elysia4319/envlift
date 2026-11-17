package recipes

import (
	"strings"

	"github.com/Elysia4319/envlift/internal/config"
)

// Merge 合并内置配方与用户配方：Builtin 在前，同名用户配方覆盖内置，其余追加。
//
// CLI 与 GUI/engine 必须共用这一份逻辑；两边各写一份迟早会在覆盖规则上分叉。
func Merge(cfg *config.Config) []config.Installer {
	out := append([]config.Installer{}, Builtin()...)
	idx := make(map[string]int, len(out))
	for i, r := range out {
		idx[strings.ToLower(r.Name)] = i
	}
	if cfg == nil {
		return out
	}
	for _, r := range cfg.Installers {
		key := strings.ToLower(r.Name)
		if i, ok := idx[key]; ok {
			out[i] = r
			continue
		}
		idx[key] = len(out)
		out = append(out, r)
	}
	return out
}
