package plan

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/targetid"
)

// ④ 决策的测试矩阵（F1/F2）。全部是纯数据，不碰注册表、不跑 winget。

func dec(target, kind, action string) Decision {
	return Decision{TargetID: target, Kind: kind, Action: action}
}

func ptr(s string) *string { return &s }

// targetIDOf 取计划里某条目的 target_id（测试里靠名字找）。
func targetIDOf(t *testing.T, p *Plan, name string) string {
	t.Helper()
	for _, it := range p.Items {
		if it.Name == name {
			return it.TargetID
		}
	}
	t.Fatalf("计划里没有条目 %q", name)
	return ""
}

func decisionsIssues(t *testing.T, p *Plan, ds Decisions) []DecisionIssue {
	t.Helper()
	errs := ValidateDecisions(p, ds)
	if errs == nil {
		return nil
	}
	return errs.Issues
}

// B4：未知 target_id 必须报错并指出是第几条。
func TestDecisionUnknownTarget(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.47.1", "Git.Git")}
	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))

	issues := decisionsIssues(t, p, Decisions{dec("nonexistent01", DecKindSoftware, ActionSkip)})
	if len(issues) != 1 {
		t.Fatalf("issues = %d，期望 1", len(issues))
	}
	if issues[0].Code != CodeDecisionTargetAbsent || issues[0].Index != 1 {
		t.Errorf("issue = %+v", issues[0])
	}
	if !strings.Contains(issues[0].Reason, "找不到") {
		t.Errorf("原因应说明找不到: %q", issues[0].Reason)
	}
}

// B4：已排除（x-excluded）的条目被引用也要报错，理由要能区分。
func TestDecisionOnExcludedTarget(t *testing.T) {
	cfg := baseConfig()
	excluded := software("Old App", "1.0", "")
	cfg.Software = []config.Software{software("Git", "2.47.1", "Git.Git")}
	cfg.Extra = map[string]interface{}{"x-excluded": []config.Software{excluded}}
	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))

	issues := decisionsIssues(t, p, Decisions{dec(targetid.Software(excluded), DecKindSoftware, ActionSkip)})
	if len(issues) != 1 || issues[0].Code != CodeDecisionTargetAbsent {
		t.Fatalf("issues = %+v", issues)
	}
	if !strings.Contains(issues[0].Reason, "排除") {
		t.Errorf("原因应说明已被排除: %q", issues[0].Reason)
	}
}

// B4：kind 与实际条目不符、未知 action、重复决策都要报错。
func TestDecisionKindActionAndDuplicate(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.47.1", "Git.Git")}
	cfg.Path.User = []config.PathEntry{{Value: `C:\Tools`}}
	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	swID := targetIDOf(t, p, "Git")

	issues := decisionsIssues(t, p, Decisions{dec(swID, DecKindPath, ActionSkip)})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "属于") {
		t.Errorf("kind 不匹配应报错: %+v", issues)
	}

	issues = decisionsIssues(t, p, Decisions{dec(swID, DecKindSoftware, "delete_everything")})
	if len(issues) != 1 || issues[0].Code != CodeInvalidDecisions {
		t.Fatalf("未知 action: %+v", issues)
	}
	if !strings.Contains(issues[0].Reason, ActionMarkManual) {
		t.Errorf("错误信息应列出合法动作: %q", issues[0].Reason)
	}

	issues = decisionsIssues(t, p, Decisions{
		dec(swID, DecKindSoftware, ActionSkip),
		dec(swID, DecKindSoftware, ActionMarkManual),
	})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "同一个 target_id") {
		t.Errorf("重复决策应报错: %+v", issues)
	}
}

