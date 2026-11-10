// Package probe 探测本机语言运行时：执行无副作用版本命令，并识别版本管理器。
package probe

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Target 是解析出来的可执行文件及其来源。
type Target struct {
	Path   string
	Source string
	Index  int
}

// Resolver 把命令名解析为绝对路径。
//
// A2：解析与执行必须同源。只把解析换成注册表查找、却仍用 exec.Command("python")
// 执行，命令会重新走会话 PATH，结果就是「配置里记 A 的路径、报 B 的版本」。
type Resolver interface {
	Resolve(name string) (Target, bool)
}

// Spec 描述一个待探测的运行时。
type Spec struct {
	Name string   // 运行时名，写入配置的 runtimes[].name
	Args []string // 版本命令参数，如 {"--version"}
}

// Result 是单个运行时的探测结果。Version 为 nil 表示 unknown（未安装或超时）。
type Result struct {
	Name       string
	Command    string
	Version    *string
	Manager    *string
	Executable *string
	// Source / Index 记录可执行文件的来源段与段内序号（A5）。
	Source string
	Index  int
}

// Prober 便于测试注入假实现。
type Prober interface {
	Probe(specs []Spec, resolver Resolver) []Result
}

// New 返回执行真实命令的 Prober。timeout 为单个探测的上限。
func New(timeout time.Duration) Prober {
	return execProber{timeout: timeout, run: execRunner}
}

// Runner 抽象「用给定绝对路径执行命令并返回合并输出」，便于测试断言 A2。
type Runner func(ctx context.Context, path string, args []string) ([]byte, error)

func execRunner(ctx context.Context, path string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	// 同时捕获 stdout 与 stderr：java、dotnet 的版本信息走 stderr。
	return cmd.CombinedOutput()
}

type execProber struct {
	timeout time.Duration
	run     Runner
}

func (p execProber) Probe(specs []Spec, resolver Resolver) []Result {
	out := make([]Result, 0, len(specs))
	for _, s := range specs {
		out = append(out, p.probeOne(s, resolver))
	}
	return out
}

func (p execProber) probeOne(s Spec, resolver Resolver) Result {
	res := Result{Name: s.Name, Command: s.Name, Index: -1}
	if resolver == nil {
		return res
	}

	target, ok := resolver.Resolve(s.Name)
	if !ok {
		return res // 未安装：Version / Executable 均留 nil
	}
	exe := target.Path
	res.Executable = &exe
	res.Source = target.Source
	res.Index = target.Index
	res.Manager = managerOf(exe)

	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	// A2：用解析出的绝对路径执行，不再交给 LookPath 重新解析。
	out, _ := p.run(ctx, exe, s.Args)

	if ctx.Err() == context.DeadlineExceeded {
		return res // 超时即 unknown，绝不拖住整个 scan
	}
	if v := parseVersion(string(out)); v != "" {
		res.Version = &v
	}
	return res
}

var reVersion = regexp.MustCompile(`\d+(?:\.\d+)+`)

func parseVersion(out string) string {
	if m := reVersion.FindString(out); m != "" {
		return m
	}
	return ""
}

// managerMarkers 按可执行文件路径识别版本管理器。
//
// 为什么必须标 managed_by：fnm / nvm 下的 node 是 shim，只记版本会误导还原——
// 还原时装一个同版本的 node 并不会得到同样的 shim 结构。
var managerMarkers = []struct {
	marker  string
	manager string
}{
	{`\fnm\`, "fnm"},
	{`\nvm\`, "nvm"},
	{`\.pyenv\`, "pyenv"}, // pyenv-win 实际装在 %USERPROFILE%\.pyenv\pyenv-win\
	{`\pyenv\`, "pyenv"},
	{`pyenv-win`, "pyenv"},
	{`miniconda`, "conda"},
	{`anaconda`, "conda"},
	{`\.sdkman\`, "sdkman"},
	{`\.rustup\`, "rustup"},
}

func managerOf(exe string) *string {
	lower := strings.ToLower(exe)
	for _, m := range managerMarkers {
		if strings.Contains(lower, m.marker) {
			name := m.manager
			return &name
		}
	}
	return nil
}
