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

func sampleCondaKey() string {
	return CondaStateKey(`C:\Users\olduser\miniconda3\envs\py311`)
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
	if !strings.Contains(it.Command, "create --yes") || !strings.Contains(it.Command, `C:\work\conda\py311`) {
		t.Fatalf("创建命令不对: %s", it.Command)
	}
	if !strings.Contains(it.Command, "--override-channels -c conda-forge") {
		t.Fatalf("创建命令必须显式覆盖频道: %s", it.Command)
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
		sampleCondaKey(): {
			Name: "py311", Present: true, Prefix: `C:\Users\olduser\miniconda3\envs\py311`, PythonVersion: "3.11.9",
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
	yes := true
	env.RestorePip = &yes
	cfg := condaBaseConfig(env)
	target := Target{CondaEnvs: map[string]CondaEnvState{
		sampleCondaKey(): {
			Name: "py311", Present: true, Prefix: `C:\Users\olduser\miniconda3\envs\py311`, PythonVersion: "3.11.9",
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

func TestCondaModeDefaultIsNone(t *testing.T) {
	mode, err := normalizeCondaMode("")
	if err != nil || mode != CondaModeNone {
		t.Fatalf("默认 mode=%q err=%v，期望 none", mode, err)
	}
}

func TestCondaExplicitModeOverridesScanDefaults(t *testing.T) {
	env := sampleCondaEnv()
	no := false
	env.RestoreConda = &no
	env.RestorePip = &no
	cfg := condaBaseConfig(env)
	opts := condaOptions(cfg, Target{CondaEnvs: map[string]CondaEnvState{}})
	opts.CondaPackageMode = CondaModeConda
	opts.CondaModeExplicit = true
	p, err := Build(opts)
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	if p.Items[0].Status != StatusPlanned || !p.Items[0].Conda.RestoreConda {
		t.Fatalf("显式 --conda-packages=conda 应覆盖 fresh scan 默认值: %+v", p.Items[0])
	}

	opts.CondaModeExplicit = false
	p, err = Build(opts)
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	if p.Items[0].Status != StatusSkipped {
		t.Fatalf("未显式覆盖时 per-env 默认 false 应 skipped: %+v", p.Items[0])
	}
}

func TestCondaPlanSameNameDifferentPrefixIsNotSkipped(t *testing.T) {
	cfg := condaBaseConfig(sampleCondaEnv())
	target := Target{CondaEnvs: map[string]CondaEnvState{
		CondaStateKey(`D:\OtherConda\envs\py311`): {
			Name: "py311", Present: true, Prefix: `D:\OtherConda\envs\py311`, PythonVersion: "3.11.9",
			Packages:    conda.CondaIndex([]config.CondaPackage{{Name: "numpy", Version: "1.26.4", Build: "py311_0"}}),
			PipPackages: conda.PipIndex(nil),
		},
	}}
	p, err := Build(condaOptions(cfg, target))
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	it := p.Items[0]
	if it.Status == StatusSkipped || it.Status == StatusPlanned {
		t.Fatalf("同名不同 prefix 不应按名称复用: %+v", it)
	}
	if !strings.Contains(it.Reason, "prefix 不一致") {
		t.Fatalf("原因应说明 prefix 不一致: %s", it.Reason)
	}
}

func TestCondaPlanUpdateCommandHasNoUnsupportedYes(t *testing.T) {
	cfg := condaBaseConfig(sampleCondaEnv())
	target := Target{CondaEnvs: map[string]CondaEnvState{
		sampleCondaKey(): {
			Name: "py311", Present: true, Prefix: `C:\Users\olduser\miniconda3\envs\py311`, PythonVersion: "3.11.9",
			Packages:    conda.CondaIndex(nil),
			PipPackages: conda.PipIndex(nil),
		},
	}}
	p, err := Build(condaOptions(cfg, target))
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	it := p.Items[0]
	if it.Status != StatusPlanned {
		t.Fatalf("缺包应 planned: %+v", it)
	}
	if !strings.Contains(it.Command, "install --yes --prefix") {
		t.Fatalf("更新命令不对: %s", it.Command)
	}
	if !strings.Contains(it.Command, "--override-channels -c conda-forge") {
		t.Fatalf("更新命令必须显式覆盖频道: %s", it.Command)
	}
	if strings.Contains(it.Command, "env update") {
		t.Fatalf("不应使用不支持 --yes 的 conda env update: %s", it.Command)
	}
	lines := CondaSpecLines(it.Conda)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"python=3.11.9", "numpy=1.26.4=py311_0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("specs.txt 缺少 %q: %s", want, joined)
		}
	}
}
