package match

import (
	"fmt"
	"strings"

	"github.com/Elysia4319/envlift/internal/config"
)

// Entry 是一条软件条目的配方命中情况。
//
// Index 对齐 config.software 下标：真实机器上同名不同版本的条目很常见，
// 用名字当键会让它们互相串味。
type Entry struct {
	Index     int      `json:"index"`
	Name      string   `json:"name"`
	Recipe    string   `json:"recipe,omitempty"`
	Basis     string   `json:"basis"`
	Ambiguous []string `json:"ambiguous,omitempty"`
}

// Summary 是一份软件清单的配方覆盖情况。
type Summary struct {
	Recipes                int     `json:"recipes"`
	Recognized             int     `json:"recognized"`
	Unrecognized           int     `json:"unrecognized"`
	UnrecognizedWithWinget int     `json:"unrecognized_with_winget"`
	Entries                []Entry `json:"entries"`
}

// Matched 报告该条目是否被唯一一个配方覆盖。
func (e Entry) Matched() bool { return e.Recipe != "" && len(e.Ambiguous) == 0 }

// Summarize 用四级锚点逐条判定配方覆盖情况。
//
// 这个统计必须只有一份实现：CLI 的扫描预览与 GUI 的扫描页显示的「已识别 / 未识别」
// 是同一件事，各写一套迟早对不上——歧义条目算不算「有 winget id 的缺口」这种
// 细节，两边判得不一样时，用户会看到自相矛盾的两个数字。
//
// 注意：这里只做「配方能不能认出这条软件」，**不做**计划层的去重与多版本判定
// （A1/A5）——那是 plan.Build 的职责，两件事不能混。
func Summarize(recipes []config.Installer, sw []config.Software) (Summary, error) {
	m, err := NewMatcher(recipes)
	if err != nil {
		return Summary{}, fmt.Errorf("配方表无法加载，覆盖统计不可用: %w", err)
	}
	out := Summary{Recipes: len(recipes), Entries: make([]Entry, 0, len(sw))}
	for i, s := range sw {
		idx, basis, all := m.Find(s.Name, s.WingetID)
		// 空串/纯空白视为「没有 winget id」：注册表缺字段时常写成空值，
		// 这种条目去 winget 里查不到任何东西，不算配方缺口。
		hasWinget := s.WingetID != nil && strings.TrimSpace(*s.WingetID) != ""

		if idx >= 0 {
			out.Recognized++
			out.Entries = append(out.Entries, Entry{
				Index: i, Name: s.Name, Recipe: recipes[idx].Name, Basis: basis,
			})
			continue
		}

		out.Unrecognized++
		if hasWinget {
			out.UnrecognizedWithWinget++
		}
		e := Entry{Index: i, Name: s.Name, Basis: basis}
		if len(all) > 1 {
			// 歧义：多个配方都命中，不能算识别成功（C6），但要把候选列出来。
			names := make([]string, 0, len(all))
			for _, c := range all {
				names = append(names, c.RecipeName)
			}
			e.Ambiguous = names
		}
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}
