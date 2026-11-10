package plan

import (
	"fmt"
	"strings"

	"github.com/Elysia4319/envlift/internal/targetid"
)

// 本文件是 ④ 的决策类型与校验（B1/B3/B4/B5/B6）。
//
// 决策是**机器相关的选择**，不是环境快照的一部分（D3/D17）：envlift.yaml 仍是
// 可版本化的单一快照，envlift.decisions.yaml 与配置里的 x-excluded 一样，是
// 本机侧的可丢弃覆盖层，因此进 .gitignore、不进 schema。
//
// 类型放在 plan 包（与 Plan 同包）是刻意的：api 层只做 JSON 反序列化与错误映射，
// 禁止另造一套 pending / decision 结构——两套结构迟早会在字段语义上分叉。

// 决策的条目类别。取值与 targetid.Kind* 一致，看到 kind 就知道去哪个数组里找。
const (
	DecKindSoftware = targetid.KindSoftware
	DecKindRuntime  = targetid.KindRuntime
	DecKindPath     = targetid.KindPath
)

// 决策动作（B2 取值表）。前端与后端必须逐字一致，docs/decisions.md D17 是同一张表。
//
//	kind      action                  params                  说明
//
// software  select                  {version} / {installer} / {recipe}  一配方命中多包 / 一条目命中多配方
// software  override_representative —                       把被覆盖条目提为代表（shared_recipe）
// software  skip                    —                       本次不装
// software  mark_manual             —                       转人工，报告给人工步骤
// runtime   select                  {version}               多版本共存（multi_version=true）选装哪个
// runtime   skip / mark_manual      —                       版本管理器托管的走 A6，给 fnm/nvm 命令
// path      add                     —                       + 默认
// path      accept                  —                       ~ 用户名重写，D4-R1 唯一自动项，默认接受
// path      rewrite                 {new_value}             ? 无同盘符，指定新盘符或按原盘符建目录
// path      skip                    —                       不写入
// path      mark_invalid            —                       ! 失效，默认 skip，显式标记便于下次识别
//
// =（已存在）无需决策，不出现在待确认区。
const (
	ActionSelect                 = "select"
	ActionOverrideRepresentative = "override_representative"
	ActionSkip                   = "skip"
	ActionMarkManual             = "mark_manual"
	ActionAdd                    = "add"
	ActionAccept                 = "accept"
	ActionRewrite                = "rewrite"
	ActionMarkInvalid            = "mark_invalid"
)

// 决策相关的稳定错误码（D4）。前端按 code 分支，不做自然语言匹配。
const (
	CodeInvalidDecisions     = "invalid_decisions"
	CodeDecisionTargetAbsent = "decision_target_not_found"
	CodeInvalidDecisionParam = "invalid_decision_params"
)

// 候选值的类别：决策 select 的参数必须与它匹配。
const (
	CandidateInstaller = "installer"
	CandidateVersion   = "version"
	// CandidateRecipe 表示该条目命中了多个配方（M4）。用 select{recipe} 指定其中一个；
	// 不指定就只能 skip / mark_manual，校验阶段会明确拒绝并列出候选配方名。
	CandidateRecipe = "recipe"
)

// DecisionParams 是 action 的附加参数。
//
// 用指针字段 + 结构体本身可空，才能严格区分「没传 params」「传了空 params」
// 与「传了值」三种情况。B3 要求前两种都必须报错而不是猜。
type DecisionParams struct {
	// Version 给 select 用：选哪个版本。
	Version *string `yaml:"version,omitempty" json:"version,omitempty"`
	// Installer 给 select 用：一配方命中多个安装包时选哪个文件。
	Installer *string `yaml:"installer,omitempty" json:"installer,omitempty"`
	// Recipe 给 select 用：一条目命中多个配方时选哪个（M4）。
	Recipe *string `yaml:"recipe,omitempty" json:"recipe,omitempty"`
	// NewValue 给 path.rewrite 用：新的路径值。
	NewValue *string `yaml:"new_value,omitempty" json:"new_value,omitempty"`
}

