package apply

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Elysia4319/envlift/internal/pathmgmt"
	"github.com/Elysia4319/envlift/internal/plan"
	"github.com/Elysia4319/envlift/internal/relocation"
)

// C8：全部用假执行器与假 PATH 管理器，单测不装任何软件、不改本机 PATH。

type fakeExec struct {
	calls   []string
	results map[string]ExecResult
}

type fakeLocations struct {
	loc   string
	found bool
	err   error
}

func (f fakeLocations) InstallLocation(string) (string, bool, error) {
	return f.loc, f.found, f.err
}

type fakeLocationCandidates struct {
	locs []string
}

func (f fakeLocationCandidates) InstallLocation(string) (string, bool, error) {
	if len(f.locs) == 1 {
		return f.locs[0], true, nil
	}
	return "", false, nil
}

func (f fakeLocationCandidates) InstallLocations(string) ([]string, error) {
	return f.locs, nil
}

func (f *fakeExec) Run(_ context.Context, command string, _ time.Duration) ExecResult {
	f.calls = append(f.calls, command)
	if r, ok := f.results[command]; ok {
		return r
	}
	return ExecResult{ExitCode: 0}
}

type write struct {
	hive string
	e    pathmgmt.Entry
}

type fakePaths struct {
	snap     pathmgmt.Snapshot
	snapErr  error
	current  map[string]pathmgmt.Entry
	writes   []write
	notified bool
}

func (f *fakePaths) Snapshot() (pathmgmt.Snapshot, error) {
	if f.snapErr != nil {
		return pathmgmt.Snapshot{}, f.snapErr
	}
	return f.snap, nil
}

func (f *fakePaths) Current(hive string) (pathmgmt.Entry, error) {
	if e, ok := f.current[hive]; ok {
		return e, nil
	}
	return pathmgmt.Entry{}, nil
}

func (f *fakePaths) Write(hive string, e pathmgmt.Entry) error {
	f.writes = append(f.writes, write{hive: hive, e: e})
	return nil
}

func (f *fakePaths) Notify() error { f.notified = true; return nil }

func newJournal(t *testing.T) *Journal {
	t.Helper()
	j, err := NewJournal(filepath.Join(t.TempDir(), "j.json"), "envlift.yaml")
	if err != nil {
		t.Fatalf("NewJournal 失败: %v", err)
	}
	return j
}

func testPlan(items ...plan.Item) *plan.Plan {
	return &plan.Plan{
		App: "envlift", Kind: "restore-plan", Schema: 1,
		Target: plan.Target{WingetOK: true, NetworkOK: true},
		Items:  items,
	}
}

