// Package apply 执行还原计划：二次确认、事务日志、PATH 备份与回滚。
//
// 这是全工具唯一会改动系统的部分，所以每个外部能力都做成接口，测试全部用假实现。
package apply

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Elysia4319/envlift/internal/pathenv"
	"github.com/Elysia4319/envlift/internal/pathmgmt"
	"github.com/Elysia4319/envlift/internal/plan"
)

// 计划里的层级。
const (
	tierWinget = plan.TierWinget
	tierLocal  = plan.TierLocal
	tierManual = plan.TierManual
)

// Options 是 Run 的入参。
type Options struct {
	Plan    *plan.Plan
	Journal *Journal
	Exec    Executor
	Paths   pathmgmt.Manager
	// Confirm 用于逐项确认需要提权的操作（P4/C1）。
	Confirm func(prompt string) bool
	Out     io.Writer
	// Elevated 报告当前进程是否已提权。
	Elevated bool
	// SkipAdmin 表示用户显式选择了「只跑非提权部分」（D15 skip_admin）。
	// 此时提权项一律标 blocked，绝不放行——包括系统级 PATH 写入。
	SkipAdmin bool
	// Preflight 在 apply 开始时重新探测环境（C5）。
	Preflight func() (wingetOK, networkOK bool, note string)
	Timeout   time.Duration
	Now       func() time.Time
}

// Result 是一次 apply 的汇总。
type Result struct {
	Executed int `json:"executed"`
	Skipped  int `json:"skipped"`
	Failed   int `json:"failed"`
	Blocked  int `json:"blocked"`
	// Undecided 是「因未决策被跳过」的条目数（C2：不阻断、不算失败）。
	Undecided int    `json:"undecided"`
	PathAdded int    `json:"path_added"`
	Stopped   string `json:"stopped,omitempty"`
	BakPath   string `json:"bak_path,omitempty"`
}

