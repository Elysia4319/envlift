// Package conda 负责 conda/pip 环境的只读采集、清单生成与校验。
//
// 本包不直接改环境：扫描只调用 conda/pip 的只读子命令；真实恢复由 apply 包
// 通过注入的 Runner 执行。所有外部命令与文件系统都做成接口，单测不安装 conda。
package conda

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Elysia4319/envlift/internal/config"
)

// Command 是一条待执行命令。Path 可以是可执行文件名，也可以是绝对路径。
type Command struct {
	Path string
	Args []string
	Env  []string
	Dir  string
}

// Result 是一次命令执行结果。
type Result struct {
	ExitCode int
	Output   string
	TimedOut bool
	Err      error
}

// Runner 抽象 conda/pip 命令执行，测试注入假实现。
type Runner interface {
	Run(ctx context.Context, cmd Command, timeout time.Duration) Result
}

// ExecRunner 是真实实现。
type ExecRunner struct{}

// Run 执行命令并合并 stdout/stderr。
func (ExecRunner) Run(ctx context.Context, cmd Command, timeout time.Duration) Result {
	if strings.TrimSpace(cmd.Path) == "" {
		return Result{ExitCode: -1, Err: fmt.Errorf("命令路径为空")}
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := exec.CommandContext(cctx, cmd.Path, cmd.Args...)
	if cmd.Dir != "" {
		c.Dir = cmd.Dir
	}
	if len(cmd.Env) > 0 {
		c.Env = append(os.Environ(), cmd.Env...)
	}
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := c.CombinedOutput()

	res := Result{Output: string(out)}
	if cctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		res.Err = fmt.Errorf("执行超时（%s）", timeout)
		return res
	}
	if err != nil {
		res.Err = err
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
		}
	}
	return res
}

// FileSystem 抽象临时文件操作，便于测试。
type FileSystem interface {
	MkdirAll(path string, perm os.FileMode) error
	WriteFile(path string, data []byte, perm os.FileMode) error
	RemoveAll(path string) error
	Stat(path string) (os.FileInfo, error)
}

// OSFileSystem 是真实文件系统。
type OSFileSystem struct{}

func (OSFileSystem) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (OSFileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (OSFileSystem) RemoveAll(path string) error { return os.RemoveAll(path) }

func (OSFileSystem) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

// ScanResult 是一次 conda 只读扫描的结果。
type ScanResult struct {
	Envs     []config.CondaEnv
	Warnings []string
	// CondaFound 表示至少一个 conda 安装可用；false 时 Envs 为空。
	CondaFound bool
}

// Scanner 采集本机全部可发现的 conda 安装与命名环境。
type Scanner struct {
	Runner Runner
	FS     FileSystem
	// LookPath / Getenv / ExtraPaths 都是可注入的，便于测试多安装与路径含空格。
	LookPath   func(string) (string, error)
	Getenv     func(string) string
	ExtraPaths []string
	Timeout    time.Duration
	// KeepPipBootstrap 保留 pip/setuptools/wheel 等基础包；默认过滤。
	KeepPipBootstrap bool
}

// NewScanner 返回使用真实命令与文件系统的扫描器。
func NewScanner() *Scanner {
	return &Scanner{
		Runner:   ExecRunner{},
		FS:       OSFileSystem{},
		LookPath: exec.LookPath,
		Getenv:   os.Getenv,
		Timeout:  2 * time.Minute,
	}
}

// Scan 执行只读采集。任何单个环境的失败都记 warning，不拖垮整次扫描。
func (s *Scanner) Scan(ctx context.Context) (ScanResult, error) {
	s.applyDefaults()
	candidates := s.discover()
	if len(candidates) == 0 {
		return ScanResult{Warnings: []string{"未找到 conda 安装，可跳过 conda 环境采集"}}, nil
	}

	var out ScanResult
	seenRoot := map[string]bool{}
	for _, exe := range candidates {
		info, err := s.info(ctx, exe)
		if err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("跳过 conda %s: %v", exe, err))
			continue
		}
		root := strings.TrimSpace(info.RootPrefix)
		if root == "" {
			root = inferRoot(exe)
		}
		key := normalizePath(root)
		if key == "" || seenRoot[key] {
			continue
		}
		seenRoot[key] = true
		out.CondaFound = true

		prefixes, err := s.envList(ctx, exe)
		if err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("读取 %s 的环境列表失败: %v", root, err))
			continue
		}
		prefixes = dedupePaths(prefixes)
		if !containsPath(prefixes, root) {
			prefixes = append([]string{root}, prefixes...)
		}
		for _, prefix := range prefixes {
			env, warns := s.scanEnv(ctx, exe, root, info.CondaVersion, prefix)
			out.Warnings = append(out.Warnings, warns...)
			if env.Name != "" {
				out.Envs = append(out.Envs, env)
			}
		}
	}
	sort.SliceStable(out.Envs, func(i, j int) bool {
		if !strings.EqualFold(out.Envs[i].Name, out.Envs[j].Name) {
			return strings.ToLower(out.Envs[i].Name) < strings.ToLower(out.Envs[j].Name)
		}
		return strings.ToLower(out.Envs[i].Manager) < strings.ToLower(out.Envs[j].Manager)
	})
	if out.CondaFound && len(out.Envs) == 0 {
		out.Warnings = append(out.Warnings, "找到了 conda，但没有可读取的命名环境")
	}
	return out, nil
}

