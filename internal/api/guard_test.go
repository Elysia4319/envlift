package api

import (
	"strings"
	"testing"

	"github.com/Elysia4319/envlift/internal/plan"
)

// D15 的防线段用纯函数测，好处是能穷举「空载荷 / 半载荷 / 显式声明」各种组合，
// 不用起端口、不碰系统。

func adminPlan(items ...plan.Item) *plan.Plan {
	return &plan.Plan{
		App: "envlift", Kind: "restore-plan", Schema: 1,
		Target: plan.Target{WingetOK: true, NetworkOK: true},
		Items:  items,
	}
}

func planned(name string, needsAdmin, expectReboot bool) plan.Item {
	return plan.Item{
		Kind: "software", Name: name, Status: plan.StatusPlanned, Tier: plan.TierWinget,
		Command: "winget install --id " + name, NeedsAdmin: needsAdmin, ExpectReboot: expectReboot,
	}
}

func TestGuardRejectsMissingMode(t *testing.T) {
	p := adminPlan(planned("Java", true, false))
	for _, mode := range []string{"", "ALL", "skip", "yes"} {
		e := ValidateApplyLoad(p, Confirmations{Mode: mode})
		if e == nil {
			t.Fatalf("mode=%q 应被拒绝（不默认任何行为）", mode)
		}
		if e.Code != CodeInvalidMode {
			t.Errorf("mode=%q 错误码 = %q，期望 %s", mode, e.Code, CodeInvalidMode)
		}
	}
	if e := ValidateApplyLoad(adminPlan(planned("Git", false, false)), Confirmations{Mode: ConfirmModeAll}); e != nil {
		t.Errorf("mode=all 不应被判为非法: %+v", e)
	}
}

// 这就是那次真事故的形状：confirmations 是空对象，计划里有 needs_admin 项。
func TestGuardRejectsEmptyConfirmationsWithAdminItems(t *testing.T) {
	p := adminPlan(planned("Git", false, false), planned("Java", true, false))
	e := ValidateApplyLoad(p, Confirmations{Mode: ConfirmModeAll})
	if e == nil {
		t.Fatal("空 admin_confirmed + 有 needs_admin 项应拒绝")
	}
	if e.Code != CodeAdminMissing {
		t.Errorf("错误码 = %q，期望 %s", e.Code, CodeAdminMissing)
	}
	if !contains(e.Message, "Java") {
		t.Errorf("错误消息应含缺失项名: %q", e.Message)
	}
	d, _ := e.Details.(map[string]any)
	missing, _ := d["missing"].([]AdminRequirement)
	if len(missing) != 1 || missing[0].Name != "Java" {
		t.Errorf("details.missing 应只列 Java: %+v", d["missing"])
	}
}

func TestGuardRejectsPartialAdminConfirmation(t *testing.T) {
	p := adminPlan(planned("A-app", true, false), planned("B-app", true, false), planned("C-app", false, false))
	e := ValidateApplyLoad(p, Confirmations{Mode: ConfirmModeAll, AdminConfirmed: []string{"A-app"}})
	if e == nil {
		t.Fatal("只确认一部分应拒绝")
	}
	d, _ := e.Details.(map[string]any)
	missing, _ := d["missing"].([]AdminRequirement)
	if len(missing) != 1 || missing[0].Name != "B-app" {
		t.Fatalf("缺少项应只有 B-app: %+v", missing)
	}
	if !contains(e.Message, "B-app") || contains(e.Message, "A-app") {
		t.Errorf("消息应点名 B-app 且不点名已确认的 A-app: %q", e.Message)
	}
}

func TestGuardAcceptsFullAdminConfirmation(t *testing.T) {
	p := adminPlan(planned("A-app", true, false))
	if e := ValidateApplyLoad(p, Confirmations{Mode: ConfirmModeAll, AdminConfirmed: []string{"A-app"}}); e != nil {
		t.Errorf("全量确认应放行: %+v", e)
	}
}

func TestGuardSkipAdminRequiresAck(t *testing.T) {
	p := adminPlan(planned("Java", true, false))
	e := ValidateApplyLoad(p, Confirmations{Mode: ConfirmModeSkipAdmin})
	if e == nil {
		t.Fatal("skip_admin 无 ack 应拒绝")
	}
	if e.Code != CodeSkippedAdminAckMissing {
		t.Errorf("错误码 = %q", e.Code)
	}
	if !contains(e.Message, "Java") {
		t.Errorf("消息应列出将被阻止的项: %q", e.Message)
	}
	if e := ValidateApplyLoad(p, Confirmations{Mode: ConfirmModeSkipAdmin, AckSkippedAdmin: true}); e != nil {
		t.Errorf("skip_admin + ack 应放行: %+v", e)
	}
}

