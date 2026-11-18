package apply

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/plan"
)

type condaExec struct {
	calls   []string
	envs    [][]string
	respond func(string) ExecResult
}

func (f *condaExec) Run(_ context.Context, command string, _ time.Duration) ExecResult {
	f.calls = append(f.calls, command)
	if f.respond != nil {
		return f.respond(command)
	}
	return ExecResult{ExitCode: 0}
}

func (f *condaExec) RunWithEnv(_ context.Context, command string, _ time.Duration, env []string) ExecResult {
	f.envs = append(f.envs, env)
	return f.Run(context.Background(), command, 0)
}

func condaPlanItem(t *testing.T, spec *plan.CondaSpec) *plan.Plan {
	t.Helper()
	id := "conda-test"
	spec.WorkDir = filepath.Join(t.TempDir(), "work")
	it := plan.Item{
		TargetID: id, Kind: plan.KindCondaEnv, Name: spec.Name,
		Status: plan.StatusPlanned, Tier: plan.TierConda,
		Command: strings.Join(plan.CondaCommands(spec), " && "),
		Conda:   spec,
	}
	return testPlan(it)
}

func sampleSpec() *plan.CondaSpec {
	return &plan.CondaSpec{
		Name: "py311", Prefix: `C:\Apps\py311`, CondaExe: "conda",
		PythonVersion: "3.11.9", RestoreConda: true, RestorePip: true, Create: true,
		Packages: []config.CondaPackage{
			{Name: "numpy", Version: "1.26.4", Build: "py311_0", Channel: "conda-forge"},
		},
		PipPackages: []config.PipPackage{{Name: "requests", Version: "2.31.0"}},
	}
}

func TestCondaApplyWritesTempFilesAndCleansUp(t *testing.T) {
	ex := &condaExec{respond: func(cmd string) ExecResult {
		switch {
		case strings.Contains(cmd, "env create"):
			return ExecResult{ExitCode: 0}
		case strings.Contains(cmd, "pip install"):
			return ExecResult{ExitCode: 0}
		case strings.Contains(cmd, "conda list"):
			return ExecResult{Output: `[{"name":"numpy","version":"1.26.4","build":"py311_0"}]`}
		case strings.Contains(cmd, "pip list"):
			return ExecResult{Output: `[{"name":"requests","version":"2.31.0"}]`}
		default:
			return ExecResult{ExitCode: 0}
		}
	}}
	spec := sampleSpec()
	p := condaPlanItem(t, spec)
	res, err := Run(baseOpts(t, p, ex, &fakePaths{}))
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Executed != 1 || res.Unverified != 0 || res.Failed != 0 {
		t.Fatalf("结果不对: %+v", res)
	}
	if len(ex.envs) < 2 {
		t.Fatalf("conda/pip 应通过 RunWithEnv 注入环境: %+v", ex.envs)
	}
	if _, err := os.Stat(spec.WorkDir); !os.IsNotExist(err) {
		t.Fatalf("成功后应清理临时目录: %v", err)
	}
	if len(p.Items[0].CondaMissing) != 0 {
		t.Fatalf("不应有缺包: %+v", p.Items[0].CondaMissing)
	}
}

func TestCondaApplyUnverifiedWhenPackageMissing(t *testing.T) {
	ex := &condaExec{respond: func(cmd string) ExecResult {
		switch {
		case strings.Contains(cmd, "conda list"):
			return ExecResult{Output: `[]`}
		case strings.Contains(cmd, "pip list"):
			return ExecResult{Output: `[]`}
		default:
			return ExecResult{ExitCode: 0}
		}
	}}
	spec := sampleSpec()
	p := condaPlanItem(t, spec)
	res, err := Run(baseOpts(t, p, ex, &fakePaths{}))
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Unverified != 1 || res.Executed != 0 || res.Failed != 0 {
		t.Fatalf("缺包应 unverified: %+v", res)
	}
	if _, err := os.Stat(spec.WorkDir); err != nil {
		t.Fatalf("未验证时应保留临时目录便于排查: %v", err)
	}
}

func TestCondaApplyFailureKeepsEnvironment(t *testing.T) {
	ex := &condaExec{respond: func(cmd string) ExecResult {
		if strings.Contains(cmd, "env create") {
			return ExecResult{ExitCode: 1, Output: "solver failed"}
		}
		return ExecResult{ExitCode: 0}
	}}
	spec := sampleSpec()
	p := condaPlanItem(t, spec)
	res, err := Run(baseOpts(t, p, ex, &fakePaths{}))
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Failed != 1 || !strings.Contains(res.Stopped, "保留环境") {
		t.Fatalf("失败结果不对: %+v", res)
	}
	if _, err := os.Stat(spec.WorkDir); err != nil {
		t.Fatalf("失败时应保留临时目录: %v", err)
	}
}