func (s *Scanner) scanEnv(ctx context.Context, exe, root, condaVersion, prefix string) (config.CondaEnv, []string) {
	base := samePath(prefix, root)
	name := "base"
	if !base {
		name = filepath.Base(strings.TrimRight(prefix, `\/`))
	}
	if strings.TrimSpace(name) == "" {
		name = "base"
	}
	env := config.CondaEnv{
		Name: name, Prefix: prefix, Manager: root, CondaExe: exe,
		CondaVersion: condaVersion, Base: base,
	}
	// 低风险默认：扫描只记录，不默认恢复；用户必须在 GUI 勾选或显式传
	// --conda-packages=conda|pip|both 才执行。
	enabled := false
	env.RestoreConda = &enabled
	env.RestorePip = &enabled

	var warns []string
	condaNames := map[string]bool{}
	var pypiFromConda []config.PipPackage
	pkgs, err := s.condaList(ctx, exe, prefix)
	if err != nil {
		warns = append(warns, fmt.Sprintf("读取 conda 环境 %s 的包列表失败: %v", name, err))
	} else {
		for _, p := range pkgs {
			if strings.EqualFold(strings.TrimSpace(p.Channel), "pypi") {
				// conda list 把 pip 安装的包标成 channel=pypi；它们属于 pip 清单。
				pypiFromConda = append(pypiFromConda, config.PipPackage{Name: p.Name, Version: p.Version})
				continue
			}
			env.Packages = append(env.Packages, p)
			condaNames[NormalizePipName(p.Name)] = true
			if strings.EqualFold(p.Name, "python") {
				env.PythonVersion = p.Version
			}
		}
	}
	pips, err := s.pipList(ctx, prefix)
	if err != nil {
		warns = append(warns, fmt.Sprintf("读取 conda 环境 %s 的 pip 包列表失败: %v", name, err))
	}
	// conda 安装的包也会出现在 pip list 里（共享 site-packages）。按 PEP 503
	// 规范化名称剔除重叠项，否则 requirements.txt 会把 numpy 等再装一遍。
	pips = mergePip(pips, pypiFromConda)
	pips = filterPipOverlap(pips, condaNames)
	env.PipPackages = filterPipBootstrap(pips, s.KeepPipBootstrap)
	return env, warns
}

func (s *Scanner) discover() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(strings.Trim(p, `"`))
		if p == "" {
			return
		}
		key := normalizePath(p)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, p)
	}

	add(s.Getenv("CONDA_EXE"))
	for _, name := range []string{"conda.exe", "conda"} {
		if p, err := s.LookPath(name); err == nil {
			add(p)
		}
	}
	home := s.Getenv("USERPROFILE")
	local := s.Getenv("LOCALAPPDATA")
	programData := s.Getenv("ProgramData")
	common := []string{
		filepath.Join(home, "miniconda3", "Scripts", "conda.exe"),
		filepath.Join(home, "anaconda3", "Scripts", "conda.exe"),
		filepath.Join(local, "miniconda3", "Scripts", "conda.exe"),
		filepath.Join(local, "anaconda3", "Scripts", "conda.exe"),
		filepath.Join(programData, "miniconda3", "Scripts", "conda.exe"),
		filepath.Join(programData, "anaconda3", "Scripts", "conda.exe"),
		`C:\miniconda3\Scripts\conda.exe`,
		`C:\anaconda3\Scripts\conda.exe`,
	}
	common = append(common, s.ExtraPaths...)
	for _, p := range common {
		if st, err := s.FS.Stat(p); err == nil && !st.IsDir() {
			add(p)
		}
	}
	return out
}

