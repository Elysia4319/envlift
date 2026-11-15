package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Elysia4319/envlift/internal/apply"
	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/engine"
	"github.com/Elysia4319/envlift/internal/pathenv"
	"github.com/Elysia4319/envlift/internal/pathmgmt"
	"github.com/Elysia4319/envlift/internal/plan"
	"github.com/Elysia4319/envlift/internal/probe"
	"github.com/Elysia4319/envlift/internal/report"
	"github.com/Elysia4319/envlift/internal/scan"
	"github.com/Elysia4319/envlift/internal/sysinfo"
	"github.com/Elysia4319/envlift/internal/winreg"
)

// 默认探测地址。可用 ENVLIFT_NET_PROBE 覆盖（如指向 127.0.0.1:1 以模拟断网）。
const defaultNetworkProbe = "cdn.winget.microsoft.com:443"

func runRestore(args []string) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgPath := fs.String("config", "envlift.yaml", "配置路径")
	apply := fs.Bool("apply", false, "真正执行（默认只预演）")
	yes := fs.Bool("yes", false, "跳过无需管理员运行的确认")
	planOut := fs.String("plan-out", "", "把计划写成 JSON 文件（默认脱敏）")
	journalDir := fs.String("journal-dir", "reports", "journal、报告与 PATH 备份的落盘目录")
	writeReport := fs.Bool("report", false, "把报告（md + json）写进报告目录")
	rawPlan := fs.Bool("raw-plan", false, "计划 JSON 原样输出（含真实用户名/主机名）")
	installerDir := fs.String("installers", "installers", "本地安装包目录")
	fromUser := fs.String("from-user", "", "--raw 配置中的原用户名，用于重写")
	fromHost := fs.String("from-host", "", "--raw 配置中的原主机名，用于重写")
	rollback := fs.Bool("rollback", false, "把 PATH 回滚到 journal 里的快照")
	journalPath := fs.String("journal", "", "指定 journal 文件（回滚用，缺省取 reports/ 下最新）")
	decisionsPath := fs.String("decisions", "", "决策文件（缺省读配置同目录的 envlift.decisions.yaml，存在则加载）")
	if err := fs.Parse(args); err != nil {
		return exitError
	}

	// 回滚不依赖配置：只需要 journal 里的 PATH 快照。
	if *rollback {
		return runRollback(*journalPath, *journalDir)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return exitError
	}

	// 预检：只读探测目标机（dry-run 时即本机）。
	reg := winreg.New()
	scanner := &scan.Scanner{
		Registry: reg,
		SysInfo:  sysinfo.New(reg),
		Prober:   probe.New(4 * time.Second),
		Redactor: config.NewRedactor("", ""),
	}
	env, err := scanner.ProbeEnvironment()
	if err != nil {
		fmt.Fprintf(os.Stderr, "环境预检失败: %v\n", err)
		return exitError
	}

	// C8：不含占位符即 --raw 导出，按字面值还原，绝不猜测旧身份。
	rawMode := plan.DetectRaw(cfg)
	if rawMode && *fromUser == "" && *fromHost == "" {
		fmt.Fprintln(os.Stderr, "警告: 配置中不含 ${USER}/${HOST} 占位符，判定为 --raw 导出。")
		fmt.Fprintln(os.Stderr, "      路径将按字面值还原，不会猜测原机器的用户名/主机名。")
		fmt.Fprintln(os.Stderr, "      需要重写时请显式指定 --from-user <旧用户名> --from-host <旧主机名>。")
		fmt.Fprintln(os.Stderr, "")
	}
	if *fromUser != "" || *fromHost != "" {
		config.NewRedactor(*fromUser, *fromHost).Apply(cfg)
	}

	redactor := config.NewRedactor(env.Info.Username, env.Info.Hostname)
	if err := redactor.Restore(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "代入目标机身份失败: %v\n", err)
		return exitError
	}

	files, err := listInstallerFiles(*installerDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取安装包目录 %s 失败: %v\n", *installerDir, err)
		return exitError
	}
	absInstallerDir, err := filepath.Abs(*installerDir)
	if err != nil {
		absInstallerDir = *installerDir
	}

	target := buildTarget(env)
	target.NetworkOK, target.NetworkNote = probeNetwork(3 * time.Second)

	// ④ 决策：显式指定就必须能读到（读不到是错误，不是「没有决策」）；
	// 缺省时读配置同目录的 envlift.decisions.yaml，不存在等同于没有决策。
	decPath := *decisionsPath
	explicit := decPath != ""
	if decPath == "" {
		decPath = filepath.Join(filepath.Dir(*cfgPath), "envlift.decisions.yaml")
	}
	ds, err := engine.LoadDecisionsFile(decPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载决策文件失败: %v\n", err)
		return exitError
	}
	if explicit && ds == nil {
		fmt.Fprintf(os.Stderr, "决策文件 %s 不存在或为空；--decisions 明确指定时不允许当成「没有决策」。\n", decPath)
		return exitError
	}

	p, err := plan.Build(plan.Options{
		Config:         cfg,
		Target:         target,
		InstallerDir:   absInstallerDir,
		InstallerFiles: files,
		Recipes:        mergeRecipes(cfg),
		Now:            time.Now(),
		Exists:         pathExists,
		Expand:         redactor.ExpandString,
		ExpandVars:     pathenv.ExpandVars,
		RawConfig:      rawMode,
		Decisions:      ds,
	})
	if err != nil {
		var derr *plan.DecisionErrors
		if errors.As(err, &derr) {
			fmt.Fprintln(os.Stderr, "决策无法应用（一条都不执行）：")
			for _, is := range derr.Issues {
				if is.TargetID != "" {
					fmt.Fprintf(os.Stderr, "  - 第 %d 条决策（%s）：%s\n", is.Index, is.TargetID, is.Reason)
				} else {
					fmt.Fprintf(os.Stderr, "  - 第 %d 条决策：%s\n", is.Index, is.Reason)
				}
			}
			return exitRefused
		}
		fmt.Fprintf(os.Stderr, "生成计划失败: %v\n", err)
		return exitError
	}

	renderPlan(os.Stdout, p, *cfgPath, *installerDir)

	if *planOut != "" {
		// D-a：--plan-out 允许写到任意路径，所以默认脱敏是唯一可靠防线。
		if !*rawPlan {
			config.NewRedactor(env.Info.Username, env.Info.Hostname).RedactStrings(p)
		} else {
			fmt.Fprintln(os.Stderr, "警告: --raw-plan 已关闭计划脱敏，文件含真实用户名与主机名。")
		}
		if err := writePlanJSON(*planOut, p); err != nil {
			fmt.Fprintf(os.Stderr, "写出计划失败: %v\n", err)
			return exitError
		}
		fmt.Printf("\n计划已写出: %s\n", *planOut)
	}

	code := exitOK
	// C2：数据类待确认不再阻断（未决策项会被跳过并进报告），但退出码仍是 2，
	// 提醒「这不是全绿」。
	if p.IsBlocked() || p.Summary.BlockedNeedsDecision > 0 {
		code = exitRefused
	}

	if *apply {
		return doApply(p, env, *cfgPath, *journalDir, *yes, ds, newPrompter(os.Stdin, os.Stdout))
	}

	if *writeReport {
		md, js, err := report.Write(*journalDir, report.Input{
			Plan: p, ConfigPath: *cfgPath, Now: time.Now(),
			Decisions: ds, Redactor: redactor,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "写报告失败: %v\n", err)
			return exitError
		}
		fmt.Printf("\n报告已写出: %s\n          %s\n", md, js)
	}

	fmt.Println("\n这是预演，未执行任何命令、未写入任何文件。")
	fmt.Println("确认无误后运行: envlift restore --apply")
	_ = *yes
	return code
}

