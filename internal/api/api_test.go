package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/plan"
	"github.com/Elysia4319/envlift/internal/report"
	"github.com/Elysia4319/envlift/internal/task"
)

const testToken = "test-token-abc"

// fakeDeps 全部用假实现：不读注册表、不调 winget、不装软件、不改 PATH（E3）。
func fakeDeps() Deps {
	cfg := &config.Config{App: config.AppName, Schema: config.CurrentSchema}
	var savedDecisions plan.Decisions
	return Deps{
		LoadDecisions: func() (plan.Decisions, error) { return savedDecisions, nil },
		SaveDecisions: func(ds plan.Decisions) error { savedDecisions = ds; return nil },
		Status: func(ctx context.Context) (any, error) {
			return map[string]any{"os": "Windows 11", "elevated": false}, nil
		},
		Scan: func(ctx context.Context, raw bool, r task.Reporter) ([]byte, error) {
			r.Phase("读注册表", 1, 2)
			r.Log("info", "扫描中")
			r.Phase("winget list", 2, 2)
			return []byte(`{"software":3}`), nil
		},
		Commit: func(req CommitRequest) (CommitResult, error) {
			if strings.Contains(req.Out, "..") {
				return CommitResult{}, ErrPathForbidden
			}
			if len(req.Exclude) > 0 && req.Exclude[0] == "nope" {
				return CommitResult{}, &UnknownTargets{IDs: req.Exclude}
			}
			return CommitResult{Written: req.Out, Total: 3, Excluded: len(req.Exclude)}, nil
		},
		LoadConfig: func(path string) (*config.Config, error) { return cfg, nil },
		BuildPlan: func(c *config.Config, decisions json.RawMessage) (*plan.Plan, error) {
			return &plan.Plan{
				App: config.AppName, Kind: "restore-plan", Schema: config.CurrentSchema,
				Target: plan.Target{NetworkOK: true, WingetOK: true},
				Items:  []plan.Item{{Kind: "software", Name: "Git", Status: plan.StatusPlanned}},
			}, nil
		},
		Apply: func(ctx context.Context, p *plan.Plan, conf Confirmations, r task.Reporter) ([]byte, error) {
			r.Log("info", "开始执行")
			return []byte(`{"executed":1}`), nil
		},
		Recheck:     func() (bool, string) { return true, "" },
		Rollback:    func(journalID string) error { return nil },
		ListReports: func() ([]ReportInfo, error) { return []ReportInfo{{Name: "restore-1.md"}}, nil },
		ReadReport: func(name, format string) ([]byte, error) {
			return []byte("# report"), nil
		},
	}
}

func newServer(d Deps) *Server {
	return New(d, task.NewManager(20, 100), testToken, "reports")
}

func req(t *testing.T, s *Server, method, path, body string, withToken bool) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if withToken {
		r.Header.Set("X-Envlift-Token", testToken)
	}
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	return w
}

func decodeErr(t *testing.T, w *httptest.ResponseRecorder) APIError {
	t.Helper()
	var e errEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("响应不是错误信封: %s", w.Body.String())
	}
	return e.Error
}

func waitTask(t *testing.T, s *Server, id string) task.Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap, ok := s.tasks.Get(id, 0)
		if ok && snap.State.Terminal() {
			return snap
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("任务 %s 未完成", id)
	return task.Snapshot{}
}

func TestAuthRequired(t *testing.T) {
	s := newServer(fakeDeps())
	cases := []struct {
		name string
		path string
	}{
		{"无 token", "/api/v1/status"},
		{"query 传 token", "/api/v1/status?token=" + testToken},
	}
	for _, c := range cases {
		w := req(t, s, http.MethodGet, c.path, "", false)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s 应 401，实际 %d", c.name, w.Code)
		}
		if e := decodeErr(t, w); e.Code != CodeUnauthorized {
			t.Errorf("%s 错误码 = %q", c.name, e.Code)
		}
	}
}

func TestWrongToken(t *testing.T) {
	s := newServer(fakeDeps())
	r := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	r.Header.Set("X-Envlift-Token", "wrong")
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("错误 token 应 401，实际 %d", w.Code)
	}
}