// Empty 报告 params 是否一个值都没有（nil 与空结构等价）。
func (p *DecisionParams) Empty() bool {
	return p == nil || (p.Version == nil && p.Installer == nil && p.Recipe == nil && p.NewValue == nil)
}

// Decision 是一条决策。
//
// B5：除 target_id 外必须同时存人类可读的项名（name / version / value），
// 便于手改与排查；校验一律以 target_id 为准，项名只作提示，不一致时以配置为准
// 并在计划的 warnings 里标注。
type Decision struct {
	TargetID string          `yaml:"target_id" json:"target_id"`
	Kind     string          `yaml:"kind" json:"kind"`
	Action   string          `yaml:"action" json:"action"`
	Params   *DecisionParams `yaml:"params,omitempty" json:"params,omitempty"`
	// 以下三项只给人看，不参与校验。
	Name    string `yaml:"name,omitempty" json:"name,omitempty"`
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	Value   string `yaml:"value,omitempty" json:"value,omitempty"`
}

// Decisions 是一组决策。api 的 payload 字段与 yaml 文件里的 decisions 数组同构。
type Decisions []Decision

// DecisionsFile 是 envlift.decisions.yaml 的根结构。
//
// 与配置一样带 app/schema：手写文件写错了版本时，宁可拒绝也不要按猜的解析。
type DecisionsFile struct {
	App       string    `yaml:"app" json:"app"`
	Schema    int       `yaml:"schema" json:"schema"`
	Decisions Decisions `yaml:"decisions" json:"decisions"`
}

// DecisionIssue 是一条决策存在的问题。
type DecisionIssue struct {
	Index    int    `json:"index"` // 从 1 开始，与人的数法一致
	TargetID string `json:"target_id,omitempty"`
	Code     string `json:"code"`
	Reason   string `json:"reason"`
}

// DecisionErrors 是一组决策问题的集合。
//
// 一次把所有问题都列出来：只报第一条会逼用户「改一条、重跑、再发现下一条」。
type DecisionErrors struct {
	Issues []DecisionIssue
}

func (e *DecisionErrors) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, is := range e.Issues {
		if is.TargetID != "" {
			parts = append(parts, fmt.Sprintf("第 %d 条决策（%s）：%s", is.Index, is.TargetID, is.Reason))
			continue
		}
		parts = append(parts, fmt.Sprintf("第 %d 条决策：%s", is.Index, is.Reason))
	}
	return strings.Join(parts, "；")
}

// PrimaryCode 返回信封里用的错误码：全部同码就用该码，混合则退回 invalid_decisions。
func (e *DecisionErrors) PrimaryCode() string {
	if len(e.Issues) == 0 {
		return CodeInvalidDecisions
	}
	code := e.Issues[0].Code
	for _, is := range e.Issues[1:] {
		if is.Code != code {
			return CodeInvalidDecisions
		}
	}
	return code
}

