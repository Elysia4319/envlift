// Package relocation 记录一次性的安装目录变更，避免每次 apply 重复重装。
package relocation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath 是工作目录下的默认账本文件名。
const DefaultPath = "envlift.relocations.yaml"

// Entry 是一次已完成的安装目录变更。
type Entry struct {
	TargetID   string `yaml:"target_id" json:"target_id"`
	InstallDir string `yaml:"install_dir" json:"install_dir"`
	// ActualDir 是安装后从注册表读到的实际目录；读不到时留空，不猜。
	ActualDir   string `yaml:"actual_dir,omitempty" json:"actual_dir,omitempty"`
	Verified    bool   `yaml:"verified" json:"verified"`
	CompletedAt string `yaml:"completed_at" json:"completed_at"`
	LastCommand string `yaml:"last_command,omitempty" json:"last_command,omitempty"`
}

// Ledger 是本地重装账本。
type Ledger struct {
	Version     int     `yaml:"version" json:"version"`
	Relocations []Entry `yaml:"relocations" json:"relocations"`
}

// New 返回空账本。
func New() *Ledger {
	return &Ledger{Version: 1, Relocations: []Entry{}}
}

// Load 读取账本；文件不存在视为空账本。
func Load(path string) (*Ledger, error) {
	if path == "" {
		path = DefaultPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return New(), nil
		}
		return nil, fmt.Errorf("读取重装账本 %s 失败: %w", path, err)
	}
	var l Ledger
	if err := yaml.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("解析重装账本 %s 失败: %w", path, err)
	}
	if l.Version > 1 {
		return nil, fmt.Errorf("账本 %s 版本为 %d，账本版本高于本程序支持（最高 %d），请升级 envlift",
			path, l.Version, 1)
	}
	if l.Version == 0 {
		l.Version = 1
	}
	if l.Relocations == nil {
		l.Relocations = []Entry{}
	}
	return &l, nil
}

// Save 原子替换账本文件。
func Save(path string, l *Ledger) error {
	if path == "" {
		path = DefaultPath
	}
	if l == nil {
		return fmt.Errorf("重装账本为空")
	}
	if l.Version == 0 {
		l.Version = 1
	}
	data, err := yaml.Marshal(l)
	if err != nil {
		return fmt.Errorf("编码重装账本失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建重装账本目录失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入重装账本失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换重装账本失败: %w", err)
	}
	return nil
}

// Find 按 target_id 与归一化目录查找已完成记录。
func (l *Ledger) Find(targetID, installDir string) (Entry, bool) {
	if l == nil {
		return Entry{}, false
	}
	want := NormalizeDir(installDir)
	for _, e := range l.Relocations {
		if e.TargetID == targetID && NormalizeDir(e.InstallDir) == want {
			return e, true
		}
	}
	return Entry{}, false
}

// Upsert 写入或更新同一 target_id 的记录。
func (l *Ledger) Upsert(e Entry) {
	for i := range l.Relocations {
		if l.Relocations[i].TargetID == e.TargetID {
			l.Relocations[i] = e
			return
		}
	}
	l.Relocations = append(l.Relocations, e)
}

// NormalizeDir 统一大小写、斜杠、尾随反斜杠与 \\?\ 前缀，仅用于比较。
func NormalizeDir(s string) string {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, `\\?\unc\`) {
		s = `\\` + s[len(`\\?\unc\`):]
	} else if strings.HasPrefix(s, `\\?\`) {
		s = s[len(`\\?\`):]
	}
	s = strings.ReplaceAll(s, "/", `\`)
	s = strings.TrimRight(s, `\`)
	return strings.ToLower(s)
}