// B3：params 的严格性——该有必须有，不该有必须没有。
func TestDecisionParamsStrictness(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.47.1", "Git.Git")}
	cfg.Path.User = []config.PathEntry{{Value: `C:\Tools`}}
	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	swID := targetIDOf(t, p, "Git")

	cases := []struct {
		name string
		d    Decision
		want string
	}{
		{"skip 带 params", Decision{TargetID: swID, Kind: DecKindSoftware, Action: ActionSkip,
			Params: &DecisionParams{Version: ptr("1")}}, "不接受 params"},
		{"mark_manual 带 params", Decision{TargetID: swID, Kind: DecKindSoftware, Action: ActionMarkManual,
			Params: &DecisionParams{NewValue: ptr("x")}}, "不接受 params"},
		{"select 没有 params", Decision{TargetID: swID, Kind: DecKindSoftware, Action: ActionSelect}, "必须带 params"},
		{"select 同时给两个", Decision{TargetID: swID, Kind: DecKindSoftware, Action: ActionSelect,
			Params: &DecisionParams{Version: ptr("1"), Installer: ptr("a.exe")}}, "只能给一个"},
		{"select 值为空", Decision{TargetID: swID, Kind: DecKindSoftware, Action: ActionSelect,
			Params: &DecisionParams{Version: ptr("  ")}}, "为空"},
		{"select 没有候选", Decision{TargetID: swID, Kind: DecKindSoftware, Action: ActionSelect,
			Params: &DecisionParams{Version: ptr("2.47.1")}}, "没有可选的候选值"},
	}
	for _, c := range cases {
		issues := decisionsIssues(t, p, Decisions{c.d})
		if len(issues) != 1 || !strings.Contains(issues[0].Reason, c.want) {
			t.Errorf("%s: issues = %+v，期望含 %q", c.name, issues, c.want)
		}
	}
}

// 安装包歧义：select{installer} 命中候选 → 变成可执行的本地包条目。
func TestDecisionSelectInstaller(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("App", "1.0", "")}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	target := okTarget()
	target.WingetOK = false
	p := mustBuild(t, testOptions(cfg, target, []string{"Setup-1.exe", "Setup-2.exe"}, []config.Installer{r}))

	it := p.Items[0]
	if it.CandidateKind != CandidateInstaller {
		t.Fatalf("setup 无效: kind=%q candidates=%v", it.CandidateKind, it.Candidates)
	}
	if len(it.Candidates) != 2 {
		t.Fatalf("候选 = %v", it.Candidates)
	}

	// 不在候选里 → 400 并列出合法候选
	issues := decisionsIssues(t, p, Decisions{{TargetID: it.TargetID, Kind: it.Kind, Action: ActionSelect,
		Params: &DecisionParams{Installer: ptr("nope.exe")}}})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "Setup-1.exe") {
		t.Fatalf("非法安装包应列出候选: %+v", issues)
	}

	// 合法 → 计划里该条目变成可执行的本地包
	got := mustBuildDec(t, testOptions(cfg, target, []string{"Setup-1.exe", "Setup-2.exe"}, []config.Installer{r}),
		Decisions{{TargetID: it.TargetID, Kind: it.Kind, Action: ActionSelect,
			Params: &DecisionParams{Installer: ptr("Setup-2.exe")}}})
	gi := got.Items[0]
	if gi.Status != StatusPlanned || gi.Tier != TierLocal {
		t.Fatalf("选包后应可执行: status=%q tier=%q reason=%q", gi.Status, gi.Tier, gi.Reason)
	}
	if !strings.Contains(gi.Command, "Setup-2.exe") {
		t.Errorf("命令没用选中的安装包: %q", gi.Command)
	}
	if gi.InstallerFile != "Setup-2.exe" {
		t.Errorf("installer_file = %q", gi.InstallerFile)
	}
	if gi.CandidateKind != "" || len(gi.Candidates) != 0 {
		t.Error("选完以后不应再挂候选")
	}
}

