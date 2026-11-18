// Command envlift 把一台 Windows 机器的开发环境扫描成单一配置文件，
// 并在另一台机器上先预演、后还原。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Elysia4319/envlift/internal/conda"
	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/match"
	"github.com/Elysia4319/envlift/internal/probe"
	"github.com/Elysia4319/envlift/internal/recipes"
	"github.com/Elysia4319/envlift/internal/scan"
	"github.com/Elysia4319/envlift/internal/softscan"
	"github.com/Elysia4319/envlift/internal/sysinfo"
	"github.com/Elysia4319/envlift/internal/winreg"
)

var version = "0.1.0-dev"

// 退出码约定。
const (
	exitOK      = 0
	exitError   = 1
	exitRefused = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// D1-R1：带子命令走 CLI，无参数或只带 GUI 参数走 GUI。
	if isGUIInvocation(args) {
		opts, err := parseGUIFlags(args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n\n", err)
			usage()
			return exitError
		}
		return runGUI(opts)
	}

	switch args[0] {
	case "version", "-v", "--version":
		fmt.Printf("envlift %s (schema v%d)\n", version, config.CurrentSchema)
		return exitOK
	case "help", "-h", "--help":
		usage()
		return exitOK
	case "scan":
		return runScan(args[1:])
	case "restore":
		return runRestore(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", args[0])
		usage()
		return exitError
	}
}

func usage() {
	fmt.Print(`envlift — 把一台 Windows 机器的开发环境原样搬走，在另一台上原样落地

用法:
  envlift scan    [--out <path>] [--raw] [--overwrite] [--yes]
                  [--keep-pip-bootstrap]
                                                   只读扫描，预览确认后写配置
  envlift restore [--config <path>] [--apply] [--yes]
                  [--plan-out <file>] [--installers <dir>]
                  [--conda-packages conda|pip|both|none]
                  [--from-user <old>] [--from-host <old>]
                                                   还原（默认仅预演）
  envlift                     无参数：启动图形界面
  envlift --serve [--no-open] [--port N]
                              显式启动图形界面服务
  envlift version
  envlift help

退出码: 0 成功 / 1 出错 / 2 未写入（文件已存在或用户取消）
`)
}

func runScan(args []string) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "envlift.yaml", "输出路径")
	raw := fs.Bool("raw", false, "关闭脱敏，输出真实用户名与主机名")
	overwrite := fs.Bool("overwrite", false, "目标文件已存在时覆盖（先备份为 .bak）")
	yes := fs.Bool("yes", false, "跳过写入确认")
	keepPipBootstrap := fs.Bool("keep-pip-bootstrap", false, "保留 pip/setuptools/wheel 等基础包")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	reg := winreg.New()
	scanner := &scan.Scanner{
		Registry:     reg,
		Tree:         reg,
		WingetRunner: softscan.NewWingetRunner(),
		SysInfo:      sysinfo.New(reg),
		Prober:       probe.New(4 * time.Second),
		Redactor:     config.NewRedactor("", ""),
		Conda:        conda.NewScanner(),
	}
	scanner.Redactor.Raw = *raw
	scanner.Conda.KeepPipBootstrap = *keepPipBootstrap

	fmt.Println("正在只读扫描…")
	res, err := scanner.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "扫描失败: %v\n", err)
		return exitError
	}

	renderPreview(os.Stdout, res)
	if *raw {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "警告: --raw 已关闭脱敏，本文件将包含真实用户名与主机名。")
		fmt.Fprintln(os.Stderr, "      请勿提交到版本库；建议用 --out 输出为 *.raw.yaml（该模式已被 .gitignore 忽略）。")
	}

	if _, statErr := os.Stat(*out); statErr == nil && !*overwrite {
		fmt.Fprintf(os.Stderr, "\n目标文件已存在，未写入: %s\n", *out)
		fmt.Fprintln(os.Stderr, "可选做法：")
		fmt.Fprintf(os.Stderr, "  1) 换个文件名: envlift scan --out envlift-2.yaml\n")
		fmt.Fprintf(os.Stderr, "  2) 覆盖并备份: envlift scan --overwrite   （原文件先备份为 %s.bak）\n", *out)
		return exitRefused
	}

	if !*yes {
		if !newPrompter(os.Stdin, os.Stdout).confirm(fmt.Sprintf("\n写入 %s ?", *out)) {
			fmt.Println("已取消，未写入任何文件。")
			return exitRefused
		}
	}

	if err := config.Save(*out, res.Config); err != nil {
		fmt.Fprintf(os.Stderr, "写入失败: %v\n", err)
		return exitError
	}
	fmt.Printf("\n已写入 %s\n", *out)
	fmt.Printf("  PATH %d 条（用户 %d / 系统 %d）· 运行时 %d 个 · conda 环境 %d 个 · 盘符 %d 个\n",
		len(res.Config.Path.User)+len(res.Config.Path.Machine),
		len(res.Config.Path.User), len(res.Config.Path.Machine),
		len(res.Config.Runtimes), len(res.Config.CondaEnvs), len(res.Config.DriveMap))
	fmt.Println("  下一步: 审阅该文件，手工补全 drive_map.purpose；还原前请先运行 envlift restore（默认预演）。")
	return exitOK
}

