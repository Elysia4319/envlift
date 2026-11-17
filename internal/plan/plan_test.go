package plan

import (
	"strings"
	"testing"
	"time"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/targetid"
)

// 全部测试用构造数据，不读注册表、不跑 winget、不读 installers 目录（C2）。

func baseConfig() *config.Config {
	return &config.Config{
		App:         config.AppName,
		Schema:      config.CurrentSchema,
		GeneratedAt: "2026-01-01T00:00:00Z",
		SourceMachine: config.SourceMachine{
			OS:       config.OSInfo{Version: "Windows 11", Build: "26100"},
			Arch:     "amd64",
			Hostname: config.HostPlaceholder,
			Username: config.UserPlaceholder,
		},
	}
}

func software(name, version, wingetID string) config.Software {
	sw := config.Software{
		Name:          name,
		Version:       config.StringPtr(version),
		Sources:       []string{"registry"},
		SilentCapable: true,
	}
	if wingetID != "" {
		sw.WingetID = config.StringPtr(wingetID)
		sw.Sources = []string{"registry", "winget"}
	}
	return sw
}

func recipe(name, match, silent string, deps ...string) config.Installer {
	return config.Installer{
		Name:       name,
		Match:      match,
		SilentArgs: silent,
		Source:     config.InstallerBuiltin,
		VerifiedAt: config.StringPtr("2026-01-15"),
		DependsOn:  deps,
	}
}

func okTarget() Target {
	return Target{
		OS:            "Windows 11",
		Build:         "26100",
		Arch:          "amd64",
		Username:      "newuser",
		Hostname:      "NEW-PC",
		WingetOK:      true,
		WingetVer:     "1.29.380",
		WingetForceOK: true,
		NetworkOK:     true,
		Runtimes:      map[string]RuntimeState{},
		Drives:        []string{"C:", "D:"},
	}
}

func testOptions(cfg *config.Config, t Target, files []string, recipes []config.Installer) Options {
	return Options{
		Config:         cfg,
		Target:         t,
		InstallerDir:   `C:\proj\installers`,
		InstallerFiles: files,
		Recipes:        recipes,
		Now:            time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Exists:         func(string) bool { return true },
		Expand: func(s string) string {
			return strings.NewReplacer(
				config.UserPlaceholder, "newuser",
				config.HostPlaceholder, "NEW-PC",
			).Replace(s)
		},
	}
}

func mustBuild(t *testing.T, o Options) *Plan {
	t.Helper()
	p, err := Build(o)
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	return p
}

func TestWingetTierWhenAvailable(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.47.1", "Git.Git")}

	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	it := p.Items[0]
	// 未指定安装目录时，命令必须与既有格式逐字符一致。
	if want := "winget install --id Git.Git --exact --silent --accept-package-agreements --accept-source-agreements"; it.Command != want {
		t.Errorf("默认 winget 命令变化:\n got  %q\n want %q", it.Command, want)
	}

	if it.Tier != TierWinget {
		t.Fatalf("层级 = %q，期望 winget", it.Tier)
	}
	if !strings.Contains(it.Command, "winget install --id Git.Git --exact") {
		t.Errorf("命令不含 winget 安装: %q", it.Command)
	}
	if it.Status != StatusPlanned {
		t.Errorf("状态 = %q", it.Status)
	}
	if p.Summary.Winget != 1 {
		t.Errorf("汇总 winget = %d", p.Summary.Winget)
	}
}

// VM 验证 F1 抓到的缺口：winget 层级没有继承配方的 verify / expect_reboot，
// 导致装完后重跑仍会再次执行 winget install，并因「已安装」失败。
func TestWingetTierCarriesRecipeVerify(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.56.0", "Git.Git")}

	r := recipe("git", "Git-*-64-bit.exe", "/VERYSILENT")
	r.WingetIDs = []string{"Git.Git"}
	r.Verify = &config.Verify{Command: "git --version", ExpectContains: "git version"}
	r.ExpectReboot = true

	p := mustBuild(t, testOptions(cfg, okTarget(), nil, []config.Installer{r}))
	it := p.Items[0]
	if it.Tier != TierWinget {
		t.Fatalf("层级 = %q，期望 winget", it.Tier)
	}
	if it.VerifyCmd != "git --version" {
		t.Errorf("VerifyCmd = %q，期望继承配方 verify", it.VerifyCmd)
	}
	if it.VerifyExpect != "git version" {
		t.Errorf("VerifyExpect = %q", it.VerifyExpect)
	}
	if !it.ExpectReboot {
		t.Error("winget 层级应继承配方的 expect_reboot")
	}
	if it.RecipeSource != config.InstallerBuiltin {
		t.Errorf("RecipeSource = %q", it.RecipeSource)
	}
}