// 配方歧义：select{recipe} 指定其中一个配方（A2 补的参数）。
func TestDecisionSelectRecipe(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git version 2.47.1", "2.47.1", "")}
	r1 := recipe("git-a", "Git-*.exe", "/S")
	r1.Aliases = []string{"git"}
	r2 := recipe("git-b", "git-*.msi", "/qn")
	r2.Aliases = []string{"git"}
	recipes := []config.Installer{r1, r2}
	files := []string{"Git-2.47.exe"} // 只有 git-a 有安装包
	target := okTarget()
	target.WingetOK = false

	base := mustBuild(t, testOptions(cfg, target, files, recipes))
	it := base.Items[0]
	if it.CandidateKind != CandidateRecipe || len(it.Candidates) != 2 {
		t.Fatalf("候选 = %v / %q", it.Candidates, it.CandidateKind)
	}

	// 不给 recipe → 400，并提示用 params.recipe
	issues := decisionsIssues(t, base, Decisions{{TargetID: it.TargetID, Kind: it.Kind, Action: ActionSelect}})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "必须带 params") {
		t.Fatalf("缺 params: %+v", issues)
	}
	issues = decisionsIssues(t, base, Decisions{{TargetID: it.TargetID, Kind: it.Kind, Action: ActionSelect,
		Params: &DecisionParams{Installer: ptr("Git-2.47.exe")}}})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "params.recipe") {
		t.Fatalf("配方歧义时应提示用 recipe: %+v", issues)
	}

	// 三个参数互斥
	issues = decisionsIssues(t, base, Decisions{{TargetID: it.TargetID, Kind: it.Kind, Action: ActionSelect,
		Params: &DecisionParams{Version: ptr("2.47.1"), Recipe: ptr("git-a")}}})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "只能给一个") {
		t.Fatalf("多给参数应报错: %+v", issues)
	}

	// 不在候选里 → 400 并列出合法候选
	issues = decisionsIssues(t, base, Decisions{{TargetID: it.TargetID, Kind: it.Kind, Action: ActionSelect,
		Params: &DecisionParams{Recipe: ptr("git-c")}}})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "git-a") {
		t.Fatalf("非法配方应列出候选: %+v", issues)
	}

	// 选有安装包的配方 → 变成可执行的本地包
	got := mustBuildDec(t, testOptions(cfg, target, files, recipes), Decisions{{
		TargetID: it.TargetID, Kind: it.Kind, Action: ActionSelect,
		Params: &DecisionParams{Recipe: ptr("git-a")},
	}})
	gi := got.Items[0]
	if gi.Status != StatusPlanned || gi.Tier != TierLocal {
		t.Fatalf("选配方后应可执行: %q / %q / %q", gi.Status, gi.Tier, gi.Reason)
	}
	if gi.RecipeName != "git-a" || !strings.Contains(gi.Command, "Git-2.47.exe") {
		t.Errorf("配方或命令不对: %q / %q", gi.RecipeName, gi.Command)
	}
	if gi.Decision != ActionSelect {
		t.Errorf("决策徽章缺失: %q", gi.Decision)
	}

	// 选没有安装包的配方 → manual，但不再是歧义
	got = mustBuildDec(t, testOptions(cfg, target, files, recipes), Decisions{{
		TargetID: it.TargetID, Kind: it.Kind, Action: ActionSelect,
		Params: &DecisionParams{Recipe: ptr("git-b")},
	}})
	if got.Items[0].Status != StatusManual {
		t.Errorf("无安装包的配方应转 manual，实际 %q", got.Items[0].Status)
	}
}

// set_location：本地包按配方模板追加目录参数，默认命令保持不变。
func TestSetLocationLocalRewritesCommand(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("App", "1.0", "")}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	r.InstallDirArg = `/DIR="{install_dir}"`
	target := okTarget()
	target.WingetOK = false
	o := testOptions(cfg, target, []string{"Setup-1.exe"}, []config.Installer{r})

	base := mustBuild(t, o)
	it := base.Items[0]
	if it.Tier != TierLocal || !it.InstallDirSupported {
		t.Fatalf("setup 无效: tier=%q supported=%v", it.Tier, it.InstallDirSupported)
	}
	before := it.Command

	got := mustBuildDec(t, o, Decisions{{TargetID: it.TargetID, Kind: it.Kind,
		Action: ActionSetLocation, Params: &DecisionParams{InstallDir: ptr(`D:\Apps\App`)}}})
	gi := got.Items[0]
	if gi.InstallDir != `D:\Apps\App` || !gi.InstallDirApplied {
		t.Errorf("安装目录未写入: %+v", gi)
	}
	want := before + ` /DIR="D:\Apps\App"`
	if gi.Command != want {
		t.Errorf("命令 = %q\n期望 = %q", gi.Command, want)
	}
	if got.Items[0].TargetID != it.TargetID {
		t.Error("target_id 不应因安装目录变化")
	}
}

