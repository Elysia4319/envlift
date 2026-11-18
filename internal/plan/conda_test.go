package plan

import (
	"strings"
	"testing"

	"github.com/Elysia4319/envlift/internal/conda"
	"github.com/Elysia4319/envlift/internal/config"
)

func condaBaseConfig(envs ...config.CondaEnv) *config.Config {
	return &config.Config{
		App: config.AppName, Schema: config.CurrentSchema,
		SourceMachine: config.SourceMachine{
			Arch: "amd64", Hostname: "${HOST}", Username: "${USER}",
		},
		Path:     config.PathSection{User: []config.PathEntry{}, Machine: []config.PathEntry{}},
		Runtimes: []config.Runtime{}, Software: []config.Software{},
		CondaEnvs: envs,
		Mirrors:   map[string]config.MirrorProfile{},
	}
}

func condaOptions(cfg *config.Config, target Target) Options {
	return Options{
		Config:           cfg,
		Target:           target,
		Recipes:          nil,
		InstallerDir:     `C:\installers`,
		InstallerFiles:   nil,
		Exists:           func(string) bool { return false },
		Expand:           func(s string) string { return strings.ReplaceAll(s, "${USER}", "olduser") },
		ExpandVars:       func(s string) string { return s },
		CondaPackageMode: CondaModeBoth,
		CondaWorkDir:     `C:\work\conda`,
	}
}

func sampleCondaEnv() config.CondaEnv {
	yes, no := true, false
	return config.CondaEnv{
		Name: "py311", Prefix: `C:\Users\${USER}\miniconda3\envs\py311`,
		PythonVersion: "3.11.9", RestoreConda: &yes, RestorePip: &no,
		Packages: []config.CondaPackage{
			{Name: "numpy", Version: "1.26.4", Build: "py311_0", Channel: "conda-forge"},
		},
		PipPackages: []config.PipPackage{{Name: "requests", Version: "2.31.0"}},
	}
}

func TestCondaPlanCreatesMissingEnv(t *testing.T) {
	cfg := condaBaseConfig(sampleCondaEnv())
	p, err := Build(condaOptions(cfg, Target{CondaEnvs: map[string]CondaEnvState{}}))
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	if len(p.Items) != 1 {
		t.Fatalf("条目数=%d", len(p.Items))
	}
	it := p.Items[0]
	if it.Kind != KindCondaEnv || it.Tier != TierConda || it.Status != StatusPlanned {
		t.Fatalf("状态不对: %+v", it)
	}
	if !strings.Contains(it.Command, "env create") || !strings.Contains(it.Command, `C:\work\conda\py311`) {
		t.Fatalf("创建命令不对: %s", it.Command)
	}
	if len(it.CondaMissing) != 1 || it.CondaMissing[0] != "numpy" {
		t.Fatalf("缺包列表不对: %+v", it.CondaMissing)
	}
	if p.Summary.Conda != 1 {
		t.Fatalf("summary conda=%d", p.Summary.Conda)
	}
}

func TestCondaPlanSkipsWhenPackagesMatch(t *testing.T) {
	cfg := condaBaseConfig(sampleCondaEnv())
	target := Target{CondaEnvs: map[string]CondaEnvState{
		"py311": {
			Present: true, PythonVersion: "3.11.9",
			Packages:    conda.CondaIndex([]config.CondaPackage{{Name: "numpy", Version: "1.26.4", Build: "py311_0"}}),
			PipPackages: conda.PipIndex(nil),
		},
	}}
	p, err := Build(condaOptions(cfg, target))
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	if p.Items[0].Status != StatusSkipped {
		t.Fatalf("包匹配应 skipped: %+v", p.Items[0])
	}
}

func TestCondaPlanUpdatesMissingPip(t *testing.T) {
	env := sampleCondaEnv()
	env.RestorePip = nil // 默认 true
	cfg := condaBaseConfig(env)
	target := Target{CondaEnvs: map[string]CondaEnvState{
		"py311": {
			Present: true, PythonVersion: "3.11.9",
			Packages:    conda.CondaIndex([]config.CondaPackage{{Name: "numpy", Version: "1.26.4", Build: "py311_0"}}),
			PipPackages: conda.PipIndex(nil),
		},
	}}
	p, err := Build(condaOptions(cfg, target))
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	it := p.Items[0]
	if it.Status != StatusPlanned || len(it.PipMissing) != 1 {
		t.Fatalf("缺 pip 应 planned: %+v", it)
	}
	if !strings.Contains(it.Command, "pip install") || !strings.Contains(it.Command, "requirements.txt") {
		t.Fatalf("pip 命令不对: %s", it.Command)
	}
}

func TestCondaPlanBaseAndDuplicate(t *testing.T) {
	yes := true
	base := config.CondaEnv{Name: "base", Prefix: `C:\miniconda3`, Base: true,
		RestoreConda: &yes, RestorePip: &yes}
	cfg := condaBaseConfig(base, sampleCondaEnv(), sampleCondaEnv())
	p, err := Build(condaOptions(cfg, Target{CondaEnvs: map[string]CondaEnvState{}}))
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	if p.Items[0].Status != StatusManual {
		t.Fatalf("base 应 manual: %+v", p.Items[0])
	}
	for _, it := range p.Items[1:] {
		if it.Status != StatusAmbiguous && it.Status != StatusBlockedNeedsDecision {
			t.Fatalf("同名环境应 ambiguous/blocked: %+v", it)
		}
	}
}

func TestCondaPlanModeNoneSkips(t *testing.T) {
	cfg := condaBaseConfig(sampleCondaEnv())
	opts := condaOptions(cfg, Target{CondaEnvs: map[string]CondaEnvState{}})
	opts.CondaPackageMode = CondaModeNone
	p, err := Build(opts)
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	if p.Items[0].Status != StatusSkipped {
		t.Fatalf("mode=none 应 skipped: %+v", p.Items[0])
	}
}