// 安装目录是否可指定只看配方是否显式声明模板，默认命令不因新增字段而改变。
func TestInstallDirSupportedAndDefaultCommandsUnchanged(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		software("App", "1.0", ""),
		software("Plain", "1.0", ""),
	}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	r.InstallDirArg = `/DIR="{install_dir}"`
	plain := recipe("plain", "Plain-*.exe", "/S")
	plain.Aliases = []string{"plain"}

	target := okTarget()
	target.WingetOK = false
	p := mustBuild(t, testOptions(cfg, target, []string{"Setup-1.exe", "Plain-1.exe"},
		[]config.Installer{r, plain}))
	byName := map[string]Item{}
	for _, it := range p.Items {
		byName[it.Name] = it
	}
	if !byName["App"].InstallDirSupported {
		t.Error("声明了 install_dir_arg 的配方应标记 supported")
	}
	if byName["Plain"].InstallDirSupported {
		t.Error("未声明模板的配方不应标记 supported")
	}
	if want := `"C:\proj\installers\Plain-1.exe" /S`; byName["Plain"].Command != want {
		t.Errorf("默认本地命令变化:\n got  %q\n want %q", byName["Plain"].Command, want)
	}
	if want := `"C:\proj\installers\Setup-1.exe" /S`; byName["App"].Command != want {
		t.Errorf("未指定目录时命令不应变化:\n got  %q\n want %q", byName["App"].Command, want)
	}
}

func TestNetworkDownFallsBackToLocal(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.47.1", "Git.Git")}
	recipes := []config.Installer{recipe("Git", "Git-*.exe", "/VERYSILENT")}

	target := okTarget()
	target.NetworkOK = false
	target.NetworkNote = "dial tcp: i/o timeout"

	p := mustBuild(t, testOptions(cfg, target, []string{"Git-2.47.1-64-bit.exe"}, recipes))
	it := p.Items[0]

	if it.Tier != TierLocal {
		t.Fatalf("断网时应降级到本地包，实际 %q", it.Tier)
	}
	if !strings.Contains(it.Reason, "网络不可达") {
		t.Errorf("应说明降级原因，实际: %q", it.Reason)
	}
	if !strings.Contains(it.Command, `Git-2.47.1-64-bit.exe" /VERYSILENT`) {
		t.Errorf("本地命令不对: %q", it.Command)
	}
}

func TestWingetFailureHasLocalFallbackPlan(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.56.0", "Git.Git")}
	r := recipe("Git", "Git-*-64-bit.exe", "/VERYSILENT")
	r.Verify = &config.Verify{Command: "git --version", ExpectContains: "git version"}

	p := mustBuild(t, testOptions(cfg, okTarget(), []string{"Git-2.56.0-64-bit.exe"}, []config.Installer{r}))
	it := p.Items[0]
	if it.Tier != TierWinget {
		t.Fatalf("联网时应优先 winget，实际 %q", it.Tier)
	}
	if it.FallbackPlan == nil {
		t.Fatal("winget 失败时应带可执行的本地包 fallback")
	}
	if it.FallbackPlan.Tier != TierLocal {
		t.Errorf("fallback tier = %q", it.FallbackPlan.Tier)
	}
	if !strings.Contains(it.FallbackPlan.Command, `Git-2.56.0-64-bit.exe" /VERYSILENT`) {
		t.Errorf("fallback 命令不对: %q", it.FallbackPlan.Command)
	}
	if it.FallbackPlan.VerifyCmd != "git --version" {
		t.Errorf("fallback verify = %q", it.FallbackPlan.VerifyCmd)
	}
}

func TestWingetUnavailableFallsBackToLocal(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.47.1", "Git.Git")}
	recipes := []config.Installer{recipe("Git", "Git-*.exe", "/VERYSILENT")}

	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target, []string{"Git-2.47.1.exe"}, recipes))
	if p.Items[0].Tier != TierLocal {
		t.Errorf("winget 不可用时应降级本地包，实际 %q", p.Items[0].Tier)
	}
	if !strings.Contains(p.Items[0].Reason, "winget 不可用") {
		t.Errorf("应说明原因: %q", p.Items[0].Reason)
	}
}