// set_location：winget 包按配方模板追加 --location，并同步重算本地降级命令。
func TestSetLocationWingetRewritesCommandAndFallback(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("App", "1.0", "Vendor.App")}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	r.WingetIDs = []string{"Vendor.App"}
	r.WingetLocationArg = `--location "{install_dir}"`
	r.InstallDirArg = `/DIR="{install_dir}"`
	o := testOptions(cfg, okTarget(), []string{"Setup-1.exe"}, []config.Installer{r})

	base := mustBuild(t, o)
	it := base.Items[0]
	if it.Tier != TierWinget || it.FallbackPlan == nil {
		t.Fatalf("setup 无效: tier=%q fallback=%v", it.Tier, it.FallbackPlan)
	}
	wantWinget := it.Command + ` --location "D:\Apps\App" --force`
	wantLocal := it.FallbackPlan.Command + ` /DIR="D:\Apps\App"`

	got := mustBuildDec(t, o, Decisions{{TargetID: it.TargetID, Kind: it.Kind,
		Action: ActionSetLocation, Params: &DecisionParams{InstallDir: ptr(`D:\Apps\App`)}}})
	gi := got.Items[0]
	if gi.Command != wantWinget {
		t.Errorf("winget 命令 = %q\n期望 = %q", gi.Command, wantWinget)
	}
	if gi.FallbackPlan == nil || gi.FallbackPlan.Command != wantLocal {
		t.Errorf("降级命令未同步: %+v", gi.FallbackPlan)
	}
	if gi.FallbackPlan.InstallDir != `D:\Apps\App` {
		t.Errorf("降级方案应记录安装目录: %+v", gi.FallbackPlan)
	}
}

func TestSetLocationWingetWithoutForceBlocksPlan(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("App", "1.0", "Vendor.App")}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	r.WingetIDs = []string{"Vendor.App"}
	r.WingetLocationArg = `--location "{install_dir}"`
	target := okTarget()
	target.WingetForceOK = false
	o := testOptions(cfg, target, nil, []config.Installer{r})
	p := mustBuild(t, o)

	got, err := Build(Options{
		Config: o.Config, Target: o.Target, InstallerDir: o.InstallerDir,
		InstallerFiles: o.InstallerFiles, Recipes: o.Recipes, Now: o.Now,
		Exists: o.Exists, Expand: o.Expand, ExpandVars: o.ExpandVars,
		Decisions: Decisions{{TargetID: p.Items[0].TargetID, Kind: DecKindSoftware,
			Action: ActionSetLocation, Params: &DecisionParams{InstallDir: ptr(`D:\Apps\App`)}}},
	})
	if err != nil {
		t.Fatalf("Build 不应返回错误，应标计划 blocked: %v", err)
	}
	if !got.IsBlocked() || !strings.Contains(got.Blocked, "winget 不支持强制重装") {
		t.Errorf("计划应 blocked 且原因可读: blocked=%q", got.Blocked)
	}
	if got.Items[0].Status != StatusBlockedNeedsDecision {
		t.Errorf("条目应标 blocked，实际 %q", got.Items[0].Status)
	}
}