type infoJSON struct {
	CondaVersion string `json:"conda_version"`
	CondaExe     string `json:"conda_exe"`
	RootPrefix   string `json:"root_prefix"`
}

func (s *Scanner) info(ctx context.Context, exe string) (infoJSON, error) {
	res := s.Runner.Run(ctx, Command{Path: exe, Args: []string{"info", "--json"}}, s.Timeout)
	if res.Err != nil || res.ExitCode != 0 {
		return infoJSON{}, commandErr(res)
	}
	var info infoJSON
	if err := decodeJSON(res.Output, &info); err != nil {
		return infoJSON{}, fmt.Errorf("解析 conda info --json 失败: %w", err)
	}
	if strings.TrimSpace(info.CondaExe) != "" {
		// conda 可能把 exe 报成另一个路径；保留实际调用的那个，避免跨安装串台。
	}
	return info, nil
}

func (s *Scanner) envList(ctx context.Context, exe string) ([]string, error) {
	res := s.Runner.Run(ctx, Command{Path: exe, Args: []string{"env", "list", "--json"}}, s.Timeout)
	if res.Err != nil || res.ExitCode != 0 {
		return nil, commandErr(res)
	}
	var payload struct {
		Envs []string `json:"envs"`
	}
	if err := decodeJSON(res.Output, &payload); err != nil {
		return nil, fmt.Errorf("解析 conda env list --json 失败: %w", err)
	}
	return payload.Envs, nil
}

