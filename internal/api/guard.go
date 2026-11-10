package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Elysia4319/envlift/internal/plan"
)

// apply 的确认模式（D15）。**没有默认值**：缺失即 400。
//
// 这条防线是补一个真实踩过的缝：`confirmations` 为空对象时，旧实现把它当成
// 「用户可以跑」，于是 apply 直接执行了需要提权的安装命令。现在把「要不要跑
// 提权项」变成必须显式声明的三态之一，空载荷再也无法被解释成「可以跑」。
const (
	// ConfirmModeAll 表示全部执行：计划里每个 needs_admin 项都必须出现在
	// admin_confirmed 里，缺一个就拒绝启动任务。
	ConfirmModeAll = "all"
	// ConfirmModeSkipAdmin 表示只跑非提权部分：admin_confirmed 允许为空，
	// 但必须同时传 ack_skipped_admin=true，执行时提权项标 blocked。
	ConfirmModeSkipAdmin = "skip_admin"
)

// AdminRequirement 是一项「必须在载荷里经人确认」的提权操作。
//
// 除计划条目外，**系统级 PATH 写入也算提权操作** —— 它不在 items 里，
// 若只统计 items，就会出现「needs_admin 计数为 0 但执行时仍要写 HKLM」的缝。
type AdminRequirement struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // software | runtime | path
	Command string `json:"command,omitempty"`
}

const pathReqPrefix = "PATH[system] "

// adminRequirements 列出计划里需要提权的项，按执行顺序。
func adminRequirements(p *plan.Plan) []AdminRequirement {
	if p == nil {
		return nil
	}
	var out []AdminRequirement
	for _, it := range p.Items {
		if it.Status != plan.StatusPlanned || it.Command == "" || !it.NeedsAdmin {
			continue
		}
		out = append(out, AdminRequirement{Name: it.Name, Kind: it.Kind, Command: it.Command})
	}
	for _, ch := range p.Path {
		if ch.Section != "machine" || (ch.Mark != plan.MarkAdd && ch.Mark != plan.MarkRewrite) {
			continue
		}
		out = append(out, AdminRequirement{Name: pathReqPrefix + ch.Value, Kind: "path"})
	}
	return out
}

// rebootRequirements 列出会触发重启、因而必须先确认的项名。
//
// skip_admin 模式下提权项本来就会被阻止、不会执行，所以不计入重启确认，
// 否则「只跑非提权部分」会被一个根本没机会执行的项卡死。
func rebootRequirements(p *plan.Plan, skipAdmin bool) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, it := range p.Items {
		if it.Status != plan.StatusPlanned || it.Command == "" || !it.ExpectReboot {
			continue
		}
		if skipAdmin && it.NeedsAdmin {
			continue
		}
		out = append(out, it.Name)
	}
	return out
}

// ValidateMode 只校验 mode 取值，不依赖计划。
//
// 单独拆出来是因为校验顺序（A3）要求 `mode` 在 plan_hash 之前判定，
// 而 mode 是否合法与计划内容无关，不应该被配置读取失败挡住。
func ValidateMode(mode string) *APIError {
	switch mode {
	case ConfirmModeAll, ConfirmModeSkipAdmin:
		return nil
	}
	return &APIError{
		Code: CodeInvalidMode,
		Message: fmt.Sprintf("confirmations.mode 必填，取值只能是 %q 或 %q，实际为 %q",
			ConfirmModeAll, ConfirmModeSkipAdmin, mode),
		Details: map[string]any{"got": mode, "allowed": []string{ConfirmModeAll, ConfirmModeSkipAdmin}},
	}
}

// ValidateApplyLoad 校验 apply 载荷（D15）。返回 nil 表示可以启动任务。
//
// 纯函数：不碰系统、不发请求，便于穷举测试。
func ValidateApplyLoad(p *plan.Plan, c Confirmations) *APIError {
	if e := ValidateMode(c.Mode); e != nil {
		return e
	}

	reqs := adminRequirements(p)
	skipAdmin := c.Mode == ConfirmModeSkipAdmin

	if skipAdmin {
		if !c.AckSkippedAdmin {
			return &APIError{
				Code: CodeSkippedAdminAckMissing,
				Message: fmt.Sprintf("mode=%s 必须同时传 ack_skipped_admin=true；本次会被阻止的、需要管理员运行的项 %d 个%s",
					ConfirmModeSkipAdmin, len(reqs), joinNames(reqs)),
				Details: map[string]any{"mode": c.Mode, "blocked_items": reqs},
			}
		}
	} else {
		var missing []AdminRequirement
		for _, r := range reqs {
			if !c.HasAdmin(r.Name) {
				missing = append(missing, r)
			}
		}
		if len(missing) > 0 {
			return &APIError{
				Code: CodeAdminMissing,
				Message: fmt.Sprintf("计划中有 %d 项需要管理员运行但未逐项确认%s",
					len(missing), joinNames(missing)),
				Details: map[string]any{"mode": c.Mode, "missing": missing, "required": reqs},
			}
		}
	}

	if reboots := rebootRequirements(p, skipAdmin); len(reboots) > 0 && !c.RebootAck {
		return &APIError{
			Code: CodeRebootAckRequired,
			Message: fmt.Sprintf("计划中有 %d 项执行后会重启，必须传 reboot_ack=true 才能启动%s",
				len(reboots), joinNames2(reboots)),
			Details: map[string]any{"items": reboots},
		}
	}
	return nil
}

func joinNames(rs []AdminRequirement) string {
	if len(rs) == 0 {
		return ""
	}
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		names = append(names, r.Name)
	}
	return "：" + strings.Join(names, "、")
}

func joinNames2(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return "：" + strings.Join(names, "、")
}

// writeGuardErr 把校验失败转成 400 响应。
func writeGuardErr(w http.ResponseWriter, e *APIError) {
	writeJSON(w, http.StatusBadRequest, errEnvelope{Error: *e})
}
