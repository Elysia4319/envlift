package engine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Elysia4319/envlift/internal/api"
	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/plan"
	"github.com/Elysia4319/envlift/internal/targetid"
)

// A2 的排除导出是**纯函数 + 落盘行为**两部分，都在这里测：
// 不碰注册表、不调 winget，只操作临时目录（F5）。

func soft(name, version string) config.Software {
	return config.Software{
		Name:            name,
		Version:         config.StringPtr(version),
		Hive:            config.StringPtr("HKLM"),
		ArchView:        config.StringPtr("64"),
		InstallLocation: config.StringPtr(`C:\Apps\` + name),
		Sources:         []string{"registry"},
	}
}

func TestPartitionExcludedKeepsOrderAndRest(t *testing.T) {
	all := []config.Software{soft("A", "1"), soft("B", "2"), soft("C", "3")}
	excludeB := targetid.Software(all[1])

	kept, excluded, unknown := partitionExcluded(all, []string{excludeB})
	if len(unknown) != 0 {
		t.Fatalf("不该有未知 id: %v", unknown)
	}
	if len(excluded) != 1 || excluded[0].Name != "B" {
		t.Fatalf("排除的应是 B: %+v", excluded)
	}
	if len(kept) != 2 || kept[0].Name != "A" || kept[1].Name != "C" {
		t.Fatalf("其余条目应保持原顺序: %+v", kept)
	}
}

// 未排除时必须与现在完全一致：原切片原样返回，不复制、不乱序。
func TestPartitionExcludedEmptyIsNoop(t *testing.T) {
	all := []config.Software{soft("A", "1"), soft("B", "2")}
	kept, excluded, unknown := partitionExcluded(all, nil)
	if len(kept) != 2 || len(excluded) != 0 || len(unknown) != 0 {
		t.Fatalf("空 exclude 应原样返回: %+v %+v %+v", kept, excluded, unknown)
	}
	if &kept[0] != &all[0] {
		t.Error("空 exclude 时不该复制切片")
	}
}

func TestPartitionExcludedUnknownID(t *testing.T) {
	all := []config.Software{soft("A", "1")}
	_, _, unknown := partitionExcluded(all, []string{"deadbeef1234", "feedface5678"})
	if len(unknown) != 2 {
		t.Fatalf("两个不存在的 id 都应报告: %v", unknown)
	}
}

// commit 端到端：被排除的条目进 x-excluded，software: 只剩其余的，
// 且配置能被重新加载（x- 前缀读端无条件忽略，D9）。
func TestCommitWritesExcludedSection(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "envlift.yaml")
	c := Config{ConfigPath: cfgPath, ReportsDir: dir}

	scanCfg := &config.Config{
		App: config.AppName, Schema: config.CurrentSchema,
		Software: []config.Software{soft("Keep", "1"), soft("Drop", "2")},
	}
	payload, err := json.Marshal(scanPayload{
		Config:  scanCfg,
		Targets: scanTargets(scanCfg.Software),
	})
	if err != nil {
		t.Fatalf("构造扫描结果失败: %v", err)
	}

	res, err := commit(c, api.CommitRequest{
		ScanResult: payload,
		Out:        cfgPath,
		Exclude:    []string{targetid.Software(scanCfg.Software[1])},
	})
	if err != nil {
		t.Fatalf("commit 失败: %v", err)
	}
	if res.Total != 2 || res.Excluded != 1 {
		t.Errorf("计数口径不对（总数应仍是采到的 2）: %+v", res)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("读回配置失败: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "x-excluded:") {
		t.Errorf("配置里应有 x-excluded 区: %s", text)
	}
	if !strings.Contains(text, "Drop") {
		t.Error("被排除的条目应原样保留在 x-excluded 里，不能真的删掉")
	}

	back, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("带 x-excluded 的配置应能重新加载: %v", err)
	}
	if len(back.Software) != 1 || back.Software[0].Name != "Keep" {
		t.Errorf("software 应只剩 Keep: %+v", back.Software)
	}
	if _, ok := back.Extra["x-excluded"]; !ok {
		t.Error("读端应把 x-excluded 收进 Extra（忽略但保留）")
	}
}

// 未知 id 必须报错，且不写盘——静默忽略会让「我以为排除了」变成实际写进去。
func TestCommitUnknownExcludeDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "envlift.yaml")
	c := Config{ConfigPath: cfgPath, ReportsDir: dir}

	scanCfg := &config.Config{App: config.AppName, Schema: config.CurrentSchema,
		Software: []config.Software{soft("Keep", "1")}}
	payload, _ := json.Marshal(scanPayload{Config: scanCfg})

	_, err := commit(c, api.CommitRequest{ScanResult: payload, Out: cfgPath, Exclude: []string{"nope"}})
	if err == nil {
		t.Fatal("未知 target_id 应报错")
	}
	var unknown *api.UnknownTargets
	if !asUnknown(err, &unknown) {
		t.Errorf("错误类型应为 *api.UnknownTargets，实际 %T", err)
	}
	if _, statErr := os.Stat(cfgPath); statErr == nil {
		t.Error("报错时不该留下配置文件")
	}
}

func asUnknown(err error, target **api.UnknownTargets) bool {
	u, ok := err.(*api.UnknownTargets)
	if ok {
		*target = u
	}
	return ok
}

// ④：决策文件的读写（engine 层，与配置同目录）。全部落临时目录，不碰系统。
func TestDecisionsFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "envlift.decisions.yaml")

	in := plan.Decisions{
		{TargetID: "abc", Kind: plan.DecKindSoftware, Action: plan.ActionSelect,
			Params: &plan.DecisionParams{Version: config.StringPtr("2.47.1")},
			Name:   "Git", Version: "2.47.1"},
		{TargetID: "def", Kind: plan.DecKindPath, Action: plan.ActionRewrite,
			Params: &plan.DecisionParams{NewValue: config.StringPtr(`C:\Tools`)},
			Value:  `D:\Tools`},
	}
	if err := SaveDecisionsFile(path, in); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, err := LoadDecisionsFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("条数 = %d", len(got))
	}
	if got[0].TargetID != "abc" || got[0].Params == nil || *got[0].Params.Version != "2.47.1" {
		t.Errorf("第一条不对: %+v", got[0])
	}
	if got[0].Name != "Git" {
		t.Errorf("B5：人类可读项名没落盘: %+v", got[0])
	}
	if got[1].Params == nil || *got[1].Params.NewValue != `C:\Tools` {
		t.Errorf("第二条不对: %+v", got[1])
	}
}

func TestLoadDecisionsFileMissingIsEmpty(t *testing.T) {
	got, err := LoadDecisionsFile(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("文件不存在不应报错: %v", err)
	}
	if got != nil {
		t.Errorf("应返回 nil，实际 %v", got)
	}
}

func TestLoadDecisionsFileRejectsBadHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.yaml")
	if err := os.WriteFile(path, []byte("app: something-else\nschema: 1\ndecisions: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDecisionsFile(path); err == nil {
		t.Error("app 不对应拒绝")
	}
	if err := os.WriteFile(path, []byte("app: envlift\nschema: 99\ndecisions: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDecisionsFile(path); err == nil {
		t.Error("schema 过新应拒绝（D9）")
	}
}

// 请求里的 decisions 数组：空 / null 视为无决策；对象、未知字段一律拒绝。
func TestParseDecisions(t *testing.T) {
	if ds, err := parseDecisions(nil); err != nil || ds != nil {
		t.Errorf("空载荷: %v / %v", ds, err)
	}
	if ds, err := parseDecisions(json.RawMessage("null")); err != nil || ds != nil {
		t.Errorf("null: %v / %v", ds, err)
	}
	ds, err := parseDecisions(json.RawMessage(`[{"target_id":"a","kind":"software","action":"skip"}]`))
	if err != nil || len(ds) != 1 || ds[0].TargetID != "a" {
		t.Fatalf("正常数组: %v / %v", ds, err)
	}
	for _, bad := range []string{
		`{"decisions":[]}`, // 对象而不是数组
		`[{"target_id":"a","kind":"software","action":"x","zz":1}]`, // 未知字段
	} {
		if _, err := parseDecisions(json.RawMessage(bad)); err == nil {
			t.Errorf("应拒绝: %s", bad)
		}
	}
}

// A1-c：scan 侧派生视图（scanTargets）与 plan 条目必须给出同一个 id。
//
// 这两个 id 一旦分叉，③ 的排除导出与 ④ 的决策就会引用到「另一个条目」，
// 而且不会有任何报错——所以这里直接把两边算出来比一遍。
func TestScanTargetsMatchPlanItems(t *testing.T) {
	cfg := &config.Config{
		App:    config.AppName,
		Schema: config.CurrentSchema,
		Software: []config.Software{
			soft("Git", "2.47.1"),
			soft("Vim", "9.1"),
		},
	}
	p, err := plan.Build(plan.Options{
		Config: cfg,
		Target: plan.Target{Runtimes: map[string]plan.RuntimeState{}},
		Now:    time.Now(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	targets := scanTargets(cfg.Software)
	if len(targets) != len(p.Items) {
		t.Fatalf("条目数不同: %d vs %d", len(targets), len(p.Items))
	}
	for i, tg := range targets {
		if tg.Index != i {
			t.Errorf("第 %d 个 target 的 index = %d", i, tg.Index)
		}
		if tg.TargetID != p.Items[i].TargetID {
			t.Errorf("%s: scan 侧 %s ≠ plan 侧 %s", p.Items[i].Name, tg.TargetID, p.Items[i].TargetID)
		}
	}
}

// D：超过体积上限的报告——读详情要明确报错，列表只给 stat 摘要（不解析、不读满）。
func TestOversizedReportIsGuarded(t *testing.T) {
	dir := t.TempDir()
	name := "restore-20990101-000000.json"
	path := filepath.Join(dir, name)

	// 造一份确实超过上限的文件（内容不必是合法报告：先看体积，再谈解析）。
	big := make([]byte, api.MaxReportBytes+1024)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(path, big, 0o644); err != nil {
		t.Fatal(err)
	}

	c := Config{ConfigPath: filepath.Join(dir, "envlift.yaml"), ReportsDir: dir}
	if _, err := readReport(c, name, "json"); !errors.Is(err, api.ErrReportTooLarge) {
		t.Errorf("详情应返回 ErrReportTooLarge，实际 %v", err)
	}

	list, err := listReports(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Truncated {
		t.Fatalf("大文件的列表项应标 truncated: %+v", list)
	}
	if list[0].Summary != nil || list[0].ReportSchema != 0 {
		t.Errorf("超限时不该尝试解析: %+v", list[0])
	}
}