func buildTarget(env *scan.Environment) plan.Target {
	t := plan.Target{
		OS:            env.Info.OSVersion,
		Build:         env.Info.OSBuild,
		Arch:          env.Info.Arch,
		Username:      env.Info.Username,
		Hostname:      env.Info.Hostname,
		Elevated:      env.Info.Elevated,
		WingetOK:      env.Info.WingetAvailable,
		WingetVer:     env.Info.WingetVersion,
		WingetForceOK: env.Info.WingetForceAvailable,
		Runtimes:      make(map[string]plan.RuntimeState, len(env.Runtimes)),
		PathDirs:      env.PathDirs,
	}
	for _, d := range env.Info.Drives {
		t.Drives = append(t.Drives, d.Letter)
	}
	for _, r := range env.Runtimes {
		st := plan.RuntimeState{Present: r.Executable != nil}
		if r.Version != nil {
			st.Version = *r.Version
		}
		if r.Executable != nil {
			st.Path = *r.Executable
		}
		t.Runtimes[r.Name] = st
	}
	return t
}

func probeNetwork(timeout time.Duration) (bool, string) {
	target := defaultNetworkProbe
	if v := os.Getenv("ENVLIFT_NET_PROBE"); v != "" {
		target = v
	}
	conn, err := net.DialTimeout("tcp", target, timeout)
	if err != nil {
		return false, err.Error()
	}
	_ = conn.Close()
	return true, ""
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func listInstallerFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // 目录不存在等同于没有本地包，不是错误
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// mergeRecipes 合并内置配方与用户配方：同名时用户条目覆盖内置（D6）。
// 本轮内置表为空，逻辑先立好，第 ④ 步接入时不用改调用方。
func mergeRecipes(cfg *config.Config) []config.Installer {
	builtin := []config.Installer{}

	byName := make(map[string]int, len(builtin)+len(cfg.Installers))
	out := make([]config.Installer, 0, len(builtin)+len(cfg.Installers))
	for _, r := range builtin {
		byName[strings.ToLower(r.Name)] = len(out)
		out = append(out, r)
	}
	for _, r := range cfg.Installers {
		key := strings.ToLower(r.Name)
		if i, ok := byName[key]; ok {
			out[i] = r
			continue
		}
		byName[key] = len(out)
		out = append(out, r)
	}
	return out
}

func writePlanJSON(path string, p *plan.Plan) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func renderPlan(w io.Writer, p *plan.Plan, cfgPath, installerDir string) {
	fmt.Fprintf(w, "envlift restore — 预演（未执行任何操作）\n")
	fmt.Fprintf(w, "配置 %s · schema v%d · 生成于 %s\n", cfgPath, p.Schema, p.GeneratedAt)
	if p.RawConfig {
		fmt.Fprintf(w, "注意 配置不含占位符（--raw 导出），按字面值还原\n")
	}

	fmt.Fprintln(w, "\n环境预检")
	fmt.Fprintf(w, "  %s%s\n", pad("目标机", 12), fmt.Sprintf("%s (build %s) / %s", p.Target.OS, p.Target.Build, p.Target.Arch))
	fmt.Fprintf(w, "  %s%s\n", pad("用户", 12), p.Target.Username+"@"+p.Target.Hostname)
	priv := "普通用户"
	if p.Target.Elevated {
		priv = "管理员运行"
	}
	fmt.Fprintf(w, "  %s%s\n", pad("权限", 12), priv)

	winget := "不可用（winget 级将整级跳过）"
	if p.Target.WingetOK {
		winget = "可用 " + strings.TrimSpace(p.Target.WingetVer)
	}
	fmt.Fprintf(w, "  %s%s\n", pad("winget", 12), winget)

	network := "可达"
	if !p.Target.NetworkOK {
		network = "不可达（按断网处理，winget 级整级跳过）"
		if p.Target.NetworkNote != "" {
			network += " · " + p.Target.NetworkNote
		}
	}
	fmt.Fprintf(w, "  %s%s\n", pad("网络", 12), network)
	fmt.Fprintf(w, "  %s%s\n", pad("安装包", 12),
		fmt.Sprintf("%s 下 %d 个文件，命中 %d 个配方 · 配方表 %d 条",
			installerDir, p.Preflight.InstallerFiles, p.Preflight.InstallerMatched, p.Preflight.Recipes))

	fmt.Fprintln(w, "\n执行计划（按执行顺序）")
	if len(p.Items) == 0 {
		fmt.Fprintln(w, "  （无）")
	}
	for i, it := range p.Items {
		fmt.Fprintf(w, "  %2d. [%s] %s\n", i+1, it.Kind, it.Name)
		if it.Expected != "" || it.Current != "" {
			cur := it.Current
			if cur == "" {
				cur = "-"
			}
			fmt.Fprintf(w, "      期望 %s / 当前 %s\n", orDash(it.Expected), cur)
		}
		if it.MovedBy != "" {
			fmt.Fprintf(w, "      顺序 %s\n", it.MovedBy)
		}

		switch it.Status {
		case plan.StatusSkipped:
			fmt.Fprintf(w, "      层级 跳过 · %s\n", it.Reason)
		case plan.StatusManual:
			fmt.Fprintf(w, "      层级 manual · %s\n", it.Reason)
			if it.ManualHint != "" {
				fmt.Fprintf(w, "      人工步骤 %s\n", it.ManualHint)
			}
		case plan.StatusAmbiguous, plan.StatusBlockedNeedsDecision:
			fmt.Fprintf(w, "      层级 待决策（本次跳过） · %s\n", it.Reason)
		default:
			fmt.Fprintf(w, "      层级 %s\n", it.Tier)
			if it.RecipeName != "" {
				fmt.Fprintf(w, "      配方 %s（来源 %s，验证于 %s）\n",
					it.RecipeName, orDash(it.RecipeSource), orDash(it.VerifiedAt))
			}
			if it.Command != "" {
				fmt.Fprintf(w, "      命令 %s\n", it.Command)
			}
			if it.NeedsAdmin {
				fmt.Fprintln(w, "      需要 管理员运行（执行时逐项询问）")
			}
			if it.ExpectReboot {
				fmt.Fprintln(w, "      重启 完成后需重启才能继续")
			}
			if it.VerifyCmd != "" {
				fmt.Fprintf(w, "      校验 %s\n", verifyText(it.VerifyCmd, it.VerifyExpect))
			} else {
				fmt.Fprintln(w, "      校验 全局三级校验")
			}
			if it.Fallback != "" {
				fmt.Fprintf(w, "      降级 %s\n", it.Fallback)
			}
			if it.Reason != "" {
				fmt.Fprintf(w, "      说明 %s\n", it.Reason)
			}
		}
	}

	fmt.Fprintln(w, "\nPATH 变更")
	fmt.Fprintf(w, "  备份落点 reports/path-backup-%s.json（执行时生成，另有同名 .reg）\n",
		time.Now().Format("20060102-150405"))
	for _, section := range []struct{ key, label string }{
		{"user", "用户级"}, {"machine", "系统级"},
	} {
		fmt.Fprintf(w, "  %s\n", section.label)
		count := 0
		for _, ch := range p.Path {
			if ch.Section != section.key {
				continue
			}
			count++
			reason := ""
			if ch.Reason != "" {
				reason = "  (" + ch.Reason + ")"
			}
			if ch.Decision != "" {
				reason += "  决策=" + ch.Decision
			}
			fmt.Fprintf(w, "    %s %s%s\n", ch.Mark, ch.Value, reason)
		}
		if count == 0 {
			fmt.Fprintln(w, "    （无）")
		}
	}

	risks := collectRisks(p)
	fmt.Fprintln(w, "\n待确认与风险")
	if len(risks) == 0 {
		fmt.Fprintln(w, "  （无）")
	}
	for _, r := range risks {
		fmt.Fprintf(w, "  - %s\n", r)
	}

	fmt.Fprintln(w, "\n汇总")
	fmt.Fprintf(w, "  winget %d · 本地包 %d · manual %d · 跳过 %d · 待确认 %d · 因未决策跳过 %d\n",
		p.Summary.Winget, p.Summary.Local, p.Summary.Manual, p.Summary.Skipped,
		p.Summary.Pending, p.Summary.BlockedNeedsDecision)

	if p.IsBlocked() {
		fmt.Fprintf(w, "\n已阻止: %s\n", p.Blocked)
	}
}

func collectRisks(p *plan.Plan) []string {
	var out []string
	for _, it := range p.Items {
		switch it.Status {
		case plan.StatusAmbiguous, plan.StatusBlockedNeedsDecision:
			out = append(out, fmt.Sprintf("%s：%s", it.Name, it.Reason))
		case plan.StatusManual:
			hint := it.ManualHint
			if hint == "" {
				hint = "需手工安装"
			}
			out = append(out, fmt.Sprintf("%s 需人工：%s（%s）", it.Name, hint, it.Reason))
		default:
			if it.VerifiedAt == "" && it.RecipeSource == config.InstallerBuiltin {
				out = append(out, fmt.Sprintf("%s：内置配方缺少 verified_at，可能已过期", it.Name))
			}
			if it.NeedsAdmin {
				out = append(out, fmt.Sprintf("%s：需要管理员运行", it.Name))
			}
			if it.ExpectReboot {
				out = append(out, fmt.Sprintf("%s：完成后需重启，执行会在此停止", it.Name))
			}
		}
	}
	for _, ch := range p.Path {
		if ch.Mark == plan.MarkPending || ch.Mark == plan.MarkInvalid {
			out = append(out, fmt.Sprintf("PATH[%s] %s：%s", ch.Section, ch.Value, ch.Reason))
		}
	}
	return out
}

func verifyText(cmd, expect string) string {
	if expect == "" {
		return cmd
	}
	return fmt.Sprintf("%s（输出需含 %q）", cmd, expect)
}

// doApply 执行计划。C1：独立于 dry-run 的二次确认，--yes 不能绕过它。
func doApply(p *plan.Plan, env *scan.Environment, cfgPath, journalDir string, yes bool, ds plan.Decisions, pr *prompter) int {
	if p.IsBlocked() {
		fmt.Fprintf(os.Stderr, "计划被阻止，不能执行: %s\n", p.Blocked)
		return exitRefused
	}
	// C2：未决策项跳过执行，不阻断整批；先明确告诉用户这次会少做几件事。
	if n := p.Summary.BlockedNeedsDecision; n > 0 {
		fmt.Printf("注意: %d 项因未决策将被跳过（C2），其余照常执行。\n", n)
	}

	executable := 0
	adminCount := 0
	rebootCount := 0
	for _, it := range p.Items {
		if it.Status != plan.StatusPlanned || it.Command == "" {
			continue
		}
		executable++
		if it.NeedsAdmin {
			adminCount++
		}
		if it.ExpectReboot {
			rebootCount++
		}
	}

	fmt.Printf("\n即将执行 %d 项，其中 %d 项需管理员运行，%d 项将触发重启。\n", executable, adminCount, rebootCount)
	fmt.Printf("PATH 变更 %d 条。\n", p.Summary.Pending+p.Summary.Winget*0+countWrites(p))
	if !env.Info.Elevated && adminCount > 0 {
		fmt.Println("注意: 当前进程未以管理员运行，需要管理员运行的项会被阻止。")
	}
	if !pr.applyConfirmation() {
		fmt.Println("已取消，未执行任何操作。")
		return exitRefused
	}

	if err := os.MkdirAll(journalDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "创建 %s 失败: %v\n", journalDir, err)
		return exitError
	}
	jpath := filepath.Join(journalDir, ".envlift-journal-"+time.Now().Format("20060102-150405")+".json")
	j, err := apply.NewJournal(jpath, cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建 journal 失败: %v\n", err)
		return exitError
	}
	fmt.Printf("事务日志: %s\n\n", j.Path())

	res, err := apply.Run(apply.Options{
		Plan:      p,
		Journal:   j,
		Exec:      apply.NewExecutor(),
		Paths:     pathmgmt.New(),
		Locations: apply.NewRegistryLocationReader(),
		Out:       os.Stdout,
		Elevated:  env.Info.Elevated,
		Confirm:   pr.confirm,
		Preflight: func() (bool, bool, string) {
			ok, note := probeNetwork(3 * time.Second)
			return env.Info.WingetAvailable, ok, note
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n执行中止: %v\n", err)
		return exitError
	}

	fmt.Printf("\n执行完成: 成功 %d · 跳过 %d · 失败 %d · 阻止 %d · PATH 新增 %d\n",
		res.Executed, res.Skipped, res.Failed, res.Blocked, res.PathAdded)
	if res.Stopped != "" {
		fmt.Printf("停止原因: %s\n", res.Stopped)
	}
	fmt.Printf("回滚命令: envlift restore --rollback --journal \"%s\"\n", j.Path())

	// 报告默认在 apply 后写出：执行过什么必须有据可查。
	md, js, rerr := report.Write(journalDir, report.Input{
		Plan: p, Apply: res, Steps: j.Steps, JournalPath: j.Path(), ConfigPath: cfgPath,
		// CLI 的 --apply 逐项询问提权，等价于 mode=all。
		ApplyMode: "all", Decisions: ds, Now: time.Now(),
		Redactor: config.NewRedactor(env.Info.Username, env.Info.Hostname),
	})
	if rerr != nil {
		fmt.Fprintf(os.Stderr, "写报告失败: %v\n", rerr)
	} else {
		fmt.Printf("报告: %s\n      %s\n", md, js)
	}

	if res.Failed > 0 || res.Blocked > 0 {
		return exitError
	}
	if res.Undecided > 0 {
		return exitRefused
	}
	if res.Stopped != "" {
		return exitRefused
	}
	return exitOK
}

func countWrites(p *plan.Plan) int {
	n := 0
	for _, ch := range p.Path {
		if ch.Mark == plan.MarkAdd || ch.Mark == plan.MarkRewrite {
			n++
		}
	}
	return n
}

// runRollback 把 PATH 恢复到 journal 里的快照。不做软件卸载（D11）。
func runRollback(journalPath, journalDir string) int {
	path := journalPath
	if path == "" {
		latest, err := apply.LatestJournal(journalDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return exitError
		}
		path = latest
	}

	j, err := apply.LoadJournal(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return exitError
	}
	if j.Snapshot == nil {
		fmt.Fprintf(os.Stderr, "journal 中没有 PATH 快照，无法回滚: %s\n", path)
		return exitError
	}

	fmt.Printf("回滚 %s\n", path)
	fmt.Printf("快照时间 %s\n", j.Snapshot.TakenAt)
	if err := apply.Rollback(j, pathmgmt.New(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "回滚失败: %v\n", err)
		return exitError
	}
	fmt.Println("回滚完成。请重新打开终端以加载恢复后的 PATH。")
	return exitOK
}