// Run 执行计划。
func Run(opts Options) (*Result, error) {
	if opts.Plan == nil || opts.Journal == nil {
		return nil, fmt.Errorf("缺少计划或日志")
	}
	if opts.Exec == nil || opts.Paths == nil {
		return nil, fmt.Errorf("缺少执行器或 PATH 管理器")
	}
	refreshProcessPath(opts.Paths)
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Confirm == nil {
		opts.Confirm = func(string) bool { return false }
	}

	res := &Result{}

	// C5/C6：apply 开始时重新探测，与 dry-run 不一致就停，不自行改道。
	if opts.Preflight != nil {
		wingetOK, networkOK, note := opts.Preflight()
		if wingetOK != opts.Plan.Target.WingetOK || networkOK != opts.Plan.Target.NetworkOK {
			msg := fmt.Sprintf("环境与 dry-run 时不一致（dry-run: winget=%v 网络=%v；现在: winget=%v 网络=%v %s），请重跑 dry-run",
				opts.Plan.Target.WingetOK, opts.Plan.Target.NetworkOK, wingetOK, networkOK, note)
			_ = opts.Journal.Finish(msg)
			return nil, fmt.Errorf("%s", msg)
		}
	}

	pathValues := collectPathValues(opts.Plan)
	// D15 skip_admin：系统级 PATH 写入也是提权操作，整段排除并记账。
	if opts.SkipAdmin && len(pathValues[pathmgmt.HiveMachine]) > 0 {
		fmt.Fprintf(opts.Out, "  [阻止] 系统级 PATH %d 条 —— mode=skip_admin，未写入\n", len(pathValues[pathmgmt.HiveMachine]))
		_ = opts.Journal.Append(Step{
			Kind: "path", Name: "PATH[system]", Tier: "path", NeedsAdmin: true,
			Status: StepBlocked, StartedAt: opts.Now().Format(time.RFC3339),
			FinishedAt: opts.Now().Format(time.RFC3339),
			Output:     "mode=skip_admin：系统级 PATH 未写入",
		})
		res.Blocked += len(pathValues[pathmgmt.HiveMachine])
		delete(pathValues, pathmgmt.HiveMachine)
	}
	needPath := len(pathValues[pathmgmt.HiveUser]) > 0 || len(pathValues[pathmgmt.HiveMachine]) > 0

	// C3：任何 PATH 变更之前先备份，备份失败即中止。
	if needPath {
		snap, err := opts.Paths.Snapshot()
		if err != nil {
			_ = opts.Journal.Finish("PATH 备份失败，已中止")
			return nil, fmt.Errorf("PATH 备份失败，未做任何改动: %w", err)
		}
		if err := opts.Journal.SetSnapshot(snap); err != nil {
			return nil, err
		}
		bak, err := WriteSnapshotBak(opts.Journal.Path(), snap)
		if err != nil {
			_ = opts.Journal.Finish("PATH 备份文件写入失败，已中止")
			return nil, err
		}
		res.BakPath = bak
		fmt.Fprintf(opts.Out, "PATH 已备份: %s\n", bak)
	}

	// 执行软件/运行时。
	for i, it := range opts.Plan.Items {
		// C2：未决策的条目跳过执行，但必须进 journal——静默跳过等于没发生，
		// 事后看报告会以为「本来就没什么可做」。
		if it.Status == plan.StatusBlockedNeedsDecision {
			now := opts.Now().Format(time.RFC3339)
			_ = opts.Journal.Append(Step{
				Index: i, Kind: it.Kind, Name: it.Name, Status: StepSkipped,
				StartedAt: now, FinishedAt: now,
				Output: "未决策（blocked-needs-decision），本次跳过：" + it.Reason,
			})
			res.Undecided++
			fmt.Fprintf(opts.Out, "  [未决策] %s —— 本次跳过\n", it.Name)
			continue
		}
		if it.Status != plan.StatusPlanned {
			continue
		}
		if it.Tier == tierManual || it.Command == "" {
			continue
		}

		started := opts.Now().Format(time.RFC3339)

		// D15 skip_admin：用户显式声明只跑非提权部分，提权项一律阻止，
		// 既不静默跳过（那样报告里看不见），也不因已提权就顺手执行。
		if it.NeedsAdmin && opts.SkipAdmin {
			started := opts.Now().Format(time.RFC3339)
			_ = opts.Journal.Append(Step{
				Index: i, Kind: it.Kind, Name: it.Name, Tier: it.Tier,
				Command: it.Command, NeedsAdmin: true, Status: StepBlocked,
				StartedAt: started, FinishedAt: started,
				Output: "mode=skip_admin：本次未确认管理员运行，已阻止（不影响无需管理员运行的项）",
			})
			res.Blocked++
			fmt.Fprintf(opts.Out, "  [阻止] %s —— mode=skip_admin，需管理员运行的项未执行\n", it.Name)
			continue
		}
		// C1/P4：需要提权而当前没提权 → 停下，不静默跳过也不自动提权。
		if it.NeedsAdmin && !opts.Elevated {
			step := Step{
				Index: i, Kind: it.Kind, Name: it.Name, Tier: it.Tier,
				Command: it.Command, NeedsAdmin: true, Status: StepBlocked,
				StartedAt: started, FinishedAt: started,
				Output: "需要管理员运行，当前进程未以管理员运行",
			}
			_ = opts.Journal.Append(step)
			res.Blocked++
			fmt.Fprintf(opts.Out, "  [阻止] %s —— 需要管理员运行，当前未以管理员运行\n", it.Name)
			// C-2：单个提权项只跳过它自己，不中断整批。
			continue
		}
		if it.NeedsAdmin {
			if !opts.Confirm(fmt.Sprintf("条目 %s 需要管理员运行，执行: %s", it.Name, it.Command)) {
				_ = opts.Journal.Append(Step{
					Index: i, Kind: it.Kind, Name: it.Name, Tier: it.Tier,
					Command: it.Command, NeedsAdmin: true, Status: StepSkipped,
					StartedAt: started, FinishedAt: started, Output: "用户拒绝管理员运行",
				})
				res.Skipped++
				continue
			}
		}

		// C4.1/P1：先跑 verify 判断是否已完成，已完成就跳过，不重装。
		// 指定安装目录是显式重装：verify 通过也不能跳过。
		if it.Reinstall || it.InstallDirApplied {
			fmt.Fprintf(opts.Out, "  [重装] %s —— 指定了安装目录，跳过幂等检查\n", it.Name)
		} else if it.VerifyCmd != "" {
			vr := opts.Exec.Run(context.Background(), it.VerifyCmd, 60*time.Second)
			if verifyPassed(vr, it.VerifyExpect) {
				// 已经装好：verify 的输出就是「现在装的是什么版本」，回填给报告。
				ver, okVer := ParseVersion(vr.Output)
				st := Step{
					Index: i, Kind: it.Kind, Name: it.Name, Tier: it.Tier,
					Command: it.VerifyCmd, Status: StepSkipped,
					StartedAt: started, FinishedAt: opts.Now().Format(time.RFC3339),
					Output: "幂等检查：已完成",
				}
				if okVer {
					st.ActualVersion = ver
				} else {
					st.VersionRaw = truncate(vr.Output, 300)
				}
				_ = opts.Journal.Append(st)
				fmt.Fprintf(opts.Out, "  [跳过] %s —— 已完成\n", it.Name)
				res.Skipped++
				continue
			}
		}

		fmt.Fprintf(opts.Out, "  [执行] %s —— %s\n", it.Name, it.Command)
		rr := opts.Exec.Run(context.Background(), it.Command, opts.Timeout)

		status := StepOK
		if rr.ExitCode != 0 || rr.Err != nil {
			status = StepFailed
			res.Failed++
		} else {
			res.Executed++
		}
		step := Step{
			Index: i, Kind: it.Kind, Name: it.Name, Tier: it.Tier,
			Command: it.Command, NeedsAdmin: it.NeedsAdmin, ExpectReboot: it.ExpectReboot,
			Reinstall: it.Reinstall,
			ExitCode:  rr.ExitCode, Output: truncate(rr.Output, 2000), Status: status,
			StartedAt: started, FinishedAt: opts.Now().Format(time.RFC3339),
		}

		// D7：单个 winget 包失败时，只有该包降级到本地包，不整级改道。
		if status == StepFailed && it.FallbackPlan != nil {
			fb := it.FallbackPlan
			fmt.Fprintf(opts.Out, "  [降级] %s —— %s\n", it.Name, fb.Command)
			frr := opts.Exec.Run(context.Background(), fb.Command, opts.Timeout)
			step.Tier = fb.Tier
			step.Command = fb.Command
			step.NeedsAdmin = fb.NeedsAdmin
			step.ExpectReboot = fb.ExpectReboot
			step.ExitCode = frr.ExitCode
			step.Output = truncate(step.Output+"\n--- 降级到本地包 ---\n"+frr.Output, 2000)
			it.VerifyCmd = fb.VerifyCmd
			it.VerifyExpect = fb.VerifyExpect
			it.ExpectReboot = fb.ExpectReboot
			if frr.ExitCode == 0 && frr.Err == nil {
				step.Status = StepOK
				status = StepOK
				res.Failed--
				res.Executed++
			} else {
				step.Status = StepFailed
			}
		}

		// 执行后再 verify 一次，判定真实结果（C4.4），顺带回填执行后的版本（C1）。
		if status == StepOK && it.VerifyCmd != "" {
			// 安装器通常直接改注册表 PATH，不会改父进程环境。重跑 verify 前
			// 按注册表重建进程 PATH，否则刚装好的命令仍会报「找不到」。
			refreshProcessPath(opts.Paths)
			vr := opts.Exec.Run(context.Background(), it.VerifyCmd, 60*time.Second)
			if !verifyPassed(vr, it.VerifyExpect) {
				step.Status = StepFailed
				detail := vr.Output
				if vr.ExitCode == 0 && it.VerifyExpect != "" && !strings.Contains(vr.Output, it.VerifyExpect) {
					detail = fmt.Sprintf("输出不含期望内容 %q\n%s", it.VerifyExpect, vr.Output)
				}
				step.Output = truncate(step.Output+"\n--- verify 失败 ---\n"+detail, 2000)
				res.Failed++
				res.Executed--
			} else if ver, okVer := ParseVersion(vr.Output); okVer {
				step.ActualVersion = ver
			} else {
				// 解析不出来就留空 + 原始输出，报告里标 version_unparsed。
				step.VersionRaw = truncate(vr.Output, 300)
			}
		}
		_ = opts.Journal.Append(step)

		if step.Status == StepFailed {
			// D11：失败即停并保留现状，不自动卸载。
			res.Stopped = fmt.Sprintf("条目 %s 执行失败（退出码 %d），已停止并保留现状", it.Name, step.ExitCode)
			_ = opts.Journal.Finish(res.Stopped)
			return res, nil
		}

		// P2/C7：需要重启就停在这里，靠下次运行的幂等检查跳过已完成项。
		if it.ExpectReboot {
			res.Stopped = fmt.Sprintf("条目 %s 完成后需要重启，请重启后重新运行 envlift restore --apply", it.Name)
			_ = opts.Journal.Finish(res.Stopped)
			return res, nil
		}
	}

	// PATH 写入放在软件之后，保证目录真实存在（§5.8）。
	if needPath {
		added, err := applyPath(opts, pathValues)
		res.PathAdded = added
		if err != nil {
			res.Stopped = err.Error()
			_ = opts.Journal.Finish(res.Stopped)
			return res, nil
		}
	}

	_ = opts.Journal.Finish("")
	return res, nil
}

