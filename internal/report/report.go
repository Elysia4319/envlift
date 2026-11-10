// Package report 把计划与执行结果写成 md + json 双份报告。
//
// 分工（D18）：
//   - json 是**结构化产物**，跨机器对比（diff）、GUI 渲染、外发都以它为准；
//   - md 是同一份数据的可读渲染，前端不解析它（只当原文展示）。
//
// 两条硬约束：
//   - 报告自带 report_schema；高版本一律拒绝渲染，不做降级猜测（同 D9）。
//   - 报告**一律脱敏**（用户名 → ${USER}，主机名 → ${HOST}），不提供 raw 开关：
//     它是用来跨机器对比、可能外发的产物，与 --plan-out 的 --raw-plan 区分开。
package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Elysia4319/envlift/internal/apply"
	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/plan"
)

// CurrentSchema 是当前报告格式版本。
const CurrentSchema = 1

// 报告无法渲染的两种原因：比本程序新（升级再说）与旧到没有版本号（重新生成）。
var (
	ErrSchemaTooNew = errors.New("报告由更高版本的 envlift 生成，请升级后查看")
	ErrSchemaTooOld = errors.New("报告缺少 report_schema，属于旧格式，请用当前版本重新生成一份")
)

// Kind 是报告的种类标识。
const Kind = "restore-report"

// Env 是报告生成时的目标机环境（用户名/主机名只有占位符）。
type Env struct {
	OS        string `json:"os"`
	Build     string `json:"build"`
	Arch      string `json:"arch"`
	Username  string `json:"username"`
	Hostname  string `json:"hostname"`
	WingetOK  bool   `json:"winget_ok"`
	NetworkOK bool   `json:"network_ok"`
	Elevated  bool   `json:"elevated"`
}

// Summary 是报告里的计数。条目与 PATH 合并计数，口径与计划汇总一致。
type Summary struct {
	Total                int  `json:"total"`
	Succeeded            int  `json:"succeeded"`
	Skipped              int  `json:"skipped"`
	Manual               int  `json:"manual"`
	BlockedNeedsDecision int  `json:"blocked_needs_decision"`
	Failed               int  `json:"failed"`
	PathWrites           int  `json:"path_writes"`
	AwaitingReboot       bool `json:"awaiting_reboot"`
}

// ItemResult 是报告里的一条软件/运行时结果（C3）。
type ItemResult struct {
	TargetID        string `json:"target_id"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	ExpectedVersion string `json:"expected_version,omitempty"`
	// BeforeVersion 是**计划生成时**目标机上的版本。
	BeforeVersion string `json:"before_version,omitempty"`
	// ActualVersion 是**执行后**从 verify 输出里解析出的版本（C1）。
	// 解析不出来就留空，并在 VersionUnparsed 里标明——留空可以，编不行。
	ActualVersion   string `json:"actual_version,omitempty"`
	VersionUnparsed bool   `json:"version_unparsed,omitempty"`
	// VersionRaw 是解析失败时的原始输出前缀（C2），便于人工判断。
	VersionRaw    string `json:"version_raw,omitempty"`
	Tier          string `json:"tier,omitempty"`
	Status        string `json:"status"`
	Command       string `json:"command,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`
	VerifyResult  string `json:"verify_result,omitempty"` // pass | fail | not_run
	FailureReason string `json:"failure_reason,omitempty"`
	NeedsAdmin    bool   `json:"needs_admin"`
	ExpectReboot  bool   `json:"expect_reboot"`
	// Decision 是本次用在这条上的决策动作（④），diff 时能解释「为什么两边不同」。
	Decision string `json:"decision,omitempty"`
	// Reason 是「为什么是这个状态」（manual/covered/blocked 的解释）。
	Reason string `json:"reason,omitempty"`
	// ManualHint 是 manual 项可直接照做的人工步骤（下载/账号/选项）。
	ManualHint string `json:"manual_hint,omitempty"`
}