func TestManualWhenNoRoute(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Adobe Thing", "30.8", "")}

	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	it := p.Items[0]

	if it.Tier != TierManual || it.Status != StatusManual {
		t.Fatalf("应标 manual，实际 tier=%q status=%q", it.Tier, it.Status)
	}
	if it.Command != "" {
		t.Error("manual 项不应有命令")
	}
	if p.Summary.Manual != 1 {
		t.Errorf("汇总 manual = %d", p.Summary.Manual)
	}
}

func TestAmbiguousWhenTwoRecipesMatchByAlias(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git version 2.47.1", "2.47.1", "")}
	// 两个不同名配方都通过别名命中同一条目 → 必须暴露歧义，不能挑一个。
	r1 := recipe("git-a", "Git-*.exe", "/S")
	r1.Aliases = []string{"git"}
	r2 := recipe("git-b", "git-*.msi", "/qn")
	r2.Aliases = []string{"git"}
	recipes := []config.Installer{r1, r2}

	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target, []string{"Git-2.47.exe", "git-2.47.msi"}, recipes))
	it := p.Items[0]
	if it.Status != StatusBlockedNeedsDecision {
		t.Fatalf("两个配方命中同一包应标待确认（blocked-needs-decision），实际 %q", it.Status)
	}
	if it.MatchedBy != "alias" {
		t.Errorf("matched_by = %q，期望 alias", it.MatchedBy)
	}
	if !strings.Contains(it.Reason, "git-a") || !strings.Contains(it.Reason, "git-b") {
		t.Errorf("原因应列出两个候选配方: %q", it.Reason)
	}
}

func TestMatchedByWingetID(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git version 2.47.1", "2.47.1", "Git.Git")}
	r := recipe("git", "Git-*.exe", "/S")
	r.WingetIDs = []string{"Git.Git"}

	p := mustBuild(t, testOptions(cfg, okTarget(), nil, []config.Installer{r}))
	it := p.Items[0]
	if it.MatchedBy != "winget_id" {
		t.Errorf("matched_by = %q，期望 winget_id", it.MatchedBy)
	}
	if it.RecipeName != "git" {
		t.Errorf("应记下命中的配方名，实际 %q", it.RecipeName)
	}
	if p.Preflight.SoftwareMatched != 1 {
		t.Errorf("识别计数 = %d，期望 1", p.Preflight.SoftwareMatched)
	}
}

func TestMatchedRecipeWithoutInstallerFallsToManual(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git version 2.47.1", "2.47.1", "")}
	r := recipe("git", "Git-*.exe", "/S")
	r.Aliases = []string{"git"}

	// installers/ 为空：识别到了配方，但没有安装包。
	p := mustBuild(t, testOptions(cfg, okTarget(), nil, []config.Installer{r}))
	it := p.Items[0]
	if it.Tier != TierManual || it.Status != StatusManual {
		t.Fatalf("应降为 manual，实际 tier=%q status=%q", it.Tier, it.Status)
	}
	if it.MatchedBy != "alias" {
		t.Errorf("仍应记录 matched_by，实际 %q", it.MatchedBy)
	}
	if !strings.Contains(it.Reason, "已识别配方") {
		t.Errorf("原因应说明已识别但缺安装包: %q", it.Reason)
	}
}

func swFull(name, ver, hive, arch string, pinned bool) config.Software {
	sw := software(name, ver, "")
	if hive != "" {
		sw.Hive = config.StringPtr(hive)
	}
	if arch != "" {
		sw.ArchView = config.StringPtr(arch)
	}
	if pinned {
		sw.Pinned = config.BoolPtr(true)
	}
	return sw
}