func TestStatus(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodGet, "/api/v1/status", "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Windows 11") {
		t.Errorf("响应内容不对: %s", w.Body.String())
	}
}

func TestScanTaskAndPoll(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodPost, "/api/v1/scan", `{"raw":false}`, true)
	if w.Code != http.StatusAccepted {
		t.Fatalf("scan 应 202，实际 %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.TaskID == "" {
		t.Fatal("应返回 task_id")
	}

	snap := waitTask(t, s, body.TaskID)
	if snap.State != task.Succeeded {
		t.Fatalf("状态 = %q", snap.State)
	}
	if string(snap.Result) != `{"software":3}` {
		t.Errorf("扫描结果未保存: %s", snap.Result)
	}
	if len(snap.Logs) == 0 {
		t.Error("应有日志")
	}
}

// scan 只产出预览，绝不写盘；写盘必须显式 commit（A3/B4）。
func TestScanDoesNotWriteUntilCommit(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodPost, "/api/v1/scan", `{}`, true)
	var body struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	waitTask(t, s, body.TaskID)

	commit := req(t, s, http.MethodPost, "/api/v1/scan/commit",
		`{"task_id":"`+body.TaskID+`","out":"envlift.yaml","on_exist":"rename"}`, true)
	if commit.Code != http.StatusOK {
		t.Fatalf("commit 应 200，实际 %d: %s", commit.Code, commit.Body.String())
	}
	if !strings.Contains(commit.Body.String(), "envlift.yaml") {
		t.Errorf("应回传写入路径: %s", commit.Body.String())
	}
}

func TestCommitRejectsUnknownTask(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodPost, "/api/v1/scan/commit", `{"task_id":"nope","out":"x.yaml"}`, true)
	if w.Code != http.StatusBadRequest {
		t.Errorf("未知 task_id 应 400，实际 %d", w.Code)
	}
}

// A2：exclude 里有不存在的 target_id → 400 且列出具体 id（不静默忽略）。
func TestCommitRejectsUnknownExcludeTarget(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodPost, "/api/v1/scan", `{}`, true)
	var body struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	waitTask(t, s, body.TaskID)

	c := req(t, s, http.MethodPost, "/api/v1/scan/commit",
		`{"task_id":"`+body.TaskID+`","out":"envlift.yaml","exclude":["nope"]}`, true)
	if c.Code != http.StatusBadRequest {
		t.Fatalf("未知 target_id 应 400，实际 %d: %s", c.Code, c.Body.String())
	}
	e := decodeErr(t, c)
	if e.Code != CodeUnknownTarget {
		t.Errorf("错误码 = %q，期望 %s", e.Code, CodeUnknownTarget)
	}
	if !strings.Contains(c.Body.String(), "nope") {
		t.Errorf("details 应列出具体 id: %s", c.Body.String())
	}
}

// 正常排除：回执里 total 仍是采到的条数，excluded 单列。
func TestCommitWithExcludeReportsCounts(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodPost, "/api/v1/scan", `{}`, true)
	var body struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	waitTask(t, s, body.TaskID)

	c := req(t, s, http.MethodPost, "/api/v1/scan/commit",
		`{"task_id":"`+body.TaskID+`","out":"envlift.yaml","exclude":["abc123def456"]}`, true)
	if c.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", c.Code, c.Body.String())
	}
	var res CommitResult
	if err := json.Unmarshal(c.Body.Bytes(), &res); err != nil {
		t.Fatalf("回执解析失败: %v", err)
	}
	if res.Written != "envlift.yaml" || res.Total != 3 || res.Excluded != 1 {
		t.Errorf("回执不对: %+v", res)
	}
}

func TestCommitRejectsBadOnExist(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodPost, "/api/v1/scan/commit", `{"task_id":"x","on_exist":"overwrite"}`, true)
	if w.Code != http.StatusBadRequest {
		t.Errorf("非法 on_exist 应 400，实际 %d", w.Code)
	}
}

