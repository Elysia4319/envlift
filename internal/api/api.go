// Package api 提供 GUI 的 /api/v1 接口层。
//
// 分工：本包只做 HTTP 语义（路由、鉴权、错误信封、参数校验、plan_hash 校验），
// 真正的扫描/计划/执行全部通过 Deps 注入内核函数（D-2：GUI 不自己实现业务逻辑）。
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/plan"
	"github.com/Elysia4319/envlift/internal/report"
	"github.com/Elysia4319/envlift/internal/task"
)

// 错误码（C 段：稳定字符串，前端靠它分支，不靠自然语言）。
const (
	CodeUnauthorized  = "unauthorized"
	CodeBusy          = "task_busy"
	CodeNotFound      = "not_found"
	CodeBadRequest    = "bad_request"
	CodePlanStale     = "plan_stale"
	CodePlanNetwork   = "plan_stale_network"
	CodeSchemaTooNew  = "config_schema_too_new"
	CodePathForbidden = "path_forbidden"
	CodeInternal      = "internal"
	// CodeUnknownTarget：exclude / decisions 里出现了计划或扫描结果中不存在的标识。
	// 一律报错，绝不静默忽略——静默忽略会把「我以为排除了/跳过了」变成实际执行。
	CodeUnknownTarget = "unknown_target"
	// D15：apply 入口防线的三个错误码，前端按码分支，不匹配自然语言。
	CodeInvalidMode            = "invalid_mode"
	CodeAdminMissing           = "admin_confirmation_missing"
	CodeSkippedAdminAckMissing = "skipped_admin_ack_missing"
	CodeRebootAckRequired      = "reboot_ack_required"
	// ④ 决策相关（D4）。取值与 plan 包里的常量同源，前端只认字符串。
	CodeInvalidDecisions      = plan.CodeInvalidDecisions
	CodeDecisionTargetAbsent  = plan.CodeDecisionTargetAbsent
	CodeInvalidDecisionParams = plan.CodeInvalidDecisionParam
)

// APIError 是统一错误信封的内容。
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

type errEnvelope struct {
	Error APIError `json:"error"`
}

// ReportInfo 是 reports 目录里的一项。
//
// Kind 区分两种文件：还原报告（md + json 双份）与事务日志 journal（json）。
// PATH 页要靠 kind=journal 的条目给出回滚入口，报告页只列 kind=report。
type ReportInfo struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // report | journal
	Modified  string `json:"modified"`
	SizeBytes int64  `json:"size_bytes"`

	// 以下是报告头部（列表页直接用，不必再逐份拉详情）。
	ReportSchema int             `json:"report_schema,omitempty"`
	Mode         string          `json:"mode,omitempty"`
	Summary      *report.Summary `json:"summary,omitempty"`
	Env          *report.Env     `json:"env,omitempty"`
	PlanHash     string          `json:"plan_hash,omitempty"`
	// Truncated：文件超过体积上限，只给 stat 摘要，不解析。
	Truncated bool `json:"truncated,omitempty"`
	// Unsupported：格式版本不受支持（更高或没有版本号），前端只显示「需升级」。
	Unsupported bool `json:"unsupported,omitempty"`
}

// 报告目录里的两种文件类型。
const (
	KindReport  = "report"
	KindJournal = "journal"
)

// Confirmations 是 apply 的独立确认载荷（C-2 / D15）。
//
// Mode 没有默认值：空载荷不再被解释成「可以跑」，这是 D15 要堵的缝。
type Confirmations struct {
	// Mode 见 ConfirmModeAll / ConfirmModeSkipAdmin，必填。
	Mode string `json:"mode"`
	// AckSkippedAdmin 在 mode=skip_admin 时必须为 true，声明「我知道提权项会被跳过」。
	AckSkippedAdmin bool     `json:"ack_skipped_admin"`
	ConfirmedItems  []string `json:"confirmed_items"`
	AdminConfirmed  []string `json:"admin_confirmed"`
	RebootAck       bool     `json:"reboot_ack"`
}

// SkipAdmin 报告本次是否只跑非提权部分。
func (c Confirmations) SkipAdmin() bool { return c.Mode == ConfirmModeSkipAdmin }