// ValidateDecisions 校验决策能否应用到计划 p 上。
//
// p 必须是「尚未应用决策」的计划：校验要看候选值、同组关系与原始状态。
// 一次列出全部问题，不短路在第一条。
func ValidateDecisions(p *Plan, ds Decisions) *DecisionErrors {
	if p == nil || len(ds) == 0 {
		return nil
	}
	errs := &DecisionErrors{}
	seen := make(map[string]int, len(ds))
	for i, d := range ds {
		idx := i + 1
		report := func(code, format string, a ...any) {
			errs.Issues = append(errs.Issues, DecisionIssue{
				Index: idx, TargetID: d.TargetID, Code: code, Reason: fmt.Sprintf(format, a...),
			})
		}
		if strings.TrimSpace(d.TargetID) == "" {
			report(CodeInvalidDecisions, "缺少 target_id")
			continue
		}
		if prev, ok := seen[d.TargetID]; ok {
			report(CodeInvalidDecisions, "与第 %d 条决策指向同一个 target_id；同一个条目只能有一条决策", prev)
			continue
		}
		seen[d.TargetID] = idx

		it, pathIdx := p.findTarget(d.TargetID)
		if it == nil && pathIdx < 0 {
			if p.excludedTargets[d.TargetID] {
				report(CodeDecisionTargetAbsent, "该条目已被排除（x-excluded），不能对它下决策")
			} else {
				report(CodeDecisionTargetAbsent, "计划中找不到这个 target_id（配置或环境已变化，请重新生成计划）")
			}
			continue
		}

		kind := DecKindPath
		if it != nil {
			kind = it.Kind
		}
		if d.Kind != kind {
			report(CodeInvalidDecisions, "kind=%q，但该 target_id 属于 %q", d.Kind, kind)
			continue
		}

		switch kind {
		case DecKindSoftware, DecKindRuntime:
			p.validateItemDecision(d, it, report)
		case DecKindPath:
			p.validatePathDecision(d, p.Path[pathIdx], report)
		}
	}
	if len(errs.Issues) == 0 {
		return nil
	}
	return errs
}

func (p *Plan) validateItemDecision(d Decision, it *Item, report func(code, format string, a ...any)) {
	switch d.Action {
	case ActionSkip, ActionMarkManual:
		if !d.Params.Empty() {
			report(CodeInvalidDecisionParam, "action=%s 不接受 params", d.Action)
		}
	case ActionOverrideRepresentative:
		if it.Kind != DecKindSoftware {
			report(CodeInvalidDecisions, "override_representative 只适用于 software 条目，当前是 %s", it.Kind)
			return
		}
		if !d.Params.Empty() {
			report(CodeInvalidDecisionParam, "action=%s 不接受 params", d.Action)
			return
		}
		if it.RecipeName == "" {
			report(CodeInvalidDecisions, "该条目没有共用配方的同组条目（shared_recipe），无法改选代表")
			return
		}
		if len(p.groupMembers(it.TargetID)) < 2 {
			report(CodeInvalidDecisions, "该配方在计划里只有这一个条目，没有可改选的代表")
		}
	case ActionSelect:
		p.validateSelect(d, it, report)
	default:
		report(CodeInvalidDecisions, "未知 action=%q；%s 允许的动作：%s",
			d.Action, it.Kind, strings.Join(allowedActions(it.Kind), ", "))
	}
}

func (p *Plan) validateSelect(d Decision, it *Item, report func(code, format string, a ...any)) {
	pr := d.Params
	if pr.Empty() {
		report(CodeInvalidDecisionParam, "action=select 必须带 params（version 或 installer）")
		return
	}
	hasVer, hasIns, hasRec := pr.Version != nil, pr.Installer != nil, pr.Recipe != nil
	if pr.NewValue != nil {
		report(CodeInvalidDecisionParam, "action=select 不接受 params.new_value")
		return
	}
	given := 0
	for _, ok := range []bool{hasVer, hasIns, hasRec} {
		if ok {
			given++
		}
	}
	if given != 1 {
		report(CodeInvalidDecisionParam,
			"action=select 的 params 只能给一个：version / installer / recipe，实际给了 %d 个", given)
		return
	}

	want := ""
	switch {
	case hasIns:
		want = strings.TrimSpace(*pr.Installer)
	case hasRec:
		want = strings.TrimSpace(*pr.Recipe)
	default:
		want = strings.TrimSpace(*pr.Version)
	}
	if want == "" {
		report(CodeInvalidDecisionParam, "params 值为空")
		return
	}

	switch it.CandidateKind {
	case CandidateInstaller:
		if !hasIns {
			report(CodeInvalidDecisionParam, "该条目要在 %d 个安装包中选一个，请用 params.installer（可选：%s）",
				len(it.Candidates), strings.Join(it.Candidates, ", "))
			return
		}
	case CandidateVersion:
		if !hasVer {
			report(CodeInvalidDecisionParam, "该条目要在多个版本中选一个，请用 params.version（可选：%s）",
				strings.Join(it.Candidates, ", "))
			return
		}
	case CandidateRecipe:
		if !hasRec {
			report(CodeInvalidDecisionParam, "该条目命中多个配方（%s），请用 params.recipe 指定其中一个",
				strings.Join(it.Candidates, ", "))
			return
		}
	default:
		report(CodeInvalidDecisionParam, "该条目没有可选的候选值（没有多版本、也没有多个安装包），select 无意义")
		return
	}

	for _, cand := range it.Candidates {
		if strings.EqualFold(strings.TrimSpace(cand), want) {
			return
		}
	}
	report(CodeInvalidDecisionParam, "%q 不在候选列表里；合法候选：%s", want, strings.Join(it.Candidates, ", "))
}