func TestTaskBusyReturns409(t *testing.T) {
	d := fakeDeps()
	block := make(chan struct{})
	d.Scan = func(ctx context.Context, raw bool, r task.Reporter) ([]byte, error) {
		<-block
		return nil, nil
	}
	s := newServer(d)

	if w := req(t, s, http.MethodPost, "/api/v1/scan", `{}`, true); w.Code != http.StatusAccepted {
		t.Fatalf("首个 scan 应 202，实际 %d", w.Code)
	}
	w := req(t, s, http.MethodPost, "/api/v1/scan", `{}`, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("第二个任务应 409，实际 %d", w.Code)
	}
	e := decodeErr(t, w)
	if e.Code != CodeBusy {
		t.Errorf("错误码 = %q，期望 task_busy", e.Code)
	}
	details, _ := e.Details.(map[string]any)
	if details["task_id"] == nil || details["task_id"] == "" {
		t.Errorf("409 应带当前任务 id: %+v", e.Details)
	}
	close(block)
}

func TestTaskSinceIncremental(t *testing.T) {
	d := fakeDeps()
	d.Scan = func(ctx context.Context, raw bool, r task.Reporter) ([]byte, error) {
		r.Log("info", "a")
		r.Log("info", "b")
		r.Log("info", "c")
		return nil, nil
	}
	s := newServer(d)
	w := req(t, s, http.MethodPost, "/api/v1/scan", `{}`, true)
	var body struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	snap := waitTask(t, s, body.TaskID)

	all := req(t, s, http.MethodGet, "/api/v1/task/"+snap.ID, "", true)
	var full task.Snapshot
	_ = json.Unmarshal(all.Body.Bytes(), &full)
	if len(full.Logs) != 3 {
		t.Fatalf("应返回 3 条日志，实际 %d", len(full.Logs))
	}

	inc := req(t, s, http.MethodGet, "/api/v1/task/"+snap.ID+"?since=2", "", true)
	var part task.Snapshot
	_ = json.Unmarshal(inc.Body.Bytes(), &part)
	if len(part.Logs) != 1 || part.Logs[0].Seq != 3 {
		t.Errorf("since=2 应只返回 seq=3，实际 %#v", part.Logs)
	}
}

func TestPlanReturnsHash(t *testing.T) {
	s := newServer(fakeDeps())
	// D1：plan 同步返回——决策不合法必须当场 400，而不是塞进任务结果。
	w := req(t, s, http.MethodPost, "/api/v1/plan", `{}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("plan 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		PlanHash string    `json:"plan_hash"`
		Plan     plan.Plan `json:"plan"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("计划结果解析失败: %v (%s)", err, w.Body.String())
	}
	if out.PlanHash == "" || len(out.PlanHash) != 64 {
		t.Errorf("plan_hash 格式不对: %q", out.PlanHash)
	}
}

func TestApplyRejectsStalePlan(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodPost, "/api/v1/apply",
		`{"plan_hash":"deadbeef","confirmations":{"mode":"all"}}`, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("哈希不匹配应 409，实际 %d: %s", w.Code, w.Body.String())
	}
	e := decodeErr(t, w)
	if e.Code != CodePlanStale {
		t.Errorf("错误码 = %q，期望 plan_stale", e.Code)
	}
}

func TestApplyRequiresPlanHash(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodPost, "/api/v1/apply", `{"confirmations":{"mode":"all"}}`, true)
	if w.Code != http.StatusBadRequest {
		t.Errorf("缺 plan_hash 应 400，实际 %d", w.Code)
	}
}

// A3：mode 的判定必须早于 plan_hash —— 否则一个连「要不要跑提权项」都没说的
// 请求，会被报成「哈希过期」，把根因藏起来。
func TestApplyValidationOrderModeBeforeHash(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodPost, "/api/v1/apply", `{"plan_hash":"deadbeef","confirmations":{}}`, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 mode 应 400（不是 409 哈希），实际 %d: %s", w.Code, w.Body.String())
	}
	if e := decodeErr(t, w); e.Code != CodeInvalidMode {
		t.Errorf("错误码 = %q，期望 %s", e.Code, CodeInvalidMode)
	}

	// mode 合法后才轮到哈希判定。
	w2 := req(t, s, http.MethodPost, "/api/v1/apply", `{"plan_hash":"deadbeef","confirmations":{"mode":"all"}}`, true)
	if w2.Code != http.StatusConflict {
		t.Errorf("mode 合法时哈希不匹配应 409，实际 %d", w2.Code)
	}
}

