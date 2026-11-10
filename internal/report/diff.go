package report

import (
	"fmt"
	"sort"

	"github.com/Elysia4319/envlift/internal/plan"
)

// 差异类别（D1 的五类）。
const (
	ChangeStatus  = "status"
	ChangeTier    = "tier"
	ChangeVersion = "version"
	ChangeAdded   = "added"
	ChangeRemoved = "removed"
)

// Side 是比对某一侧的值（只留展示需要的字段）。
type Side struct {
	Status   string `json:"status"`
	Tier     string `json:"tier,omitempty"`
	Expected string `json:"expected_version,omitempty"`
	Before   string `json:"before_version,omitempty"`
	After    string `json:"after_version,omitempty"`
	Unparsed bool   `json:"version_unparsed,omitempty"`
	Command  string `json:"command,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Decision string `json:"decision,omitempty"`
}

// ItemDiff 是一条差异。Left 为 nil 表示「这一侧没有」。
type ItemDiff struct {
	TargetID string `json:"target_id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Change   string `json:"change"`
	Left     *Side  `json:"left,omitempty"`
	Right    *Side  `json:"right,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// Diff 是两份报告的差异。
type Diff struct {
	A    string `json:"a"`
	B    string `json:"b"`
	EnvA Env    `json:"env_a"`
	EnvB Env    `json:"env_b"`
	// EnvWarnings 非空表示两台机器/两次运行的 env 不同——先看它，再看条目。
	EnvWarnings []string `json:"env_warnings"`
	EnvNote     string   `json:"env_note,omitempty"`

	StatusChanges  []ItemDiff `json:"status_changes"`
	TierChanges    []ItemDiff `json:"tier_changes"`
	VersionChanges []ItemDiff `json:"version_changes"`
	Added          []ItemDiff `json:"added"`
	Removed        []ItemDiff `json:"removed"`
	Unchanged      int        `json:"unchanged"`
}

// ComputeDiff 以 target_id 为键比较两份报告（D1/D2/D3）。
//
// 纯函数：入参就是两份 Document，不读文件、不看环境，便于构造数据穷举测试。
func ComputeDiff(a, b *Document) *Diff {
	if a == nil || b == nil {
		return &Diff{EnvWarnings: []string{"缺少一侧报告"}}
	}
	d := &Diff{
		A: a.GeneratedAt, B: b.GeneratedAt, EnvA: a.Env, EnvB: b.Env,
		StatusChanges: []ItemDiff{}, TierChanges: []ItemDiff{},
		VersionChanges: []ItemDiff{}, Added: []ItemDiff{}, Removed: []ItemDiff{},
	}
	d.EnvWarnings = envWarnings(a, b)
	if len(d.EnvWarnings) > 0 {
		d.EnvNote = "两份报告的环境不同，条目差异可能由此产生；先确认上面这些差异是不是预期的"
	}

	left := index(a)
	right := index(b)
	for _, id := range unionKeys(left, right) {
		l, inL := left[id]
		r, inR := right[id]
		switch {
		case inL && !inR:
			d.Removed = append(d.Removed, ItemDiff{
				TargetID: id, Kind: l.Kind, Name: l.Name, Change: ChangeRemoved,
				Left: sideOf(l), Detail: "A 有、B 没有：" + l.Name,
			})
		case !inL && inR:
			d.Added = append(d.Added, ItemDiff{
				TargetID: id, Kind: r.Kind, Name: r.Name, Change: ChangeAdded,
				Right: sideOf(r), Detail: "B 新增：" + r.Name,
			})
		default:
			// 一条条目可以同时出现在多个分组里：状态变化与层级变化回答的是
			// 两个不同的问题（「跑成没跑成」vs「换了一条路」），断网时两者
			// 往往同时发生，只报一个会把另一个信号藏起来。
			before := len(d.StatusChanges) + len(d.TierChanges) + len(d.VersionChanges)
			compareOne(id, l, r, d)
			if len(d.StatusChanges)+len(d.TierChanges)+len(d.VersionChanges) == before {
				d.Unchanged++
			}
		}
	}
	return d
}

// compareOne 把一条同时存在于两侧的条目按维度归组（同一维度不同才进该组）。
//
// 同一条目可能同时出现在「状态」与「层级」两组里——这是刻意的：
// 「succeeded → manual」与「winget → manual」是两条独立信息。
func compareOne(id string, l, r ItemResult, d *Diff) {
	base := ItemDiff{TargetID: id, Kind: l.Kind, Name: l.Name, Left: sideOf(l), Right: sideOf(r)}

	if l.Status != r.Status {
		x := base
		x.Change = ChangeStatus
		x.Detail = "状态 " + l.Status + " → " + r.Status
		d.StatusChanges = append(d.StatusChanges, x)
	}
	if l.Tier != r.Tier {
		x := base
		x.Change = ChangeTier
		x.Detail = "层级 " + orDash(l.Tier) + " → " + orDash(r.Tier)
		d.TierChanges = append(d.TierChanges, x)
	}
	if changed, detail := versionChange(l, r); changed {
		x := base
		x.Change = ChangeVersion
		x.Detail = detail
		d.VersionChanges = append(d.VersionChanges, x)
	}
}

// versionChange 分三层看版本（C4/D2）：
//
//	期望版本不同      —— 配置本来就要装不同版本（注意：target_id 含版本，通常表现为新增/消失）
//	执行前版本不同    —— 目标机原本装的就是别的版本
//	执行后版本不同    —— 装完之后的版本不同（来自 verify 输出，可能被标 unparsed）
func versionChange(l, r ItemResult) (bool, string) {
	if l.ExpectedVersion != r.ExpectedVersion {
		return true, "期望版本 " + orDash(l.ExpectedVersion) + " → " + orDash(r.ExpectedVersion)
	}
	if l.BeforeVersion != r.BeforeVersion {
		return true, "执行前版本 " + orDash(l.BeforeVersion) + " → " + orDash(r.BeforeVersion)
	}
	if l.ActualVersion != r.ActualVersion {
		detail := "执行后版本 " + orDash(l.ActualVersion) + " → " + orDash(r.ActualVersion)
		if l.VersionUnparsed || r.VersionUnparsed {
			detail += "（有一侧解析不出，见 version_raw）"
		}
		return true, detail
	}
	return false, ""
}

func sideOf(it ItemResult) *Side {
	return &Side{
		Status: it.Status, Tier: it.Tier, Expected: it.ExpectedVersion,
		Before: it.BeforeVersion, After: it.ActualVersion, Unparsed: it.VersionUnparsed,
		Command: it.Command, ExitCode: it.ExitCode, Decision: it.Decision,
	}
}

// index 把条目与 PATH 变更拍平成同一张以 target_id 为键的表（D1）。
func index(d *Document) map[string]ItemResult {
	out := make(map[string]ItemResult, len(d.Items)+len(d.PathChanges))
	for _, it := range d.Items {
		out[it.TargetID] = it
	}
	for _, p := range d.PathChanges {
		out[p.TargetID] = ItemResult{
			TargetID: p.TargetID,
			Kind:     "path",
			Name:     "PATH[" + p.Section + "] " + p.Value,
			Status:   pathStatus(p, d.Mode),
			Tier:     "path",
			Decision: p.Decision,
			Reason:   p.Reason,
		}
	}
	return out
}

// pathStatus 把 PATH 的标记折算成与条目可比的状态。
func pathStatus(p PathResult, mode string) string {
	if p.Status != "" {
		return p.Status
	}
	switch p.Mark {
	case plan.MarkAdd, plan.MarkRewrite:
		if mode == "apply" {
			return "succeeded"
		}
		return "planned"
	case plan.MarkSkip:
		return "skipped"
	case plan.MarkInvalid:
		return "invalid"
	case plan.MarkPending:
		return "pending"
	}
	return p.Mark
}

func unionKeys(a, b map[string]ItemResult) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for k := range a {
		seen[k] = true
		out = append(out, k)
	}
	for k := range b {
		if !seen[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// envWarnings 比较两侧环境（D3）。主机名不参与：两侧本来就是 ${HOST}。
func envWarnings(a, b *Document) []string {
	var out []string
	add := func(field, av, bv string) {
		if av != bv {
			out = append(out, fmt.Sprintf("%s 不同：%s → %s", field, orDash(av), orDash(bv)))
		}
	}
	add("系统", a.Env.OS, b.Env.OS)
	add("build", a.Env.Build, b.Env.Build)
	add("架构", a.Env.Arch, b.Env.Arch)
	add("winget", boolText(a.Env.WingetOK, "可用", "不可用"), boolText(b.Env.WingetOK, "可用", "不可用"))
	add("网络", boolText(a.Env.NetworkOK, "可达", "不可达"), boolText(b.Env.NetworkOK, "可达", "不可达"))
	add("管理员运行", boolText(a.Env.Elevated, "是", "否"), boolText(b.Env.Elevated, "是", "否"))
	add("mode", a.Mode, b.Mode)
	if a.Decisions.ApplyMode != b.Decisions.ApplyMode {
		out = append(out, fmt.Sprintf("apply mode 不同：%s → %s",
			orDash(a.Decisions.ApplyMode), orDash(b.Decisions.ApplyMode)))
	}
	return out
}

func boolText(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}