func (p *Plan) validatePathDecision(d Decision, ch PathChange, report func(code, format string, a ...any)) {
	switch d.Action {
	case ActionAdd, ActionSkip, ActionMarkInvalid:
		if !d.Params.Empty() {
			report(CodeInvalidDecisionParam, "action=%s 不接受 params", d.Action)
		}
	case ActionAccept:
		if !d.Params.Empty() {
			report(CodeInvalidDecisionParam, "action=%s 不接受 params", d.Action)
		}
		if ch.Mark != MarkRewrite {
			report(CodeInvalidDecisions, "该条目不是用户名重写项（%s 标记），accept 无意义", MarkRewrite)
		}
	case ActionRewrite:
		if d.Params == nil || d.Params.NewValue == nil {
			report(CodeInvalidDecisionParam, "action=rewrite 必须带 params.new_value")
			return
		}
		if d.Params.Version != nil || d.Params.Installer != nil {
			report(CodeInvalidDecisionParam, "action=rewrite 只接受 params.new_value")
			return
		}
		if reason := validateNewValue(strings.TrimSpace(*d.Params.NewValue), p.Target.Drives); reason != "" {
			report(CodeInvalidDecisionParam, "%s", reason)
		}
	default:
		report(CodeInvalidDecisions, "未知 action=%q；path 允许的动作：%s", d.Action, strings.Join(allowedActions(DecKindPath), ", "))
	}
}

// validateNewValue 校验 rewrite 的新路径值，返回空串表示合法。
//
// 这些值最终会写进注册表并被 shell 解析：控制字符、未配对的引号、盘符不存在
// 都会在写入之后才炸，所以在这里就要拦住。
func validateNewValue(v string, drives []string) string {
	if v == "" {
		return "params.new_value 不能为空"
	}
	if len(v) > 260 {
		return fmt.Sprintf("params.new_value 长度 %d 超过 Windows 路径上限 260", len(v))
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "params.new_value 含换行或控制字符"
		}
	}
	if strings.Contains(v, "\"") {
		return "params.new_value 不能含双引号（会被 shell 截断）"
	}
	if strings.Count(v, "'")%2 != 0 {
		return "params.new_value 含未配对的单引号"
	}
	if strings.HasPrefix(v, "\\\\") {
		return "" // UNC 路径不按盘符判断
	}
	if len(v) < 3 || v[1] != ':' || (v[2] != '\\' && v[2] != '/') {
		return "params.new_value 必须是盘符开头的绝对路径（如 D:\\Tools）或 UNC 路径"
	}
	letter := strings.ToUpper(v[:1])
	for _, d := range drives {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(d)), letter+":") {
			return ""
		}
	}
	return fmt.Sprintf("目标机没有盘符 %s:（可用：%s）；请改用存在的盘符或按原盘符建目录",
		letter, strings.Join(drives, ", "))
}

