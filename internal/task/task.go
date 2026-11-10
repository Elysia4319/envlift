// Package task 提供异步任务管理：状态机、单任务锁、增量日志、取消。
//
// 刻意不依赖 HTTP —— 测试直接注入「慢速假任务」就能验证全部行为，
// 不需要起端口、不碰注册表、不调 winget（E1/E3）。
package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// State 是任务状态。
type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	Cancelled State = "cancelled"
	// AwaitingReboot 是独立终态，不是 Failed：expect_reboot 的项完成后落到这里，
	// 前端据此提示「重启后重新运行」（B1/P2）。
	AwaitingReboot State = "awaiting_reboot"
)

// Terminal 报告是否为终态。
func (s State) Terminal() bool {
	switch s {
	case Succeeded, Failed, Cancelled, AwaitingReboot:
		return true
	}
	return false
}

// ErrAwaitReboot 由工作函数返回，表示需要重启才能继续。
var ErrAwaitReboot = errors.New("需要重启后继续")

// ErrBusy 表示已有任务在跑。
var ErrBusy = errors.New("已有任务在运行")

// LogEntry 是一条增量日志。
type LogEntry struct {
	Seq     int64  `json:"seq"`
	Time    string `json:"time"`
	Level   string `json:"level"` // info | warn | error
	Message string `json:"message"`
}

// Snapshot 是给前端的任务快照。
type Snapshot struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State State  `json:"state"`
	Phase string `json:"phase,omitempty"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Error string `json:"error,omitempty"`
	// Result 用 RawMessage：[]byte 会被 encoding/json 编成 base64 字符串，
	// 前端拿到的是乱码而不是结构化 JSON。
	Result  json.RawMessage `json:"result,omitempty"`
	Logs    []LogEntry      `json:"logs,omitempty"`
	NextSeq int64           `json:"next_seq"`
}

// Reporter 是工作函数上报进度与日志的接口。
type Reporter interface {
	Log(level, message string)
	Phase(name string, done, total int)
}

// Work 是一个任务的工作函数。返回值会被序列化进快照的 Result。
type Work func(ctx context.Context, r Reporter) (result []byte, err error)

type taskState struct {
	id       string
	kind     string
	state    State
	phase    string
	done     int
	total    int
	errMsg   string
	result   []byte
	logs     []LogEntry
	seq      int64
	cancel   context.CancelFunc
	canceled bool
}

// Manager 管理任务。
type Manager struct {
	mu      sync.Mutex
	tasks   map[string]*taskState
	order   []string
	active  string
	maxKeep int
	logCap  int
}

// NewManager 创建管理器。maxKeep 是保留的任务数，logCap 是单任务日志上限（B3/B6）。
func NewManager(maxKeep, logCap int) *Manager {
	if maxKeep <= 0 {
		maxKeep = 20
	}
	if logCap <= 0 {
		logCap = 5000
	}
	return &Manager{
		tasks:   make(map[string]*taskState),
		maxKeep: maxKeep,
		logCap:  logCap,
	}
}

// Busy 返回当前运行中的任务（B2 单任务锁）。
func (m *Manager) Busy() (string, State, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == "" {
		return "", "", false
	}
	t := m.tasks[m.active]
	if t == nil || t.state.Terminal() {
		return "", "", false
	}
	return t.id, t.state, true
}

// Start 启动一个任务。已有任务在跑时返回 ErrBusy 与当前任务 id（B2）。
func (m *Manager) Start(kind string, work Work) (string, error) {
	m.mu.Lock()
	if id, state, busy := m.activeTaskLocked(); busy {
		m.mu.Unlock()
		return id, fmt.Errorf("%w（当前任务 %s，状态 %s）", ErrBusy, id, state)
	}
	id := m.newIDLocked()
	ctx, cancel := context.WithCancel(context.Background())
	t := &taskState{
		id:     id,
		kind:   kind,
		state:  Queued,
		cancel: cancel,
	}
	m.tasks[id] = t
	m.order = append(m.order, id)
	m.active = id
	m.trimLocked()
	m.mu.Unlock()

	go m.run(ctx, t, work)
	return id, nil
}

func (m *Manager) activeTaskLocked() (string, State, bool) {
	if m.active == "" {
		return "", "", false
	}
	t := m.tasks[m.active]
	if t == nil || t.state.Terminal() {
		return "", "", false
	}
	return t.id, t.state, true
}

func (m *Manager) newIDLocked() string {
	return fmt.Sprintf("t%d", time.Now().UnixNano())
}

func (m *Manager) trimLocked() {
	for len(m.order) > m.maxKeep {
		oldest := m.order[0]
		m.order = m.order[1:]
		if oldest != m.active {
			delete(m.tasks, oldest)
		}
	}
}

func (m *Manager) run(ctx context.Context, t *taskState, work Work) {
	m.setRunning(t)
	rep := &reporter{m: m, t: t}
	result, err := work(ctx, rep)

	m.mu.Lock()
	defer m.mu.Unlock()
	t.result = result
	switch {
	case t.canceled:
		t.state = Cancelled
		if t.errMsg == "" {
			t.errMsg = "任务已取消"
		}
	case errors.Is(err, ErrAwaitReboot):
		t.state = AwaitingReboot
		t.errMsg = ""
	case err != nil:
		t.state = Failed
		t.errMsg = err.Error()
	default:
		t.state = Succeeded
	}
	t.phase = ""
	if m.active == t.id {
		m.active = ""
	}
}

func (m *Manager) setRunning(t *taskState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.state == Queued {
		t.state = Running
	}
}

// Get 返回任务快照；since > 0 时只返回 seq 之后的新增日志（B3）。
func (m *Manager) Get(id string, since int64) (Snapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return Snapshot{}, false
	}
	snap := Snapshot{
		ID:      t.id,
		Kind:    t.kind,
		State:   t.state,
		Phase:   t.phase,
		Done:    t.done,
		Total:   t.total,
		Error:   t.errMsg,
		Result:  json.RawMessage(t.result),
		NextSeq: t.seq + 1,
	}
	for _, e := range t.logs {
		if e.Seq > since {
			snap.Logs = append(snap.Logs, e)
		}
	}
	return snap, true
}

// Cancel 尽力而为地取消任务（B5）。已发出的子命令不会被强杀。
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	t, ok := m.tasks[id]
	if !ok || t.state.Terminal() {
		m.mu.Unlock()
		return false
	}
	t.canceled = true
	if t.cancel != nil {
		t.cancel()
	}
	m.mu.Unlock()
	return true
}

// reporter 把工作函数的上报写回任务状态。
type reporter struct {
	m *Manager
	t *taskState
}

func (r *reporter) Log(level, message string) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	r.t.seq++
	r.t.logs = append(r.t.logs, LogEntry{
		Seq:     r.t.seq,
		Time:    time.Now().Format(time.RFC3339),
		Level:   level,
		Message: message,
	})
	// ring buffer：超出上限丢最旧的（B3）。
	if len(r.t.logs) > r.m.logCap {
		r.t.logs = r.t.logs[len(r.t.logs)-r.m.logCap:]
	}
}

func (r *reporter) Phase(name string, done, total int) {
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	r.t.phase = name
	r.t.done = done
	r.t.total = total
}