// HasAdmin 报告某条目是否已单独确认提权。
func (c Confirmations) HasAdmin(name string) bool {
	for _, n := range c.AdminConfirmed {
		if n == name {
			return true
		}
	}
	return false
}

// Deps 是内核函数的注入点。测试用假实现，生产用 engine 包。
// CommitRequest 是写配置的入参。
//
// Exclude 是 A2 授权的内核语义扩展：把指定条目从 `software:` 挪到配置顶层
// `x-excluded:` 区。默认空 = 全量导出（忠实快照），排除是用户显式选择的结果。
type CommitRequest struct {
	ScanResult []byte
	Out        string
	OnExist    string
	Exclude    []string
}

// CommitResult 回报写盘结果。
//
// Total 是**采到的**条目总数（不因排除而变小），Excluded 单列，否则用户会看到
// 「排除 5 条后总数少了 5」这种对不上的数字。
type CommitResult struct {
	Written  string `json:"written"`
	Total    int    `json:"total"`
	Excluded int    `json:"excluded"`
}

// UnknownTargets 表示 exclude 里出现了扫描结果里不存在的 target_id。
//
// 必须报错而不是静默忽略：用户以为「排除了」，实际那条还在配置里，
// 结果和他想的不一样却不报错——属于最难发现的错。
type UnknownTargets struct {
	IDs []string
}

func (e *UnknownTargets) Error() string {
	return "exclude 里有扫描结果中不存在的 target_id: " + strings.Join(e.IDs, "、")
}

type Deps struct {
	Status     func(ctx context.Context) (any, error)
	Scan       func(ctx context.Context, raw bool, r task.Reporter) ([]byte, error)
	Commit     func(req CommitRequest) (CommitResult, error)
	LoadConfig func(path string) (*config.Config, error)
	BuildPlan  func(cfg *config.Config, decisions json.RawMessage) (*plan.Plan, error)
	// LoadDecisions / SaveDecisions 读写 envlift.decisions.yaml（D3）。
	LoadDecisions func() (plan.Decisions, error)
	SaveDecisions func(ds plan.Decisions) error
	Apply         func(ctx context.Context, p *plan.Plan, conf Confirmations, r task.Reporter) ([]byte, error)
	Recheck       func() (networkOK bool, note string)
	Rollback      func(journalID string) error
	ListReports   func() ([]ReportInfo, error)
	ReadReport    func(name, format string) ([]byte, error)
}

// Server 是 API 层。
type Server struct {
	deps       Deps
	tasks      *task.Manager
	token      string
	reportsDir string
}

// New 创建 API 层。
func New(deps Deps, tasks *task.Manager, token, reportsDir string) *Server {
	return &Server{deps: deps, tasks: tasks, token: token, reportsDir: reportsDir}
}

const maxBody = 1 << 20 // 1MB，防止超大请求体（D-5）

// Routes 返回带 /api/v1 前缀的处理器。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	mux.HandleFunc("POST /api/v1/scan", s.handleScan)
	mux.HandleFunc("POST /api/v1/scan/commit", s.handleScanCommit)
	mux.HandleFunc("GET /api/v1/config/current", s.handleConfigCurrent)
	mux.HandleFunc("POST /api/v1/config/load", s.handleConfigLoad)
	mux.HandleFunc("POST /api/v1/plan", s.handlePlan)
	mux.HandleFunc("POST /api/v1/apply", s.handleApply)
	mux.HandleFunc("GET /api/v1/decisions", s.handleDecisionsGet)
	mux.HandleFunc("POST /api/v1/decisions", s.handleDecisionsSave)
	mux.HandleFunc("GET /api/v1/task/{id}", s.handleTask)
	mux.HandleFunc("POST /api/v1/task/{id}/cancel", s.handleTaskCancel)
	mux.HandleFunc("GET /api/v1/reports", s.handleReports)
	mux.HandleFunc("GET /api/v1/reports/diff", s.handleReportDiff)
	mux.HandleFunc("GET /api/v1/reports/{name}", s.handleReport)
	mux.HandleFunc("POST /api/v1/rollback", s.handleRollback)
	return s.requireToken(mux)
}