func (s *Scanner) condaList(ctx context.Context, exe, prefix string) ([]config.CondaPackage, error) {
	res := s.Runner.Run(ctx, Command{
		Path: exe,
		Args: []string{"list", "--json", "--prefix", prefix},
	}, s.Timeout)
	if res.Err != nil || res.ExitCode != 0 {
		return nil, commandErr(res)
	}
	var raw []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Build   string `json:"build"`
		Channel string `json:"channel"`
	}
	if err := decodeJSON(res.Output, &raw); err != nil {
		return nil, fmt.Errorf("解析 conda list --json 失败: %w", err)
	}
	out := make([]config.CondaPackage, 0, len(raw))
	for _, p := range raw {
		if strings.TrimSpace(p.Name) == "" {
			continue
		}
		out = append(out, config.CondaPackage{
			Name: p.Name, Version: p.Version, Build: p.Build,
			Channel: RedactURL(p.Channel),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (s *Scanner) pipList(ctx context.Context, prefix string) ([]config.PipPackage, error) {
	py := filepath.Join(prefix, "python.exe")
	if _, err := s.FS.Stat(py); err != nil {
		return nil, fmt.Errorf("找不到 %s", py)
	}
	res := s.Runner.Run(ctx, Command{
		Path: py,
		Args: []string{"-m", "pip", "list", "--format=json"},
	}, s.Timeout)
	if res.Err != nil || res.ExitCode != 0 {
		return nil, commandErr(res)
	}
	var raw []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := decodeJSON(res.Output, &raw); err != nil {
		return nil, fmt.Errorf("解析 pip list --format=json 失败: %w", err)
	}
	out := make([]config.PipPackage, 0, len(raw))
	for _, p := range raw {
		if strings.TrimSpace(p.Name) == "" {
			continue
		}
		out = append(out, config.PipPackage{Name: p.Name, Version: p.Version})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return NormalizePipName(out[i].Name) < NormalizePipName(out[j].Name)
	})
	return out, nil
}

// ParseCondaList 解析 conda list --json 输出。
func ParseCondaList(data []byte) ([]config.CondaPackage, error) {
	var raw []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Build   string `json:"build"`
		Channel string `json:"channel"`
	}
	if err := decodeJSON(string(data), &raw); err != nil {
		return nil, err
	}
	out := make([]config.CondaPackage, 0, len(raw))
	for _, p := range raw {
		if strings.TrimSpace(p.Name) == "" {
			continue
		}
		out = append(out, config.CondaPackage{
			Name: p.Name, Version: p.Version, Build: p.Build,
			Channel: RedactURL(p.Channel),
		})
	}
	return out, nil
}

// ParsePipList 解析 pip list --format=json 输出。
func ParsePipList(data []byte) ([]config.PipPackage, error) {
	var raw []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := decodeJSON(string(data), &raw); err != nil {
		return nil, err
	}
	out := make([]config.PipPackage, 0, len(raw))
	for _, p := range raw {
		if strings.TrimSpace(p.Name) == "" {
			continue
		}
		out = append(out, config.PipPackage{Name: p.Name, Version: p.Version})
	}
	return out, nil
}

func (s *Scanner) applyDefaults() {
	if s.Runner == nil {
		s.Runner = ExecRunner{}
	}
	if s.FS == nil {
		s.FS = OSFileSystem{}
	}
	if s.LookPath == nil {
		s.LookPath = exec.LookPath
	}
	if s.Getenv == nil {
		s.Getenv = os.Getenv
	}
	if s.Timeout <= 0 {
		s.Timeout = 2 * time.Minute
	}
}

func commandErr(r Result) error {
	out := strings.TrimSpace(r.Output)
	if len(out) > 300 {
		out = out[:300] + "…"
	}
	if r.Err != nil {
		if out != "" {
			return fmt.Errorf("%v: %s", r.Err, out)
		}
		return r.Err
	}
	if out != "" {
		return fmt.Errorf("退出码 %d: %s", r.ExitCode, out)
	}
	return fmt.Errorf("退出码 %d", r.ExitCode)
}

func inferRoot(exe string) string {
	// <root>\Scripts\conda.exe
	dir := filepath.Dir(exe)
	if strings.EqualFold(filepath.Base(dir), "Scripts") {
		return filepath.Dir(dir)
	}
	return dir
}

func samePath(a, b string) bool {
	return normalizePath(a) == normalizePath(b)
}

func normalizePath(p string) string {
	p = strings.TrimSpace(strings.Trim(p, `"`))
	p = strings.ReplaceAll(p, "/", `\`)
	p = strings.TrimRight(p, `\`)
	return strings.ToLower(p)
}

func containsPath(list []string, want string) bool {
	for _, p := range list {
		if samePath(p, want) {
			return true
		}
	}
	return false
}

// dedupePaths 按 Windows 路径语义去重，保留首次出现的原始大小写。
func dedupePaths(list []string) []string {
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, p := range list {
		key := normalizePath(p)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// RedactURL 去掉 URL 里的用户名密码与敏感查询参数。
//
// 非 URL 的 conda channel 名（conda-forge / defaults）原样返回。
func RedactURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return raw
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	q := u.Query()
	changed := false
	for key := range q {
		if sensitiveKey(key) {
			q.Set(key, "${REDACTED}")
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func sensitiveKey(k string) bool {
	k = strings.ToLower(strings.TrimSpace(k))
	switch k {
	case "token", "access_token", "auth", "apikey", "api_key", "key", "password", "passwd", "secret":
		return true
	}
	return strings.Contains(k, "token") || strings.Contains(k, "password")
}

func decodeJSON(s string, dst any) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("输出为空")
	}
	if err := json.Unmarshal([]byte(s), dst); err == nil {
		return nil
	}
	// conda 有时会在 JSON 前后打印警告；取第一个 { / [ 到最后一个 } / ]。
	startObj := strings.IndexAny(s, "{[")
	if startObj < 0 {
		return fmt.Errorf("输出不是 JSON: %s", truncate(s, 160))
	}
	endObj := strings.LastIndexAny(s, "}]")
	if endObj <= startObj {
		return fmt.Errorf("输出不是 JSON: %s", truncate(s, 160))
	}
	var err error
	if err = json.Unmarshal([]byte(s[startObj:endObj+1]), dst); err != nil {
		return err
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