// collectPathValues 收集需要写入的 PATH 条目，按 section 分组。
// 只有 +（新增）与 ~（用户名重写后新增）会写入；= 跳过、? 待确认、! 失效都不写。
func collectPathValues(p *plan.Plan) map[string][]string {
	out := map[string][]string{}
	for _, ch := range p.Path {
		if ch.Mark != plan.MarkAdd && ch.Mark != plan.MarkRewrite {
			continue
		}
		hive := pathmgmt.HiveUser
		if ch.Section == "machine" {
			hive = pathmgmt.HiveMachine
		}
		out[hive] = append(out[hive], ch.Value)
	}
	return out
}

func applyPath(opts Options, values map[string][]string) (int, error) {
	total := 0
	for _, hive := range []string{pathmgmt.HiveUser, pathmgmt.HiveMachine} {
		vals := values[hive]
		if len(vals) == 0 {
			continue
		}
		if hive == pathmgmt.HiveMachine && !opts.Elevated {
			return total, fmt.Errorf("写入系统级 PATH 需要管理员运行，当前进程未以管理员运行")
		}
		if hive == pathmgmt.HiveMachine {
			if !opts.Confirm("写入系统级 PATH（需管理员运行）") {
				return total, fmt.Errorf("用户拒绝写入系统级 PATH")
			}
		}

		cur, err := opts.Paths.Current(hive)
		if err != nil {
			return total, err
		}
		newValue, added := pathmgmt.AppendMissing(cur.Value, vals)
		if len(added) == 0 {
			continue
		}
		// 值类型原样保留：REG_EXPAND_SZ 写成 REG_SZ 会让 %VAR% 变成字面量。
		cur.Value = newValue
		if err := opts.Paths.Write(hive, cur); err != nil {
			return total, err
		}
		total += len(added)
		fmt.Fprintf(opts.Out, "  [PATH] %s 新增 %d 条\n", hive, len(added))
		// PATH 写入也要落进 journal 的步骤流：只靠日志文本判断「这次到底写没写」
		// 是不够的，回看 journal 时必须能一眼看到写的是哪个 hive、追加了哪几条。
		now := opts.Now().Format(time.RFC3339)
		_ = opts.Journal.Append(Step{
			Kind: "path", Name: "PATH[" + hive + "]", Tier: "path",
			Command: "(registry write) " + hive + `\Environment\Path`,
			Status:  StepOK, Output: truncate("新增: "+strings.Join(added, "; "), 2000),
			StartedAt: now, FinishedAt: now,
		})
	}
	if total > 0 {
		// 广播 WM_SETTINGCHANGE，否则新开进程读不到新 PATH。
		if err := opts.Paths.Notify(); err != nil {
			fmt.Fprintf(opts.Out, "  警告: 广播环境变更失败，新开的终端可能读不到新 PATH: %v\n", err)
		}
	}
	return total, nil
}