// ApplyDecisions 把已校验通过的决策落到计划上。
//
// 顺序有讲究：先做「改选代表」（override_representative / select{version}），
// 因为它会改变同组其它条目的状态；再逐条落 skip / mark_manual / select{installer}；
// 最后把仍然悬空的待确认项标成 blocked-needs-decision（C2：不阻断整体，只跳过）。
func ApplyDecisions(p *Plan, ds Decisions) {
	if p == nil || len(ds) == 0 {
		if p != nil {
			p.markUndecided()
		}
		return
	}
	for _, d := range ds {
		switch d.Action {
		case ActionOverrideRepresentative:
			p.promote(d.TargetID, d)
		case ActionSelect:
			if d.Params != nil && d.Params.Version != nil {
				p.promoteVersion(d, strings.TrimSpace(*d.Params.Version))
			}
		}
	}
	for _, d := range ds {
		p.applyOne(d)
	}
	p.markUndecided()
}

// applyOne 落一条不需要改选代表的决策。
func (p *Plan) applyOne(d Decision) {
	if i := p.itemIndex(d.TargetID); i >= 0 {
		p.applyItemDecision(i, d)
		return
	}
	if i := p.pathIndex(d.TargetID); i >= 0 {
		p.applyPathDecision(i, d)
	}
}

func (p *Plan) applyItemDecision(i int, d Decision) {
	it := &p.Items[i]
	p.noteNameMismatch(d, it.Name)
	// 已经因为「改选代表」被覆盖的条目不再执行，也不该挂决策徽章——
	// 决策真正生效的是它选中的那一条。
	if it.Status == StatusCovered {
		return
	}
	it.Decision = d.Action
	switch d.Action {
	case ActionSkip:
		it.Status = StatusSkipped
		it.Tier = ""
		it.Command = ""
		it.Reason = "按决策跳过本次安装（④）"
	case ActionMarkManual:
		it.Tier = TierManual
		it.Status = StatusManual
		it.Command = ""
		if it.ManualHint == "" {
			it.ManualHint = "由决策转人工：请按该软件官方安装方式手动安装"
		}
		it.Reason = "按决策转人工（④）；原判断：" + it.Reason
	case ActionSelect:
		// version 已在 promoteVersion 里处理；这里处理选安装包与选配方。
		if d.Params != nil && d.Params.Recipe != nil {
			if r := p.recipeResolvers[d.TargetID]; r != nil {
				p.Items[i] = r(*it, strings.TrimSpace(*d.Params.Recipe))
				it = &p.Items[i]
				it.Decision = d.Action
			}
			it.Reason = "按决策选用配方 " + strings.TrimSpace(*d.Params.Recipe) + "（④）"
			return
		}
		if d.Params != nil && d.Params.Installer != nil {
			file := strings.TrimSpace(*d.Params.Installer)
			if r := p.resolvers[d.TargetID]; r != nil {
				p.Items[i] = r(*it, file)
				it = &p.Items[i]
				it.Decision = d.Action
			}
			it.Reason = "按决策选择安装包 " + file + "（④）"
		}
	}
}

func (p *Plan) applyPathDecision(i int, d Decision) {
	ch := &p.Path[i]
	p.noteNameMismatch(d, ch.Value)
	ch.Decision = d.Action
	switch d.Action {
	case ActionSkip:
		ch.Status = PathStatusSkipped
		ch.Reason = "按决策不写入（④）"
	case ActionMarkInvalid:
		ch.Mark = MarkInvalid
		ch.Status = PathStatusSkipped
		ch.Reason = "按决策标记为失效（④）；下次扫描仍会识别出来"
	case ActionAdd:
		ch.Mark = MarkAdd
		ch.Reason = "按决策直接写入（④）"
	case ActionAccept:
		ch.Mark = MarkRewrite
		ch.Reason = "按决策接受用户名重写（D4-R1）"
	case ActionRewrite:
		val := strings.TrimSpace(*d.Params.NewValue)
		ch.Mark = MarkAdd
		ch.Value = val
		ch.Reason = "按决策改写落点为 " + val + "（④）"
	}
}

