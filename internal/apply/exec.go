package apply

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ExecResult 是一次命令执行的结果。
type ExecResult struct {
	ExitCode int
	Output   string
	TimedOut bool
	Err      error
}

// Executor 抽象命令执行，便于测试注入假执行器（C8）。
//
// 单测里**不准真装任何软件、不准真调 winget install**。
type Executor interface {
	Run(ctx context.Context, command string, timeout time.Duration) ExecResult
}

// CommandExecutor 是真实实现。
type CommandExecutor struct{}

// NewExecutor 返回真实执行器。
func NewExecutor() Executor { return CommandExecutor{} }

func (CommandExecutor) Run(ctx context.Context, command string, timeout time.Duration) ExecResult {
	argv, err := SplitCommand(command)
	if err != nil || len(argv) == 0 {
		return ExecResult{ExitCode: -1, Err: fmt.Errorf("命令无法解析: %q", command)}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, runErr := cmd.CombinedOutput()

	res := ExecResult{Output: string(out)}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		res.Err = fmt.Errorf("执行超时（%s）", timeout)
		return res
	}
	if runErr != nil {
		res.Err = runErr
		if ee, ok := runErr.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
		}
		return res
	}
	return res
}

// SplitCommand 按 shell 风格切分命令行，支持双引号包裹含空格的路径。
//
// 不走 cmd.exe：多一层解释器会引入转义问题，而且我们要精确控制退出码。
func SplitCommand(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuote := false
	hasToken := false

	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			hasToken = true
		case (r == ' ' || r == '\t') && !inQuote:
			if hasToken {
				out = append(out, cur.String())
				cur.Reset()
				hasToken = false
			}
		default:
			cur.WriteRune(r)
			hasToken = true
		}
	}
	if inQuote {
		return nil, fmt.Errorf("引号未闭合: %q", s)
	}
	if hasToken {
		out = append(out, cur.String())
	}
	return out, nil
}