// A1：非 multi_version 产品，同配方多条目只留一个代表，其余标 covered。
func TestDedupeKeepsRepresentative(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		swFull("Git version 2.40.0", "2.40.0", "HKLM", "64", false),
		swFull("Git version 2.56.0", "2.56.0", "HKLM", "64", false),
	}
	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target, []string{"Git-2.56.0.exe"}, []config.Installer{recipe("git", "Git-*.exe", "/S")}))
	planned, covered := 0, 0
	for _, it := range p.Items {
		switch it.Status {
		case StatusPlanned:
			planned++
			if it.Expected != "2.56.0" {
				t.Errorf("应保留版本更高的条目，实际 %s", it.Expected)
			}
		case StatusCovered:
			covered++
		}
	}
	if planned != 1 || covered != 1 {
		t.Fatalf("planned=%d covered=%d，期望 1/1: %#v", planned, covered, p.Items)
	}
}

// A1：multi_version 产品不去重，每个版本独立保留。
func TestDedupeSkippedForMultiVersion(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		swFull("Eclipse Temurin JDK 17", "17.0.20", "HKLM", "64", false),
		swFull("Eclipse Temurin JDK 21", "21.0.12", "HKLM", "64", false),
		swFull("Eclipse Temurin JDK 25", "25.0.4", "HKLM", "64", false),
	}
	r := recipe("jdk", "OpenJDK-*.msi", "/qn")
	r.Aliases = []string{"Eclipse Temurin JDK"}
	r.MultiVersion = true

	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target, []string{"OpenJDK-17.msi"}, []config.Installer{r}))
	planned := 0
	for _, it := range p.Items {
		if it.Status == StatusPlanned {
			planned++
		}
	}
	if planned != 3 {
		t.Errorf("多版本产品应全部保留，实际 planned=%d", planned)
	}
}

// A2：HKCU 优先于 HKLM。
func TestDedupePrefersHKCU(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		swFull("Tool 1.0", "1.0", "HKLM", "64", false),
		swFull("Tool 1.0", "1.0", "HKCU", "64", false),
	}
	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target, []string{"Tool-1.0.exe"}, []config.Installer{recipe("tool", "Tool-*.exe", "/S")}))
	for _, it := range p.Items {
		if it.Status == StatusPlanned && it.Hive != "HKCU" {
			t.Errorf("应选 HKCU 条目当代表，实际 hive=%q", it.Hive)
		}
	}
}

// A2.1：显式 pin 优先于其他规则。
func TestDedupePinnedWins(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		swFull("Tool 2.0", "2.0", "HKLM", "64", false),
		swFull("Tool 1.0", "1.0", "HKCU", "64", true), // 被 pin，版本更低也优先
	}
	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target, []string{"Tool-1.0.exe"}, []config.Installer{recipe("tool", "Tool-*.exe", "/S")}))
	for _, it := range p.Items {
		if it.Status == StatusPlanned && it.Expected != "1.0" {
			t.Errorf("pinned 条目应优先，实际保留 %s", it.Expected)
		}
	}
}

// A2.1：多个 pinned 说明用户标冲突了，报错不猜。
func TestDedupeMultiplePinnedIsAmbiguous(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		swFull("Tool 1.0", "1.0", "HKLM", "64", true),
		swFull("Tool 2.0", "2.0", "HKLM", "64", true),
	}
	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target, []string{"Tool-1.0.exe"}, []config.Installer{recipe("tool", "Tool-*.exe", "/S")}))
	for _, it := range p.Items {
		if it.Status != StatusBlockedNeedsDecision {
			t.Errorf("多个 pinned 应判待确认（blocked-needs-decision），实际 %q", it.Status)
		}
	}
}

// A3：版本号无法解析且没有其他区分依据时，不做选择。
func TestDedupeUnparseableVersionIsAmbiguous(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		swFull("Tool", "build-a", "", "", false),
		swFull("Tool", "build-b", "", "", false),
	}
	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target, []string{"Tool-1.0.exe"}, []config.Installer{recipe("tool", "Tool-*.exe", "/S")}))
	got := false
	for _, it := range p.Items {
		if it.Status == StatusBlockedNeedsDecision {
			got = true
		}
	}
	if !got {
		t.Error("版本号不可解析时应判待确认")
	}
}

// A4：代表不可装时回落到次优的可装条目。
func TestDedupeFallsBackToInstallable(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		swFull("Tool 2.0", "2.0", "HKLM", "64", false), // 版本最高
		swFull("Tool 1.0", "1.0", "HKLM", "32", false),
	}
	// 只给 32 位安装包：高版本那条没有对应安装包，但两条都匹配同一配方，
	// 所以用「无 winget + 无包」的组会整体 manual；这里改为给包，让两条都可装。
	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target, []string{"Tool-1.0.exe"}, []config.Installer{recipe("tool", "Tool-*.exe", "/S")}))
	planned := 0
	for _, it := range p.Items {
		if it.Status == StatusPlanned {
			planned++
		}
	}
	if planned != 1 {
		t.Errorf("去重后应只剩 1 条可执行，实际 %d", planned)
	}
}