// promote 把 targetID 指向的条目恢复成「去重前的自己」，同组其它条目标 covered。
func (p *Plan) promote(targetID string, d Decision) {
	i := p.itemIndex(targetID)
	if i < 0 {
		return
	}
	chosen, ok := p.baseByID[targetID]
	if !ok {
		return
	}
	p.promoteTo(i, chosen, d)
}

// promoteVersion 在 targetID 所在的同配方组里按版本挑一个条目做代表。
func (p *Plan) promoteVersion(d Decision, version string) {
	group := p.groupMembers(d.TargetID)
	chosen := -1
	for _, gi := range group {
		if strings.EqualFold(strings.TrimSpace(p.Items[gi].Expected), version) {
			chosen = gi
			break
		}
	}
	if chosen < 0 {
		return // 校验阶段已保证候选存在
	}
	base, ok := p.baseByID[p.Items[chosen].TargetID]
	if !ok {
		return
	}
	p.promoteTo(chosen, base, d)
}

// promoteTo 把第 i 条设为代表：其余同组条目一律 covered。
func (p *Plan) promoteTo(i int, base Item, d Decision) {
	target := p.Items[i]
	restored := base
	// 目标机现状、候选列表属于「本次计划」，不能被去重前的快照覆盖。
	restored.Current = target.Current
	restored.Decision = string(d.Action)

	for _, gi := range p.groupMembers(target.TargetID) {
		if gi == i {
			continue
		}
		other := &p.Items[gi]
		if other.Status == StatusSkipped {
			continue // 已装好的条目不必因为改选而变 covered
		}
		other.Status = StatusCovered
		other.Tier = ""
		other.Command = ""
		other.Decision = ""
		other.Reason = fmt.Sprintf("按决策改选 %s（%s），本条不再执行（④）",
			restored.Name, orUnknown(restored.Expected))
	}
	p.Items[i] = restored
}

// markUndecided 把没有决策的待确认项标成 blocked-needs-decision。
//
// C2：数据类待确认不阻断整体——跳过执行、进报告，让用户能在确认面板上看到
// 「N 项因未决策将被跳过」。安全类闸口（admin / reboot / plan_hash）不在这里，
// 它们仍按 D15 整体拒绝。
func (p *Plan) markUndecided() {
	for i := range p.Items {
		it := &p.Items[i]
		if it.Status != StatusAmbiguous {
			continue
		}
		it.Status = StatusBlockedNeedsDecision
		it.Tier = ""
		it.Command = ""
		it.Reason = "未决策，本次跳过：" + it.Reason
	}
	for i := range p.Path {
		ch := &p.Path[i]
		if ch.Mark != MarkPending || ch.Decision != "" {
			continue
		}
		ch.Status = StatusBlockedNeedsDecision
		ch.Reason += "（未决策，本次跳过）"
	}
}

func (p *Plan) itemIndex(targetID string) int {
	for i := range p.Items {
		if p.Items[i].TargetID == targetID {
			return i
		}
	}
	return -1
}

func (p *Plan) pathIndex(targetID string) int {
	for i := range p.Path {
		if p.Path[i].TargetID == targetID {
			return i
		}
	}
	return -1
}

func (p *Plan) findTarget(targetID string) (*Item, int) {
	if i := p.itemIndex(targetID); i >= 0 {
		return &p.Items[i], -1
	}
	if i := p.pathIndex(targetID); i >= 0 {
		return nil, i
	}
	return nil, -1
}

