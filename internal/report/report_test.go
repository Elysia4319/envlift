package report

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Elysia4319/envlift/internal/apply"
	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/plan"
)

func samplePlan() *plan.Plan {
	return &plan.Plan{
		App: "envlift", Kind: "restore-plan", Schema: 1,
		GeneratedAt: "2026-01-01T00:00:00Z",
		Target: plan.Target{
			OS: "Windows 11", Build: "26100", Arch: "amd64",
			Username: "${USER}", Hostname: "${HOST}",
			Elevated: false, WingetOK: true, NetworkOK: true,
		},
		Items: []plan.Item{
			{TargetID: "a1", Kind: "software", Name: "Git", Expected: "2.56.0", Current: "2.40.0",
				Tier: plan.TierWinget, Status: plan.StatusPlanned, MatchedBy: "winget_id",
				Command: "winget install --id Git.Git", VerifyCmd: "git --version"},
			{TargetID: "a2", Kind: "software", Name: "Adobe Thing", Status: plan.StatusManual,
				MatchedBy: "none", Reason: "无 winget 包映射，且没有配方覆盖",
				ManualHint: "需厂商账号登录后安装"},
			{TargetID: "a3", Kind: "software", Name: "Tool 1.0", Status: plan.StatusCovered,
				Reason: "被同配方的 Tool 2.0（2.0）覆盖，跳过（A1 去重）"},
			{TargetID: "a4", Kind: "software", Name: "待拍板的工具", Status: plan.StatusBlockedNeedsDecision,
				Reason: "命中多个配方，未决策"},
		},
		Path: []plan.PathChange{
			{TargetID: "p1", Section: "user", Mark: plan.MarkAdd, Value: `C:\new`},
			{TargetID: "p2", Section: "machine", Mark: plan.MarkPending, Value: `Z:\nowhere`,
				Reason: "目标机无该盘符", Status: plan.StatusBlockedNeedsDecision},
		},
	}
}

func TestWriteProducesBothFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	md, js, err := Write(dir, Input{Plan: samplePlan(), ConfigPath: "envlift.yaml", Now: now})
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	if filepath.Base(md) != "restore-20260102-030405.md" {
		t.Errorf("md 文件名不对: %s", md)
	}
	if filepath.Base(js) != "restore-20260102-030405.json" {
		t.Errorf("json 文件名不对: %s", js)
	}

	mdData, _ := os.ReadFile(md)
	s := string(mdData)
	for _, want := range []string{
		"# envlift 还原报告", "## 1. 概要", "## 2. 逐项结果",
		"## 3. PATH 变更", "## 4. 待人工处理", "## 5. 风险与提示", "## 6. 后续建议",
		"预演（未执行）", "需厂商账号登录后安装", "被同配方的",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("md 缺少内容: %q", want)
		}
	}

	data, _ := os.ReadFile(js)
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("解析报告失败: %v", err)
	}
	if doc.ReportSchema != CurrentSchema || doc.Kind != Kind || doc.Mode != "dry-run" {
		t.Errorf("报告头部不对: schema=%d kind=%q mode=%q", doc.ReportSchema, doc.Kind, doc.Mode)
	}
	if len(doc.Items) != 4 || len(doc.PathChanges) != 2 {
		t.Errorf("条目数不对: %d items, %d paths", len(doc.Items), len(doc.PathChanges))
	}
	if doc.PlanHash == "" || len(doc.PlanHash) != 64 {
		t.Errorf("plan_hash 不对: %q", doc.PlanHash)
	}
	if doc.Env.Username != config.UserPlaceholder || doc.Env.Hostname != config.HostPlaceholder {
		t.Errorf("env 里应是占位符: %+v", doc.Env)
	}
	if doc.Summary.Manual != 1 || doc.Summary.BlockedNeedsDecision != 2 || doc.Summary.PathWrites != 1 {
		t.Errorf("摘要口径不对: %+v", doc.Summary)
	}
	if len(doc.Decisions.Blocked) != 2 {
		t.Errorf("未决策项应进决策摘要: %+v", doc.Decisions.Blocked)
	}
}

