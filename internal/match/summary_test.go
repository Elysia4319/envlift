package match

import (
	"testing"

	"github.com/Elysia4319/envlift/internal/config"
)

func soft(name string, wingetID *string) config.Software {
	return config.Software{Name: name, WingetID: wingetID, Sources: []string{"registry"}}
}

func recipesForTest() []config.Installer {
	return []config.Installer{
		{Name: "git", WingetIDs: []string{"Git.Git"}, Aliases: []string{"Git"}, Match: "*.exe", Source: config.InstallerUser},
		{Name: "vscode", Aliases: []string{"Visual Studio Code"}, Match: "*.exe", Source: config.InstallerUser},
		// 两个配方都能命中同一个 DisplayName，制造歧义
		{Name: "dup-a", Aliases: []string{"Ambiguous App"}, Match: "*.exe", Source: config.InstallerUser},
		{Name: "dup-b", Aliases: []string{"Ambiguous App"}, Match: "*.exe", Source: config.InstallerUser},
	}
}

func TestSummarizeCountsAndEntries(t *testing.T) {
	git := "Git.Git"
	gap := "Gap.Tool.NotInRecipes"
	sw := []config.Software{
		soft("Git", &git),               // winget_id 命中
		soft("Visual Studio Code", nil), // alias 命中
		soft("Ambiguous App", nil),      // 多配方命中 → 不算识别成功
		soft("Some Tool", nil),          // 完全没配方
		soft("Gap Tool", &gap),          // 没配方但有 winget id → 配方缺口
	}
	sum, err := Summarize(recipesForTest(), sw)
	if err != nil {
		t.Fatalf("Summarize 失败: %v", err)
	}
	if sum.Recipes != 4 {
		t.Errorf("配方数 = %d", sum.Recipes)
	}
	if sum.Recognized != 2 {
		t.Errorf("已识别 = %d，期望 2", sum.Recognized)
	}
	if sum.Unrecognized != 3 {
		t.Errorf("未识别 = %d，期望 3", sum.Unrecognized)
	}
	if sum.UnrecognizedWithWinget != 1 {
		t.Errorf("有 winget id 的未识别 = %d，期望 1（只有 Gap Tool）", sum.UnrecognizedWithWinget)
	}
	if len(sum.Entries) != len(sw) {
		t.Fatalf("entries = %d，应逐条对应 %d", len(sum.Entries), len(sw))
	}
	// index 必须对齐 config.software 下标，否则前端会张冠李戴。
	for i, e := range sum.Entries {
		if e.Index != i {
			t.Errorf("entries[%d].Index = %d", i, e.Index)
		}
		if e.Name != sw[i].Name {
			t.Errorf("entries[%d].Name = %q，期望 %q", i, e.Name, sw[i].Name)
		}
	}
	if sum.Entries[0].Basis != BasisWingetID || !sum.Entries[0].Matched() {
		t.Errorf("Git 应靠 winget_id 命中: %+v", sum.Entries[0])
	}
	if sum.Entries[1].Basis != BasisAlias || sum.Entries[1].Recipe != "vscode" {
		t.Errorf("VSCode 应靠 alias 命中: %+v", sum.Entries[1])
	}
	if len(sum.Entries[2].Ambiguous) != 2 || sum.Entries[2].Matched() {
		t.Errorf("歧义条目应列出两个候选且不算识别成功: %+v", sum.Entries[2])
	}
	if sum.Entries[3].Basis != BasisNone || sum.Entries[3].Matched() {
		t.Errorf("Some Tool 应无命中: %+v", sum.Entries[3])
	}
}

// 空 winget id 不算「有 winget id」：空串来自注册表缺字段，去 winget 里查不到任何东西。
func TestSummarizeEmptyWingetIDIsNotAGap(t *testing.T) {
	empty := "   "
	sum, err := Summarize(recipesForTest(), []config.Software{soft("No Recipe App", &empty)})
	if err != nil {
		t.Fatalf("Summarize 失败: %v", err)
	}
	if sum.UnrecognizedWithWinget != 0 {
		t.Errorf("空 winget id 不该计入配方缺口，实际 %d", sum.UnrecognizedWithWinget)
	}
}

// 配方表本身坏了（正则非法）必须报错，不能静默返回全零统计。
func TestSummarizeReportsBrokenRecipeTable(t *testing.T) {
	broken := []config.Installer{{Name: "bad", MatchDisplay: "([", Match: "*.exe", Source: config.InstallerUser}}
	if _, err := Summarize(broken, []config.Software{soft("X", nil)}); err == nil {
		t.Fatal("非法 match_display 应返回错误")
	}
}

func TestSummarizeEmptySoftware(t *testing.T) {
	sum, err := Summarize(recipesForTest(), nil)
	if err != nil {
		t.Fatalf("Summarize 失败: %v", err)
	}
	if sum.Recognized != 0 || sum.Unrecognized != 0 || len(sum.Entries) != 0 {
		t.Errorf("空清单应全零: %+v", sum)
	}
	if sum.Recipes != 4 {
		t.Errorf("配方数仍应报告: %d", sum.Recipes)
	}
}
