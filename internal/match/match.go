package match

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Elysia4319/envlift/internal/config"
)

// 匹配依据（M5：必须可审计）。
const (
	BasisWingetID = "winget_id"
	BasisAlias    = "alias"
	BasisDisplay  = "display_regex"
	BasisName     = "name"
	BasisNone     = "none"
)

// Candidate 是一次命中的记录。
type Candidate struct {
	RecipeName string `json:"recipe"`
	Basis      string `json:"basis"`
}

// Matcher 在一组配方里为软件条目找归属。
//
// 正则预编译一次，避免每条软件都重新编译（真实机器上几百条软件）。
type Matcher struct {
	recipes []config.Installer
	display []*regexp.Regexp
}

// NewMatcher 编译配方里的 match_display。非法正则返回错误——
// 配置校验阶段已经拦过一遍，这里是第二道。
func NewMatcher(recipes []config.Installer) (*Matcher, error) {
	m := &Matcher{recipes: recipes, display: make([]*regexp.Regexp, len(recipes))}
	for i, r := range recipes {
		if r.MatchDisplay == "" {
			continue
		}
		re, err := regexp.Compile(r.MatchDisplay)
		if err != nil {
			return nil, fmt.Errorf("配方 %s 的 match_display 非法: %w", r.Name, err)
		}
		m.display[i] = re
	}
	return m, nil
}

// Find 按 M2 的四级锚点匹配，命中即停。
//
// 返回：
//   - idx >= 0：唯一命中，basis 说明靠哪一级命中
//   - idx == -1 且 len(all) > 1：该级有多个配方命中，属于歧义，调用方必须标
//     needs-disambiguation 并列出 all
//   - idx == -1 且 len(all) == 0：没有配方覆盖
//
// 配方的 name 只作条目标识，不参与 DisplayName 匹配——两件事分开，
// 避免「配方改名就匹配不上」。
func (m *Matcher) Find(displayName string, wingetID *string) (int, string, []Candidate) {
	normName := Normalize(displayName)

	levels := []struct {
		basis string
		hit   func(i int) bool
	}{
		{BasisWingetID, func(i int) bool {
			if wingetID == nil || strings.TrimSpace(*wingetID) == "" {
				return false
			}
			want := strings.TrimSpace(*wingetID)
			for _, id := range m.recipes[i].WingetIDs {
				if strings.EqualFold(strings.TrimSpace(id), want) {
					return true
				}
			}
			return false
		}},
		{BasisAlias, func(i int) bool {
			if normName == "" {
				return false
			}
			for _, a := range m.recipes[i].Aliases {
				if Normalize(a) == normName {
					return true
				}
			}
			return false
		}},
		{BasisDisplay, func(i int) bool {
			return m.display[i] != nil && m.display[i].MatchString(displayName)
		}},
		{BasisName, func(i int) bool {
			return normName != "" && Normalize(m.recipes[i].Name) == normName
		}},
	}

	for _, lv := range levels {
		var hits []int
		for i := range m.recipes {
			if lv.hit(i) {
				hits = append(hits, i)
			}
		}
		if len(hits) == 0 {
			continue
		}
		all := make([]Candidate, len(hits))
		for j, i := range hits {
			all[j] = Candidate{RecipeName: m.recipes[i].Name, Basis: lv.basis}
		}
		if len(hits) == 1 {
			return hits[0], lv.basis, all
		}
		return -1, lv.basis, all
	}
	return -1, BasisNone, nil
}