// 配方未声明对应参数时拒绝，不能静默忽略。
func TestSetLocationRejectsUnsupportedRecipe(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("App", "1.0", "Vendor.App")}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	r.WingetIDs = []string{"Vendor.App"}
	o := testOptions(cfg, okTarget(), []string{"Setup-1.exe"}, []config.Installer{r})
	p := mustBuild(t, o)

	issues := decisionsIssues(t, p, Decisions{{TargetID: p.Items[0].TargetID, Kind: DecKindSoftware,
		Action: ActionSetLocation, Params: &DecisionParams{InstallDir: ptr(`D:\Apps\App`)}}})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "未声明安装目录参数") {
		t.Fatalf("应拒绝未声明参数的配方: %+v", issues)
	}

	// 只声明本地参数时，winget 层级仍应拒绝。
	r.WingetLocationArg = ""
	r.InstallDirArg = `/DIR="{install_dir}"`
	p = mustBuild(t, testOptions(cfg, okTarget(), []string{"Setup-1.exe"}, []config.Installer{r}))
	if p.Items[0].InstallDirSupported {
		t.Error("winget 条目只声明本地参数时不应标记 supported")
	}
	issues = decisionsIssues(t, p, Decisions{{TargetID: p.Items[0].TargetID, Kind: DecKindSoftware,
		Action: ActionSetLocation, Params: &DecisionParams{InstallDir: ptr(`D:\Apps\App`)}}})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "未声明 winget 安装目录参数") {
		t.Fatalf("winget 缺参数应拒绝: %+v", issues)
	}
}

// resolver 缺失时必须在校验阶段拒绝，不能留下“字段已应用但命令没重算”的静默路径。
func TestSetLocationRejectsMissingResolver(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("App", "1.0", "")}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	r.InstallDirArg = `/DIR="{install_dir}"`
	target := okTarget()
	target.WingetOK = false
	p := mustBuild(t, testOptions(cfg, target, []string{"Setup-1.exe"}, []config.Installer{r}))
	delete(p.locationResolvers, p.Items[0].TargetID)

	issues := decisionsIssues(t, p, Decisions{{TargetID: p.Items[0].TargetID, Kind: DecKindSoftware,
		Action: ActionSetLocation, Params: &DecisionParams{InstallDir: ptr(`D:\Apps\App`)}}})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "缺少命令重算信息") {
		t.Fatalf("resolver 缺失应拒绝: %+v", issues)
	}
}

// 第一版明确拒绝 select{installer} 与 set_location 的组合，并给出可读原因。
func TestSetLocationRejectsInstallerSelectCombination(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("App", "1.0", "")}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	r.InstallDirArg = `/DIR="{install_dir}"`
	target := okTarget()
	target.WingetOK = false
	p := mustBuild(t, testOptions(cfg, target, []string{"Setup-1.exe", "Setup-2.exe"}, []config.Installer{r}))
	id := p.Items[0].TargetID

	issues := decisionsIssues(t, p, Decisions{
		{TargetID: id, Kind: DecKindSoftware, Action: ActionSelect,
			Params: &DecisionParams{Installer: ptr("Setup-1.exe")}},
		{TargetID: id, Kind: DecKindSoftware, Action: ActionSetLocation,
			Params: &DecisionParams{InstallDir: ptr(`D:\Apps\App`)}},
	})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "不支持 select{installer} 与 set_location 组合") {
		t.Fatalf("组合决策应明确拒绝: %+v", issues)
	}
}

// 路径校验与 path.rewrite 共用：非绝对路径、盘符不存在都要拒绝。
func TestSetLocationValidatesPath(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("App", "1.0", "")}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	r.InstallDirArg = `/DIR="{install_dir}"`
	target := okTarget()
	target.WingetOK = false
	p := mustBuild(t, testOptions(cfg, target, []string{"Setup-1.exe"}, []config.Installer{r}))
	id := p.Items[0].TargetID

	cases := []struct{ dir, want string }{
		{`Apps\Git`, "必须是盘符开头的绝对路径"},
		{`Z:\Apps\Git`, "没有盘符 Z:"},
		{``, "不能为空"},
	}
	for _, c := range cases {
		issues := decisionsIssues(t, p, Decisions{{TargetID: id, Kind: DecKindSoftware,
			Action: ActionSetLocation, Params: &DecisionParams{InstallDir: ptr(c.dir)}}})
		if len(issues) != 1 || !strings.Contains(issues[0].Reason, c.want) {
			t.Errorf("dir=%q: issues = %+v，期望含 %q", c.dir, issues, c.want)
		}
	}
}