// D15 的主用例：空载荷 + 计划里有提权项 → 400，且不产生 task_id。
func TestApplyEmptyConfirmationsWithAdminPlanRejected(t *testing.T) {
	d := fakeDeps()
	d.BuildPlan = func(c *config.Config, decisions json.RawMessage) (*plan.Plan, error) {
		return adminPlan(planned("Java", true, false)), nil
	}
	s := newServer(d)

	for _, c := range []struct {
		body string
		code string
	}{
		// 连 mode 都没说 → invalid_mode（不默认任何行为）。
		{`{"plan_hash":"x","confirmations":{}}`, CodeInvalidMode},
		// 说了 mode=all 却一个提权项都没确认 → admin_confirmation_missing。
		{`{"plan_hash":"x","confirmations":{"mode":"all","admin_confirmed":[]}}`, CodeAdminMissing},
	} {
		w := req(t, s, http.MethodPost, "/api/v1/apply", c.body, true)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实际 %d", c.body, w.Code)
		}
		if e := decodeErr(t, w); e.Code != c.code {
			t.Errorf("%s 错误码 = %q，期望 %s", c.body, e.Code, c.code)
		}
		if strings.Contains(w.Body.String(), "task_id") {
			t.Error("被拒的请求不应产生 task_id")
		}
	}
	// 拒绝后必须没有任务在跑，否则后续请求会被 409 卡住。
	if id, _, busy := s.tasks.Busy(); busy {
		t.Errorf("被拒后不应留任务: %s", id)
	}
}

// skip_admin：缺 ack 拒；带 ack 放行，且执行器收到 SkipAdmin=true。
func TestApplySkipAdminFlow(t *testing.T) {
	d := fakeDeps()
	d.BuildPlan = func(c *config.Config, decisions json.RawMessage) (*plan.Plan, error) {
		return adminPlan(planned("Java", true, false)), nil
	}
	var got Confirmations
	d.Apply = func(ctx context.Context, p *plan.Plan, conf Confirmations, r task.Reporter) ([]byte, error) {
		got = conf
		return []byte(`{"blocked":1}`), nil
	}
	s := newServer(d)
	hash := plan.Hash(adminPlan(planned("Java", true, false)))

	w := req(t, s, http.MethodPost, "/api/v1/apply",
		`{"plan_hash":"`+hash+`","confirmations":{"mode":"skip_admin"}}`, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("skip_admin 缺 ack 应 400，实际 %d", w.Code)
	}
	if e := decodeErr(t, w); e.Code != CodeSkippedAdminAckMissing {
		t.Errorf("错误码 = %q", e.Code)
	}

	w2 := req(t, s, http.MethodPost, "/api/v1/apply",
		`{"plan_hash":"`+hash+`","confirmations":{"mode":"skip_admin","ack_skipped_admin":true}}`, true)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("skip_admin + ack 应 202，实际 %d: %s", w2.Code, w2.Body.String())
	}
	var body struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &body)
	waitTask(t, s, body.TaskID)
	if !got.SkipAdmin() {
		t.Errorf("执行器没收到 SkipAdmin：%+v", got)
	}
}

func TestApplyRebootAckRequired(t *testing.T) {
	p := adminPlan(planned("Driver", false, true))
	d := fakeDeps()
	d.BuildPlan = func(c *config.Config, decisions json.RawMessage) (*plan.Plan, error) { return p, nil }
	s := newServer(d)
	hash := plan.Hash(p)

	w := req(t, s, http.MethodPost, "/api/v1/apply",
		`{"plan_hash":"`+hash+`","confirmations":{"mode":"all"}}`, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 reboot_ack 应 400，实际 %d", w.Code)
	}
	if e := decodeErr(t, w); e.Code != CodeRebootAckRequired {
		t.Errorf("错误码 = %q", e.Code)
	}
	if !strings.Contains(w.Body.String(), "Driver") {
		t.Errorf("应点名具体项: %s", w.Body.String())
	}

	w2 := req(t, s, http.MethodPost, "/api/v1/apply",
		`{"plan_hash":"`+hash+`","confirmations":{"mode":"all","reboot_ack":true}}`, true)
	if w2.Code != http.StatusAccepted {
		t.Errorf("reboot_ack=true 应 202，实际 %d: %s", w2.Code, w2.Body.String())
	}
}