// A6：版本管理器托管的运行时不能走包管理器装。
func TestManagedRuntimeIsManual(t *testing.T) {
	cfg := baseConfig()
	cfg.Runtimes = []config.Runtime{{
		Name: "node", Command: "node", Version: config.StringPtr("24.22.0"),
		Manager: config.StringPtr("fnm"),
	}}

	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	it := p.Items[0]
	if it.Status != StatusManual || it.Tier != TierManual {
		t.Fatalf("托管运行时应为 manual，实际 status=%q tier=%q", it.Status, it.Tier)
	}
	if !strings.Contains(it.ManualHint, "fnm install") {
		t.Errorf("人工步骤应给出版本管理器命令: %q", it.ManualHint)
	}
}

func TestOrderingRespectsDependsOn(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		software("A", "1.0", ""),
		software("B", "1.0", ""),
	}
	recipes := []config.Installer{
		recipe("A", "A-*.exe", "/S", "B"), // A 依赖 B
		recipe("B", "B-*.exe", "/S"),
	}
	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target, []string{"A-1.0.exe", "B-1.0.exe"}, recipes))
	if len(p.Items) != 2 {
		t.Fatalf("条目数 = %d", len(p.Items))
	}
	if p.Items[0].Name != "B" || p.Items[1].Name != "A" {
		t.Fatalf("顺序应为 B→A，实际 %s→%s", p.Items[0].Name, p.Items[1].Name)
	}
	if !strings.Contains(p.Items[0].MovedBy, "被 A 依赖") {
		t.Errorf("B 的移动原因不对: %q", p.Items[0].MovedBy)
	}
	if !strings.Contains(p.Items[1].MovedBy, "依赖 B") {
		t.Errorf("A 的移动原因不对: %q", p.Items[1].MovedBy)
	}
}

func TestCycleBlocksPlan(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		software("A", "1.0", ""),
		software("B", "1.0", ""),
	}
	recipes := []config.Installer{
		recipe("A", "A-*.exe", "/S", "B"),
		recipe("B", "B-*.exe", "/S", "A"),
	}
	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target, []string{"A-1.0.exe", "B-1.0.exe"}, recipes))
	if !p.IsBlocked() {
		t.Fatal("存在依赖环时应阻止计划")
	}
	if !strings.Contains(p.Blocked, "环") {
		t.Errorf("阻止原因应提到环: %q", p.Blocked)
	}
}

func TestPathChangeMarks(t *testing.T) {
	cfg := baseConfig()
	cfg.Path = config.PathSection{
		User: []config.PathEntry{
			{Value: `C:\existing`},
			{Value: `C:\Users\${USER}\bin`},
			{Value: `E:\apps`},
			{Value: `C:\missing`},
			{Value: `C:\new`},
			{Value: ""},
		},
	}
	target := okTarget()
	target.PathDirs = []string{`C:\existing`}

	o := testOptions(cfg, target, nil, nil)
	o.Exists = func(p string) bool { return p != `C:\missing` }

	p := mustBuild(t, o)
	want := []string{MarkSkip, MarkRewrite, MarkPending, MarkInvalid, MarkAdd, MarkInvalid}
	if len(p.Path) != len(want) {
		t.Fatalf("PATH 变更数 = %d，期望 %d", len(p.Path), len(want))
	}
	for i, w := range want {
		if p.Path[i].Mark != w {
			t.Errorf("[%d] %q 标记 = %q，期望 %q（值 %q）", i, p.Path[i].Value, p.Path[i].Mark, w, p.Path[i].Value)
		}
	}
	// C7：展示的必须是展开后的值，不能留占位符。
	if strings.Contains(p.Path[1].Value, config.UserPlaceholder) {
		t.Errorf("PATH 值应已展开: %q", p.Path[1].Value)
	}
}

