package plan

import (
	"testing"

	"github.com/Elysia4319/envlift/internal/config"
)

func samplePlan() *Plan {
	return &Plan{
		App: "envlift", Kind: "restore-plan", Schema: config.CurrentSchema,
		GeneratedAt: "2026-01-01T00:00:00Z",
		Target:      Target{OS: "Windows 11", NetworkOK: true, WingetOK: true},
		Items: []Item{
			{Kind: "software", Name: "Git", Expected: "2.56.0", Tier: TierWinget,
				Status: StatusPlanned, Command: "winget install --id Git.Git"},
		},
		Path: []PathChange{{Section: "user", Mark: MarkAdd, Value: `C://new`}},
	}
}

func TestHashStableAcrossTime(t *testing.T) {
	a := samplePlan()
	b := samplePlan()
	b.GeneratedAt = "2030-12-31T23:59:59Z" // 时间不同不应影响哈希
	if Hash(a) != Hash(b) {
		t.Error("GeneratedAt 不应影响 plan_hash")
	}
	if Hash(a) == "" {
		t.Error("哈希不应为空")
	}
	if len(Hash(a)) != 64 {
		t.Errorf("应是 sha256 hex（64 字符），实际 %d", len(Hash(a)))
	}
}

func TestHashChangesWithContent(t *testing.T) {
	a := samplePlan()
	base := Hash(a)

	a.Items[0].Command = "winget install --id Other.App"
	if Hash(a) == base {
		t.Error("命令变了哈希必须变——否则前端展示的与实际执行的可能不是同一份")
	}
}

func TestHashChangesWithPath(t *testing.T) {
	a := samplePlan()
	base := Hash(a)
	a.Path = append(a.Path, PathChange{Section: "user", Mark: MarkAdd, Value: `C:\x`})
	if Hash(a) == base {
		t.Error("PATH 变更变了哈希必须变")
	}
}

func TestHashNil(t *testing.T) {
	if Hash(nil) != "" {
		t.Error("nil 计划应返回空哈希")
	}
}
