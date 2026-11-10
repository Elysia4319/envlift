package report

import (
	"strings"
	"testing"
)

func baseDoc() *Document {
	return &Document{
		ReportSchema: CurrentSchema, Kind: Kind, GeneratedAt: "2026-01-01T00:00:00Z",
		Mode: "apply",
		Env:  Env{OS: "Windows 11", Build: "26100", Arch: "amd64", Username: "${USER}", Hostname: "${HOST}", WingetOK: true, NetworkOK: true},
		Items: []ItemResult{
			{TargetID: "a", Kind: "software", Name: "Git", ExpectedVersion: "2.56.0", ActualVersion: "2.56.0",
				Tier: "winget", Status: "succeeded"},
			{TargetID: "b", Kind: "software", Name: "Node", ExpectedVersion: "22.0.0",
				BeforeVersion: "21.0.0", ActualVersion: "22.0.0",
				Tier: "local", Status: "succeeded"},
		},
		PathChanges: []PathResult{
			{TargetID: "p1", Section: "user", Mark: "+", Value: `C:\Tools`},
		},
	}
}

func TestDiffStatusTierVersion(t *testing.T) {
	a := baseDoc()
	b := baseDoc()
	// 状态变化：succeeded → failed
	b.Items[0].Status = "failed"
	b.Items[0].FailureReason = "退出码 3"
	// 层级变化（状态不变）：local → manual
	b.Items[1].Tier = "manual"

	d := ComputeDiff(a, b)
	if len(d.StatusChanges) != 1 || d.StatusChanges[0].TargetID != "a" {
		t.Fatalf("状态差异 = %+v", d.StatusChanges)
	}
	if !strings.Contains(d.StatusChanges[0].Detail, "succeeded → failed") {
		t.Errorf("状态差异说明 = %q", d.StatusChanges[0].Detail)
	}
	if len(d.TierChanges) != 1 || d.TierChanges[0].TargetID != "b" {
		t.Fatalf("层级差异 = %+v", d.TierChanges)
	}
	if len(d.EnvWarnings) != 0 {
		t.Errorf("env 相同不该告警: %v", d.EnvWarnings)
	}
}

func TestDiffVersionKinds(t *testing.T) {
	a := baseDoc()
	b := baseDoc()
	// 期望相同、执行后版本不同
	b.Items[1].ActualVersion = "23.0.0"
	d := ComputeDiff(a, b)
	if len(d.VersionChanges) != 1 {
		t.Fatalf("版本差异 = %+v", d.VersionChanges)
	}
	if !strings.Contains(d.VersionChanges[0].Detail, "执行后版本") {
		t.Errorf("应说明是执行后版本不同: %q", d.VersionChanges[0].Detail)
	}

	// 期望相同、执行前（目标机原有）版本不同
	c := baseDoc()
	c.Items[1].BeforeVersion = "20.0.0"
	if d := ComputeDiff(a, c); len(d.VersionChanges) != 1 ||
		!strings.Contains(d.VersionChanges[0].Detail, "执行前版本") {
		t.Errorf("应区分执行前版本: %+v", d.VersionChanges)
	}

	// 期望版本本身就不同
	b2 := baseDoc()
	b2.Items[1].ExpectedVersion = "23.0.0"
	d2 := ComputeDiff(a, b2)
	if len(d2.VersionChanges) != 1 || !strings.Contains(d2.VersionChanges[0].Detail, "期望版本") {
		t.Fatalf("期望版本差异: %+v", d2.VersionChanges)
	}
}

func TestDiffAddedRemoved(t *testing.T) {
	a := baseDoc()
	b := baseDoc()
	b.Items = b.Items[:1] // b 少了 b2
	b.Items = append(b.Items, ItemResult{TargetID: "c", Kind: "runtime", Name: "python", Status: "manual"})

	d := ComputeDiff(a, b)
	if len(d.Removed) != 1 || d.Removed[0].TargetID != "b" {
		t.Fatalf("消失 = %+v", d.Removed)
	}
	if len(d.Added) != 1 || d.Added[0].TargetID != "c" {
		t.Fatalf("新增 = %+v", d.Added)
	}
}

// D3：环境差异要先说清楚；主机名不同不算差异（两侧都是 ${HOST}）。
func TestDiffEnvWarnings(t *testing.T) {
	a := baseDoc()
	b := baseDoc()
	b.Env.WingetOK = false
	b.Env.Hostname = "${HOST}"

	d := ComputeDiff(a, b)
	if len(d.EnvWarnings) != 1 || !strings.Contains(d.EnvWarnings[0], "winget") {
		t.Fatalf("env 告警 = %v", d.EnvWarnings)
	}
	if d.EnvNote == "" {
		t.Error("env 不同时必须给一句「差异可能由此产生」")
	}

	// 只有主机名不同 → 不告警
	c := baseDoc()
	c.Env.Hostname = "${HOST}"
	if got := ComputeDiff(a, c).EnvWarnings; len(got) != 0 {
		t.Errorf("主机名不该算差异: %v", got)
	}

	// apply mode 不同（dry-run vs apply）也要提示
	e := baseDoc()
	e.Mode = "dry-run"
	if got := ComputeDiff(a, e).EnvWarnings; len(got) == 0 {
		t.Error("dry-run 与 apply 对比应提示 mode 不同")
	}
}

// PATH 变更也在同一张键空间里比（D1）。
func TestDiffPathStatus(t *testing.T) {
	a := baseDoc() // 已写入
	b := baseDoc()
	b.PathChanges[0].Status = "skipped" // 第二次被决策跳过
	b.PathChanges[0].Decision = "skip"

	d := ComputeDiff(a, b)
	if len(d.StatusChanges) != 1 || d.StatusChanges[0].Kind != "path" {
		t.Fatalf("PATH 状态差异 = %+v", d.StatusChanges)
	}
	if !strings.Contains(d.StatusChanges[0].Name, "PATH[user]") {
		t.Errorf("PATH 差异的名字 = %q", d.StatusChanges[0].Name)
	}
}

func TestDiffUnchangedCount(t *testing.T) {
	a := baseDoc()
	d := ComputeDiff(a, baseDoc())
	if d.Unchanged != 3 { // 2 条目 + 1 PATH
		t.Errorf("unchanged = %d", d.Unchanged)
	}
	if len(d.StatusChanges)+len(d.TierChanges)+len(d.VersionChanges)+len(d.Added)+len(d.Removed) != 0 {
		t.Error("完全相同不应有差异项")
	}
}

// 断网时「状态 planned → manual」与「层级 winget → manual」会同时发生：
// 两个分组都要出现（它们回答的是不同问题），但不能算成两条「未变化」。
func TestDiffSameItemInStatusAndTier(t *testing.T) {
	a := baseDoc()
	b := baseDoc()
	b.Items[0].Status = "manual"
	b.Items[0].Tier = "manual"

	d := ComputeDiff(a, b)
	if len(d.StatusChanges) != 1 || len(d.TierChanges) != 1 {
		t.Fatalf("两组都该有: status=%d tier=%d", len(d.StatusChanges), len(d.TierChanges))
	}
	if d.StatusChanges[0].TargetID != d.TierChanges[0].TargetID {
		t.Error("两组应是同一条目")
	}
	if d.Unchanged != 2 { // 另 1 条目 + 1 PATH
		t.Errorf("unchanged = %d", d.Unchanged)
	}
}