func TestRuntimeIdempotentSkip(t *testing.T) {
	cfg := baseConfig()
	cfg.Runtimes = []config.Runtime{{
		Name: "python", Command: "python", Version: config.StringPtr("3.14.8"),
	}}

	target := okTarget()
	target.Runtimes["python"] = RuntimeState{Present: true, Version: "3.14.8", Path: `D:\Conda\python.exe`}

	p := mustBuild(t, testOptions(cfg, target, nil, nil))
	it := p.Items[0]
	if it.Status != StatusSkipped {
		t.Fatalf("已装且版本一致应跳过，实际 %q", it.Status)
	}
	if !strings.Contains(it.Reason, "版本一致") {
		t.Errorf("原因不对: %q", it.Reason)
	}
	if p.Summary.Skipped != 1 {
		t.Errorf("汇总 skipped = %d", p.Summary.Skipped)
	}
}

func TestRuntimeVersionMismatchReportsButSkips(t *testing.T) {
	cfg := baseConfig()
	cfg.Runtimes = []config.Runtime{{
		Name: "python", Command: "python", Version: config.StringPtr("3.13.0"),
	}}
	target := okTarget()
	target.Runtimes["python"] = RuntimeState{Present: true, Version: "3.14.8"}

	p := mustBuild(t, testOptions(cfg, target, nil, nil))
	it := p.Items[0]
	if it.Status != StatusSkipped {
		t.Errorf("已存在时不应重装，实际 %q", it.Status)
	}
	if !strings.Contains(it.Reason, "版本不同") {
		t.Errorf("应报出版本差异: %q", it.Reason)
	}
}

func TestDetectRaw(t *testing.T) {
	raw := baseConfig()
	raw.SourceMachine.Username = "olduser"
	raw.SourceMachine.Hostname = "OLD-PC"
	raw.Path = config.PathSection{User: []config.PathEntry{{Value: `C://Users//olduser//bin`}}}
	if !DetectRaw(raw) {
		t.Error("不含占位符时应判定为 --raw 导出")
	}

	normal := baseConfig()
	normal.Path = config.PathSection{User: []config.PathEntry{{Value: `C://Users//${USER}//bin`}}}
	if DetectRaw(normal) {
		t.Error("含占位符时不应判定为 --raw")
	}
}

func TestBuildPropagatesRawFlag(t *testing.T) {
	cfg := baseConfig()
	o := testOptions(cfg, okTarget(), nil, nil)
	o.RawConfig = true
	if p := mustBuild(t, o); !p.RawConfig {
		t.Error("Build 应把 RawConfig 带进计划")
	}
}

func TestBuildDoesNotMutateInput(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.47.1", "Git.Git")}
	before := *cfg.Software[0].WingetID

	mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	if *cfg.Software[0].WingetID != before {
		t.Error("Build 不应修改入参配置")
	}
}

// C6：同一配方命中多个安装包时必须交给人决定，不能取第一个了事。
func TestAmbiguousWhenRecipeMatchesMultipleFiles(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Tool", "1.0", "")}
	recipes := []config.Installer{recipe("Tool", "Tool-*.exe", "/S")}
	target := okTarget()
	target.WingetOK = false

	p := mustBuild(t, testOptions(cfg, target,
		[]string{"Tool-1.0.exe", "Tool-2.0.exe"}, recipes))
	it := p.Items[0]
	if it.Status != StatusBlockedNeedsDecision {
		t.Fatalf("一个配方命中两个安装包应标待确认（blocked-needs-decision），实际 %q", it.Status)
	}
	if !strings.Contains(it.Reason, "Tool-2.0.exe") {
		t.Errorf("原因应列出候选文件: %q", it.Reason)
	}
	if p.Summary.BlockedNeedsDecision != 1 {
		t.Errorf("汇总 blocked_needs_decision = %d", p.Summary.BlockedNeedsDecision)
	}
}

// 回归：winget 层级必须标 NeedsAdmin，否则 C-2 的 admin_confirmed 形同虚设。
// 这个 bug 是真机验证时抓到的——apply 直接执行了 winget install。
func TestWingetTierRequiresAdmin(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.56.0", "Git.Git")}

	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	it := p.Items[0]
	if it.Tier != TierWinget {
		t.Fatalf("层级 = %q", it.Tier)
	}
	if !it.NeedsAdmin {
		t.Error("winget 层级必须标 NeedsAdmin=true")
	}
}