func TestUnverifiedStepIsNotSucceeded(t *testing.T) {
	p := &plan.Plan{
		App: "envlift", Kind: "restore-plan", Schema: config.CurrentSchema,
		Target: plan.Target{OS: "Windows 11", Arch: "amd64", Username: "${USER}", Hostname: "${HOST}"},
		Items: []plan.Item{{
			TargetID: "c1", Kind: plan.KindCondaEnv, Name: "py311",
			Tier: plan.TierConda, Status: plan.StatusPlanned,
			Command: "conda install", VerifyCmd: "conda list --json",
		}},
	}
	doc := Build(Input{
		Plan:  p,
		Apply: &apply.Result{Unverified: 1},
		Steps: []apply.Step{{
			Index: 0, Kind: plan.KindCondaEnv, Name: "py311", Tier: plan.TierConda,
			Status: apply.StepUnverified, UnverifiedReason: "numpy（缺失）",
		}},
		Now: time.Now(),
	}, time.Now())
	if doc.Items[0].Status != "unverified" {
		t.Fatalf("状态=%q，期望 unverified", doc.Items[0].Status)
	}
	if doc.Summary.Unverified != 1 || doc.Summary.Succeeded != 0 {
		t.Fatalf("summary 不对: %+v", doc.Summary)
	}
}

