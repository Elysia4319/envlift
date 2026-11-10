package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Elysia4319/envlift/internal/api"
	"github.com/Elysia4319/envlift/internal/engine"
	"github.com/Elysia4319/envlift/internal/task"
	"github.com/Elysia4319/envlift/internal/webui"
)

// guiOpts 是 GUI 启动参数。
type guiOpts struct {
	// serve 显式要求启动服务（便于脚本与自动化；无子命令时本来也会走 GUI）。
	serve bool
	// noOpen 不自动打开浏览器。
	noOpen bool
	// port 固定端口，0 表示由系统分配。
	port int
}

// isGUIInvocation 判断这次调用该走 GUI 还是 CLI（D1-R1）。
//
// 规则：无参数 → GUI；已知子命令 → CLI；以 - 开头（GUI 参数）→ GUI；
// 其他未知词 → 交给 CLI 分支报「未知命令」。
func isGUIInvocation(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "scan", "restore", "version", "help", "-h", "--help", "-v", "--version":
		return false
	}
	return strings.HasPrefix(args[0], "-")
}

func parseGUIFlags(args []string) (guiOpts, error) {
	var opts guiOpts
	fs := flag.NewFlagSet("envlift", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&opts.serve, "serve", false, "启动图形界面服务")
	fs.BoolVar(&opts.noOpen, "no-open", false, "不自动打开浏览器")
	fs.IntVar(&opts.port, "port", 0, "固定端口（0 表示随机）")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, fmt.Errorf("未知参数: %s", fs.Arg(0))
	}
	return opts, nil
}

// runGUI 启动本地 GUI 服务并打开系统默认浏览器（D1-R1 / D14）。
//
// 打开浏览器失败不算致命：打印 URL 让用户手动粘贴，GUI 不因此失败（C-1）。
func runGUI(opts guiOpts) int {
	token, err := webui.NewToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		return exitError
	}
	srv, err := webui.NewOn(token, opts.port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		return exitError
	}
	defer srv.Shutdown()

	url := srv.URL()
	fmt.Println("envlift GUI 已启动")
	fmt.Printf("  地址: %s\n", url)
	fmt.Println("  仅监听 127.0.0.1；按 Ctrl-C 或点界面上的退出按钮即关闭")
	fmt.Println()

	if opts.noOpen {
		fmt.Println("  已指定 --no-open，不自动打开浏览器")
	} else if err := openBrowser(url); err != nil {
		fmt.Fprintf(os.Stderr, "自动打开浏览器失败（可手动粘贴上面的地址）: %v\n", err)
	}

	// 挂载 /api/v1：GUI 的所有业务都走内核函数（D-2）。
	srv.API = api.New(engine.Deps(engine.Config{
		ConfigPath:   "envlift.yaml",
		InstallerDir: "installers",
		ReportsDir:   "reports",
		NetProbe:     netProbeTarget(),
	}), task.NewManager(20, 5000), token, "reports").Routes()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	select {
	case <-srv.Quit():
		fmt.Println("收到退出请求，正在关闭…")
	case <-sig:
		fmt.Println("\n收到中断信号，正在关闭…")
	case err := <-errCh:
		if err != nil {
			fmt.Fprintf(os.Stderr, "服务异常: %v\n", err)
			return exitError
		}
	}
	_ = srv.Shutdown()
	fmt.Println("已退出。")
	return exitOK
}

// netProbeTarget 允许用环境变量覆盖探测地址（便于模拟断网）。
func netProbeTarget() string {
	if v := os.Getenv("ENVLIFT_NET_PROBE"); v != "" {
		return v
	}
	return "cdn.winget.microsoft.com:443"
}

var (
	shell32           = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW = shell32.NewProc("ShellExecuteW")
)

// openBrowser 用 ShellExecuteW 交给系统默认浏览器。
//
// 不经过命令行解释器，也不调用系统自带的协议转发工具——那类工具容易被安全策略
// 拦下，而且引入一层命令解析反而增加注入面。
func openBrowser(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	r, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0, 0,
		windows.SW_SHOWNORMAL,
	)
	if r <= 32 {
		return fmt.Errorf("ShellExecuteW 返回 %d: %v", r, callErr)
	}
	return nil
}
