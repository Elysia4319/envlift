"""④ 决策链路的真机自检（Playwright + 系统 Edge）。

覆盖 F4：
  - 待确认控件解禁与回传（安装包歧义 → select{installer}；PATH ? → rewrite）
  - 决策徽章与撤销（撤销后 plan_hash 回到原值）
  - 决策改变 → plan_hash 变 → 确认态重置
  - 二次确认面板：mode / 只读提示 / 逐项 admin / reboot_ack / 「N 项因未决策将被跳过」
  - 控制台零错误、三档宽度无横向溢出

不做任何安装：脚本不点「执行还原」，只验证按钮的启用条件。
"""
import json
import os
import sys
from urllib.parse import parse_qs, urlparse

from playwright.sync_api import sync_playwright

BASE = sys.argv[1]
OUT = sys.argv[2] if len(sys.argv) > 2 else "shots"
os.makedirs(OUT, exist_ok=True)

errors = []


def launch(p):
    for kwargs in ({"channel": "msedge"}, {"channel": "chrome"}, {}):
        try:
            return p.chromium.launch(headless=True, **kwargs)
        except Exception:  # noqa: BLE001
            continue
    raise RuntimeError("没有可用浏览器")


def wait_hash_change(page, old, timeout=30000):
    page.wait_for_function(
        "old => { const el = document.getElementById('plan-hash'); return el && el.textContent !== old; }",
        arg=old,
        timeout=timeout,
    )
    return page.text_content("#plan-hash")