// requireToken 只认请求头（D-1：token 不进 query，避免出现在浏览器历史与 Referer）。
func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Envlift-Token") != s.token || s.token == "" {
			writeErr(w, http.StatusUnauthorized, CodeUnauthorized, "缺少或错误的 token（只接受 X-Envlift-Token 请求头）", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string, details any) {
	writeJSON(w, status, errEnvelope{Error: APIError{Code: code, Message: msg, Details: details}})
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, CodeBadRequest, "请求体解析失败: "+err.Error(), nil)
		return false
	}
	return true
}

// startTask 启动任务；忙时回 409 并带当前任务 id（B2）。
func (s *Server) startTask(w http.ResponseWriter, kind string, work task.Work) {
	id, err := s.tasks.Start(kind, work)
	if err != nil {
		if errors.Is(err, task.ErrBusy) {
			cur, state, _ := s.tasks.Busy()
			writeErr(w, http.StatusConflict, CodeBusy, "已有任务在运行", map[string]any{
				"task_id": cur, "state": state,
			})
			return
		}
		writeErr(w, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"task_id": id})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.deps.Status(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Raw bool `json:"raw"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	s.startTask(w, "scan", func(ctx context.Context, rep task.Reporter) ([]byte, error) {
		return s.deps.Scan(ctx, req.Raw, rep)
	})
}

func (s *Server) handleScanCommit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID  string   `json:"task_id"`
		Out     string   `json:"out"`
		OnExist string   `json:"on_exist"`
		Exclude []string `json:"exclude"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.OnExist != "" && req.OnExist != "rename" && req.OnExist != "backup_overwrite" {
		writeErr(w, http.StatusBadRequest, CodeBadRequest, `on_exist 只能是 "rename" 或 "backup_overwrite"`, nil)
		return
	}
	snap, ok := s.tasks.Get(req.TaskID, 0)
	if !ok || snap.Kind != "scan" || snap.State != task.Succeeded {
		writeErr(w, http.StatusBadRequest, CodeBadRequest, "task_id 无效或扫描尚未成功完成", nil)
		return
	}
	res, err := s.deps.Commit(CommitRequest{
		ScanResult: snap.Result,
		Out:        req.Out,
		OnExist:    req.OnExist,
		Exclude:    req.Exclude,
	})
	if err != nil {
		var unknown *UnknownTargets
		if errors.As(err, &unknown) {
			writeErr(w, http.StatusBadRequest, CodeUnknownTarget, err.Error(),
				map[string]any{"unknown": unknown.IDs})
			return
		}
		if errors.Is(err, ErrPathForbidden) {
			writeErr(w, http.StatusForbidden, CodePathForbidden, err.Error(), nil)
			return
		}
		writeErr(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleConfigCurrent(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.deps.LoadConfig("envlift.yaml")
	if err != nil {
		writeErr(w, http.StatusNotFound, CodeNotFound, "当前目录没有可用配置: "+err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleConfigLoad(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, CodeBadRequest, "path 不能为空", nil)
		return
	}
	cfg, err := s.deps.LoadConfig(req.Path)
	if err != nil {
		if errors.Is(err, config.ErrSchemaTooNew) {
			writeErr(w, http.StatusUnprocessableEntity, CodeSchemaTooNew,
				"配置 schema 版本高于本程序支持，请升级 envlift", map[string]any{"path": req.Path})
			return
		}
		writeErr(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config    string          `json:"config"`
		Decisions json.RawMessage `json:"decisions"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	cfgPath := orDefault(req.Config, "envlift.yaml")
	cfg, err := s.deps.LoadConfig(cfgPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
		return
	}
	// 同步返回（D1）：决策不合法必须当场 400 并逐条列出，而不是塞进任务结果里，
	// 让人对着一个 202 去猜哪里错了。
	p, err := s.deps.BuildPlan(cfg, req.Decisions)
	if err != nil {
		if writeDecisionsErr(w, err) {
			return
		}
		writeErr(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan_hash": plan.Hash(p), "plan": p})
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config    string          `json:"config"`
		Decisions json.RawMessage `json:"decisions"`
		PlanHash  string          `json:"plan_hash"`
		// 两种等价形态都接受：嵌套 confirmations（D15 原文）与 D2 的扁平字段。
		// 扁平字段优先；两者都空时按 mode 必填报错，不存在默认放行。
		Confirmations   Confirmations `json:"confirmations"`
		Mode            string        `json:"mode"`
		AckSkippedAdmin bool          `json:"ack_skipped_admin"`
		ConfirmedItems  []string      `json:"confirmed_items"`
		AdminConfirmed  []string      `json:"admin_confirmed"`
		RebootAck       bool          `json:"reboot_ack"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	conf := req.Confirmations
	if conf.Mode == "" {
		conf = Confirmations{
			Mode: req.Mode, AckSkippedAdmin: req.AckSkippedAdmin,
			ConfirmedItems: req.ConfirmedItems, AdminConfirmed: req.AdminConfirmed,
			RebootAck: req.RebootAck,
		}
	}
	// A3：校验顺序 = token → 请求体结构 → mode → 计划 → confirmations/reboot_ack
	// → plan_hash → 网络重探测。任何一步不过都不产生 task_id。
	if e := ValidateMode(conf.Mode); e != nil {
		writeGuardErr(w, e)
		return
	}
	cfgPath := orDefault(req.Config, "envlift.yaml")
	cfg, err := s.deps.LoadConfig(cfgPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
		return
	}
	// C-1：重新生成计划并比对哈希——前端展示的必须与实际执行的是同一份。
	p, err := s.deps.BuildPlan(cfg, req.Decisions)
	if err != nil {
		if writeDecisionsErr(w, err) {
			return
		}
		writeErr(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
		return
	}
	// D15：mode / admin_confirmed / ack_skipped_admin / reboot_ack 在这里定生死。
	if e := ValidateApplyLoad(p, conf); e != nil {
		writeGuardErr(w, e)
		return
	}
	if req.PlanHash == "" {
		writeErr(w, http.StatusBadRequest, CodeBadRequest, "缺少 plan_hash：apply 必须携带 plan 生成时返回的哈希", nil)
		return
	}
	if got := plan.Hash(p); got != req.PlanHash {
		writeErr(w, http.StatusConflict, CodePlanStale,
			"计划已过期：配置或环境在预览之后发生了变化，请重新生成计划",
			map[string]any{"expected": req.PlanHash, "actual": got})
		return
	}
	// C-3：apply 开始时重新探测网络，与计划生成时不一致就停，不自行改道。
	if s.deps.Recheck != nil {
		ok, note := s.deps.Recheck()
		if ok != p.Target.NetworkOK {
			writeErr(w, http.StatusConflict, CodePlanNetwork,
				"网络状态与生成计划时不一致，请重跑预演",
				map[string]any{"plan_network_ok": p.Target.NetworkOK, "now_ok": ok, "note": note})
			return
		}
	}

	s.startTask(w, "apply", func(ctx context.Context, rep task.Reporter) ([]byte, error) {
		return s.deps.Apply(ctx, p, conf, rep)
	})
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	since := int64(0)
	if v := r.URL.Query().Get("since"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &since); err != nil {
			writeErr(w, http.StatusBadRequest, CodeBadRequest, "since 必须是整数", nil)
			return
		}
	}
	snap, ok := s.tasks.Get(id, since)
	if !ok {
		writeErr(w, http.StatusNotFound, CodeNotFound, "任务不存在或已被淘汰", nil)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.tasks.Cancel(id) {
		writeErr(w, http.StatusNotFound, CodeNotFound, "任务不存在或已结束", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
}

func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.ListReports()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": list})
}

// handleReport 读单份报告。
//
// format=json：结构化（前端渲染用），items 支持 offset/limit 分页；
// format=md  ：原文（前端以 <pre> 展示，不做 md→HTML 渲染）。
// 格式版本不受支持时返回 200 + supported:false，前端显示「请升级」而不是白屏。
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !safeReportName(name) {
		writeErr(w, http.StatusForbidden, CodePathForbidden, "非法的报告名", nil)
		return
	}
	format := orDefault(r.URL.Query().Get("format"), "md")
	switch format {
	case "md":
		// 详情页传的是 json 那份的名字；md 是同一份报告的另一种呈现。
		mdName := name
		if strings.HasSuffix(mdName, ".json") {
			mdName = strings.TrimSuffix(mdName, ".json") + ".md"
		}
		data, err := s.deps.ReadReport(mdName, "md")
		if err != nil {
			if errors.Is(err, ErrReportTooLarge) {
				writeErr(w, http.StatusBadRequest, CodeReportTooLarge, err.Error(),
					map[string]any{"limit_bytes": MaxReportBytes})
				return
			}
			writeErr(w, http.StatusNotFound, CodeNotFound, err.Error(), nil)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)

	case "json":
		// journal 不是 report（没有 report_schema），回滚弹窗要读它的 path_snapshot；
		// 这一条按原文返回，绝不用报告格式去解析它。
		if isJournalName(name) {
			data, err := s.deps.ReadReport(name, "json")
			if err != nil {
				writeErr(w, http.StatusNotFound, CodeNotFound, err.Error(), nil)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		doc, e := s.loadReport(name)
		if e != nil {
			writeJSON(w, http.StatusBadRequest, errEnvelope{Error: *e})
			return
		}
		offset, limit := parsePaging(r)
		total := len(doc.Items)
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		out := *doc
		out.Items = doc.Items[offset:end]
		writeJSON(w, http.StatusOK, map[string]any{
			"supported": true, "report": out,
			"items_total": total, "offset": offset, "limit": limit,
		})

	default:
		writeErr(w, http.StatusBadRequest, CodeBadRequest, `format 只能是 md 或 json`, nil)
	}
}

// isJournalName 判断是不是事务日志文件（回滚入口要读它的原始 JSON）。
func isJournalName(name string) bool { return strings.HasPrefix(name, ".envlift-journal-") }

// safeReportName 只允许 reports 目录下的普通文件名（D-3）。
func safeReportName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") {
		return false
	}
	if filepath.Base(name) != name {
		return false
	}
	for _, r := range name {
		ok := r == '-' || r == '_' || r == '.' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JournalID string `json:"journal_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if !safeReportName(req.JournalID) {
		writeErr(w, http.StatusForbidden, CodePathForbidden, "非法的 journal_id", nil)
		return
	}
	if err := s.deps.Rollback(req.JournalID); err != nil {
		writeErr(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rolled_back": true})
}

// ErrPathForbidden 表示写入路径不在白名单内（D-4）。
var ErrPathForbidden = errors.New("写入路径不在白名单内（只允许配置的 out、reports、journal 目录）")

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// writeDecisionsErr 把决策校验失败转成 400 + 逐条 details（B4 / D4）。
//
// details.issues 直接是前端要渲染的列表：第几条、哪个条目、为什么。
// 「加载了但没生效」这种静默失败在这里被彻底堵死。
func writeDecisionsErr(w http.ResponseWriter, err error) bool {
	var de *plan.DecisionErrors
	if !errors.As(err, &de) {
		return false
	}
	writeErr(w, http.StatusBadRequest, de.PrimaryCode(), de.Error(), map[string]any{
		"issues": de.Issues,
	})
	return true
}

func (s *Server) handleDecisionsGet(w http.ResponseWriter, r *http.Request) {
	if s.deps.LoadDecisions == nil {
		writeJSON(w, http.StatusOK, map[string]any{"decisions": plan.Decisions{}})
		return
	}
	ds, err := s.deps.LoadDecisions()
	if err != nil {
		writeErr(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
		return
	}
	if ds == nil {
		ds = plan.Decisions{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": ds})
}

func (s *Server) handleDecisionsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Decisions plan.Decisions `json:"decisions"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if s.deps.SaveDecisions == nil {
		writeErr(w, http.StatusInternalServerError, CodeInternal, "当前环境不支持保存决策文件", nil)
		return
	}
	// 保存前先做一遍结构校验：能落盘的必须能被应用，别把坏数据写进文件。
	if e := validateDecisionShape(req.Decisions); e != nil {
		writeErr(w, http.StatusBadRequest, e.Code, e.Message, e.Details)
		return
	}
	if err := s.deps.SaveDecisions(req.Decisions); err != nil {
		writeErr(w, http.StatusInternalServerError, CodeInternal, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": len(req.Decisions)})
}

// validateDecisionShape 只校验「结构上能落盘」：target_id / kind / action 必填，
// 取值范围合法。是否命中计划由 plan.Build 负责（那里才有计划）。
func validateDecisionShape(ds plan.Decisions) *APIError {
	var issues []plan.DecisionIssue
	for i, d := range ds {
		idx := i + 1
		add := func(reason string) {
			issues = append(issues, plan.DecisionIssue{
				Index: idx, TargetID: d.TargetID, Code: plan.CodeInvalidDecisions, Reason: reason,
			})
		}
		if strings.TrimSpace(d.TargetID) == "" {
			add("缺少 target_id")
		}
		switch d.Kind {
		case plan.DecKindSoftware, plan.DecKindRuntime, plan.DecKindPath:
		default:
			add(fmt.Sprintf("未知 kind=%q（只允许 software / runtime / path）", d.Kind))
		}
		if d.Action == "" {
			add("缺少 action")
		}
	}
	if len(issues) == 0 {
		return nil
	}
	return &APIError{
		Code:    plan.CodeInvalidDecisions,
		Message: "决策结构不合法",
		Details: map[string]any{"issues": issues},
	}
}

// 报告接口用的常量与错误。
const (
	// MaxReportBytes 是详情接口的体积上限：超过就明确报错，不给一个会超时的请求。
	MaxReportBytes = 5 << 20
	// DefaultItemPage 是 items 的默认分页大小（与扫描页一致：一屏放得下）。
	DefaultItemPage = 50
)

// ErrReportTooLarge 表示报告超过体积上限。
var ErrReportTooLarge = errors.New("报告超过 5MB 体积上限，请用命令行或 grep 查看原文")

// CodeReportUnsupported / CodeReportTooLarge 是报告页的稳定错误码。
const (
	CodeReportUnsupported = "report_unsupported_schema"
	CodeReportTooLarge    = "report_too_large"
)

// parsePaging 读 offset / limit；非法值回落到默认（不静默截断）。
func parsePaging(r *http.Request) (offset, limit int) {
	limit = DefaultItemPage
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return offset, limit
}

// loadReport 读一份结构化报告；体积超限与格式过高分别给出稳定错误码。
func (s *Server) loadReport(name string) (*report.Document, *APIError) {
	data, err := s.deps.ReadReport(name, "json")
	if err != nil {
		if errors.Is(err, ErrReportTooLarge) {
			return nil, &APIError{Code: CodeReportTooLarge, Message: err.Error(),
				Details: map[string]any{"limit_bytes": MaxReportBytes, "name": name}}
		}
		return nil, &APIError{Code: CodeNotFound, Message: err.Error()}
	}
	doc, perr := report.Parse(data)
	schema := 0
	if doc != nil {
		schema = doc.ReportSchema
	}
	if perr != nil {
		return nil, &APIError{Code: CodeReportUnsupported, Message: perr.Error(),
			Details: map[string]any{"report_schema": schema, "supported": report.CurrentSchema}}
	}
	return doc, nil
}

func (s *Server) handleReportDiff(w http.ResponseWriter, r *http.Request) {
	a := r.URL.Query().Get("a")
	b := r.URL.Query().Get("b")
	for _, n := range []string{a, b} {
		if !safeReportName(n) {
			writeErr(w, http.StatusBadRequest, CodeBadRequest, "a / b 必须是 reports 目录下的报告文件名", nil)
			return
		}
	}
	da, e := s.loadReport(a)
	if e != nil {
		writeErr(w, http.StatusBadRequest, e.Code, "A："+e.Message, e.Details)
		return
	}
	db, eb := s.loadReport(b)
	if eb != nil {
		writeErr(w, http.StatusBadRequest, eb.Code, "B："+eb.Message, eb.Details)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"a": a, "b": b, "diff": report.ComputeDiff(da, db),
	})
}