// A4（plan 侧）：target_id 只在构建计划时生成，且满足「换顺序不变、重跑不变」。
func TestPlanTargetIDsAreStable(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		{Name: "Git", Version: config.StringPtr("2.47.1"), Publisher: config.StringPtr("Git"), Hive: config.StringPtr("HKLM"), ArchView: config.StringPtr("64")},
		{Name: "Vim", Version: config.StringPtr("9.1"), Publisher: config.StringPtr("Vim"), Hive: config.StringPtr("HKLM"), ArchView: config.StringPtr("64")},
	}
	cfg.Path.User = []config.PathEntry{{Value: `C:\Tools\bin`}, {Value: `D:\SDK\bin`}}

	first, err := Build(testOptions(cfg, okTarget(), nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, it := range first.Items {
		ids[it.Name] = it.TargetID
	}
	pathIDs := map[string]string{}
	for _, ch := range first.Path {
		pathIDs[ch.Value] = ch.TargetID
	}
	if len(ids) != 2 || len(pathIDs) != 2 {
		t.Fatalf("entries = %d / %d", len(ids), len(pathIDs))
	}

	// 顺序反转 + 重跑一次：同一个条目必须还是同一个 id
	cfg.Software[0], cfg.Software[1] = cfg.Software[1], cfg.Software[0]
	cfg.Path.User[0], cfg.Path.User[1] = cfg.Path.User[1], cfg.Path.User[0]
	second, err := Build(testOptions(cfg, okTarget(), nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range second.Items {
		if it.TargetID != ids[it.Name] {
			t.Errorf("%s 换顺序后 id 变了: %s -> %s", it.Name, ids[it.Name], it.TargetID)
		}
	}
	for _, ch := range second.Path {
		if ch.TargetID != pathIDs[ch.Value] {
			t.Errorf("PATH %s 换顺序后 id 变了", ch.Value)
		}
	}
}

// A4（plan 侧）：publisher / 版本 / scope / 盘符任一变化 → id 必须变。
func TestPlanTargetIDChangesWithContent(t *testing.T) {
	build := func(mut func(*config.Config)) string {
		cfg := baseConfig()
		cfg.Software = []config.Software{{
			Name: "Git", Version: config.StringPtr("2.47.1"),
			Publisher: config.StringPtr("Git"), Hive: config.StringPtr("HKLM"), ArchView: config.StringPtr("64"),
		}}
		cfg.Path.User = []config.PathEntry{{Value: `C:\Tools`}}
		mut(cfg)
		p, err := Build(testOptions(cfg, okTarget(), nil, nil))
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if len(p.Items) != 1 || len(p.Path) != 1 {
			t.Fatalf("条目数异常: %d / %d", len(p.Items), len(p.Path))
		}
		return p.Items[0].TargetID + "|" + p.Path[0].TargetID
	}

	base := build(func(*config.Config) {})
	for name, mut := range map[string]func(*config.Config){
		"版本":        func(c *config.Config) { c.Software[0].Version = config.StringPtr("2.47.2") },
		"publisher": func(c *config.Config) { c.Software[0].Publisher = config.StringPtr("Other") },
		"scope":     func(c *config.Config) { c.Software[0].Hive = config.StringPtr("HKCU") },
		"盘符":        func(c *config.Config) { c.Path.User[0].Value = `D:\Tools` },
	} {
		if got := build(mut); got == base {
			t.Errorf("%s 变化后 id 没有变", name)
		}
	}
}

// A1-b：id 必须在「代入 ${USER}/${HOST} 之后」的值上计算，且 scan 侧派生视图
// 与 plan 侧必须走同一个函数（targetid.*）。
//
// 这里把同一份配置写成两遍：一遍带占位符（由 Expand 代入），一遍已是字面值。
// 两次构建的 id 必须逐一相同——否则排除导出引用的 id 与决策引用的 id 会分叉，
// 而且是静默失效。
func TestTargetIDSameBeforeAndAfterSubstitution(t *testing.T) {
	withPH := baseConfig()
	withPH.Software = []config.Software{{
		Name: "Git", Version: config.StringPtr("2.47.1"),
		Publisher: config.StringPtr("Git"), Hive: config.StringPtr("HKLM"), ArchView: config.StringPtr("64"),
	}}
	withPH.Runtimes = []config.Runtime{{
		Name: "node", Version: config.StringPtr("22.22.2"), Manager: config.StringPtr("fnm"),
		Executable: config.StringPtr(`${USER}\fnm\node.exe`),
	}}
	withPH.Path.User = []config.PathEntry{{Value: `${USER}\Tools\bin`}}

	literal := baseConfig()
	literal.Software = []config.Software{{
		Name: "Git", Version: config.StringPtr("2.47.1"),
		Publisher: config.StringPtr("Git"), Hive: config.StringPtr("HKLM"), ArchView: config.StringPtr("64"),
	}}
	literal.Runtimes = []config.Runtime{{
		Name: "node", Version: config.StringPtr("22.22.2"), Manager: config.StringPtr("fnm"),
		Executable: config.StringPtr(`newuser\fnm\node.exe`),
	}}
	literal.Path.User = []config.PathEntry{{Value: `newuser\Tools\bin`}}

	a := mustBuild(t, testOptions(withPH, okTarget(), nil, nil))
	b := mustBuild(t, testOptions(literal, okTarget(), nil, nil))

	if len(a.Items) != len(b.Items) || len(a.Path) != len(b.Path) {
		t.Fatalf("条目数不同: %d/%d items, %d/%d path", len(a.Items), len(b.Items), len(a.Path), len(b.Path))
	}
	for i := range a.Items {
		if a.Items[i].TargetID != b.Items[i].TargetID {
			t.Errorf("%s 代入前后 id 不同: %s vs %s", a.Items[i].Name, a.Items[i].TargetID, b.Items[i].TargetID)
		}
	}
	for i := range a.Path {
		if a.Path[i].TargetID != b.Path[i].TargetID {
			t.Errorf("PATH %s 代入前后 id 不同: %s vs %s", a.Path[i].Value, a.Path[i].TargetID, b.Path[i].TargetID)
		}
	}
}

// A1-c：scan 侧派生视图用的就是 targetid.Software，必须与 plan 条目 id 一致。
func TestPlanItemIDMatchesScanSideFunction(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		{Name: "Git", Version: config.StringPtr("2.47.1"), Hive: config.StringPtr("HKLM")},
		{Name: "Vim", Version: config.StringPtr("9.1")},
	}
	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	for i, it := range p.Items {
		want := targetid.Software(cfg.Software[i])
		if it.TargetID != want {
			t.Errorf("%s: plan id=%s，scan 侧函数给出 %s", it.Name, it.TargetID, want)
		}
	}
}

// 回归：Plan 里带着未导出的 map（baseByID / resolvers / recipeResolvers），
// 脱敏走反射时必须跳过它们——曾经因为对未导出字段 SetMapIndex 直接 panic，
// 导致 `restore --plan-out` 一用就崩（④ 引入未导出字段之后埋下的）。
func TestRedactStringsOnPlanDoesNotPanic(t *testing.T) {
	mk := func(cfg *config.Config, files []string, recipes []config.Installer) *Plan {
		t.Helper()
		target := okTarget()
		target.WingetOK = false
		return mustBuild(t, testOptions(cfg, target, files, recipes))
	}

	// 1) 普通计划
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.47.1", "Git.Git")}
	cfg.Path.User = []config.PathEntry{{Value: `${USER}\bin`}}
	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))

	// 2) 带安装包歧义（resolvers 非空）
	c2 := baseConfig()
	c2.Software = []config.Software{software("App", "1.0", "")}
	r := recipe("app", "Setup-*.exe", "/S")
	r.Aliases = []string{"app"}
	p2 := mk(c2, []string{"Setup-1.exe", "Setup-2.exe"}, []config.Installer{r})

	// 3) 带配方歧义（recipeResolvers 非空）
	c3 := baseConfig()
	c3.Software = []config.Software{software("Git version 2.47.1", "2.47.1", "")}
	r1 := recipe("git-a", "Git-*.exe", "/S")
	r1.Aliases = []string{"git"}
	r2 := recipe("git-b", "git-*.msi", "/qn")
	r2.Aliases = []string{"git"}
	p3 := mk(c3, []string{"Git-2.47.exe"}, []config.Installer{r1, r2})

	red := config.NewRedactor("newuser", "NEW-PC")
	for i, pl := range []*Plan{p, p2, p3} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("第 %d 份计划脱敏时 panic: %v", i+1, r)
				}
			}()
			red.RedactStrings(pl)
		}()
	}
}