// params 必须且只能有 install_dir。
func TestSetLocationRejectsExtraParams(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("App", "1.0", "")}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	r.InstallDirArg = `/DIR="{install_dir}"`
	target := okTarget()
	target.WingetOK = false
	p := mustBuild(t, testOptions(cfg, target, []string{"Setup-1.exe"}, []config.Installer{r}))
	id := p.Items[0].TargetID

	issues := decisionsIssues(t, p, Decisions{{TargetID: id, Kind: DecKindSoftware,
		Action: ActionSetLocation, Params: &DecisionParams{InstallDir: ptr(`D:\Apps`), Version: ptr("1.0")}}})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "只接受 params.install_dir") {
		t.Errorf("额外参数应拒绝: %+v", issues)
	}
}

// 选择安装包后仍可指定目录，且目录不改变 target_id。
func TestSetLocationAfterSelectInstaller(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("App", "1.0", "")}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	r.InstallDirArg = `/DIR="{install_dir}"`
	target := okTarget()
	target.WingetOK = false
	o := testOptions(cfg, target, []string{"Setup-1.exe"}, []config.Installer{r})
	p := mustBuild(t, o)
	id := p.Items[0].TargetID

	got := mustBuildDec(t, o, Decisions{{TargetID: id, Kind: DecKindSoftware, Action: ActionSetLocation,
		Params: &DecisionParams{InstallDir: ptr(`D:\Apps\App`)}}})
	if got.Items[0].Command != `"C:\proj\installers\Setup-1.exe" /S /DIR="D:\Apps\App"` {
		t.Errorf("命令 = %q", got.Items[0].Command)
	}
	if got.Items[0].TargetID != id {
		t.Error("target_id 不应因目录变化")
	}
}

// 多版本共用配方：select{version} 改选代表，其余标 covered；重算必须幂等。
func TestDecisionSelectVersionPromotesRepresentative(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		swFull("Git version 2.40.0", "2.40.0", "HKLM", "64", false),
		swFull("Git version 2.56.0", "2.56.0", "HKLM", "64", false),
	}
	recipes := []config.Installer{recipe("git", "Git-*.exe", "/S")}
	files := []string{"Git-2.40.0.exe"}
	target := okTarget()
	target.WingetOK = false

	base := mustBuild(t, testOptions(cfg, target, files, recipes))
	var rep, other *Item
	for i := range base.Items {
		switch base.Items[i].Expected {
		case "2.56.0":
			rep = &base.Items[i]
		case "2.40.0":
			other = &base.Items[i]
		}
	}
	if rep == nil || other == nil {
		t.Fatal("找不到两个版本条目")
	}
	// 默认按 A2 优先级选高版本作代表
	if rep.Status != StatusPlanned || other.Status != StatusCovered {
		t.Fatalf("初始代表不正确: %q / %q", rep.Status, other.Status)
	}
	if len(rep.Candidates) != 2 || rep.CandidateKind != CandidateVersion {
		t.Fatalf("版本候选 = %v / %q", rep.Candidates, rep.CandidateKind)
	}

	// 决策改成另一个版本
	ds := Decisions{{TargetID: rep.TargetID, Kind: rep.Kind, Action: ActionSelect,
		Params: &DecisionParams{Version: ptr("2.40.0")}}}
	got := mustBuildDec(t, testOptions(cfg, target, files, recipes), ds)

	for _, it := range got.Items {
		switch it.Expected {
		case "2.40.0":
			if it.Decision != ActionSelect {
				t.Errorf("2.40.0 的 decision = %q", it.Decision)
			}
			if it.Status != StatusPlanned || it.Tier != TierLocal {
				t.Errorf("2.40.0 应成为可执行代表: %q / %q / %q", it.Status, it.Tier, it.Reason)
			}
			if !strings.Contains(it.Command, "Git-2.40.0.exe") {
				t.Errorf("命令没指向正确的安装包: %q", it.Command)
			}
		case "2.56.0":
			if it.Status != StatusCovered {
				t.Errorf("2.56.0 应被覆盖: %q", it.Status)
			}
			if it.Decision != "" {
				t.Errorf("被覆盖条目不该带决策徽章: %q", it.Decision)
			}
		}
	}

	// 幂等：同一份决策重算两次，结果必须逐字一致（哈希相同）。
	again := mustBuildDec(t, testOptions(cfg, target, files, recipes), ds)
	if Hash(got) != Hash(again) {
		t.Error("同一份决策重算结果不稳定")
	}
	// 决策改变了计划内容，plan_hash 必须变（D1：前端据此重置确认态）。
	if Hash(base) == Hash(got) {
		t.Error("应用决策后 plan_hash 没变")
	}
}