func TestApplyNetworkChangedRejected(t *testing.T) {
	d := fakeDeps()
	d.Recheck = func() (bool, string) { return false, "dial tcp: i/o timeout" }
	s := newServer(d)

	// 先拿到正确哈希
	p := &plan.Plan{App: config.AppName, Kind: "restore-plan", Schema: config.CurrentSchema,
		Target: plan.Target{NetworkOK: true, WingetOK: true},
		Items:  []plan.Item{{Kind: "software", Name: "Git", Status: plan.StatusPlanned}}}
	hash := plan.Hash(p)

	w := req(t, s, http.MethodPost, "/api/v1/apply",
		`{"plan_hash":"`+hash+`","confirmations":{"mode":"all"}}`, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("网络变化应 409，实际 %d: %s", w.Code, w.Body.String())
	}
	e := decodeErr(t, w)
	if e.Code != CodePlanNetwork {
		t.Errorf("错误码 = %q，期望 plan_stale_network", e.Code)
	}
}

func TestApplyWithCorrectHashStartsTask(t *testing.T) {
	s := newServer(fakeDeps())
	p := &plan.Plan{App: config.AppName, Kind: "restore-plan", Schema: config.CurrentSchema,
		Target: plan.Target{NetworkOK: true, WingetOK: true},
		Items:  []plan.Item{{Kind: "software", Name: "Git", Status: plan.StatusPlanned}}}
	hash := plan.Hash(p)

	w := req(t, s, http.MethodPost, "/api/v1/apply",
		`{"plan_hash":"`+hash+`","confirmations":{"mode":"all","confirmed_items":["Git"],"admin_confirmed":[],"reboot_ack":true}}`, true)
	if w.Code != http.StatusAccepted {
		t.Fatalf("哈希正确应 202，实际 %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if snap := waitTask(t, s, body.TaskID); snap.State != task.Succeeded {
		t.Errorf("apply 任务应成功: %+v", snap)
	}
}

func TestReportPathTraversalRejected(t *testing.T) {
	s := newServer(fakeDeps())
	for _, name := range []string{"..%2Fsecret", "..", "a/b", `a\b`, "x:y"} {
		w := req(t, s, http.MethodGet, "/api/v1/reports/"+name+"?format=md", "", true)
		// 307 是 ServeMux 对 ".." 做路径清理后的重定向，同样没有读到文件，属于安全结果。
		if w.Code == http.StatusOK {
			t.Errorf("%q 不应返回 200，实际 %d", name, w.Code)
		}
		if strings.Contains(w.Body.String(), "root:") {
			t.Errorf("%q 读到了系统文件", name)
		}
	}
}

func TestReportFormatValidated(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodGet, "/api/v1/reports/restore-1.md?format=exe", "", true)
	if w.Code != http.StatusBadRequest {
		t.Errorf("非法 format 应 400，实际 %d", w.Code)
	}
}

func TestRollbackRejectsBadJournalID(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodPost, "/api/v1/rollback", `{"journal_id":"../../etc/passwd"}`, true)
	if w.Code != http.StatusForbidden {
		t.Errorf("非法 journal_id 应 403，实际 %d", w.Code)
	}
}

func TestConfigSchemaTooNewMapsToCode(t *testing.T) {
	d := fakeDeps()
	d.LoadConfig = func(path string) (*config.Config, error) {
		return nil, config.ErrSchemaTooNew
	}
	s := newServer(d)
	w := req(t, s, http.MethodPost, "/api/v1/config/load", `{"path":"x.yaml"}`, true)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("高版本 schema 应 422，实际 %d", w.Code)
	}
	if e := decodeErr(t, w); e.Code != CodeSchemaTooNew {
		t.Errorf("错误码 = %q", e.Code)
	}
}

