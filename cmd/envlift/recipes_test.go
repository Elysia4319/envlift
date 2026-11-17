package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/plan"
	"github.com/Elysia4319/envlift/internal/recipes"
)

func TestRecipesMergeIncludesBuiltins(t *testing.T) {
	merged := recipes.Merge(&config.Config{})
	seen := map[string]bool{}
	for _, r := range merged {
		seen[strings.ToLower(r.Name)] = true
	}
	if !seen["git"] || !seen["7zip"] {
		t.Fatalf("合并结果应包含内置 git/7zip: %+v", seen)
	}
}

func TestRecipesMergeUserOverridesBuiltin(t *testing.T) {
	cfg := &config.Config{Installers: []config.Installer{{
		Name: "Git", Match: "Custom-*.exe", SilentArgs: "/S",
		Source: config.InstallerUser, InstallDirArg: `/D={install_dir}`,
		InstallDirSource: "test",
	}}}
	merged := recipes.Merge(cfg)
	count := 0
	var got config.Installer
	for _, r := range merged {
		if strings.EqualFold(r.Name, "git") {
			count++
			got = r
		}
	}
	if count != 1 {
		t.Fatalf("同名配方应只保留一条，实际 %d", count)
	}
	if got.Match != "Custom-*.exe" || got.InstallDirArg != `/D={install_dir}` {
		t.Fatalf("用户配方未覆盖内置: %+v", got)
	}
}

func TestCLIPlanUsesBuiltin7zipInstallDirArg(t *testing.T) {
	cfg := &config.Config{
		App: config.AppName, Schema: config.CurrentSchema,
		Software: []config.Software{{
			Name: "7-Zip", Version: config.StringPtr("25.01"),
			Sources: []string{"registry"}, SilentCapable: true,
		}},
	}
	opts := plan.Options{
		Config: cfg,
		Target: plan.Target{
			OS: "Windows 11", Build: "26100", Arch: "amd64",
			Username: "newuser", Hostname: "NEW-PC",
			WingetOK: false, NetworkOK: true,
			Runtimes: map[string]plan.RuntimeState{},
			Drives:   []string{"C:", "D:"},
		},
		InstallerDir:   `C:\proj\installers`,
		InstallerFiles: []string{"7z2501-x64.exe"},
		Recipes:        recipes.Merge(cfg),
		Now:            time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Exists:         func(string) bool { return true },
		Expand:         func(s string) string { return s },
	}
	base, err := plan.Build(opts)
	if err != nil {
		t.Fatalf("基础计划失败: %v", err)
	}
	if len(base.Items) != 1 || base.Items[0].Tier != plan.TierLocal {
		t.Fatalf("内置 7zip 应命中本地层级: %+v", base.Items)
	}
	dir := `C:\My Apps\7zip`
	opts.Decisions = plan.Decisions{{
		TargetID: base.Items[0].TargetID, Kind: plan.DecKindSoftware,
		Action: plan.ActionSetLocation,
		Params: &plan.DecisionParams{InstallDir: &dir},
	}}
	p, err := plan.Build(opts)
	if err != nil {
		t.Fatalf("带目录决策的计划失败: %v", err)
	}
	it := p.Items[0]
	if !it.InstallDirApplied || !it.Reinstall {
		t.Fatalf("应标记目录应用与重装: %+v", it)
	}
	if !strings.Contains(it.Command, `/D=C:\My Apps\7zip`) {
		t.Fatalf("内置 7zip 命令应含 /D=: %q", it.Command)
	}
}
