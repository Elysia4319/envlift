// Package scan 编排只读扫描：系统信息 + PATH + 语言运行时，产出配置对象。
//
// 本包不写任何文件、不碰注册表写权限；写盘由 cmd 层负责，以便在写入前
// 展示预览并等待确认。
package scan

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/pathenv"
	"github.com/Elysia4319/envlift/internal/probe"
	"github.com/Elysia4319/envlift/internal/softscan"
	"github.com/Elysia4319/envlift/internal/sysinfo"
	"github.com/Elysia4319/envlift/internal/winreg"
)

// DefaultRuntimeSpecs 是第 ② 步要探测的运行时集合。
//
// powershell 没有 --version，只能读 $PSVersionTable；其余都是无副作用的版本命令。
var DefaultRuntimeSpecs = []probe.Spec{
	{Name: "python", Args: []string{"--version"}},
	{Name: "node", Args: []string{"-v"}},
	{Name: "java", Args: []string{"-version"}},
	{Name: "go", Args: []string{"version"}},
	{Name: "cargo", Args: []string{"--version"}},
	{Name: "git", Args: []string{"--version"}},
	{Name: "dotnet", Args: []string{"--version"}},
	{Name: "powershell", Args: []string{"-NoProfile", "-Command", "$PSVersionTable.PSVersion.ToString()"}},
}

// Scanner 汇总三路只读来源。所有外部依赖都可注入，便于测试替身。
type Scanner struct {
	Registry winreg.Reader
	SysInfo  sysinfo.Collector
	Prober   probe.Prober
	Redactor config.Redactor

	// Tree 与 WingetRunner 用于软件清单采集；为 nil 时跳过对应来源。
	Tree         winreg.Tree
	WingetRunner softscan.Runner

	// Exists / Expand / SessionPath / Lookup 用于 PATH 解析，测试可注入。
	Exists      func(string) bool
	Expand      func(string) string
	SessionPath func() string
	Lookup      pathenv.Lookup
	Now         func() time.Time
	Specs       []probe.Spec
}

// PathNotes 按分段保存 PATH 标注。
type PathNotes struct {
	User    []Note
	Machine []Note
}

// Result 是一次扫描的完整结果。
type Result struct {
	Config   *config.Config
	Info     sysinfo.Info
	Warnings []string
	Notes    PathNotes
	// PathExt 是本次解析使用的扩展名顺序，便于预览与排错。
	PathExt []string
	// Soft 是三源采集的软件清单与统计，供预览展示（M6：不写入配置）。
	Soft *softscan.Result
}

// Environment 是目标机环境的只读快照。
//
// scan 用它生成配置；restore 的预检也用它——两边看同一份事实，避免
// 「扫描说装了、还原说没装」这类不一致。
type Environment struct {
	Info        sysinfo.Info
	PathUser    []config.PathEntry
	PathMachine []config.PathEntry
	Notes       PathNotes
	PathExt     []string
	Runtimes    []probe.Result
	// PathDirs 是展开后的可搜索目录，按「系统段 → 用户段 → 会话兜底」排列。
	PathDirs []string
	// Soft 是三源采集的软件清单与统计；Tree 为 nil 时为空。
	Soft *softscan.Result
}

// ProbeEnvironment 只读探测目标机环境，不生成配置、不写文件。
func (s *Scanner) ProbeEnvironment() (*Environment, error) {
	s.applyDefaults()

	info, err := s.SysInfo.Collect()
	if err != nil {
		return nil, fmt.Errorf("采集系统信息失败: %w", err)
	}

	env := &Environment{Info: info}

	if v, err := s.Registry.ReadString(winreg.HiveCurrentUser, userPathKey, "Path"); err != nil {
		env.Info.Warnings = append(env.Info.Warnings, "读取用户级 PATH 失败: "+err.Error())
	} else {
		env.PathUser, env.Notes.User = parsePath(v, s.exists)
	}

	if v, err := s.Registry.ReadString(winreg.HiveLocalMachine, machinePathKey, "Path"); err != nil {
		env.Info.Warnings = append(env.Info.Warnings, "读取系统级 PATH 失败: "+err.Error())
	} else {
		env.PathMachine, env.Notes.Machine = parsePath(v, s.exists)
	}

	env.PathExt = s.readPathExt()
	dirs := pathenv.BuildDirs(
		s.dirList(env.PathMachine),
		s.dirList(env.PathUser),
		pathenv.SplitList(s.SessionPath()),
	)
	for _, d := range dirs {
		env.PathDirs = append(env.PathDirs, d.Path)
	}

	resolver := pathResolver{dirs: dirs, exts: env.PathExt, lookup: s.Lookup}
	env.Runtimes = s.Prober.Probe(s.Specs, resolver)

	if s.Tree != nil {
		env.Soft = softscan.Collect(s.Tree, s.WingetRunner)
	}
	return env, nil
}

