package apply

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/pathmgmt"
)

// Step 是 journal 里的一步。
type Step struct {
	Index        int    `json:"index"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Tier         string `json:"tier,omitempty"`
	Command      string `json:"command,omitempty"`
	NeedsAdmin   bool   `json:"needs_admin"`
	ExpectReboot bool   `json:"expect_reboot"`
	Reinstall    bool   `json:"reinstall,omitempty"`
	ExitCode     int    `json:"exit_code"`
	Output       string `json:"output,omitempty"`
	Status       string `json:"status"` // ok / failed / skipped / blocked
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
	// ActualVersion 是 verify 输出里解析出的版本（执行后）；解析不出来就留空，
	// 原始输出记在 VersionRaw 里，报告据此标 version_unparsed（C2）。
	ActualVersion string `json:"actual_version,omitempty"`
	VersionRaw    string `json:"version_raw,omitempty"`
}

// 步骤状态。
const (
	StepOK      = "ok"
	StepFailed  = "failed"
	StepSkipped = "skipped"
	StepBlocked = "blocked"
)

// Journal 是 apply 的事务日志（C2）。
//
// 每执行完一步立刻整体重写落盘，不做缓冲——中途崩溃或重启后，
// 靠它才能知道装到哪了（P1 幂等与 expect_reboot 都依赖这一点）。
type Journal struct {
	App        string             `json:"app"`
	Kind       string             `json:"kind"`
	Schema     int                `json:"schema"`
	StartedAt  string             `json:"started_at"`
	ConfigPath string             `json:"config_path"`
	Snapshot   *pathmgmt.Snapshot `json:"path_snapshot,omitempty"`
	Steps      []Step             `json:"steps"`
	Finished   bool               `json:"finished"`
	Stopped    string             `json:"stopped_reason,omitempty"`

	path string
}

// NewJournal 在 path 处创建并落盘一份空日志。
func NewJournal(path, configPath string) (*Journal, error) {
	j := &Journal{
		App:        config.AppName,
		Kind:       "apply-journal",
		Schema:     config.CurrentSchema,
		StartedAt:  time.Now().Format(time.RFC3339),
		ConfigPath: configPath,
		Steps:      []Step{}, // 空数组而不是 null，便于下游直接遍历
		path:       path,
	}
	if err := j.flush(); err != nil {
		return nil, err
	}
	return j, nil
}

// Path 返回日志文件路径。
func (j *Journal) Path() string { return j.path }

// SetSnapshot 记录 PATH 快照并立即落盘。
func (j *Journal) SetSnapshot(s pathmgmt.Snapshot) error {
	j.Snapshot = &s
	return j.flush()
}

// Append 追加一步并立即落盘。
func (j *Journal) Append(s Step) error {
	j.Steps = append(j.Steps, s)
	return j.flush()
}

// Finish 标记结束并落盘。
func (j *Journal) Finish(reason string) error {
	j.Finished = true
	j.Stopped = reason
	return j.flush()
}

func (j *Journal) flush() error {
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		return fmt.Errorf("创建 journal 目录失败: %w", err)
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(j.path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("写入 journal 失败: %w", err)
	}
	return nil
}

// WriteSnapshotBak 另存一份人类可读的 PATH 备份（C3）。
func WriteSnapshotBak(journalPath string, s pathmgmt.Snapshot) (string, error) {
	bak := journalPath + ".path.bak"
	content := fmt.Sprintf(`# envlift PATH 备份
# 时间: %s
# 回滚: envlift restore --rollback --journal "%s"
# 值类型: %d = REG_SZ, %d = REG_EXPAND_SZ

[HKCU\Environment]
Path=%s
Kind=%d

[HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment]
Path=%s
Kind=%d
`, s.TakenAt, journalPath,
		pathmgmt.KindSZ, pathmgmt.KindExpandSZ,
		s.User.Value, s.User.Kind,
		s.Machine.Value, s.Machine.Kind)

	if err := os.WriteFile(bak, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("写入 PATH 备份失败: %w", err)
	}
	return bak, nil
}

// LoadJournal 读回日志。
func LoadJournal(path string) (*Journal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 journal %s 失败: %w", path, err)
	}
	var j Journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("解析 journal 失败: %w", err)
	}
	j.path = path
	return &j, nil
}

// LatestJournal 在 dir 下找最新的 journal。
func LatestJournal(dir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, ".envlift-journal-*.json"))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("在 %s 下找不到 journal", dir)
	}
	newest := matches[0]
	for _, m := range matches[1:] {
		if m > newest { // 文件名带时间戳，字典序即时间序
			newest = m
		}
	}
	return newest, nil
}