// C2：没有决策的待确认项标 blocked-needs-decision，且不阻断整体。
func TestUndecidedItemsAreBlockedNotFatal(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git version 2.47.1", "2.47.1", "")}
	r1 := recipe("git-a", "Git-*.exe", "/S")
	r1.Aliases = []string{"git"}
	r2 := recipe("git-b", "git-*.msi", "/qn")
	r2.Aliases = []string{"git"}
	target := okTarget()
	target.WingetOK = false
	p := mustBuild(t, testOptions(cfg, target, []string{"Git-2.47.exe", "git-2.47.msi"}, []config.Installer{r1, r2}))

	if p.IsBlocked() {
		t.Fatal("数据类待确认不应让整份计划不可执行")
	}
	if p.Items[0].Status != StatusBlockedNeedsDecision {
		t.Errorf("status = %q，期望 %q", p.Items[0].Status, StatusBlockedNeedsDecision)
	}
	if p.Summary.BlockedNeedsDecision != 1 {
		t.Errorf("blocked 计数 = %d", p.Summary.BlockedNeedsDecision)
	}
	if !strings.Contains(p.Items[0].Reason, "未决策") {
		t.Errorf("原因应说明未决策: %q", p.Items[0].Reason)
	}
}

func mustBuildDec(t *testing.T, o Options, ds Decisions) *Plan {
	t.Helper()
	o.Decisions = ds
	return mustBuild(t, o)
}

// PATH 待确认项（?）：未决策时 blocked；rewrite / skip / mark_invalid 都要生效。
func TestPathDecisions(t *testing.T) {
	newCfg := func() *config.Config {
		cfg := baseConfig()
		cfg.Path.User = []config.PathEntry{{Value: `D:\Tools`}}
		return cfg
	}
	target := okTarget()
	target.Drives = []string{"C:"} // 没有 D: → 该条目是 ?

	base := mustBuild(t, testOptions(newCfg(), target, nil, nil))
	if len(base.Path) != 1 {
		t.Fatalf("path 条目 = %d", len(base.Path))
	}
	ch := base.Path[0]
	if ch.Mark != MarkPending {
		t.Fatalf("mark = %q，期望 %q", ch.Mark, MarkPending)
	}
	if ch.Status != StatusBlockedNeedsDecision {
		t.Errorf("未决策的 ? 应标 blocked，实际 %q", ch.Status)
	}
	if base.Summary.BlockedNeedsDecision != 1 {
		t.Errorf("blocked 计数 = %d", base.Summary.BlockedNeedsDecision)
	}

	// rewrite 到存在的盘符
	got := mustBuildDec(t, testOptions(newCfg(), target, nil, nil), Decisions{{
		TargetID: ch.TargetID, Kind: DecKindPath, Action: ActionRewrite,
		Params: &DecisionParams{NewValue: ptr(`C:\Tools`)},
	}})
	if got.Path[0].Mark != MarkAdd || got.Path[0].Value != `C:\Tools` {
		t.Errorf("rewrite 后 = %q / %q", got.Path[0].Mark, got.Path[0].Value)
	}
	if got.Path[0].Status != "" {
		t.Errorf("已决策的条目不该带 blocked 状态: %q", got.Path[0].Status)
	}

	// rewrite 到不存在的盘符 → 报错并列出可用盘符
	issues := decisionsIssues(t, base, Decisions{{
		TargetID: ch.TargetID, Kind: DecKindPath, Action: ActionRewrite,
		Params: &DecisionParams{NewValue: ptr(`E:\Tools`)},
	}})
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "没有盘符 E:") {
		t.Fatalf("不存在的盘符应报错: %+v", issues)
	}

	// skip：仍保留 ? 标记（它本来就不写入），但状态标为已决策跳过
	got = mustBuildDec(t, testOptions(newCfg(), target, nil, nil), Decisions{{
		TargetID: ch.TargetID, Kind: DecKindPath, Action: ActionSkip,
	}})
	if got.Path[0].Status != PathStatusSkipped {
		t.Errorf("skip 后 status = %q", got.Path[0].Status)
	}
	if got.Summary.BlockedNeedsDecision != 0 {
		t.Errorf("已决策的条目不该再计入 blocked: %d", got.Summary.BlockedNeedsDecision)
	}

	// mark_invalid：标成 ! 便于下次识别
	got = mustBuildDec(t, testOptions(newCfg(), target, nil, nil), Decisions{{
		TargetID: ch.TargetID, Kind: DecKindPath, Action: ActionMarkInvalid,
	}})
	if got.Path[0].Mark != MarkInvalid {
		t.Errorf("mark_invalid 后 mark = %q", got.Path[0].Mark)
	}
}