// groupMembers 返回与 targetID 共用同一配方的全部条目下标（含自己）。
//
// 配方名为空（未识别 / 命中多个配方）的条目没有组：它们之间没有「谁是代表」
// 的关系，不该被一起改选。
func (p *Plan) groupMembers(targetID string) []int {
	i := p.itemIndex(targetID)
	if i < 0 {
		return nil
	}
	recipe := p.Items[i].RecipeName
	if recipe == "" {
		return []int{i}
	}
	var out []int
	for j := range p.Items {
		if strings.EqualFold(p.Items[j].RecipeName, recipe) {
			out = append(out, j)
		}
	}
	return out
}

// allowedActions 返回某类条目允许的动作，用于错误信息里直接列出合法值。
func allowedActions(kind string) []string {
	switch kind {
	case DecKindSoftware:
		return []string{ActionSelect, ActionOverrideRepresentative, ActionSkip, ActionMarkManual}
	case DecKindRuntime:
		return []string{ActionSelect, ActionSkip, ActionMarkManual}
	case DecKindPath:
		return []string{ActionAdd, ActionAccept, ActionRewrite, ActionSkip, ActionMarkInvalid}
	}
	return nil
}

// noteNameMismatch 把「决策里写的项名与配置不一致」记进 warnings（B5）。
//
// 以配置为准：target_id 才是身份，项名只是给人看的。但完全吞掉会让人以为
// 决策打到了别的条目上，所以必须留痕。
func (p *Plan) noteNameMismatch(d Decision, actual string) {
	if d.Name == "" || actual == "" {
		return
	}
	if strings.EqualFold(strings.TrimSpace(d.Name), strings.TrimSpace(actual)) {
		return
	}
	p.Warnings = append(p.Warnings, fmt.Sprintf(
		"决策 %s 里写的项名是 %q，配置里是 %q：项名不一致；已按配置处理（target_id 为准）",
		d.TargetID, d.Name, actual))
}

// ---- A2 补充：select{recipe} ----
//
// 「一个条目命中多个配方」（M4）原先在行动集里只有 skip / mark_manual，
// 等于这类条目永远装不上，而它恰恰是配方表变稠后最常见的歧义。
// 补一个 select{recipe}，候选就是该条目 matched_by 命中的配方名。

// recipeResolver 返回「只用指定配方重算该条目」的构造函数。
//
// 决策选的是配方，不是安装包：若该配方在 installers/ 里命中多个文件，
// 条目会退回 needs-disambiguation（本次无法再决策，因为同一个 target_id 只能
// 有一条决策）——这是刻意的，宁可停下也不猜装哪一个。
func (p *Plan) recipeResolver(opts Options, byRecipe map[int][]string) func(Item, string) Item {
	return func(old Item, name string) Item {
		out := old
		out.Status = StatusPlanned
		out.Tier = ""
		out.Command = ""
		out.Reason = ""
		out.Candidates = nil
		out.CandidateKind = ""
		out.ManualHint = ""

		idx := -1
		for i := range opts.Recipes {
			if strings.EqualFold(opts.Recipes[i].Name, name) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return out
		}
		r := opts.Recipes[idx]
		out.RecipeName = r.Name
		out.RecipeSource = r.Source
		out.MatchedBy = "decision"

		files := byRecipe[idx]
		switch {
		case len(files) == 0 || r.SilentArgs == "":
			out.Tier = TierManual
			out.Status = StatusManual
			out.Reason = fmt.Sprintf("按决策选用配方 %s，但 installers/ 里没有可用安装包", r.Name)
			out.Fallback = "无自动路径"
		case len(files) > 1:
			out.Status = StatusAmbiguous
			out.Reason = fmt.Sprintf("按决策选用配方 %s，但它命中 %d 个安装包（%s），需先只留一个",
				r.Name, len(files), strings.Join(files, ", "))
			out.Candidates = files
			out.CandidateKind = CandidateInstaller
		default:
			out.Tier = TierLocal
			fillLocal(&out, opts, candidate{recipeIdx: idx, file: files[0]})
			out.Fallback = "失败即停止并保留已装部分（D11）"
		}
		return out
	}
}