func TestApplyModeRecordsSteps(t *testing.T) {
	dir := t.TempDir()
	p := samplePlan()
	ok := 0
	fail := 3
	_, js, err := Write(dir, Input{
		Plan: p,
		Apply: &apply.Result{Executed: 1, Failed: 1, PathAdded: 1,
			Stopped: "条目 X 完成后需要重启"},
		Steps: []apply.Step{
			{Index: 0, Kind: "software", Name: "Git", Status: apply.StepOK, ExitCode: ok,
				ActualVersion: "2.56.0", Tier: "local", Command: `"C:\proj\installers\Git-2.56.0-64-bit.exe" /VERYSILENT`},
			{Index: 1, Kind: "software", Name: "Adobe Thing", Status: apply.StepSkipped},
			{Index: 2, Kind: "software", Name: "Tool 1.0", Status: apply.StepFailed, ExitCode: fail, Output: "退出码 3"},
		},
		JournalPath: "reports/.envlift-journal-x.json",
		Now:         time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	data, _ := os.ReadFile(js)
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Mode != "apply" {
		t.Errorf("mode = %q", doc.Mode)
	}
	byID := map[string]ItemResult{}
	for _, it := range doc.Items {
		byID[it.TargetID] = it
	}
	if got := byID["a1"]; got.Status != "succeeded" || got.ExitCode == nil || *got.ExitCode != 0 ||
		got.VerifyResult != "pass" || got.ActualVersion != "2.56.0" || got.BeforeVersion != "2.40.0" {
		t.Errorf("成功项记录不对（期望/执行前/执行后都要留）: %+v", got)
	}
	if got := byID["a1"]; got.Tier != "local" || !strings.Contains(got.Command, "Git-2.56.0-64-bit.exe") {
		t.Errorf("报告应记录实际降级层级/命令: %+v", got)
	}
	if got := byID["a3"]; got.Status != "failed" || got.ExitCode == nil || *got.ExitCode != 3 || !strings.Contains(got.FailureReason, "退出码 3") {
		t.Errorf("失败项记录不对: %+v", got)
	}
	if !doc.Summary.AwaitingReboot {
		t.Error("停在中途且原因是重启时应标 awaiting_reboot")
	}
	if doc.Summary.Failed != 1 || doc.Summary.Succeeded != 2 { // 1 个软件 + 1 条 PATH
		t.Errorf("摘要 = %+v", doc.Summary)
	}
}

// 幂等跳过时 journal 的 command 是 verify 命令；报告不能把它当成安装命令显示。
func TestSkippedStepKeepsPlanCommand(t *testing.T) {
	p := samplePlan()
	p.Items[0].Tier = plan.TierWinget
	p.Items[0].Command = "winget install --id Git.Git"
	p.Items[0].VerifyCmd = "git --version"
	doc := Build(Input{
		Plan: p,
		Steps: []apply.Step{
			{Index: 0, Kind: "software", Name: "Git", Tier: plan.TierWinget,
				Command: "git --version", Status: apply.StepSkipped},
		},
	}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	got := doc.Items[0]
	if got.Status != "skipped" {
		t.Fatalf("status = %q", got.Status)
	}
	if got.Tier != plan.TierWinget || got.Command != "winget install --id Git.Git" {
		t.Errorf("跳过项应保留计划里的层级/命令: tier=%q command=%q", got.Tier, got.Command)
	}
}

// C5：报告一律脱敏，真实用户名/主机名不许出现。
func TestReportIsAlwaysRedacted(t *testing.T) {
	p := samplePlan()
	p.Target.Username = "alice"
	p.Target.Hostname = "ALICE-PC"
	p.Items[0].Command = `"C:\Users\alice\AppData\Local\prog.exe" /S`
	p.Items[0].Name = "alice's tool"

	doc := Build(Input{
		Plan:     p,
		Redactor: config.NewRedactor("alice", "ALICE-PC"),
	}, time.Now())

	data, _ := json.Marshal(doc)
	s := string(data)
	if strings.Contains(strings.ToLower(s), "alice") {
		t.Fatalf("报告里仍有真实用户名: %s", s)
	}
	if !strings.Contains(s, config.UserPlaceholder) {
		t.Error("应出现 ${USER} 占位符")
	}
}

func TestBuildRedactsWithoutExplicitRedactor(t *testing.T) {
	p := samplePlan()
	p.Target.Username = "alice"
	p.Target.Hostname = "ALICE-PC"
	p.Items[0].Command = `"C:\Users\alice\AppData\Local\prog.exe" /S`
	p.Items[0].Name = "alice's tool"

	doc := Build(Input{Plan: p}, time.Now())
	data, _ := json.Marshal(doc)
	s := string(data)
	if strings.Contains(strings.ToLower(s), "alice") {
		t.Fatalf("零值 Redactor 仍泄露真实用户名: %s", s)
	}
	if !strings.Contains(s, config.UserPlaceholder) {
		t.Error("应出现 ${USER} 占位符")
	}
}

func TestBuildRedactsPartialRedactor(t *testing.T) {
	p := samplePlan()
	p.Target.Username = "alice"
	p.Target.Hostname = "ALICE-PC"
	p.Items[0].Command = `"C:\Users\alice\AppData\Local\prog.exe" /S`
	p.Items[0].Name = "alice's tool"
	p.Items[0].ManualHint = "run on ALICE-PC"

	doc := Build(Input{
		Plan:     p,
		Redactor: config.NewRedactor("", "ALICE-PC"),
	}, time.Now())
	data, _ := json.Marshal(doc)
	s := string(data)
	for _, secret := range []string{"alice", "ALICE-PC"} {
		if strings.Contains(strings.ToLower(s), strings.ToLower(secret)) {
			t.Errorf("部分 Redactor 仍泄露真实值 %q: %s", secret, s)
		}
	}
	for _, want := range []string{config.UserPlaceholder, config.HostPlaceholder} {
		if !strings.Contains(s, want) {
			t.Errorf("应出现占位符 %q", want)
		}
	}
}

// TestWriteRedactsOnDisk 验证报告落盘后的 md/json 都已完成脱敏，
// 而不是只在内存 Document 上生效。
func TestWriteRedactsOnDisk(t *testing.T) {
	p := samplePlan()
	p.Target.Username = "alice"
	p.Target.Hostname = "ALICE-PC"
	p.Items[0].Name = "alice's tool"
	p.Items[0].Command = `"C:\Users\alice\AppData\Local\prog.exe" /S`
	p.Items[0].ManualHint = "run as alice on ALICE-PC"
	p.Path[0].Value = `C:\Users\alice\bin`

	md, js, err := Write(t.TempDir(), Input{
		Plan:  p,
		Apply: &apply.Result{Executed: 1},
		Steps: []apply.Step{{
			Index: 0, Kind: "software", Name: "alice's tool",
			Status: apply.StepFailed, ExitCode: 1,
			Output:     "failed for alice on ALICE-PC",
			VersionRaw: "version output for alice on ALICE-PC",
		}},
		JournalPath: `C:\Users\alice\reports\.envlift-journal-x.json`,
		ConfigPath:  `C:\Users\alice\envlift.yaml`,
		Redactor:    config.NewRedactor("alice", "ALICE-PC"),
		Now:         time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	for _, path := range []string{md, js} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", path, err)
		}
		s := string(data)
		for _, secret := range []string{"alice", "ALICE-PC"} {
			if strings.Contains(strings.ToLower(s), strings.ToLower(secret)) {
				t.Errorf("%s 仍含真实值 %q: %s", filepath.Base(path), secret, s)
			}
		}
		for _, want := range []string{config.UserPlaceholder, config.HostPlaceholder} {
			if !strings.Contains(s, want) {
				t.Errorf("%s 缺少占位符 %q", filepath.Base(path), want)
			}
		}
	}
}

// C6：md 与 json 同源——json 里的每条条目与计数都必须能在 md 里找到。
func TestMarkdownMatchesJSON(t *testing.T) {
	doc := Build(Input{Plan: samplePlan()}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	md := RenderMarkdown(doc)
	for _, it := range doc.Items {
		if !strings.Contains(md, it.Name) {
			t.Errorf("md 缺少条目 %q", it.Name)
		}
		if !strings.Contains(md, it.Status) {
			t.Errorf("md 缺少状态 %q", it.Status)
		}
	}
	for _, ch := range doc.PathChanges {
		if !strings.Contains(md, ch.Value) {
			t.Errorf("md 缺少 PATH %q", ch.Value)
		}
	}
	if !strings.Contains(md, "| 因未决策被跳过 | 2 |") {
		t.Error("md 概要与 json summary 不一致")
	}
}

func TestParseRejectsUnknownSchema(t *testing.T) {
	newer, _ := json.Marshal(Document{ReportSchema: CurrentSchema + 1})
	if _, err := Parse(newer); !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("更高版本应拒绝: %v", err)
	}
	old, _ := json.Marshal(map[string]any{"app": "envlift", "plan": map[string]any{}})
	if _, err := Parse(old); !errors.Is(err, ErrSchemaTooOld) {
		t.Errorf("没有版本号的旧报告应拒绝: %v", err)
	}
	if _, err := Parse([]byte("not json")); err == nil {
		t.Error("非 json 应报错")
	}
}

// C2：verify 输出解析不出版本时留空并标记，不编。
func TestUnparsedVersionIsMarked(t *testing.T) {
	p := samplePlan()
	p.Items = []plan.Item{{
		TargetID: "x1", Kind: "software", Name: "X", Expected: "1.0.0", Current: "0.9.0",
		Status: plan.StatusPlanned, VerifyCmd: "x --version",
	}}
	doc := Build(Input{
		Plan:  p,
		Apply: &apply.Result{Executed: 1},
		Steps: []apply.Step{{
			Index: 0, Kind: "software", Name: "X", Status: apply.StepOK,
			VersionRaw: "usage: x [options]",
		}},
	}, time.Now())
	it := doc.Items[0]
	if it.ActualVersion != "" {
		t.Errorf("解析不出来不该有版本: %q", it.ActualVersion)
	}
	if !it.VersionUnparsed {
		t.Error("应标 version_unparsed")
	}
	if !strings.Contains(it.VersionRaw, "usage") {
		t.Errorf("应保留原始输出: %q", it.VersionRaw)
	}
	if it.BeforeVersion != "0.9.0" {
		t.Errorf("执行前版本应保留: %q", it.BeforeVersion)
	}
}

func TestReportCarriesInstallDirAndMarkdownColumn(t *testing.T) {
	p := samplePlan()
	p.Items[0].InstallDir = `D:\Apps\Git`
	p.Items[0].InstallDirApplied = true
	p.Items[0].Reinstall = true
	p.Items[0].VerifyCmd = "git --version"

	doc := Build(Input{Plan: p}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if doc.Items[0].InstallDir != `D:\Apps\Git` || !doc.Items[0].InstallDirApplied {
		t.Errorf("报告未保留安装目录: %+v", doc.Items[0])
	}
	if !doc.Items[0].Reinstall {
		t.Error("报告应标记重装")
	}
	md := RenderMarkdown(doc)
	if !strings.Contains(md, "安装目录") {
		t.Error("markdown 表缺少「安装目录」列")
	}
	if !strings.Contains(md, `D:\Apps\Git`) {
		t.Error("markdown 未显示安装目录")
	}
	if !strings.Contains(md, "重装") {
		t.Error("markdown 未标记重装")
	}
	if !strings.Contains(md, "verify 命令可能仍指向旧路径") {
		t.Error("指定目录且存在 verify 时应在风险提示中提醒")
	}
}

func TestReportShowsInstallDirVerified(t *testing.T) {
	p := samplePlan()
	p.Items[0].InstallDir = `D:\Apps\Git`
	p.Items[0].InstallDirApplied = true
	doc := Build(Input{
		Plan: p,
		Steps: []apply.Step{{Index: 0, Kind: "software", Status: apply.StepOK,
			InstallDirVerified: true, InstallDirActual: `D:\Apps\Git`}},
	}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	if !doc.Items[0].InstallDirVerified {
		t.Error("报告应保留目录已验证状态")
	}
	if !strings.Contains(RenderMarkdown(doc), "目录已验证") {
		t.Error("markdown 应显示目录已验证")
	}
}

func TestReportShowsInstallDirUnverifiedActual(t *testing.T) {
	p := samplePlan()
	p.Items[0].InstallDir = `D:\Apps\Git`
	p.Items[0].InstallDirApplied = true
	doc := Build(Input{
		Plan: p,
		Steps: []apply.Step{{Index: 0, Kind: "software", Status: apply.StepOK,
			InstallDirVerified: false, InstallDirActual: `D:\Other`}},
	}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	if doc.Items[0].InstallDirVerified || doc.Items[0].InstallDirActual != `D:\Other` {
		t.Errorf("报告应保留未验证与实际目录: %+v", doc.Items[0])
	}
	md := RenderMarkdown(doc)
	if !strings.Contains(md, "目录未验证") || !strings.Contains(md, `实际：D:\Other`) {
		t.Errorf("markdown 应显示未验证与实际目录: %s", md)
	}
}

// 账本命中时计划本身已带 actual_dir；没有 apply 步骤也要能进报告。
func TestReportCarriesPlanInstallDirActual(t *testing.T) {
	p := samplePlan()
	p.Items[0].InstallDir = `D:\Apps\Git`
	p.Items[0].InstallDirApplied = true
	p.Items[0].InstallDirVerified = true
	p.Items[0].InstallDirActual = `D:\Apps\Git`

	doc := Build(Input{Plan: p}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if doc.Items[0].InstallDirActual != `D:\Apps\Git` {
		t.Errorf("报告应从计划带出实际目录: %+v", doc.Items[0])
	}
	if !strings.Contains(RenderMarkdown(doc), "目录已验证") {
		t.Error("markdown 应显示目录已验证")
	}
}
