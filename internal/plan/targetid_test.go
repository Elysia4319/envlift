package plan

import (
	"testing"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/targetid"
)

// B2：target_id 是 ④ 决策引用的地基，这里把「稳定性」钉死在 plan 的出口上。

func TestEveryItemHasTargetID(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		software("Git", "2.47.1", "Git.Git"),
		software("CAD", "30.8.2", ""),
	}
	cfg.Runtimes = []config.Runtime{{Name: "node", Command: "node", Version: config.StringPtr("22.22.2")}}
	cfg.Path = config.PathSection{User: []config.PathEntry{{Value: `C:\Tools\bin`}}}

	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	for _, it := range p.Items {
		if len(it.TargetID) != 12 {
			t.Errorf("条目 %s 的 target_id 异常: %q", it.Name, it.TargetID)
		}
	}
	if len(p.Path) != 1 || len(p.Path[0].TargetID) != 12 {
		t.Fatalf("PATH 变更也必须有 target_id: %+v", p.Path)
	}
}

// 同内容两次 Build 必须得到同一组 id——决策存下来再重算还认得上。
func TestTargetIDStableAcrossBuilds(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{
		software("Git", "2.47.1", "Git.Git"),
		software("Node.js", "22.22.2", ""),
	}
	p1 := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	p2 := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))

	first := map[string]string{}
	for _, it := range p1.Items {
		first[it.Name] = it.TargetID
	}
	for _, it := range p2.Items {
		if first[it.Name] != it.TargetID {
			t.Errorf("%s 两次 Build 的 id 不同: %q vs %q", it.Name, first[it.Name], it.TargetID)
		}
	}
}

// 条目顺序变了，id 不能跟着变——这正是不能用数组下标的原因。
func TestTargetIDIndependentOfOrder(t *testing.T) {
	a := software("Git", "2.47.1", "Git.Git")
	b := software("Node.js", "22.22.2", "")

	cfg := baseConfig()
	cfg.Software = []config.Software{a, b}
	p1 := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	byName := map[string]string{}
	for _, it := range p1.Items {
		byName[it.Name] = it.TargetID
	}

	cfg2 := baseConfig()
	cfg2.Software = []config.Software{b, a} // 反序
	p2 := mustBuild(t, testOptions(cfg2, okTarget(), nil, nil))
	for _, it := range p2.Items {
		if byName[it.Name] != it.TargetID {
			t.Errorf("%s 换顺序后 id 变了: %q vs %q", it.Name, byName[it.Name], it.TargetID)
		}
	}
}

// 版本差一位就是另一个 target：否则「选哪个版本」的决策会打到错的条目上。
func TestTargetIDChangesWithVersion(t *testing.T) {
	cfg := baseConfig()
	cfg.Software = []config.Software{software("Git", "2.47.1", "Git.Git")}
	old := mustBuild(t, testOptions(cfg, okTarget(), nil, nil)).Items[0].TargetID

	cfg2 := baseConfig()
	cfg2.Software = []config.Software{software("Git", "2.47.2", "Git.Git")}
	now := mustBuild(t, testOptions(cfg2, okTarget(), nil, nil)).Items[0].TargetID

	if old == now {
		t.Fatal("版本不同却算出同一个 target_id")
	}
}

// PATH 的 id 必须区分段别：同一条值出现在用户级与系统级是两个不同的待办。
func TestPathTargetIDIncludesSection(t *testing.T) {
	cfg := baseConfig()
	cfg.Path = config.PathSection{
		User:    []config.PathEntry{{Value: `C:\Tools\bin`}},
		Machine: []config.PathEntry{{Value: `C:\Tools\bin`}},
	}
	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	if len(p.Path) != 2 {
		t.Fatalf("应有两段 PATH 变更: %+v", p.Path)
	}
	if p.Path[0].TargetID == p.Path[1].TargetID {
		t.Error("用户级与系统级同值应有不同 target_id")
	}
}

// plan 里的 id 必须与扫描侧（engine 用 targetid.Software）算出来的一致，
// 否则「扫描页排除的那条」与「计划里要决策的那条」对不上。
func TestItemTargetIDMatchesScanSide(t *testing.T) {
	sw := software("Git", "2.47.1", "Git.Git")
	cfg := baseConfig()
	cfg.Software = []config.Software{sw}

	p := mustBuild(t, testOptions(cfg, okTarget(), nil, nil))
	if p.Items[0].TargetID != targetid.Software(sw) {
		t.Errorf("plan 与 scan 的 id 不一致: %q vs %q",
			p.Items[0].TargetID, targetid.Software(sw))
	}
}
