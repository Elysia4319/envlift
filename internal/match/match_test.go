package match

import (
	"testing"

	"github.com/Elysia4319/envlift/internal/config"
)

func strp(s string) *string { return &s }

func recipes() []config.Installer {
	return []config.Installer{
		{
			Name:      "git",
			WingetIDs: []string{"Git.Git"},
			Aliases:   []string{"git version"},
			Match:     "Git-*.exe",
			Source:    config.InstallerBuiltin,
		},
		{
			Name:         "vscode",
			WingetIDs:    []string{"Microsoft.VisualStudioCode"},
			Aliases:      []string{"Visual Studio Code"},
			MatchDisplay: `(?i)^Microsoft Visual Studio Code`,
			Match:        "VSCodeSetup-*.exe",
			Source:       config.InstallerBuiltin,
		},
		{
			Name:         "illustrator",
			MatchDisplay: `(?i)^Adobe Illustrator`,
			Match:        "Illustrator-*.exe",
			Source:       config.InstallerBuiltin,
		},
		{
			Name:   "notepad++",
			Match:  "npp-*.exe",
			Source: config.InstallerBuiltin,
		},
	}
}

func newMatcher(t *testing.T) *Matcher {
	t.Helper()
	m, err := NewMatcher(recipes())
	if err != nil {
		t.Fatalf("NewMatcher 失败: %v", err)
	}
	return m
}

func TestFindByWingetIDHasHighestPriority(t *testing.T) {
	m := newMatcher(t)
	// 名字和别名都指向别的配方，但 winget id 指向 git，必须命中 git。
	idx, basis, all := m.Find("Visual Studio Code (User)", strp("Git.Git"))
	if idx < 0 {
		t.Fatalf("应命中，all=%v", all)
	}
	if basis != BasisWingetID {
		t.Errorf("依据 = %q，期望 winget_id", basis)
	}
	if m.recipes[idx].Name != "git" {
		t.Errorf("命中 %q，期望 git", m.recipes[idx].Name)
	}
}

func TestFindByWingetIDCaseInsensitive(t *testing.T) {
	m := newMatcher(t)
	idx, basis, _ := m.Find("Whatever", strp("microsoft.visualstudiocode"))
	if idx < 0 || basis != BasisWingetID {
		t.Fatalf("winget id 匹配应大小写不敏感: idx=%d basis=%q", idx, basis)
	}
	if m.recipes[idx].Name != "vscode" {
		t.Errorf("命中 %q", m.recipes[idx].Name)
	}
}

func TestFindByAliasBeforeDisplay(t *testing.T) {
	m := newMatcher(t)
	// "Visual Studio Code" 归一后等于 vscode 的别名，也应先于正则命中。
	idx, basis, _ := m.Find("Visual Studio Code", nil)
	if idx < 0 || basis != BasisAlias {
		t.Fatalf("应走别名: idx=%d basis=%q", idx, basis)
	}
	if m.recipes[idx].Name != "vscode" {
		t.Errorf("命中 %q", m.recipes[idx].Name)
	}
}

func TestFindByDisplayRegex(t *testing.T) {
	m := newMatcher(t)
	idx, basis, _ := m.Find("Adobe Illustrator 2026", nil)
	if idx < 0 || basis != BasisDisplay {
		t.Fatalf("应走正则: idx=%d basis=%q", idx, basis)
	}
	if m.recipes[idx].Name != "illustrator" {
		t.Errorf("命中 %q", m.recipes[idx].Name)
	}
}

func TestFindByNameFallback(t *testing.T) {
	m := newMatcher(t)
	idx, basis, _ := m.Find("Notepad++ 8.6", nil)
	if idx < 0 || basis != BasisName {
		t.Fatalf("应走 name 兜底: idx=%d basis=%q", idx, basis)
	}
	if m.recipes[idx].Name != "notepad++" {
		t.Errorf("命中 %q", m.recipes[idx].Name)
	}
}

func TestFindNone(t *testing.T) {
	m := newMatcher(t)
	idx, basis, all := m.Find("SolidWorks 2024", nil)
	if idx != -1 || basis != BasisNone || len(all) != 0 {
		t.Errorf("不该命中: idx=%d basis=%q all=%v", idx, basis, all)
	}
}

func TestFindAmbiguousAtSameLevel(t *testing.T) {
	rs := []config.Installer{
		{Name: "a", Aliases: []string{"shared"}, Source: config.InstallerBuiltin},
		{Name: "b", Aliases: []string{"shared"}, Source: config.InstallerBuiltin},
	}
	m, err := NewMatcher(rs)
	if err != nil {
		t.Fatalf("NewMatcher 失败: %v", err)
	}
	idx, basis, all := m.Find("Shared 1.0", nil)
	if idx != -1 {
		t.Error("多个配方命中同一条目应判为歧义")
	}
	if basis != BasisAlias || len(all) != 2 {
		t.Errorf("应列出两个候选: basis=%q all=%v", basis, all)
	}
}

func TestNewMatcherRejectsBadRegex(t *testing.T) {
	rs := []config.Installer{{Name: "bad", MatchDisplay: `(?i)(unclosed`, Source: config.InstallerBuiltin}}
	if _, err := NewMatcher(rs); err == nil {
		t.Error("非法正则应报错")
	}
}

func TestWingetIDDoesNotFallThroughToDisplay(t *testing.T) {
	// winget id 命中就停，不再看正则——即使正则也能命中别的配方。
	rs := []config.Installer{
		{Name: "byid", WingetIDs: []string{"Vendor.App"}, Source: config.InstallerBuiltin},
		{Name: "bydisplay", MatchDisplay: `(?i).*`, Source: config.InstallerBuiltin},
	}
	m, _ := NewMatcher(rs)
	idx, basis, _ := m.Find("Vendor App 1.0", strp("Vendor.App"))
	if idx < 0 || basis != BasisWingetID || m.recipes[idx].Name != "byid" {
		t.Errorf("应先按 winget id 命中: idx=%d basis=%q", idx, basis)
	}
}