// B3：rewrite 的新值各种非法形态都必须被拒。
func TestRewriteValueValidation(t *testing.T) {
	drives := []string{"C:", "D:"}
	cases := []struct {
		val  string
		want string
	}{
		{"", "不能为空"},
		{`D:\Tools` + "\n", "控制字符"},
		{`D:\To"ols`, "双引号"},
		{`D:\O'Brien`, "单引号"},
		{`Tools\bin`, "绝对路径"},
		{"D:" + strings.Repeat("a", 300), "上限"},
		{`E:\Tools`, "没有盘符 E:"},
	}
	for _, c := range cases {
		if got := validateNewValue(c.val, drives); !strings.Contains(got, c.want) {
			t.Errorf("%q → %q，期望含 %q", c.val, got, c.want)
		}
	}
	for _, ok := range []string{`D:\Tools`, `C:/Tools`, `\\server\share\tools`} {
		if got := validateNewValue(ok, drives); got != "" {
			t.Errorf("合法值 %q 被判为非法: %q", ok, got)
		}
	}
}

// B5：决策里带的项名与配置不一致时，以配置为准并在 warnings 里标注。
func TestDecisionNameMismatchWarns(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.47.1", "Git.Git")}
	base := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	id := targetIDOf(t, base, "Git")

	got := mustBuildDec(t, testOptions(cfg, okTarget(), nil, nil), Decisions{{
		TargetID: id, Kind: DecKindSoftware, Action: ActionSkip, Name: "Gti",
	}})
	found := false
	for _, w := range got.Warnings {
		if strings.Contains(w, "不一致") {
			found = true
		}
	}
	if !found {
		t.Errorf("项名不一致应进 warnings: %v", got.Warnings)
	}
}

// B6：同一个 struct 的 yaml 与 json 字段集合必须逐字一致。
func TestDecisionsYAMLandJSONAreIsomorphic(t *testing.T) {
	d := Decision{
		TargetID: "abc123", Kind: DecKindSoftware, Action: ActionSelect, Name: "Git",
		Version: "2.47.1", Value: `C:\Git`,
		Params: &DecisionParams{Version: ptr("2.47.1")},
	}
	yamlData, err := yaml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	jsonData, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var yv, jv map[string]any
	if err := yaml.Unmarshal(yamlData, &yv); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(jsonData, &jv); err != nil {
		t.Fatal(err)
	}
	if len(yv) != len(jv) {
		t.Fatalf("字段数不同: yaml=%v json=%v", keysOf(yv), keysOf(jv))
	}
	for k := range yv {
		if _, ok := jv[k]; !ok {
			t.Errorf("yaml 里有 %q，json 里没有", k)
		}
	}
	// params 的嵌套字段也要一致
	var yp, jp map[string]any
	if err := yaml.Unmarshal(yamlData, &struct {
		Params *map[string]any `yaml:"params"`
	}{&yp}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(jsonData, &struct {
		Params *map[string]any `json:"params"`
	}{&jp}); err != nil {
		t.Fatal(err)
	}
	if len(yp) != len(jp) {
		t.Fatalf("params 字段数不同: yaml=%v json=%v", keysOf(yp), keysOf(jp))
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