// Run 执行只读扫描并生成配置对象。不产生任何文件。
func (s *Scanner) Run() (*Result, error) {
	env, err := s.ProbeEnvironment()
	if err != nil {
		return nil, err
	}
	info := env.Info

	res := &Result{Info: info, Notes: env.Notes, PathExt: env.PathExt, Soft: env.Soft}
	res.Warnings = append(res.Warnings, info.Warnings...)

	runtimes := make([]config.Runtime, 0, len(env.Runtimes))
	for _, r := range env.Runtimes {
		rt := config.Runtime{
			Name:       r.Name,
			Command:    r.Command,
			Version:    r.Version,
			Manager:    r.Manager,
			Executable: r.Executable,
		}
		if r.Source != "" {
			rt.ResolvedFrom = config.StringPtr(r.Source)
		}
		if r.Index >= 0 {
			rt.ResolvedIndex = config.IntPtr(r.Index)
		}
		runtimes = append(runtimes, rt)
	}

	driveMap := make(map[string]config.DriveInfo, len(info.Drives))
	for _, d := range info.Drives {
		// purpose 留空由人填写，scan 不猜（B2）。
		driveMap[strings.TrimSuffix(d.Letter, ":")] = config.DriveInfo{Label: d.Label}
	}

	cfg := &config.Config{
		App:         config.AppName,
		Schema:      config.CurrentSchema,
		GeneratedAt: s.Now().Format(time.RFC3339),
		SourceMachine: config.SourceMachine{
			OS: config.OSInfo{
				Version:        info.OSVersion,
				Build:          info.OSBuild,
				ProductName:    info.OSProductName,
				DisplayVersion: info.OSDisplayVersion,
			},
			Arch:     info.Arch,
			Hostname: info.Hostname,
			Username: info.Username,
		},
		DriveMap: driveMap,
		Path:     config.PathSection{User: env.PathUser, Machine: env.PathMachine},
		Runtimes: runtimes,
	}
	if env.Soft != nil {
		cfg.Software = env.Soft.Software
	}

	red := s.Redactor
	if red.Username == "" || red.Hostname == "" {
		u, h := red.Username, red.Hostname
		if u == "" {
			u = info.Username
		}
		if h == "" {
			h = info.Hostname
		}
		red = red.WithIdentity(u, h)
	}
	red.Apply(cfg)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	res.Config = cfg
	return res, nil
}

func (s *Scanner) applyDefaults() {
	if s.Exists == nil {
		s.Exists = defaultExists
	}
	if s.Expand == nil {
		s.Expand = pathenv.ExpandVars
	}
	if s.SessionPath == nil {
		s.SessionPath = func() string { return os.Getenv("PATH") }
	}
	if s.Lookup == nil {
		s.Lookup = pathenv.DiskLookup
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Specs == nil {
		s.Specs = DefaultRuntimeSpecs
	}
}

// readPathExt 读两处 PATHEXT 并按系统在前合并，读不到时回落内置默认（A4）。
func (s *Scanner) readPathExt() []string {
	var parts []string
	if v, err := s.Registry.ReadString(winreg.HiveLocalMachine, machinePathKey, "PATHEXT"); err == nil {
		parts = append(parts, v.String)
	}
	if v, err := s.Registry.ReadString(winreg.HiveCurrentUser, userPathKey, "PATHEXT"); err == nil {
		parts = append(parts, v.String)
	}
	return pathenv.SplitPathExt(strings.Join(parts, ";"))
}

// dirList 把 PATH 条目展开成可搜索目录，跳过空项。
//
// 展开仅用于文件系统查找，配置里仍保留未展开的原始字面值。
func (s *Scanner) dirList(entries []config.PathEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		v := strings.TrimSpace(e.Value)
		if v == "" {
			continue
		}
		v = strings.Trim(v, `"`)
		out = append(out, s.Expand(v))
	}
	return out
}

func (s *Scanner) exists(raw string) bool {
	v := strings.TrimSpace(raw)
	v = strings.Trim(v, `"`)
	if v == "" {
		return false
	}
	return s.Exists(s.Expand(v))
}

// pathResolver 把 pathenv 的解析结果适配成 probe.Resolver。
type pathResolver struct {
	dirs   []pathenv.Dir
	exts   []string
	lookup pathenv.Lookup
}

func (r pathResolver) Resolve(name string) (probe.Target, bool) {
	res, ok := pathenv.Resolve(name, r.dirs, r.exts, r.lookup)
	if !ok {
		return probe.Target{}, false
	}
	return probe.Target{Path: res.Path, Source: res.Source, Index: res.Index}, true
}

func defaultExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