func baseOpts(t *testing.T, p *plan.Plan, ex Executor, pm pathmgmt.Manager) Options {
	return Options{
		Plan: p, Journal: newJournal(t), Exec: ex, Paths: pm,
		Out:      os.Stdout,
		Elevated: true,
		Now:      func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
}

func TestApplySkipsWhenVerifyPasses(t *testing.T) {
	ex := &fakeExec{results: map[string]ExecResult{"git --version": {ExitCode: 0}}}
	pm := &fakePaths{}
	p := testPlan(plan.Item{
		Kind: "software", Name: "Git", Status: plan.StatusPlanned, Tier: plan.TierWinget,
		Command: "winget install --id Git.Git", VerifyCmd: "git --version",
	})

	res, err := Run(baseOpts(t, p, ex, pm))
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Skipped != 1 || res.Executed != 0 {
		t.Errorf("已完成应跳过: %+v", res)
	}
	for _, c := range ex.calls {
		if strings.HasPrefix(c, "winget install") {
			t.Error("已完成时不应执行安装命令")
		}
	}
}

// 指定安装目录意味着重装：即使 verify 已通过，也必须执行安装命令。
func TestApplyReinstallsWhenInstallDirApplied(t *testing.T) {
	ex := &fakeExec{results: map[string]ExecResult{
		"git --version": {ExitCode: 0, Output: "git version 2.56.0"},
		"install-git":   {ExitCode: 0, Output: "installed"},
	}}
	p := testPlan(plan.Item{
		Kind: "software", Name: "Git", Status: plan.StatusPlanned, Tier: plan.TierLocal,
		Command: "install-git", VerifyCmd: "git --version", VerifyExpect: "git version",
		InstallDir: `D:\Apps\Git`, InstallDirApplied: true, Reinstall: true,
	})
	var out bytes.Buffer
	o := baseOpts(t, p, ex, &fakePaths{})
	o.Out = &out

	res, err := Run(o)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Skipped != 0 || res.Executed != 1 {
		t.Fatalf("指定目录后应重装而不是跳过: %+v", res)
	}
	if !strings.Contains(out.String(), "[重装] Git") {
		t.Errorf("缺少重装日志: %s", out.String())
	}
	if len(ex.calls) == 0 || ex.calls[0] != "install-git" {
		t.Errorf("应直接执行安装命令，实际调用: %v", ex.calls)
	}

	loaded, err := LoadJournal(o.Journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Steps) != 1 || !loaded.Steps[0].Reinstall {
		t.Errorf("journal 应记录 reinstall: %+v", loaded.Steps)
	}
}

func TestApplyVerifiesInstallDir(t *testing.T) {
	cases := []struct {
		name     string
		reader   LocationReader
		verified bool
		actual   string
		output   string
	}{
		{
			name:     "match",
			reader:   fakeLocations{loc: `D:\Apps\Git\`, found: true},
			verified: true, actual: `D:\Apps\Git\`, output: "已确认",
		},
		{
			name:     "mismatch",
			reader:   fakeLocations{loc: `D:\Other`, found: true},
			verified: false, actual: `D:\Other`, output: "实际",
		},
		{
			name:     "unknown",
			reader:   fakeLocations{err: errors.New("拒绝访问")},
			verified: false, output: "读取失败",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := testPlan(plan.Item{
				Kind: "software", Name: "Git", Status: plan.StatusPlanned, Tier: plan.TierLocal,
				Command: "install-git", InstallDir: `D:\Apps\Git`,
				InstallDirApplied: true, Reinstall: true,
			})
			o := baseOpts(t, p, &fakeExec{}, &fakePaths{})
			o.Locations = c.reader
			var out bytes.Buffer
			o.Out = &out

			if _, err := Run(o); err != nil {
				t.Fatalf("Run 失败: %v", err)
			}
			loaded, err := LoadJournal(o.Journal.Path())
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Steps) != 1 {
				t.Fatalf("steps = %d", len(loaded.Steps))
			}
			st := loaded.Steps[0]
			if st.InstallDirVerified != c.verified || st.InstallDirActual != c.actual {
				t.Errorf("目录验证记录不对: %+v", st)
			}
			if !strings.Contains(st.Output, c.output) {
				t.Errorf("Step 输出应包含 %q: %q", c.output, st.Output)
			}
		})
	}
}

func TestApplyRuntimeInstallDirIsUnverified(t *testing.T) {
	p := testPlan(plan.Item{
		Kind: "runtime", Name: "node", Status: plan.StatusPlanned, Tier: plan.TierLocal,
		Command: "install-node", InstallDir: `D:\Apps\node`,
		InstallDirApplied: true, Reinstall: true,
	})
	o := baseOpts(t, p, &fakeExec{}, &fakePaths{})
	o.Locations = fakeLocations{loc: `D:\Apps\node`, found: true}
	if _, err := Run(o); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadJournal(o.Journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	st := loaded.Steps[0]
	if st.InstallDirVerified || !strings.Contains(st.Output, "runtime 目录验证暂不支持") {
		t.Errorf("runtime 应标未验证: %+v", st)
	}
}

func TestApplyAmbiguousInstallDirIsUnverified(t *testing.T) {
	p := testPlan(plan.Item{
		Kind: "software", Name: "Git", Status: plan.StatusPlanned, Tier: plan.TierLocal,
		Command: "install-git", InstallDir: `D:\Apps\Git`,
		InstallDirApplied: true, Reinstall: true,
	})
	o := baseOpts(t, p, &fakeExec{}, &fakePaths{})
	o.Locations = fakeLocationCandidates{locs: []string{`D:\Apps\Git`, `C:\Apps\Git`}}
	if _, err := Run(o); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadJournal(o.Journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	st := loaded.Steps[0]
	if st.InstallDirVerified || !strings.Contains(st.InstallDirActual, " | ") {
		t.Errorf("同名多候选应标 unverified + 候选: %+v", st)
	}
}

func TestApplyWritesRelocationLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), relocation.DefaultPath)
	p := testPlan(plan.Item{
		TargetID: "a1", Kind: "software", Name: "Git", Status: plan.StatusPlanned,
		Tier: plan.TierLocal, Command: "install-git", InstallDir: `D:\Apps\Git`,
		InstallDirApplied: true, Reinstall: true,
	})
	o := baseOpts(t, p, &fakeExec{}, &fakePaths{})
	o.Locations = fakeLocations{loc: `D:\Apps\Git`, found: true}
	o.Relocations = relocation.New()
	o.RelocationPath = path

	if _, err := Run(o); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	l, err := relocation.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := l.Find("a1", `d:/apps/git/`)
	if !ok || !e.Verified || e.LastCommand != "install-git" || e.ActualDir != `D:\Apps\Git` {
		t.Fatalf("账本记录不对: %+v / %v", e, ok)
	}
}

func TestApplyFailureDoesNotWriteRelocationLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), relocation.DefaultPath)
	p := testPlan(plan.Item{
		TargetID: "a1", Kind: "software", Name: "Git", Status: plan.StatusPlanned,
		Tier: plan.TierLocal, Command: "install-git", InstallDir: `D:\Apps\Git`,
		InstallDirApplied: true, Reinstall: true,
	})
	ex := &fakeExec{results: map[string]ExecResult{"install-git": {ExitCode: 1, Output: "boom"}}}
	o := baseOpts(t, p, ex, &fakePaths{})
	o.Locations = fakeLocations{loc: `D:\Apps\Git`, found: true}
	o.Relocations = relocation.New()
	o.RelocationPath = path
	if _, err := Run(o); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("失败不应写账本，stat err=%v", err)
	}
}

func TestApplyExpectRebootWritesRelocationLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), relocation.DefaultPath)
	p := testPlan(plan.Item{
		TargetID: "a1", Kind: "software", Name: "Git", Status: plan.StatusPlanned,
		Tier: plan.TierLocal, Command: "install-git", InstallDir: `D:\Apps\Git`,
		InstallDirApplied: true, Reinstall: true, ExpectReboot: true,
	})
	o := baseOpts(t, p, &fakeExec{}, &fakePaths{})
	o.Locations = fakeLocations{loc: `D:\Apps\Git`, found: true}
	o.Relocations = relocation.New()
	o.RelocationPath = path
	res, err := Run(o)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if !strings.Contains(res.Stopped, "重启") {
		t.Fatalf("应停在重启: %+v", res)
	}
	l, err := relocation.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Find("a1", `D:\Apps\Git`); !ok {
		t.Fatal("expect_reboot 成功后应先写账本")
	}
}

func TestApplySecondRunSkipsWhenLedgerMatch(t *testing.T) {
	p := testPlan(plan.Item{
		TargetID: "a1", Kind: "software", Name: "Git", Status: plan.StatusPlanned,
		Tier: plan.TierLocal, Command: "install-git", VerifyCmd: "git --version",
		VerifyExpect: "git version", InstallDir: `D:\Apps\Git`,
		InstallDirApplied: true, Reinstall: false,
	})
	ex := &fakeExec{results: map[string]ExecResult{"git --version": {ExitCode: 0, Output: "git version 2.56.0"}}}
	o := baseOpts(t, p, ex, &fakePaths{})
	o.Locations = fakeLocations{loc: `D:\Apps\Git`, found: true}
	o.Relocations = relocation.New()
	o.RelocationPath = filepath.Join(t.TempDir(), relocation.DefaultPath)
	res, err := Run(o)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Skipped != 1 || res.Executed != 0 {
		t.Fatalf("第二次 apply 应跳过: %+v", res)
	}
	for _, c := range ex.calls {
		if c == "install-git" {
			t.Error("账本命中且 verify 通过时不应重装")
		}
	}
}

func TestApplyLedgerWriteFailureDoesNotFailInstall(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := testPlan(plan.Item{
		TargetID: "a1", Kind: "software", Name: "Git", Status: plan.StatusPlanned,
		Tier: plan.TierLocal, Command: "install-git", InstallDir: `D:\Apps\Git`,
		InstallDirApplied: true, Reinstall: true,
	})
	o := baseOpts(t, p, &fakeExec{}, &fakePaths{})
	o.Locations = fakeLocations{loc: `D:\Apps\Git`, found: true}
	o.Relocations = relocation.New()
	o.RelocationPath = filepath.Join(blocker, "ledger.yaml")
	res, err := Run(o)
	if err != nil {
		t.Fatalf("账本写入失败不应让安装失败: %v", err)
	}
	if res.Executed != 1 || len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "写入重装账本失败") {
		t.Fatalf("应成功安装并产生账本 warning: %+v", res)
	}
}

func TestApplySkipsOnlyWhenVerifyOutputMatchesExpect(t *testing.T) {
	ex := &fakeExec{results: map[string]ExecResult{
		"git --version": {ExitCode: 0, Output: "git version 2.56.0"},
	}}
	p := testPlan(plan.Item{
		Kind: "software", Name: "Git", Status: plan.StatusPlanned, Tier: plan.TierWinget,
		Command: "winget install --id Git.Git", VerifyCmd: "git --version", VerifyExpect: "git version",
	})

	res, err := Run(baseOpts(t, p, ex, &fakePaths{}))
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Skipped != 1 || res.Executed != 0 {
		t.Errorf("输出含期望内容时应跳过: %+v", res)
	}
	for _, c := range ex.calls {
		if strings.HasPrefix(c, "winget install") {
			t.Error("verify 已通过时不应执行安装命令")
		}
	}
}

// expect_contains 必须真正参与判断：命令退出码为 0 但输出不是目标软件时，
// 既不能当成「已完成」跳过，也不能在装完后当成「成功」。
func TestApplyVerifyExpectContainsIsEnforced(t *testing.T) {
	ex := &fakeExec{results: map[string]ExecResult{
		"git --version": {ExitCode: 0, Output: "some other tool 1.0"},
		"install-git":   {ExitCode: 0, Output: "installed"},
	}}
	p := testPlan(plan.Item{
		Kind: "software", Name: "Git", Status: plan.StatusPlanned, Tier: plan.TierLocal,
		Command: "install-git", VerifyCmd: "git --version", VerifyExpect: "git version",
	})

	res, err := Run(baseOpts(t, p, ex, &fakePaths{}))
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Failed != 1 || res.Executed != 0 || res.Stopped == "" {
		t.Fatalf("输出不匹配时执行后应判失败: %+v", res)
	}
	var installed bool
	for _, c := range ex.calls {
		if c == "install-git" {
			installed = true
		}
	}
	if !installed {
		t.Error("输出不匹配时不能跳过安装")
	}
}

func TestRefreshProcessPathAppendsRegistryPaths(t *testing.T) {
	old := os.Getenv("PATH")
	defer os.Setenv("PATH", old)

	pm := &fakePaths{current: map[string]pathmgmt.Entry{
		pathmgmt.HiveMachine: {Value: `C:\machine-bin`},
		pathmgmt.HiveUser:    {Value: `C:\user-bin`},
	}}
	refreshProcessPath(pm)
	got := os.Getenv("PATH")
	if !strings.Contains(got, `C:\machine-bin`) || !strings.Contains(got, `C:\user-bin`) {
		t.Fatalf("PATH 未包含注册表目录: %q", got)
	}
	if !strings.HasPrefix(got, `C:\machine-bin;C:\user-bin`) {
		t.Fatalf("PATH 顺序应为机器级→用户级: %q", got)
	}
}

// 同一进程会多次 refresh（Run 开始一次、每次 verify 前一次）。重复调用必须幂等，
// 不能把注册表 PATH 一段段再拼一遍。
func TestRefreshProcessPathIsIdempotent(t *testing.T) {
	old := os.Getenv("PATH")
	defer os.Setenv("PATH", old)

	pm := &fakePaths{current: map[string]pathmgmt.Entry{
		pathmgmt.HiveMachine: {Value: `C:\machine-bin;C:\Windows`},
		pathmgmt.HiveUser:    {Value: `C:\user-bin`},
	}}
	os.Setenv("PATH", `C:\Windows;C:\session-only`)

	refreshProcessPath(pm)
	first := os.Getenv("PATH")
	refreshProcessPath(pm)
	second := os.Getenv("PATH")

	if first != second {
		t.Fatalf("重复刷新不应改变 PATH:\n第一次 %q\n第二次 %q", first, second)
	}
	want := `C:\machine-bin;C:\Windows;C:\user-bin;C:\session-only`
	if first != want {
		t.Errorf("PATH = %q，期望 %q（注册表优先、会话独有目录保留、不重复）", first, want)
	}
}

// REG_SZ 里的 %VAR% 是字面量，不能像 REG_EXPAND_SZ 那样展开。
func TestRefreshProcessPathHonorsValueKind(t *testing.T) {
	old := os.Getenv("PATH")
	defer os.Setenv("PATH", old)
	os.Setenv("ENVLIFT_TEST_VAR", `C:\expanded`)

	pm := &fakePaths{current: map[string]pathmgmt.Entry{
		pathmgmt.HiveMachine: {Value: `C:\machine-bin`, Kind: pathmgmt.KindSZ},
		pathmgmt.HiveUser:    {Value: `%ENVLIFT_TEST_VAR%\bin`, Kind: pathmgmt.KindExpandSZ},
	}}
	os.Setenv("PATH", "")
	refreshProcessPath(pm)

	got := os.Getenv("PATH")
	if !strings.Contains(got, `C:\expanded\bin`) {
		t.Errorf("REG_EXPAND_SZ 应展开: %q", got)
	}
	if !strings.Contains(got, `C:\machine-bin`) {
		t.Errorf("REG_SZ 应原样保留: %q", got)
	}
}

func TestApplyStopsOnFailure(t *testing.T) {
	ex := &fakeExec{results: map[string]ExecResult{
		"cmd-a": {ExitCode: 1, Output: "boom"},
	}}
	p := testPlan(
		plan.Item{Kind: "software", Name: "A", Status: plan.StatusPlanned, Tier: plan.TierLocal, Command: "cmd-a"},
		plan.Item{Kind: "software", Name: "B", Status: plan.StatusPlanned, Tier: plan.TierLocal, Command: "cmd-b"},
	)

	res, err := Run(baseOpts(t, p, ex, &fakePaths{}))
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Failed != 1 {
		t.Errorf("应记 1 次失败: %+v", res)
	}
	if res.Stopped == "" {
		t.Error("失败应停止并给出原因")
	}
	for _, c := range ex.calls {
		if c == "cmd-b" {
			t.Error("失败后不应继续执行后续条目（D11）")
		}
	}
}

func TestApplyFallsBackToLocalOnWingetFailure(t *testing.T) {
	wingetCmd := "winget install --id Git.Git"
	localCmd := `"C:\proj\installers\Git-2.56.0-64-bit.exe" /VERYSILENT`
	ex := &fakeExec{results: map[string]ExecResult{
		wingetCmd:       {ExitCode: 1, Output: "winget failed"},
		localCmd:        {ExitCode: 0, Output: "installed"},
		"git --version": {ExitCode: 0, Output: "git version 2.56.0"},
	}}
	p := testPlan(plan.Item{
		Kind: "software", Name: "Git", Status: plan.StatusPlanned, Tier: plan.TierWinget,
		Command: wingetCmd, NeedsAdmin: true,
		FallbackPlan: &plan.FallbackPlan{
			Tier: plan.TierLocal, Command: localCmd, NeedsAdmin: true,
			VerifyCmd: "git --version", VerifyExpect: "git version",
		},
	})

	opts := baseOpts(t, p, ex, &fakePaths{})
	opts.Confirm = func(string) bool { return true }
	res, err := Run(opts)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Executed != 1 || res.Failed != 0 || res.Stopped != "" {
		t.Fatalf("降级成功后应算执行成功: %+v", res)
	}
	j, err := LoadJournal(opts.Journal.Path())
	if err != nil {
		t.Fatalf("加载 journal 失败: %v", err)
	}
	if len(j.Steps) != 1 {
		t.Fatalf("steps = %d", len(j.Steps))
	}
	st := j.Steps[0]
	if st.Tier != plan.TierLocal || st.Command != localCmd || st.Status != StepOK {
		t.Errorf("降级步骤记录不对: %+v", st)
	}
	if st.ActualVersion != "2.56.0" {
		t.Errorf("actual_version = %q", st.ActualVersion)
	}
}

func TestApplyBlocksNeedsAdminWhenNotElevated(t *testing.T) {
	ex := &fakeExec{}
	o := baseOpts(t, testPlan(plan.Item{
		Kind: "software", Name: "AdminApp", Status: plan.StatusPlanned, Tier: plan.TierLocal,
		Command: "setup.exe /S", NeedsAdmin: true,
	}), ex, &fakePaths{})
	o.Elevated = false

	res, err := Run(o)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Blocked != 1 {
		t.Errorf("未提权时应阻止: %+v", res)
	}
	if len(ex.calls) != 0 {
		t.Error("未提权时不应执行任何命令")
	}
}

// D15 skip_admin：提权项一律阻止（即使已提权），非提权项照常执行。
func TestApplySkipAdminBlocksEvenWhenElevated(t *testing.T) {
	ex := &fakeExec{}
	j := newJournal(t)
	o := baseOpts(t, testPlan(
		plan.Item{Kind: "software", Name: "AdminApp", Status: plan.StatusPlanned, Tier: plan.TierLocal,
			Command: "setup-admin.exe /S", NeedsAdmin: true},
		plan.Item{Kind: "software", Name: "UserApp", Status: plan.StatusPlanned, Tier: plan.TierLocal,
			Command: "setup-user.exe /S"},
	), ex, &fakePaths{})
	o.Journal = j
	o.Elevated = true
	o.SkipAdmin = true

	res, err := Run(o)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.Blocked != 1 || res.Executed != 1 {
		t.Fatalf("应阻止 1 项、执行 1 项: %+v", res)
	}
	for _, c := range ex.calls {
		if strings.Contains(c, "setup-admin") {
			t.Error("mode=skip_admin 下提权命令不得执行")
		}
	}

	loaded, err := LoadJournal(j.Path())
	if err != nil {
		t.Fatalf("LoadJournal 失败: %v", err)
	}
	blocked := 0
	for _, s := range loaded.Steps {
		if s.Name == "AdminApp" && s.Status == StepBlocked {
			blocked++
			if !strings.Contains(s.Output, "skip_admin") {
				t.Errorf("阻止原因应写明 skip_admin: %q", s.Output)
			}
		}
	}
	if blocked != 1 {
		t.Errorf("journal 应有 1 条 blocked 记录，实际 %d", blocked)
	}
}

// 系统级 PATH 也是提权操作：skip_admin 下不写、不中断，用户级照写。
func TestApplySkipAdminSkipsMachinePathOnly(t *testing.T) {
	pm := &fakePaths{current: map[string]pathmgmt.Entry{
		pathmgmt.HiveUser: {Value: `C:\Windows`, Kind: pathmgmt.KindExpandSZ},
	}}
	j := newJournal(t)
	p := testPlan()
	p.Path = []plan.PathChange{
		{Section: "user", Mark: plan.MarkAdd, Value: `C:\UserBin`},
		{Section: "machine", Mark: plan.MarkAdd, Value: `C:\MachineBin`},
	}
	o := baseOpts(t, p, &fakeExec{}, pm)
	o.Journal = j
	o.SkipAdmin = true
	o.Elevated = false // 未提权：不设 SkipAdmin 时这里会因写 HKLM 直接报错

	res, err := Run(o)
	if err != nil {
		t.Fatalf("skip_admin 下不应因系统级 PATH 报错中止: %v", err)
	}
	if res.PathAdded != 1 || res.Blocked != 1 {
		t.Fatalf("应只写用户级 1 条、阻止系统级 1 条: %+v", res)
	}
	for _, w := range pm.writes {
		if w.hive == pathmgmt.HiveMachine {
			t.Error("系统级 PATH 不应被写入")
		}
	}
}

func TestApplyStopsOnExpectReboot(t *testing.T) {
	ex := &fakeExec{}
	p := testPlan(
		plan.Item{Kind: "software", Name: "A", Status: plan.StatusPlanned, Tier: plan.TierLocal,
			Command: "cmd-a", ExpectReboot: true},
		plan.Item{Kind: "software", Name: "B", Status: plan.StatusPlanned, Tier: plan.TierLocal, Command: "cmd-b"},
	)
	res, err := Run(baseOpts(t, p, ex, &fakePaths{}))
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if !strings.Contains(res.Stopped, "重启") {
		t.Errorf("应提示重启: %q", res.Stopped)
	}
	for _, c := range ex.calls {
		if c == "cmd-b" {
			t.Error("需要重启时应停止，不继续")
		}
	}
}

func TestPathSnapshotFailsAbortsBeforeAnyChange(t *testing.T) {
	ex := &fakeExec{}
	pm := &fakePaths{snapErr: errors.New("拒绝访问")}
	p := testPlan(plan.Item{
		Kind: "software", Name: "A", Status: plan.StatusPlanned, Tier: plan.TierLocal, Command: "cmd-a",
	})
	p.Path = []plan.PathChange{{Section: "user", Mark: plan.MarkAdd, Value: `C:\new`}}

	_, err := Run(baseOpts(t, p, ex, pm))
	if err == nil {
		t.Fatal("备份失败应中止")
	}
	if len(ex.calls) != 0 {
		t.Error("备份失败时不应执行任何安装")
	}
	if len(pm.writes) != 0 {
		t.Error("备份失败时不应写 PATH")
	}
}

func TestPathWritePreservesKind(t *testing.T) {
	pm := &fakePaths{current: map[string]pathmgmt.Entry{
		pathmgmt.HiveUser: {Value: `C:\Windows`, Kind: pathmgmt.KindExpandSZ},
	}}
	p := testPlan()
	p.Path = []plan.PathChange{
		{Section: "user", Mark: plan.MarkAdd, Value: `C:\new`},
		{Section: "user", Mark: plan.MarkSkip, Value: `C:\Windows`},
		{Section: "user", Mark: plan.MarkPending, Value: `Z:\nowhere`},
	}

	res, err := Run(baseOpts(t, p, &fakeExec{}, pm))
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if res.PathAdded != 1 {
		t.Errorf("应只新增 1 条，实际 %d", res.PathAdded)
	}
	if len(pm.writes) != 1 {
		t.Fatalf("应只写一次，实际 %d", len(pm.writes))
	}
	w := pm.writes[0]
	if w.e.Kind != pathmgmt.KindExpandSZ {
		t.Errorf("值类型被改变: %d，应为 REG_EXPAND_SZ", w.e.Kind)
	}
	if !strings.Contains(w.e.Value, `C:\new`) || !strings.Contains(w.e.Value, `C:\Windows`) {
		t.Errorf("应在原值后追加而不是替换: %q", w.e.Value)
	}
	if !pm.notified {
		t.Error("写入后应广播环境变更")
	}
}

// PATH 写入必须落进 journal 步骤流，否则回看 journal 时看不出「到底写没写」。
func TestPathWriteRecordedAsJournalStep(t *testing.T) {
	pm := &fakePaths{current: map[string]pathmgmt.Entry{
		pathmgmt.HiveUser: {Value: `C:\Windows`, Kind: pathmgmt.KindExpandSZ},
	}}
	j := newJournal(t)
	p := testPlan()
	p.Path = []plan.PathChange{{Section: "user", Mark: plan.MarkAdd, Value: `C:\NewBin`}}
	o := baseOpts(t, p, &fakeExec{}, pm)
	o.Journal = j

	if _, err := Run(o); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	loaded, err := LoadJournal(j.Path())
	if err != nil {
		t.Fatalf("LoadJournal 失败: %v", err)
	}
	var found bool
	for _, s := range loaded.Steps {
		if s.Kind == "path" && s.Name == "PATH["+pathmgmt.HiveUser+"]" {
			found = true
			if s.Status != StepOK || !strings.Contains(s.Output, `C:\NewBin`) {
				t.Errorf("PATH 步骤内容不对: %+v", s)
			}
		}
	}
	if !found {
		t.Errorf("journal 里应有一条 PATH 写入步骤: %+v", loaded.Steps)
	}
}

func TestRollbackRestoresOriginal(t *testing.T) {
	orig := pathmgmt.Snapshot{
		TakenAt: "2026-01-01T00:00:00Z",
		User:    pathmgmt.Entry{Value: `C:\Windows;C:\orig`, Kind: pathmgmt.KindExpandSZ},
		Machine: pathmgmt.Entry{Value: `C:\Windows\system32`, Kind: pathmgmt.KindSZ},
	}
	pm := &fakePaths{}
	j := newJournal(t)
	if err := j.SetSnapshot(orig); err != nil {
		t.Fatalf("SetSnapshot 失败: %v", err)
	}

	if err := Rollback(j, pm, os.Stdout); err != nil {
		t.Fatalf("Rollback 失败: %v", err)
	}
	if len(pm.writes) != 2 {
		t.Fatalf("应写回两处，实际 %d", len(pm.writes))
	}
	if pm.writes[0].e.Value != orig.User.Value || pm.writes[0].e.Kind != orig.User.Kind {
		t.Errorf("用户级 PATH 未原样恢复: %+v", pm.writes[0].e)
	}
	if pm.writes[1].e.Value != orig.Machine.Value || pm.writes[1].e.Kind != orig.Machine.Kind {
		t.Errorf("系统级 PATH 未原样恢复: %+v", pm.writes[1].e)
	}
}

func TestPreflightMismatchStops(t *testing.T) {
	o := baseOpts(t, testPlan(plan.Item{
		Kind: "software", Name: "A", Status: plan.StatusPlanned, Tier: plan.TierWinget, Command: "cmd-a",
	}), &fakeExec{}, &fakePaths{})
	o.Preflight = func() (bool, bool, string) { return true, false, "网络刚断了" }

	if _, err := Run(o); err == nil {
		t.Fatal("环境与 dry-run 不一致时应停止")
	}
}

func TestJournalRecordsSteps(t *testing.T) {
	ex := &fakeExec{}
	j := newJournal(t)
	o := baseOpts(t, testPlan(plan.Item{
		Kind: "software", Name: "A", Status: plan.StatusPlanned, Tier: plan.TierLocal, Command: "cmd-a",
	}), ex, &fakePaths{})
	o.Journal = j

	if _, err := Run(o); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	loaded, err := LoadJournal(j.Path())
	if err != nil {
		t.Fatalf("LoadJournal 失败: %v", err)
	}
	if len(loaded.Steps) != 1 {
		t.Fatalf("应记录 1 步，实际 %d", len(loaded.Steps))
	}
	if loaded.Steps[0].Command != "cmd-a" || loaded.Steps[0].Status != StepOK {
		t.Errorf("步骤记录不对: %+v", loaded.Steps[0])
	}
	if !loaded.Finished {
		t.Error("结束后应标记 finished")
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`"C:\Program Files\x\setup.exe" /S`, []string{`C:\Program Files\x\setup.exe`, "/S"}},
		{`winget install --id Git.Git --exact`, []string{"winget", "install", "--id", "Git.Git", "--exact"}},
		{`git --version`, []string{"git", "--version"}},
	}
	for _, c := range cases {
		got, err := SplitCommand(c.in)
		if err != nil {
			t.Errorf("SplitCommand(%q) 失败: %v", c.in, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("SplitCommand(%q) = %#v，期望 %#v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("SplitCommand(%q)[%d] = %q，期望 %q", c.in, i, got[i], c.want[i])
			}
		}
	}
	if _, err := SplitCommand(`"unclosed`); err == nil {
		t.Error("引号未闭合应报错")
	}
}

func TestAppendMissing(t *testing.T) {
	got, added := pathmgmt.AppendMissing(`C:\Windows;D:\Tools`, []string{`c:\windows\`, `C:\New`})
	if len(added) != 1 || added[0] != `C:\New` {
		t.Errorf("应只追加 1 条（大小写与尾部反斜杠视为同一路径）: %#v", added)
	}
	if !strings.HasSuffix(got, `C:\New`) || !strings.HasPrefix(got, `C:\Windows;D:\Tools`) {
		t.Errorf("应在末尾追加且不改动原有顺序: %q", got)
	}

	got2, added2 := pathmgmt.AppendMissing("", []string{`C:\Only`})
	if got2 != `C:\Only` || len(added2) != 1 {
		t.Errorf("空原值应直接写入: %q %#v", got2, added2)
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"git version 2.47.1.windows.1", "2.47.1", true},
		{"v22.22.2", "22.22.2", true},
		{"Python 3.14.8", "3.14.8", true},
		{"node v20.11.1\r\n", "20.11.1", true},
		{"1.2.3-beta.1", "1.2.3-beta.1", true},
		{"usage: foo [options]", "", false},
		{"", "", false},
		{"build 26300", "", false}, // 单个数字不算版本
	}
	for _, c := range cases {
		got, ok := ParseVersion(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseVersion(%q) = %q,%v，期望 %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// seqExec 模拟「先没装、装完才有版本」：pre-verify 失败 → 执行 → post-verify 通过。
type seqExec struct {
	installed map[string]bool
	calls     []string
}

func (f *seqExec) Run(_ context.Context, command string, _ time.Duration) ExecResult {
	if f.installed == nil {
		f.installed = map[string]bool{}
	}
	f.calls = append(f.calls, command)
	switch command {
	case "install-git", "install-noise":
		f.installed[command] = true
		return ExecResult{ExitCode: 0, Output: "installed"}
	case "ver-git":
		if f.installed["install-git"] {
			return ExecResult{ExitCode: 0, Output: "git version 2.47.1.windows.1"}
		}
		return ExecResult{ExitCode: 1, Output: "command not found"}
	case "ver-noise":
		if f.installed["install-noise"] {
			return ExecResult{ExitCode: 0, Output: "usage: noise-cli [options]"}
		}
		return ExecResult{ExitCode: 1, Output: "command not found"}
	}
	return ExecResult{ExitCode: 0}
}

// C1/C2：verify 输出能解析出版本就回填 actual_version；解析不出来就留空 + 原始输出。
func TestVerifyOutputFillsVersions(t *testing.T) {
	j := newJournal(t)
	p := testPlan(
		plan.Item{Kind: "software", Name: "Git", Status: plan.StatusPlanned, Tier: plan.TierLocal,
			Command: "install-git", VerifyCmd: "ver-git"},
		plan.Item{Kind: "software", Name: "Noise", Status: plan.StatusPlanned, Tier: plan.TierLocal,
			Command: "install-noise", VerifyCmd: "ver-noise"},
	)
	o := baseOpts(t, p, &seqExec{}, &fakePaths{})
	o.Journal = j
	if _, err := Run(o); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	var git, noise *Step
	for i := range j.Steps {
		switch j.Steps[i].Name {
		case "Git":
			git = &j.Steps[i]
		case "Noise":
			noise = &j.Steps[i]
		}
	}
	if git == nil || noise == nil {
		t.Fatalf("步骤缺失: %+v", j.Steps)
	}
	if git.ActualVersion != "2.47.1" {
		t.Errorf("Git 的 actual_version = %q，期望 2.47.1", git.ActualVersion)
	}
	if noise.ActualVersion != "" {
		t.Errorf("解析不出来时不该编: %q", noise.ActualVersion)
	}
	if !strings.Contains(noise.VersionRaw, "usage: noise-cli") {
		t.Errorf("解析失败应保留原始输出: %q", noise.VersionRaw)
	}
}