func TestCancelUnknownTask(t *testing.T) {
	s := newServer(fakeDeps())
	w := req(t, s, http.MethodPost, "/api/v1/task/nope/cancel", "", true)
	if w.Code != http.StatusNotFound {
		t.Errorf("未知任务取消应 404，实际 %d", w.Code)
	}
}

var _ = errors.New

// ④：决策文件读写与「结构不合法当场 400」。
func TestDecisionsSaveAndLoad(t *testing.T) {
	s := newServer(fakeDeps())
	saved := `{"decisions":[{"target_id":"t1","kind":"software","action":"skip","name":"Git"}]}`
	w := req(t, s, http.MethodPost, "/api/v1/decisions", saved, true)
	if w.Code != http.StatusOK {
		t.Fatalf("保存决策应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	w = req(t, s, http.MethodGet, "/api/v1/decisions", "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("读取决策应 200，实际 %d", w.Code)
	}
	var body struct {
		Decisions plan.Decisions `json:"decisions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Decisions) != 1 || body.Decisions[0].TargetID != "t1" {
		t.Fatalf("回读结果不对: %+v", body.Decisions)
	}

	// 缺 target_id / 未知 kind：能落盘的必须能被应用，坏数据一律拒绝
	for _, bad := range []string{
		`{"decisions":[{"kind":"software","action":"skip"}]}`,
		`{"decisions":[{"target_id":"t1","kind":"gadget","action":"skip"}]}`,
		`{"decisions":[{"target_id":"t1","kind":"software"}]}`,
	} {
		w = req(t, s, http.MethodPost, "/api/v1/decisions", bad, true)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("坏决策应 400: %s → %d", bad, w.Code)
		}
	}
}

// ④：决策错误必须 400 + 稳定 code + 逐条 details（B4 / D4）。
func TestPlanRejectsInvalidDecisions(t *testing.T) {
	d := fakeDeps()
	d.BuildPlan = func(*config.Config, json.RawMessage) (*plan.Plan, error) {
		return nil, &plan.DecisionErrors{Issues: []plan.DecisionIssue{{
			Index: 1, TargetID: "x", Code: plan.CodeDecisionTargetAbsent,
			Reason: "计划中找不到这个 target_id（配置或环境已变化，请重新生成计划）",
		}}}
	}
	s := newServer(d)
	w := req(t, s, http.MethodPost, "/api/v1/plan",
		`{"decisions":[{"target_id":"x","kind":"software","action":"skip"}]}`, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Error APIError `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Error.Code != CodeDecisionTargetAbsent {
		t.Errorf("错误码 = %q", env.Error.Code)
	}
	details, ok := env.Error.Details.(map[string]any)
	if !ok {
		t.Fatalf("details 类型不对: %#v", env.Error.Details)
	}
	issues, ok := details["issues"].([]any)
	if !ok || len(issues) != 1 {
		t.Fatalf("details.issues 不对: %#v", details)
	}
}

// ---- ⑤ 报告接口 ----

func sampleDoc(items int) *report.Document {
	list := make([]report.ItemResult, 0, items)
	for i := 0; i < items; i++ {
		list = append(list, report.ItemResult{
			TargetID: fmt.Sprintf("t%03d", i), Kind: "software", Name: "App",
			ExpectedVersion: "1.0", ActualVersion: "1.0", Tier: "winget", Status: "succeeded",
		})
	}
	return &report.Document{
		ReportSchema: report.CurrentSchema, Kind: report.Kind,
		GeneratedAt: "2026-10-27T16:00:00+08:00", Mode: "apply",
		Env:      report.Env{OS: "Windows 11", Build: "26100", Arch: "amd64", Username: "${USER}", Hostname: "${HOST}", WingetOK: true, NetworkOK: true},
		PlanHash: strings.Repeat("a", 64),
		Items:    list, PathChanges: []report.PathResult{}, Warnings: []string{},
		Summary: report.Summary{Total: items, Succeeded: items},
	}
}

func reportDeps(doc *report.Document, readErr error) Deps {
	d := fakeDeps()
	d.ReadReport = func(name, format string) ([]byte, error) {
		if readErr != nil {
			return nil, readErr
		}
		if format == "md" {
			return []byte("# report"), nil
		}
		return json.Marshal(doc)
	}
	return d
}

func TestReportDetailPaginates(t *testing.T) {
	s := newServer(reportDeps(sampleDoc(120), nil))

	w := req(t, s, http.MethodGet, "/api/v1/reports/restore-1.json?format=json", "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Supported  bool            `json:"supported"`
		Report     report.Document `json:"report"`
		ItemsTotal int             `json:"items_total"`
		Offset     int             `json:"offset"`
		Limit      int             `json:"limit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Supported || out.ItemsTotal != 120 || len(out.Report.Items) != 50 || out.Limit != 50 {
		t.Fatalf("默认分页不对: supported=%v total=%d page=%d limit=%d", out.Supported, out.ItemsTotal, len(out.Report.Items), out.Limit)
	}
	// 摘要与 env 每页都要有（前端不翻页也能画顶部）
	if out.Report.Summary.Succeeded != 120 || out.Report.Env.OS == "" {
		t.Errorf("分页响应丢了摘要/env: %+v", out.Report.Summary)
	}

	w = req(t, s, http.MethodGet, "/api/v1/reports/restore-1.json?format=json&offset=100&limit=50", "", true)
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Report.Items) != 20 || out.Offset != 100 {
		t.Errorf("第二页不对: %d 条 offset=%d", len(out.Report.Items), out.Offset)
	}
}

func TestReportDetailUnsupportedSchema(t *testing.T) {
	doc := sampleDoc(1)
	doc.ReportSchema = report.CurrentSchema + 1
	s := newServer(reportDeps(doc, nil))
	w := req(t, s, http.MethodGet, "/api/v1/reports/restore-1.json?format=json", "", true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("更高版本应拒绝渲染，实际 %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Error APIError `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Error.Code != CodeReportUnsupported {
		t.Errorf("错误码 = %q", env.Error.Code)
	}
}

func TestReportTooLarge(t *testing.T) {
	s := newServer(reportDeps(nil, ErrReportTooLarge))
	w := req(t, s, http.MethodGet, "/api/v1/reports/huge.json?format=json", "", true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("超限应 400，实际 %d", w.Code)
	}
	var env struct {
		Error APIError `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Error.Code != CodeReportTooLarge {
		t.Errorf("错误码 = %q", env.Error.Code)
	}
}

func TestReportDiffEndpoint(t *testing.T) {
	a := sampleDoc(3)
	b := sampleDoc(3)
	b.Items[0].Status = "failed"
	s := newServer(reportDeps(nil, nil))
	// 需要两份不同内容：按文件名区分
	deps := fakeDeps()
	deps.ReadReport = func(name, format string) ([]byte, error) {
		doc := a
		if strings.Contains(name, "b") {
			doc = b
		}
		return json.Marshal(doc)
	}
	s = newServer(deps)

	w := req(t, s, http.MethodGet, "/api/v1/reports/diff?a=restore-a.json&b=restore-b.json", "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("diff 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Diff report.Diff `json:"diff"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Diff.StatusChanges) != 1 || out.Diff.StatusChanges[0].TargetID != "t000" {
		t.Fatalf("diff 结果不对: %+v", out.Diff.StatusChanges)
	}

	// 缺参数 → 400
	w = req(t, s, http.MethodGet, "/api/v1/reports/diff?a=restore-a.json", "", true)
	if w.Code != http.StatusBadRequest {
		t.Errorf("缺 b 应 400，实际 %d", w.Code)
	}
}

// journal 不是 report：回滚弹窗要读它的 path_snapshot，必须按原文返回，
// 不能拿 report_schema 去解析它。
func TestJournalJSONServedRaw(t *testing.T) {
	d := fakeDeps()
	d.ReadReport = func(name, format string) ([]byte, error) {
		return []byte(`{"path_snapshot":{"user":{"value":"C:\\x","kind":2}}}`), nil
	}
	s := newServer(d)
	w := req(t, s, http.MethodGet, "/api/v1/reports/.envlift-journal-20260101-000000.json?format=json", "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("journal 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "path_snapshot") {
		t.Errorf("journal 原文被改写了: %s", w.Body.String())
	}
}