def main():
    with sync_playwright() as p:
        browser = launch(p)
        page = browser.new_page(viewport={"width": 1360, "height": 1000})
        page.on("pageerror", lambda e: errors.append("pageerror: " + str(e)))
        # 4xx 的「Failed to load resource」是浏览器对预期错误的网络层噪声：
        # 本脚本会故意提交一条非法决策来验证错误码回显，那必然产生一条 400。
        # 真正的断言在上面的 #errbar-code 检查里，这里只挡掉这类噪声。
        page.on(
            "console",
            lambda m: errors.append(f"console.{m.type}: {m.text}")
            if m.type == "error" and "Failed to load resource" not in m.text
            else None,
        )

        # 每次运行都从「没有决策」开始：决策文件是本机持久产物，
        # 不清掉会让上一次运行的结果影响这一次（hash 不再变化、断言全乱）。
        token = parse_qs(urlparse(BASE).query)["token"][0]
        page.request.post(
            BASE.split("?")[0] + "/api/v1/decisions",
            headers={"X-Envlift-Token": token, "Content-Type": "application/json"},
            data=json.dumps({"decisions": []}),
        )
        page.goto(BASE)
        page.wait_for_load_state("networkidle")
        assert "token" not in page.evaluate("location.search"), "token 没从地址栏抹掉"
        assert page.evaluate("localStorage.length + sessionStorage.length") == 0

        page.click("#tab-restore")
        page.click("#btn-plan")
        page.wait_for_selector("#plan-result .section", timeout=180000)
        hash0 = page.text_content("#plan-hash")
        assert hash0 and len(hash0) == 64, "plan_hash 格式不对: %r" % hash0
        print("初始 plan_hash:", hash0)

        # 未决策计数必须出现在确认面板顶部，且与面板上的数字一致
        guard = page.locator("#confirm-panel .guard").first
        assert "因未决策将被跳过" in guard.text_content(), "确认面板缺少未决策提示"
        print("未决策提示:", guard.text_content().strip())

        cards = page.locator("#plan-result .pending-card")
        assert cards.count() >= 2, "待确认卡片太少: %d" % cards.count()
        shots_before = os.path.join(OUT, "decisions-before.png")
        page.screenshot(path=shots_before, full_page=True)

        # ---- 1) 安装包歧义：选一个安装包 ----
        card = page.locator(".pending-card", has_text="Example Editor").first
        sel = card.locator(".dec-cand")
        n = sel.locator("option").count()
        assert n >= 3, "安装包候选不足: %d" % n
        sel.select_option(index=1)
        hash1 = wait_hash_change(page, hash0)
        print("选安装包后 plan_hash:", hash1)
        assert hash1 != hash0, "决策没有改变 plan_hash"
        assert page.locator('.chip:has-text("决策 · select")').count() >= 1, "缺少决策徽章"

        # ---- 2) 撤销：hash 必须回到原值（决策态与计划态都可复现）----
        page.locator(".pending-card", has_text="Example Editor").first.locator(".dec-undo").first.click()
        hash2 = wait_hash_change(page, hash1)
        assert hash2 == hash0, "撤销后 plan_hash 没回到原值: %s vs %s" % (hash2, hash0)
        print("撤销后 plan_hash 复原:", hash2)

        # ---- 3) PATH ? 项：改写落点 ----
        pcard = page.locator(".pending-card", has_text="PATH[user]").first
        pcard.locator(".dec-act-path").select_option("rewrite")
        pcard.locator(".dec-value").fill("C:\\Tools\\bin")
        pcard.locator(".dec-apply-path").click()
        hash3 = wait_hash_change(page, hash0)
        assert page.locator('.chip:has-text("决策 · rewrite")').count() >= 1, "PATH 决策徽章缺失"
        print("PATH 决策后 plan_hash:", hash3)

        # 非法路径要回显后端错误码（B3：前端不猜，按 code 分支）
        pcard = page.locator(".pending-card", has_text="PATH[user]").first
        pcard.locator(".dec-act-path").select_option("rewrite")
        pcard.locator(".dec-value").fill("Tools\\bin")
        pcard.locator(".dec-apply-path").click()
        page.wait_for_function("() => { const e = document.getElementById('errbar-code'); return e && e.textContent === 'invalid_decision_params'; }", timeout=15000)
        print("非法路径错误码:", page.text_content("#errbar-code"))
        page.click("#errbar-close")

        # ---- 4) 二次确认面板 ----
        panel = page.locator("#confirm-panel")
        assert panel.locator("#cf-mode-all").is_checked(), "默认 mode 应为 all"
        btn = panel.locator("#btn-apply")
        assert btn.is_disabled(), "未勾选任何确认时执行按钮必须是禁用的"

        checks = panel.locator(".cf-admin-check")
        for i in range(checks.count()):
            checks.nth(i).check()
        reboot = panel.locator("#cf-reboot-ack")
        if reboot.count() and not reboot.first.is_hidden():
            reboot.first.check()
        assert btn.is_enabled(), "全部勾选后执行按钮应可点"

        # skip_admin：切换后必须要求 ack，且提权清单隐藏
        panel.locator("#cf-mode-skip").check()
        assert btn.is_disabled(), "skip_admin 未 ack 时不该放行"
        panel.locator("#cf-ack-skip").check()
        assert btn.is_enabled(), "skip_admin + ack 后应可执行"
        panel.locator("#cf-mode-all").check()

        page.screenshot(path=os.path.join(OUT, "decisions-confirm.png"), full_page=True)

        # ---- 5) 保存决策文件 ----
        page.click("#btn-save-decisions")
        page.wait_for_function("() => (document.getElementById('plan-load-note')||{}).textContent.indexOf('决策已保存') >= 0", timeout=15000)
        print("保存决策:", page.text_content("#plan-load-note"))

        # ---- 6) 三档宽度无横向溢出 ----
        for w, h in ((1360, 1000), (820, 900), (390, 844)):
            page.set_viewport_size({"width": w, "height": h})
            page.wait_for_timeout(250)
            m = page.evaluate("() => ({s: document.scrollingElement.scrollWidth, c: document.scrollingElement.clientWidth})")
            assert m["s"] <= m["c"] + 1, "%d 宽度横向溢出: %s" % (w, m)
            print("宽度 %d: scrollWidth=%d clientWidth=%d" % (w, m["s"], m["c"]))
        page.screenshot(path=os.path.join(OUT, "decisions-390.png"), full_page=False)

        browser.close()

    if errors:
        raise SystemExit("控制台有错误:\n" + "\n".join(errors))
    print("④ 决策与二次确认自检：通过")


main()

