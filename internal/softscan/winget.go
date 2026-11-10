package softscan

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Runner 执行 winget 并返回标准输出，便于测试注入假输出（C3）。
type Runner func(args ...string) (string, error)

// WingetPackage 是 winget list 的一条记录。
type WingetPackage struct {
	Name    string `json:"name"`
	ID      string `json:"id"`
	Version string `json:"version"`
	Source  string `json:"source"`
}

const wingetTimeout = 30 * time.Second

// NewWingetRunner 返回调用真实 winget 的 Runner。
// 30 秒超时：winget 首次调用可能很慢（C3）。
func NewWingetRunner() Runner {
	return func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), wingetTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "winget", args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.Output()
		if ctx.Err() == context.DeadlineExceeded {
			return "", errors.New("winget 超时（30s）")
		}
		return string(out), err
	}
}

// ScanWinget 按「JSON → CSV → 表格」的顺序尝试结构化输出（C3）。
//
// 返回实际使用的格式，便于在报告里说明数据来源的可信度。
func ScanWinget(run Runner) ([]WingetPackage, string, error) {
	if pkgs, err := tryWinget(run, "json", ParseWingetJSON); err == nil && len(pkgs) > 0 {
		return pkgs, "json", nil
	}
	if pkgs, err := tryWinget(run, "csv", ParseWingetCSV); err == nil && len(pkgs) > 0 {
		return pkgs, "csv", nil
	}
	out, err := run("list", "--accept-source-agreements", "--disable-interactivity")
	if err != nil {
		return nil, "", err
	}
	pkgs := ParseWingetTable(out)
	if len(pkgs) == 0 {
		return nil, "table", errors.New("winget list 输出无法解析出任何包")
	}
	return pkgs, "table", nil
}

func tryWinget(run Runner, format string, parse func(string) ([]WingetPackage, error)) ([]WingetPackage, error) {
	out, err := run("list", "--output", format, "--accept-source-agreements", "--disable-interactivity")
	if err != nil {
		return nil, err
	}
	return parse(out)
}

// ParseWingetJSON 兼容两种形状：
//   - winget export 的 {"Sources":[{"Packages":[...]}]}
//   - 扁平数组 [{"PackageIdentifier":...}]
func ParseWingetJSON(out string) ([]WingetPackage, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, errors.New("空输出")
	}

	var wrapped struct {
		Sources []struct {
			Packages []struct {
				PackageIdentifier string `json:"PackageIdentifier"`
				PackageName       string `json:"PackageName"`
				Version           string `json:"Version"`
				Source            string `json:"Source"`
			} `json:"Packages"`
		} `json:"Sources"`
	}
	if err := json.Unmarshal([]byte(out), &wrapped); err == nil && len(wrapped.Sources) > 0 {
		var pkgs []WingetPackage
		for _, s := range wrapped.Sources {
			for _, p := range s.Packages {
				pkgs = append(pkgs, WingetPackage{
					Name: p.PackageName, ID: p.PackageIdentifier, Version: p.Version, Source: p.Source,
				})
			}
		}
		if len(pkgs) > 0 {
			return pkgs, nil
		}
	}

	var flat []struct {
		Name    string `json:"Name"`
		ID      string `json:"Id"`
		Version string `json:"Version"`
		Source  string `json:"Source"`
	}
	if err := json.Unmarshal([]byte(out), &flat); err == nil {
		pkgs := make([]WingetPackage, 0, len(flat))
		for _, p := range flat {
			pkgs = append(pkgs, WingetPackage{Name: p.Name, ID: p.ID, Version: p.Version, Source: p.Source})
		}
		if len(pkgs) > 0 {
			return pkgs, nil
		}
	}
	return nil, errors.New("不是可识别的 JSON")
}

// ParseWingetCSV 解析 --output csv。
func ParseWingetCSV(out string) ([]WingetPackage, error) {
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) < 2 {
		return nil, errors.New("CSV 行数不足")
	}

	header := records[0]
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(rec []string, name string) string {
		if i, ok := idx[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}

	var pkgs []WingetPackage
	for _, rec := range records[1:] {
		p := WingetPackage{
			Name:    get(rec, "name"),
			ID:      get(rec, "id"),
			Version: get(rec, "version"),
			Source:  get(rec, "source"),
		}
		if p.Name == "" || p.ID == "" {
			continue
		}
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		return nil, errors.New("CSV 中没有有效行")
	}
	return pkgs, nil
}

// ParseWingetTable 解析 winget list 的表格输出。
//
// 解析假设（C3 要求写明）：
//   - 表头行以 "Name" 开头且包含 "Id"，其后一行是全横线分隔行；
//   - 列位置取自表头标签的字节偏移，与 winget 的定宽输出一致；
//   - 若某行按字节切片后 id 字段含空格（说明该行有非 ASCII 字符导致列错位），
//     该行退回「按 2 个以上空格切分」的兜底解析。
//
// 失败降级：整体解析不出任何条目时，上层会把 winget 源标记为不可用并继续。
func ParseWingetTable(out string) []WingetPackage {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")

	headerIdx := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "Name") && strings.Contains(l, "Id") {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 || headerIdx+1 >= len(lines) {
		return nil
	}

	header := lines[headerIdx]
	cols := map[string]int{}
	for _, name := range []string{"Name", "Id", "Version", "Available", "Source"} {
		if i := strings.Index(header, name); i >= 0 {
			cols[name] = i
		}
	}
	if _, ok := cols["Name"]; !ok {
		return nil
	}
	if _, ok := cols["Id"]; !ok {
		return nil
	}

	slice := func(line, col, next string) string {
		start, ok := cols[col]
		if !ok || start >= len(line) {
			return ""
		}
		end := len(line)
		if n, ok := cols[next]; ok && n > start && n < end {
			end = n
		}
		return strings.TrimSpace(line[start:end])
	}

	var pkgs []WingetPackage
	for _, line := range lines[headerIdx+2:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name := slice(line, "Name", "Id")
		id := slice(line, "Id", "Version")
		version := slice(line, "Version", "Available")
		source := ""
		if s, ok := cols["Source"]; ok && s < len(line) {
			source = strings.TrimSpace(line[s:])
		}

		// 非 ASCII 名字会让定宽列错位，退回按 2+ 空格切分。
		if name == "" || id == "" || strings.ContainsAny(id, " \t") {
			fields := splitOnWideGap(line)
			if len(fields) < 2 {
				continue
			}
			name, id = fields[0], fields[1]
			version, source = "", ""
			if len(fields) > 2 {
				version = fields[2]
			}
			if len(fields) > 3 {
				source = fields[3]
			}
		}
		if name == "" || id == "" {
			continue
		}
		pkgs = append(pkgs, WingetPackage{Name: name, ID: id, Version: version, Source: source})
	}
	return pkgs
}

var reWideGap = regexp.MustCompile(`\s{2,}`)

// splitOnWideGap 按 2 个以上空白切分，作为定宽列错位时的兜底。
func splitOnWideGap(line string) []string {
	parts := reWideGap.Split(strings.TrimSpace(line), -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}