func TestGuardSkipAdminRejectsPartialAnyway(t *testing.T) {
	// skip_admin 是「全都不跑提权项」，不是「跑我确认的那部分」；
	// 传了 admin_confirmed 也不改变 blocked 语义，放行取决于 ack。
	p := adminPlan(planned("Java", true, false))
	e := ValidateApplyLoad(p, Confirmations{
		Mode: ConfirmModeSkipAdmin, AdminConfirmed: []string{"Java"},
	})
	if e == nil {
		t.Fatal("skip_admin 缺 ack 仍应拒绝")
	}
	if e.Code != CodeSkippedAdminAckMissing {
		t.Errorf("错误码 = %q", e.Code)
	}
}

func TestGuardRebootAckRequired(t *testing.T) {
	p := adminPlan(planned("Driver", false, true))
	e := ValidateApplyLoad(p, Confirmations{Mode: ConfirmModeAll})
	if e == nil {
		t.Fatal("有 expect_reboot 项而无 reboot_ack 应拒绝")
	}
	if e.Code != CodeRebootAckRequired {
		t.Errorf("错误码 = %q", e.Code)
	}
	if !contains(e.Message, "Driver") {
		t.Errorf("消息应含项名: %q", e.Message)
	}
	if e := ValidateApplyLoad(p, Confirmations{Mode: ConfirmModeAll, RebootAck: true}); e != nil {
		t.Errorf("reboot_ack=true 应放行: %+v", e)
	}
}

// skip_admin 下提权项本来就不会执行，不该被它的重启要求卡死。
func TestGuardSkipAdminIgnoresRebootOfBlockedItems(t *testing.T) {
	p := adminPlan(planned("Big", true, true))
	c := Confirmations{Mode: ConfirmModeSkipAdmin, AckSkippedAdmin: true}
	if e := ValidateApplyLoad(p, c); e != nil {
		t.Errorf("被阻止的提权项不应要求 reboot_ack: %+v", e)
	}

	// 非提权项的重启要求仍然算数。
	p2 := adminPlan(planned("Big", true, true), planned("Small", false, true))
	if e := ValidateApplyLoad(p2, c); e == nil || e.Code != CodeRebootAckRequired {
		t.Errorf("非提权重启项仍应要求 ack: %+v", e)
	}
}

// 系统级 PATH 写入不在 items 里，若不计入就会出现「needs_admin=0 但仍要写 HKLM」的缝。
func TestGuardCountsSystemPathAsAdminWork(t *testing.T) {
	p := adminPlan()
	p.Path = []plan.PathChange{
		{Section: "user", Mark: plan.MarkAdd, Value: `C:\Tools\bin`},
		{Section: "machine", Mark: plan.MarkAdd, Value: `D:\SDK\bin`},
	}
	e := ValidateApplyLoad(p, Confirmations{Mode: ConfirmModeAll})
	if e == nil {
		t.Fatal("有系统级 PATH 变更时应要求逐项确认")
	}
	d, _ := e.Details.(map[string]any)
	missing, _ := d["missing"].([]AdminRequirement)
	if len(missing) != 1 || missing[0].Kind != "path" || missing[0].Name != pathReqPrefix+`D:\SDK\bin` {
		t.Fatalf("缺失项应只有系统级 PATH 那一条: %+v", missing)
	}

	ok := Confirmations{Mode: ConfirmModeAll, AdminConfirmed: []string{pathReqPrefix + `D:\SDK\bin`}}
	if e := ValidateApplyLoad(p, ok); e != nil {
		t.Errorf("确认系统级 PATH 后应放行: %+v", e)
	}
}

func TestGuardIgnoresNonPlannedItems(t *testing.T) {
	p := adminPlan(
		plan.Item{Kind: "software", Name: "ManualApp", Status: plan.StatusManual, Tier: plan.TierManual, NeedsAdmin: true},
		plan.Item{Kind: "software", Name: "SkippedApp", Status: plan.StatusSkipped, NeedsAdmin: true, ExpectReboot: true},
	)
	if e := ValidateApplyLoad(p, Confirmations{Mode: ConfirmModeAll}); e != nil {
		t.Errorf("manual / skipped 项不执行，不应要求确认: %+v", e)
	}
}

func TestRebootRequirementsOnlyPlanned(t *testing.T) {
	p := adminPlan(
		plan.Item{Kind: "software", Name: "M", Status: plan.StatusManual, ExpectReboot: true},
		planned("R", false, true),
	)
	got := rebootRequirements(p, false)
	if len(got) != 1 || got[0] != "R" {
		t.Errorf("reboot 集合 = %#v，期望只有 R", got)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