// Rollback 把 PATH 恢复成 journal 里的快照。不做软件卸载（D11）。
func Rollback(j *Journal, paths pathmgmt.Manager, out io.Writer) error {
	if j.Snapshot == nil {
		return fmt.Errorf("journal %s 里没有 PATH 快照，无法回滚", j.Path())
	}
	for _, item := range []struct {
		hive string
		e    pathmgmt.Entry
	}{
		{pathmgmt.HiveUser, j.Snapshot.User},
		{pathmgmt.HiveMachine, j.Snapshot.Machine},
	} {
		// 与快照一致就不写：写没改过的键既没必要，还会平白要求管理员权限，
		// 让「只改过用户级 PATH」的场景在非提权下回滚失败。
		if cur, err := paths.Current(item.hive); err == nil &&
			cur.Value == item.e.Value && cur.Kind == item.e.Kind {
			fmt.Fprintf(out, "  [回滚] %s Path 与快照一致，跳过写入\n", item.hive)
			continue
		}
		if err := paths.Write(item.hive, item.e); err != nil {
			return err
		}
		fmt.Fprintf(out, "  [回滚] %s Path 已恢复（%d 字符，类型 %d）\n", item.hive, len(item.e.Value), item.e.Kind)
	}
	if err := paths.Notify(); err != nil {
		fmt.Fprintf(out, "  警告: 广播环境变更失败: %v\n", err)
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(截断)"
}

// verifyPassed 判断一次 verify 是否算通过：退出码为 0，且（若配置了）
// 输出包含 expect_contains。只看退出码会把「命令能跑但不是目标软件」误判成已完成。
func verifyPassed(r ExecResult, expect string) bool {
	if r.ExitCode != 0 || r.Err != nil {
		return false
	}
	return expect == "" || strings.Contains(r.Output, expect)
}

// refreshProcessPath 用注册表里的机器级 + 用户级 PATH 重建当前进程 PATH。
// 只影响本进程及其子进程，不写注册表。
func refreshProcessPath(pm pathmgmt.Manager) {
	parts := make([]string, 0, 3)
	seen := make(map[string]bool)
	add := func(p string) {
		p = strings.TrimSpace(p)
		key := strings.ToLower(strings.TrimRight(strings.Trim(p, `"`), `\`))
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		parts = append(parts, p)
	}
	for _, hive := range []string{pathmgmt.HiveMachine, pathmgmt.HiveUser} {
		if e, err := pm.Current(hive); err == nil && strings.TrimSpace(e.Value) != "" {
			value := e.Value
			// 只有 REG_EXPAND_SZ 才展开 %VAR%；REG_SZ 里的百分号是字面量。
			if e.Kind == pathmgmt.KindExpandSZ {
				value = pathenv.ExpandVars(value)
			}
			for _, p := range strings.Split(value, ";") {
				add(p)
			}
		}
	}
	if cur := os.Getenv("PATH"); cur != "" {
		for _, p := range strings.Split(cur, ";") {
			add(p)
		}
	}
	_ = os.Setenv("PATH", strings.Join(parts, ";"))
}
