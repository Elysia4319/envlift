package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Elysia4319/envlift/internal/plan"
)

// RenderMarkdown 把报告文档渲染成人读的 markdown（PRD §7.2 的六节结构）。
//
// 它只是 Document 的另一种呈现：数据全部来自同一个 Document，不存在第二份口径。
func RenderMarkdown(d *Document) string {
	var b strings.Builder

	mode := "预演（未执行）"
	if d.Mode == "apply" {
		mode = "已执行"
	}

	b.WriteString("# envlift 还原报告\n\n")
	fmt.Fprintf(&b, "- 生成时间：%s\n", d.GeneratedAt)
	fmt.Fprintf(&b, "- 模式：%s\n", mode)
	fmt.Fprintf(&b, "- 报告格式：v%d\n", d.ReportSchema)
	if d.ConfigPath != "" {
		fmt.Fprintf(&b, "- 配置：`%s`\n", d.ConfigPath)
	}
	fmt.Fprintf(&b, "- 目标机：%s (build %s) / %s · %s@%s\n",
		d.Env.OS, d.Env.Build, d.Env.Arch, d.Env.Username, d.Env.Hostname)
	if d.Journal != "" {
		fmt.Fprintf(&b, "- 事务日志：`%s`\n", d.Journal)
	}
	fmt.Fprintf(&b, "- plan_hash：`%s`\n", d.PlanHash)

	b.WriteString("\n## 1. 概要\n\n")
	b.WriteString("| 项 | 数量 |\n|---|---|\n")
	fmt.Fprintf(&b, "| 条目（含 PATH） | %d |\n", d.Summary.Total)
	fmt.Fprintf(&b, "| 成功 | %d |\n", d.Summary.Succeeded)
	fmt.Fprintf(&b, "| 跳过（已完成/被覆盖/被决策跳过） | %d |\n", d.Summary.Skipped)
	fmt.Fprintf(&b, "| 因未决策被跳过 | %d |\n", d.Summary.BlockedNeedsDecision)
	fmt.Fprintf(&b, "| manual | %d |\n", d.Summary.Manual)
	fmt.Fprintf(&b, "| 失败 | %d |\n", d.Summary.Failed)
	fmt.Fprintf(&b, "| PATH 写入 | %d |\n", d.Summary.PathWrites)
	if d.Summary.AwaitingReboot {
		b.WriteString("| 等待重启 | 是 |\n")
	}

	if d.Decisions.ApplyMode != "" || len(d.Decisions.Actions) > 0 || len(d.Decisions.Blocked) > 0 {
		b.WriteString("\n**决策摘要**\n\n")
		if d.Decisions.ApplyMode != "" {
			fmt.Fprintf(&b, "- apply mode：`%s`\n", d.Decisions.ApplyMode)
		}
		if len(d.Decisions.Actions) > 0 {
			keys := make([]string, 0, len(d.Decisions.Actions))
			for k := range d.Decisions.Actions {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s × %d", k, d.Decisions.Actions[k]))
			}
			fmt.Fprintf(&b, "- 决策：%s\n", strings.Join(parts, " · "))
		}
		if len(d.Decisions.Blocked) > 0 {
			fmt.Fprintf(&b, "- 因未决策跳过（%d）：%s\n", len(d.Decisions.Blocked), strings.Join(d.Decisions.Blocked, "、"))
		}
	}

	b.WriteString("\n## 2. 逐项结果\n\n")
	b.WriteString("| # | 类型 | 名称 | 期望 | 执行前 | 执行后 | 层级 | 安装目录 | 状态 | 退出码 | 校验 | 说明 |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for i, it := range d.Items {
		exit := "-"
		if it.ExitCode != nil {
			exit = fmt.Sprintf("%d", *it.ExitCode)
		}
		note := it.FailureReason
		if note == "" {
			note = it.Decision
		}
		if note == "" {
			note = it.Reason
		}
		if it.Reinstall {
			note = "重装；" + note
		}
		after := orDash(it.ActualVersion)
		if it.VersionUnparsed {
			after = "解析失败"
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			i+1, it.Kind, esc(it.Name), orDash(it.ExpectedVersion), orDash(it.BeforeVersion), after,
			orDash(it.Tier), orDash(it.InstallDir), it.Status, exit, orDash(it.VerifyResult), esc(note))
	}
	if len(d.Items) == 0 {
		b.WriteString("| - | - | （没有软件/运行时条目） | | | | | | | | | |\n")
	}

	b.WriteString("\n## 3. PATH 变更\n\n")
	if len(d.PathChanges) == 0 {
		b.WriteString("（无）\n")
	} else {
		b.WriteString("符号：`+` 新增 · `~` 用户名重写 · `?` 待确认 · `=` 已存在 · `!` 失效\n\n")
		b.WriteString("| 段 | 标记 | 值 | 决策 | 说明 |\n|---|---|---|---|---|\n")
		for _, ch := range d.PathChanges {
			fmt.Fprintf(&b, "| %s | `%s` | `%s` | %s | %s |\n",
				ch.Section, ch.Mark, esc(ch.Value), orDash(ch.Decision), esc(ch.Reason))
		}
	}

	b.WriteString("\n## 4. 待人工处理\n\n")
	manual := 0
	for _, it := range d.Items {
		if it.Status != plan.StatusManual {
			continue
		}
		manual++
		if hint := strings.TrimSpace(it.ManualHint); hint != "" {
			fmt.Fprintf(&b, "- **%s**（%s）：%s\n", it.Name, orDash(it.ExpectedVersion), esc(hint))
		} else {
			fmt.Fprintf(&b, "- **%s**（%s）：需按官方方式手动安装\n", it.Name, orDash(it.ExpectedVersion))
		}
	}
	if manual == 0 {
		b.WriteString("（无）\n")
	}

	b.WriteString("\n## 5. 风险与提示\n\n")
	risks := 0
	for _, w := range d.Warnings {
		fmt.Fprintf(&b, "- %s\n", w)
		risks++
	}
	for _, it := range d.Items {
		if it.Status == "failed" {
			fmt.Fprintf(&b, "- **%s** 执行失败：%s\n", it.Name, esc(it.FailureReason))
			risks++
		}
		if it.NeedsAdmin && it.Status != "succeeded" {
			fmt.Fprintf(&b, "- %s：需要管理员运行\n", it.Name)
			risks++
		}
		if it.ExpectReboot {
			fmt.Fprintf(&b, "- %s：完成后需重启\n", it.Name)
			risks++
		}
		if it.InstallDirApplied && it.VerifyCommandPresent {
			fmt.Fprintf(&b, "- %s 指定了安装目录，verify 命令可能仍指向旧路径，请在 VM 上确认\n", it.Name)
			risks++
		}
	}
	for _, ch := range d.PathChanges {
		if ch.Mark == plan.MarkPending || ch.Mark == plan.MarkInvalid {
			fmt.Fprintf(&b, "- PATH[%s] `%s`：%s\n", ch.Section, ch.Value, esc(ch.Reason))
			risks++
		}
	}
	if risks == 0 {
		b.WriteString("（无）\n")
	}

	b.WriteString("\n## 6. 后续建议\n\n")
	if d.Mode == "dry-run" {
		b.WriteString("- 这是预演结果。确认无误后执行 `envlift restore --apply`。\n")
	} else if d.Summary.AwaitingReboot {
		b.WriteString("- 执行停在「需要重启」。重启后重跑一次：已完成的项会被幂等检查跳过。\n")
	} else {
		b.WriteString("- 执行已完成。PATH 如有变更，请重开终端；回滚用 `envlift restore --rollback`。\n")
	}
	if d.Summary.BlockedNeedsDecision > 0 {
		fmt.Fprintf(&b, "- 有 %d 项因未决策被跳过。在 GUI 里逐项决策，或用 `--decisions <file>` 补上后重跑。\n",
			d.Summary.BlockedNeedsDecision)
	}
	if d.Summary.Failed > 0 {
		b.WriteString("- 有失败项：按上面的说明处理后再重跑，做过的部分不会重做。\n")
	}
	return b.String()
}

func esc(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.ReplaceAll(s, "\n", " ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