// PathResult 是报告里的一条 PATH 变更。
type PathResult struct {
	TargetID string `json:"target_id"`
	Section  string `json:"section"`
	Mark     string `json:"mark"`
	Value    string `json:"value"`
	Status   string `json:"status,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// DecisionsSummary 是本次决策的摘要（C4）。
//
// 两台机器的报告对不上时，最常见的原因就是决策不同；没有这个摘要只能靠猜。
type DecisionsSummary struct {
	ApplyMode string         `json:"apply_mode,omitempty"` // all | skip_admin
	Actions   map[string]int `json:"actions,omitempty"`    // action → 条数
	Blocked   []string       `json:"blocked,omitempty"`    // 因未决策被跳过的项名
}

// Document 是报告的全部内容（json 主体）。
type Document struct {
	ReportSchema int              `json:"report_schema"`
	Kind         string           `json:"kind"`
	GeneratedAt  string           `json:"generated_at"`
	ConfigPath   string           `json:"config_path,omitempty"`
	Journal      string           `json:"journal,omitempty"`
	Env          Env              `json:"env"`
	PlanHash     string           `json:"plan_hash"`
	Mode         string           `json:"mode"` // dry-run | apply
	Summary      Summary          `json:"summary"`
	Items        []ItemResult     `json:"items"`
	PathChanges  []PathResult     `json:"path_changes"`
	Warnings     []string         `json:"warnings"`
	Decisions    DecisionsSummary `json:"decisions"`
}

// Input 是一次报告的输入。
type Input struct {
	Plan *plan.Plan
	// Apply 为 nil 表示这是 dry-run 报告（只预演未执行）。
	Apply *apply.Result
	// Steps 是 journal 里的执行步骤（apply 模式），用于填退出码与失败原因。
	Steps       []apply.Step
	JournalPath string
	ConfigPath  string
	// ApplyMode 是 confirmations.mode（all / skip_admin），dry-run 留空。
	ApplyMode string
	// Decisions 是本次提交的决策（可为空：从计划里的 Decision 字段也能反推）。
	Decisions plan.Decisions
	// Redactor 用来把真实用户名/主机名换回占位符。报告**永远**脱敏。
	Redactor config.Redactor
	Now      time.Time
}

// Paths 返回报告的两个落盘路径。
func Paths(dir string, now time.Time) (md, js string) {
	base := "restore-" + now.Format("20060102-150405")
	return filepath.Join(dir, base+".md"), filepath.Join(dir, base+".json")
}

// Write 写出 md 与 json 两份报告，返回两个路径。
func Write(dir string, in Input) (string, string, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("创建报告目录失败: %w", err)
	}
	mdPath, jsonPath := Paths(dir, now)
	doc := Build(in, now)

	if err := os.WriteFile(mdPath, []byte(RenderMarkdown(doc)), 0o644); err != nil {
		return "", "", fmt.Errorf("写 md 报告失败: %w", err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(jsonPath, append(data, '\n'), 0o644); err != nil {
		return "", "", fmt.Errorf("写 json 报告失败: %w", err)
	}
	return mdPath, jsonPath, nil
}

// Build 生成报告文档（纯函数：不读文件、不看环境）。md 与 json 都从它渲染，
// 保证两者同源同数据。
func Build(in Input, now time.Time) *Document {
	p := in.Plan
	// plan_hash 必须在脱敏**之前**算：它对应的是 apply 校验过的那份计划。
	hash := plan.Hash(p)

	doc := &Document{
		ReportSchema: CurrentSchema,
		Kind:         Kind,
		GeneratedAt:  now.Format(time.RFC3339),
		ConfigPath:   in.ConfigPath,
		Journal:      in.JournalPath,
		PlanHash:     hash,
		Mode:         "dry-run",
	}
	if in.Apply != nil {
		doc.Mode = "apply"
	}

	if p != nil {
		doc.Env = Env{
			OS: p.Target.OS, Build: p.Target.Build, Arch: p.Target.Arch,
			Username: config.UserPlaceholder, Hostname: config.HostPlaceholder,
			WingetOK: p.Target.WingetOK, NetworkOK: p.Target.NetworkOK, Elevated: p.Target.Elevated,
		}
		doc.Warnings = append(doc.Warnings, p.Warnings...)
		doc.Items = buildItems(p, in.Steps)
		doc.PathChanges = buildPathResults(p)
		if p.Blocked != "" {
			doc.Warnings = append(doc.Warnings, "计划被阻止："+p.Blocked)
		}
	}
	doc.Decisions = buildDecisions(in.Decisions, p, in.ApplyMode)
	doc.Summary = summarize(doc, in.Apply)

	// C5：一律脱敏，没有开关。
	if p != nil && (in.Redactor.Username == "" || in.Redactor.Hostname == "") {
		user := in.Redactor.Username
		if user == "" {
			user = p.Target.Username
		}
		host := in.Redactor.Hostname
		if host == "" {
			host = p.Target.Hostname
		}
		in.Redactor = config.NewRedactor(user, host)
	}
	in.Redactor.RedactStrings(doc)
	return doc
}

func buildItems(p *plan.Plan, steps []apply.Step) []ItemResult {
	byKey := make(map[string]apply.Step, len(steps))
	for _, s := range steps {
		if s.Kind == "" || s.Kind == "path" {
			continue
		}
		byKey[strconv.Itoa(s.Index)+"|"+s.Kind] = s
	}

	out := make([]ItemResult, 0, len(p.Items))
	for i, it := range p.Items {
		r := ItemResult{
			TargetID:        it.TargetID,
			Kind:            it.Kind,
			Name:            it.Name,
			ExpectedVersion: it.Expected,
			BeforeVersion:   it.Current,
			Tier:            it.Tier,
			Status:          it.Status,
			Command:         it.Command,
			NeedsAdmin:      it.NeedsAdmin,
			ExpectReboot:    it.ExpectReboot,
			Decision:        it.Decision,
			Reason:          it.Reason,
			ManualHint:      it.ManualHint,
		}
		if s, ok := byKey[strconv.Itoa(i)+"|"+it.Kind]; ok {
			code := s.ExitCode
			r.ExitCode = &code
			// 降级执行后，实际走的层级/命令以 journal 为准，不能继续显示计划里的 winget。
			// 但只有「真的执行过」的步骤才有这个语义：幂等跳过时 journal 记的是
			// verify 命令，覆盖上去会让报告把校验命令当成安装命令。
			if s.Status == apply.StepOK || s.Status == apply.StepFailed {
				if s.Tier != "" {
					r.Tier = s.Tier
				}
				if s.Command != "" {
					r.Command = s.Command
				}
			}
			switch s.Status {
			case apply.StepOK:
				r.Status = "succeeded"
			case apply.StepFailed:
				r.Status = "failed"
				r.FailureReason = s.Output
			case apply.StepSkipped:
				r.Status = "skipped"
			case apply.StepBlocked:
				r.Status = "blocked"
				r.FailureReason = s.Output
			}
			switch {
			case it.VerifyCmd == "":
				r.VerifyResult = "not_run"
			case s.Status == apply.StepOK:
				r.VerifyResult = "pass"
			case s.Status == apply.StepFailed:
				r.VerifyResult = "fail"
			}
			r.ActualVersion = s.ActualVersion
			r.VersionRaw = s.VersionRaw
			// verify 跑过且成功、却解析不出版本 → 明确标 unparsed（不是"没跑"）。
			if it.VerifyCmd != "" && s.ActualVersion == "" {
				r.VersionUnparsed = true
			}
		}
		out = append(out, r)
	}
	return out
}

func buildPathResults(p *plan.Plan) []PathResult {
	out := make([]PathResult, 0, len(p.Path))
	for _, ch := range p.Path {
		out = append(out, PathResult{
			TargetID: ch.TargetID, Section: ch.Section, Mark: ch.Mark,
			Value: ch.Value, Status: ch.Status, Decision: ch.Decision, Reason: ch.Reason,
		})
	}
	return out
}

// buildDecisions 汇总本次决策（C4）：mode、各 action 计数、被跳过的项名。
func buildDecisions(ds plan.Decisions, p *plan.Plan, applyMode string) DecisionsSummary {
	out := DecisionsSummary{ApplyMode: applyMode, Actions: map[string]int{}}
	seen := map[string]bool{}
	add := func(id, action string) {
		if action == "" || seen[id] {
			return
		}
		seen[id] = true
		out.Actions[action]++
	}
	for _, d := range ds {
		add(d.TargetID, d.Action)
	}
	// 从计划里也能反推（例如 CLI 用了 decisions 文件、调用方没把 ds 传进来）。
	if p != nil {
		for _, it := range p.Items {
			add(it.TargetID, it.Decision)
			if it.Status == plan.StatusBlockedNeedsDecision {
				out.Blocked = append(out.Blocked, it.Name)
			}
		}
		for _, ch := range p.Path {
			add(ch.TargetID, ch.Decision)
			if ch.Status == plan.StatusBlockedNeedsDecision {
				out.Blocked = append(out.Blocked, "PATH["+ch.Section+"] "+ch.Value)
			}
		}
	}
	if len(out.Actions) == 0 {
		out.Actions = nil
	}
	return out
}

func summarize(doc *Document, res *apply.Result) Summary {
	var s Summary
	for _, it := range doc.Items {
		s.Total++
		switch it.Status {
		case "succeeded":
			s.Succeeded++
		case "failed":
			s.Failed++
		case "skipped":
			s.Skipped++
		case plan.StatusManual:
			s.Manual++
		case plan.StatusBlockedNeedsDecision:
			s.BlockedNeedsDecision++
		case "blocked":
			s.Skipped++
		}
	}
	for _, ch := range doc.PathChanges {
		s.Total++
		switch {
		case ch.Mark == plan.MarkAdd || ch.Mark == plan.MarkRewrite:
			s.PathWrites++
			if doc.Mode == "apply" {
				s.Succeeded++
			}
		case ch.Status == plan.StatusBlockedNeedsDecision:
			s.BlockedNeedsDecision++
		case ch.Status == plan.PathStatusSkipped:
			s.Skipped++
		case ch.Mark == plan.MarkInvalid:
			s.Skipped++
		}
	}
	if res != nil && strings.Contains(res.Stopped, "重启") {
		s.AwaitingReboot = true
	}
	return s
}

// Parse 解析报告 json。版本高于本程序或没有版本号都算「不可渲染」。
func Parse(data []byte) (*Document, error) {
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析报告失败: %w", err)
	}
	if doc.ReportSchema > CurrentSchema {
		return &doc, ErrSchemaTooNew
	}
	if doc.ReportSchema <= 0 {
		return &doc, ErrSchemaTooOld
	}
	return &doc, nil
}
