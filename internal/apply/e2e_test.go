package apply

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/plan"
	"github.com/Elysia4319/envlift/internal/relocation"
)

// buildInstallDirPlan 用真实 plan.Build 链路生成 set_location 计划；
// 测试里只替换执行器与注册表读取器，不手工拼 Reinstall 等状态。
func buildInstallDirPlan(t *testing.T, ledger *relocation.Ledger, dir string) *plan.Plan {
	t.Helper()
	cfg := &config.Config{
		App:         config.AppName,
		Schema:      config.CurrentSchema,
		GeneratedAt: "2026-01-01T00:00:00Z",
		Software: []config.Software{{
			Name: "App", Version: config.StringPtr("1.0"),
			Sources: []string{"registry"}, SilentCapable: true,
		}},
	}
	recipe := config.Installer{
		Name: "app", Match: "Setup-*.exe", SilentArgs: "/S",
		Source: config.InstallerBuiltin, Aliases: []string{"app"},
		InstallDirArg: `/DIR="{install_dir}"`,
		Verify:        &config.Verify{Command: "app --version", ExpectContains: "app version"},
	}
	target := plan.Target{
		OS: "Windows 11", Build: "26100", Arch: "amd64",
		Username: "newuser", Hostname: "NEW-PC",
		WingetOK: false, NetworkOK: true,
		Runtimes: map[string]plan.RuntimeState{},
		Drives:   []string{"C:", "D:"},
	}
	opts := plan.Options{
		Config: cfg, Target: target,
		InstallerDir:   `C:\proj\installers`,
		InstallerFiles: []string{"Setup-1.exe"},
		Recipes:        []config.Installer{recipe},
		Now:            time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Exists:         func(string) bool { return true },
		Expand:         func(s string) string { return s },
		Relocations:    ledger,
	}
	base, err := plan.Build(opts)
	if err != nil {
		t.Fatalf("基础计划构建失败: %v", err)
	}
	if len(base.Items) != 1 {
		t.Fatalf("测试配置应只有 1 个条目，实际 %d", len(base.Items))
	}
	opts.Decisions = plan.Decisions{{
		TargetID: base.Items[0].TargetID, Kind: plan.DecKindSoftware,
		Action: plan.ActionSetLocation,
		Params: &plan.DecisionParams{InstallDir: &dir},
	}}
	p, err := plan.Build(opts)
	if err != nil {
		t.Fatalf("带 set_location 的计划构建失败: %v", err)
	}
	return p
}

func TestInstallDirLedgerEndToEnd(t *testing.T) {
	dir1 := `D:\Apps\App`
	dir2 := `D:\Tools\App`
	ledgerPath := filepath.Join(t.TempDir(), relocation.DefaultPath)
	ledger := relocation.New()

	first := buildInstallDirPlan(t, ledger, dir1)
	it := first.Items[0]
	if !it.Reinstall || !it.InstallDirApplied {
		t.Fatalf("空账本应触发重装: %+v", it)
	}
	t.Logf("第一次 plan: reinstall=%v install_dir=%q", it.Reinstall, it.InstallDir)

	ex := &fakeExec{results: map[string]ExecResult{
		"app --version": {ExitCode: 0, Output: "app version 1.0"},
	}}
	var out bytes.Buffer
	o := baseOpts(t, first, ex, &fakePaths{})
	o.Out = &out
	o.Locations = fakeLocations{loc: dir1, found: true}
	o.Relocations = ledger
	o.RelocationPath = ledgerPath
	res, err := Run(o)
	if err != nil {
		t.Fatalf("第一次 apply 失败: %v", err)
	}
	if res.Executed != 1 || res.Failed != 0 {
		t.Fatalf("第一次 apply 应执行 1 项: %+v", res)
	}

	loaded, err := relocation.Load(ledgerPath)
	if err != nil {
		t.Fatalf("读取账本失败: %v", err)
	}
	entry, ok := loaded.Find(it.TargetID, dir1)
	if !ok || !entry.Verified || entry.ActualDir != dir1 {
		t.Fatalf("账本记录不完整: %+v / %v", entry, ok)
	}
	t.Logf("账本: verified=%v install_dir=%q actual_dir=%q",
		entry.Verified, entry.InstallDir, entry.ActualDir)

	second := buildInstallDirPlan(t, loaded, dir1)
	it2 := second.Items[0]
	if it2.Reinstall {
		t.Fatalf("第二次 plan 不应重装: %+v", it2)
	}
	if !it2.InstallDirApplied || it2.InstallDirVerified != entry.Verified || it2.InstallDirActual != entry.ActualDir {
		t.Fatalf("第二次 plan 应带出账本状态: %+v", it2)
	}
	t.Logf("第二次 plan: reinstall=%v verified=%v actual_dir=%q",
		it2.Reinstall, it2.InstallDirVerified, it2.InstallDirActual)

	ex2 := &fakeExec{results: map[string]ExecResult{
		"app --version": {ExitCode: 0, Output: "app version 1.0"},
	}}
	o2 := baseOpts(t, second, ex2, &fakePaths{})
	o2.Out = &out
	o2.Locations = fakeLocations{loc: dir1, found: true}
	o2.Relocations = loaded
	o2.RelocationPath = ledgerPath
	res2, err := Run(o2)
	if err != nil {
		t.Fatalf("第二次 apply 失败: %v", err)
	}
	if res2.Skipped != 1 || res2.Executed != 0 {
		t.Fatalf("第二次 apply 应跳过: %+v", res2)
	}
	for _, call := range ex2.calls {
		if call != "app --version" {
			t.Fatalf("第二次 apply 不应执行安装命令: %v", ex2.calls)
		}
	}

	third := buildInstallDirPlan(t, loaded, dir2)
	if !third.Items[0].Reinstall {
		t.Fatalf("改目录后应重新重装: %+v", third.Items[0])
	}
	t.Logf("第三次 plan（改目录）: reinstall=%v install_dir=%q",
		third.Items[0].Reinstall, third.Items[0].InstallDir)

	if _, err := os.Stat(ledgerPath); err != nil {
		t.Fatalf("账本应落盘: %v", err)
	}
}