// pad 按显示宽度补齐空格，让中英文混排的标签能对齐。
func pad(s string, width int) string {
	w := 0
	for _, r := range s {
		if r < 128 {
			w++
		} else {
			w += 2
		}
	}
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func renderPreview(w io.Writer, res *scan.Result) {
	info := res.Info

	fmt.Fprintln(w, "\n系统信息")
	fmt.Fprintf(w, "  %s%s\n", pad("操作系统", 12), info.OSLabel())
	fmt.Fprintf(w, "  %s%s\n", pad("版本判定", 12), versionBasis(info))
	if note := productNameNote(info); note != "" {
		fmt.Fprintf(w, "  %s%s\n", pad("注册表名", 12), note)
	}
	fmt.Fprintf(w, "  %s%s\n", pad("架构", 12), info.Arch)
	fmt.Fprintf(w, "  %s%s\n", pad("用户名", 12), orDash(res.Config.SourceMachine.Username))
	fmt.Fprintf(w, "  %s%s\n", pad("主机名", 12), orDash(res.Config.SourceMachine.Hostname))

	priv := "普通用户"
	if info.Elevated {
		priv = "管理员运行"
	}
	fmt.Fprintf(w, "  %s%s\n", pad("权限", 12), priv)

	winget := "不可用（将整级跳过 winget）"
	if info.WingetAvailable {
		winget = "可用 " + strings.TrimSpace(info.WingetVersion)
	}
	fmt.Fprintf(w, "  %s%s\n", pad("winget", 12), winget)

	if len(info.Drives) > 0 {
		parts := make([]string, 0, len(info.Drives))
		for _, d := range info.Drives {
			if d.Label != "" {
				parts = append(parts, fmt.Sprintf("%s %s", d.Letter, d.Label))
			} else {
				parts = append(parts, d.Letter)
			}
		}
		fmt.Fprintf(w, "  %s%s\n", pad("盘符", 12), strings.Join(parts, "  "))
	}

	renderPathSection(w, "PATH（用户级）", res.Config.Path.User, res.Notes.User)
	renderPathSection(w, "PATH（系统级）", res.Config.Path.Machine, res.Notes.Machine)

	fmt.Fprintln(w, "\n语言运行时")
	if len(res.Config.Runtimes) == 0 {
		fmt.Fprintln(w, "  （无）")
	}
	for _, r := range res.Config.Runtimes {
		ver := "unknown"
		if r.Version != nil {
			ver = *r.Version
		}
		mgr := "-"
		if r.Manager != nil {
			mgr = *r.Manager
		}
		exe := "-"
		if r.Executable != nil {
			exe = *r.Executable
		}
		from := "-"
		if r.ResolvedFrom != nil {
			from = *r.ResolvedFrom
			if r.ResolvedIndex != nil {
				from = fmt.Sprintf("%s#%d", from, *r.ResolvedIndex)
			}
		}
		fmt.Fprintf(w, "  %s%s  %s  %s  %s\n",
			pad(r.Name, 14), pad(ver, 16), pad(mgr, 9), pad(from, 12), exe)
	}

	renderSoftware(w, res)
	renderConda(w, res)

	if len(res.Warnings) > 0 {
		fmt.Fprintln(w, "\n警告")
		for _, warning := range res.Warnings {
			fmt.Fprintf(w, "  - %s\n", warning)
		}
	}
}

// renderConda 展示 conda 环境快照与预计恢复范围。
func renderConda(w io.Writer, res *scan.Result) {
	envs := res.Config.CondaEnvs
	fmt.Fprintf(w, "\nconda 环境\n")
	if len(envs) == 0 {
		fmt.Fprintln(w, "  （未发现）")
		return
	}
	for _, e := range envs {
		mode := "只读"
		if !e.Base {
			var parts []string
			if e.CondaEnabled() {
				parts = append(parts, "conda")
			}
			if e.PipEnabled() {
				parts = append(parts, "pip")
			}
			mode = strings.Join(parts, "+")
			if mode == "" {
				mode = "跳过"
			}
		}
		fmt.Fprintf(w, "  %s%s  py=%s  conda=%d  pip=%d  %s\n",
			pad(e.Name, 18), pad(mode, 12), orDash(e.PythonVersion),
			len(e.Packages), len(e.PipPackages), e.Prefix)
	}
}

func renderPathSection(w io.Writer, title string, entries []config.PathEntry, notes []scan.Note) {
	flagged := make(map[int][]string, len(notes))
	for _, n := range notes {
		flagged[n.Index] = n.Flags
	}

	// A7：值类型在分组标题上标一次。36 条全标 expand 等于噪声。
	expandCount := 0
	for _, e := range entries {
		if e.Expand {
			expandCount++
		}
	}
	header := fmt.Sprintf("\n%s %d 条", title, len(entries))
	mixed := false
	switch {
	case len(entries) == 0:
	case expandCount == len(entries):
		header += " · 值类型 REG_EXPAND_SZ"
	case expandCount == 0:
		header += " · 值类型 REG_SZ"
	default:
		mixed = true
		header += fmt.Sprintf(" · 值类型混合（REG_EXPAND_SZ %d / REG_SZ %d）",
			expandCount, len(entries)-expandCount)
	}
	fmt.Fprintln(w, header)

	majorityExpand := expandCount*2 >= len(entries)
	for i, e := range entries {
		value := e.Value
		if strings.TrimSpace(value) == "" {
			value = "(空条目)"
		}
		line := fmt.Sprintf("  [%2d] %s", i+1, value)

		var marks []string
		// 混合时才单独标出少数派类型。
		if mixed && e.Expand != majorityExpand {
			if e.Expand {
				marks = append(marks, "reg_expand_sz")
			} else {
				marks = append(marks, "reg_sz")
			}
		}
		marks = append(marks, flagged[i]...)
		if len(marks) > 0 {
			line += "  [" + strings.Join(marks, ", ") + "]"
		}
		fmt.Fprintln(w, line)
	}
}

// versionBasis 说明主版本是怎么判定的，避免「为什么是 Windows 11」无从追问。
func versionBasis(info sysinfo.Info) string {
	if info.OSBuild == "" {
		return "unknown（读不到 build 号）"
	}
	return fmt.Sprintf("build %s → %s", info.OSBuild, info.OSVersion)
}

// productNameNote 展示注册表 ProductName，与 build 判定冲突时显式提示。
func productNameNote(info sysinfo.Info) string {
	if info.OSProductName == "" {
		return ""
	}
	if info.OSVersion != "unknown" && !strings.Contains(info.OSProductName, info.OSVersion) {
		return info.OSProductName + "（与 build 判定不一致，仅作参考）"
	}
	return info.OSProductName
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// prompter 持有一个共享的 stdin reader。
//
// 不能每次确认都新建 bufio.Reader：它会把管道里的后续行预读进自己的缓冲区，
// 下一次新建 reader 时那些行已经丢失，表现为「只认第一行，其余确认全被拒」。
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func newPrompter(in io.Reader, out io.Writer) *prompter {
	return &prompter{in: bufio.NewReader(in), out: out}
}

// readLine 读一行并去掉首尾空白。EOF 时若已读到内容仍按该行处理，
// 这样管道末尾不带换行符的输入不会被误判为「没有输入」。
func (p *prompter) readLine(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	// PowerShell 把字符串管道给原生程序时会加 UTF-8 BOM；不剥掉的话首行
	// 「APPLY」永远匹配不上，表现为「管道确认无效」。
	return strings.TrimSpace(strings.TrimPrefix(line, "\ufeff")), nil
}

func (p *prompter) confirm(prompt string) bool {
	line, err := p.readLine(prompt + " [y/N]: ")
	if err != nil {
		return false
	}
	switch strings.ToLower(line) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// applyConfirmation 是 --apply 的独立二次确认：必须输入大写的 APPLY。
func (p *prompter) applyConfirmation() bool {
	line, err := p.readLine("\n这是真实执行，会改动系统。确认请输入 APPLY（区分大小写）: ")
	return err == nil && line == "APPLY"
}

// renderSoftware 展示三源合并后的软件清单（C6）。
//
// M6：这里的「已识别 / 未识别」只是审阅辅助，**不写进配置文件**。
// 统计口径来自 match.Summarize，与 GUI 的扫描页共用同一份实现。
func renderSoftware(w io.Writer, res *scan.Result) {
	if res.Soft == nil {
		return
	}
	filtered := 0
	var reasons []string
	for _, k := range []string{"system_component", "no_name_no_uninstall", "no_display_name", "parent_key", "release_type", "kb_or_update_patch"} {
		if n := res.Soft.Filtered[k]; n > 0 {
			filtered += n
			reasons = append(reasons, fmt.Sprintf("%s %d", k, n))
		}
	}

	fmt.Fprintf(w, "\n软件清单（三源合并）\n")
	fmt.Fprintf(w, "  原始 %d 条 · 滤除 %d 条", res.Soft.Raw, filtered)
	if len(reasons) > 0 {
		fmt.Fprintf(w, "（%s）", strings.Join(reasons, " / "))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  来源 uninstall %d / winget %d / app_paths %d", res.Soft.BySource["uninstall"],
		res.Soft.BySource["winget"], res.Soft.BySource["app_paths"])
	if res.Soft.WingetVia != "" {
		fmt.Fprintf(w, " · winget 解析格式 %s", res.Soft.WingetVia)
	}
	fmt.Fprintln(w)

	sum, err := match.Summarize(recipes.Builtin(), res.Soft.Software)
	if err != nil {
		fmt.Fprintf(w, "  覆盖统计不可用: %v\n", err)
		return
	}

	fmt.Fprintf(w, "  配方 %d 条 · 已识别 %d · 未识别 %d", sum.Recipes, sum.Recognized, sum.Unrecognized)
	if sum.Unrecognized > 0 {
		fmt.Fprintf(w, "（其中有 winget id 的 %d 条——这部分是配方缺口，其余本来就该 manual）", sum.UnrecognizedWithWinget)
	}
	fmt.Fprintln(w)

	const limit = 40
	shown := 0
	if sum.Unrecognized > 0 {
		fmt.Fprintf(w, "\n  ── 未识别（%d）──\n", sum.Unrecognized)
	}
	for _, e := range sum.Entries {
		if e.Matched() {
			continue
		}
		if shown >= limit {
			fmt.Fprintf(w, "    …未识别还有 %d 条\n", sum.Unrecognized-limit)
			break
		}
		shown++
		sw := res.Soft.Software[e.Index]
		ver := orDash(derefString(sw.Version))
		switch {
		case len(e.Ambiguous) > 0:
			fmt.Fprintf(w, "    %s  %s  [%d 个配方都能命中，需人工定夺: %s]\n",
				pad(sw.Name, 52), pad(ver, 14), len(e.Ambiguous), strings.Join(e.Ambiguous, " / "))
		case sw.WingetID != nil && strings.TrimSpace(*sw.WingetID) != "":
			fmt.Fprintf(w, "    %s  %s  [有 winget id: %s]\n", pad(sw.Name, 52), pad(ver, 14), *sw.WingetID)
		default:
			fmt.Fprintf(w, "    %s  %s\n", pad(sw.Name, 52), ver)
		}
	}

	shown = 0
	if sum.Recognized > 0 {
		fmt.Fprintf(w, "\n  ── 已识别（%d）──\n", sum.Recognized)
	}
	for _, e := range sum.Entries {
		if !e.Matched() {
			continue
		}
		if shown >= limit {
			fmt.Fprintf(w, "    …已识别还有 %d 条\n", sum.Recognized-limit)
			break
		}
		shown++
		sw := res.Soft.Software[e.Index]
		fmt.Fprintf(w, "    %s  %s  [%s → %s]\n",
			pad(sw.Name, 52), pad(orDash(derefString(sw.Version)), 12), e.Basis, e.Recipe)
	}
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
