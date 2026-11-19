"""④ 决策链路的真机自检（Playwright + 系统 Edge）。

覆盖 F4：
  - 待确认控件解禁与回传（安装包歧义 → select{installer}；PATH ? → rewrite）
  - 决策徽章与撤销（撤销后 plan_hash 回到原值）
  - 决策改变 → plan_hash 变 → 确认态重置
  - 二次确认面板：mode / 只读提示 / 逐项 admin / reboot_ack / 「N 项因未决策将被跳过」
  - 全选 / 清空 / 逐项取消 / 命令展开，以及 1440 / 820 / 390 的行布局
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

# 真实 fixture 只有 3 个管理员项，覆盖不到长列表。这里把 /api/v1/plan 的响应
# 扩成 60 条 needs_admin 项，仅用于确认面板的布局与批量交互回归。
LONG_ADMIN_COUNT = 60
PLAN_PATTERN = "**/api/v1/plan"


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


def expand_plan(page):
    """把计划响应扩成 60 条管理员项；真实字段与顺序不变。"""

    def handler(route):
        resp = route.fetch()
        data = resp.json()
        plan = data.get("plan") or {}
        items = plan.get("items") or []
        seed = next((it for it in items if it.get("needs_admin")), None)
        if not seed:
            route.fulfill(response=resp)
            return
        made = []
        for i in range(LONG_ADMIN_COUNT):
            it = dict(seed)
            it["target_id"] = "ui-long-%02d" % i
            it["name"] = "%s #%02d" % (seed.get("name") or "Package", i + 1)
            it["status"] = "planned"
            it["needs_admin"] = True
            it["expect_reboot"] = i == 0
            # 真实命令可能很短，撑不到省略号；补一段无害参数，专测单行省略/展开。
            base = it.get("command") or ""
            it["command"] = base + " --ui-check-ellipsis --no-op --trace-level verbose --timeout 600"
            made.append(it)
        others = [it for it in items if not it.get("needs_admin")]
        plan["items"] = made + others
        plan["summary"] = dict(plan.get("summary") or {})
        plan["summary"]["winget"] = LONG_ADMIN_COUNT
        plan["summary"]["blocked_needs_decision"] = 0
        plan["path_changes"] = []
        data["plan"] = plan
        route.fulfill(response=resp, json=data)

    page.route(PLAN_PATTERN, handler)


def row_layout(page):
    return page.evaluate(
        """() => {
            const rows = [...document.querySelectorAll('.cf-admin-row')];
            const r = rows.map(x => x.getBoundingClientRect());
            return {
                count: rows.length,
                lefts: [...new Set(r.map(x => Math.round(x.left)))],
                widths: [...new Set(r.map(x => Math.round(x.width)))],
                flexDir: rows.length ? getComputedStyle(rows[0]).flexDirection : '',
                scrollW: document.scrollingElement.scrollWidth,
                clientW: document.scrollingElement.clientWidth,
            };
        }"""
    )


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

        # 未决策提示只在存在未决策项时出现；fixture 的安装包目录会改变这一点。
        guard = page.locator("#confirm-panel .guard").first
        if guard.count():
            assert "因未决策将被跳过" in guard.text_content(), "确认面板未决策提示内容异常"
            print("未决策提示:", guard.text_content().strip())

        cards = page.locator("#plan-result .pending-card")
        print("待确认卡片数:", cards.count())
        shots_before = os.path.join(OUT, "decisions-before.png")
        page.screenshot(path=shots_before, full_page=True)

        # ---- 1) 安装包歧义：选一个安装包（没有歧义项时跳过）----
        card = page.locator(".pending-card", has_text="Example Editor").first
        sel = card.locator(".dec-cand")
        n = sel.locator("option").count() if sel.count() else 0
        if n >= 3:
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
        else:
            print("跳过安装包歧义：当前 fixture 无候选（options=%d）" % n)

        # ---- 3) PATH ? 项：改写落点（没有待确认 PATH 时跳过）----
        pcard = page.locator(".pending-card", has_text="PATH[user]").first
        if pcard.count():
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
            # 真实计划要重新生成一遍，慢机器上 15s 不够；按页面自己的错误条状态等。
            page.wait_for_function(
                "() => { const e = document.getElementById('errbar-code'); return e && e.textContent === 'invalid_decision_params'; }",
                timeout=90000,
            )
            print("非法路径错误码:", page.text_content("#errbar-code"))
            page.click("#errbar-close")
        else:
            print("跳过 PATH 改写：当前 fixture 无 PATH[user] 待确认项")

        # ---- 4) 二次确认面板 ----
        expand_plan(page)
        page.click("#btn-plan")
        # 面板已经存在，wait_for_selector 会立刻返回；必须等长列表真正渲染出来。
        page.wait_for_function(
            "n => document.querySelectorAll('.cf-admin-check').length === n",
            arg=LONG_ADMIN_COUNT,
            timeout=180000,
        )
        page.wait_for_timeout(300)

        panel = page.locator("#confirm-panel")
        assert panel.locator("#cf-mode-all").is_checked(), "默认 mode 应为 all"
        btn = panel.locator("#btn-apply")
        assert btn.is_disabled(), "未勾选任何确认时执行按钮必须是禁用的"

        checks = panel.locator(".cf-admin-check")
        total = checks.count()
        assert total == LONG_ADMIN_COUNT, "管理员项数量异常: %d" % total
        count = panel.locator("#cf-admin-count")
        assert count.text_content().strip() == "已选 0/%d" % total, "默认不该预勾选"

        # 全选：只改逐项确认，按钮仍要等 reboot ack。
        panel.locator("#cf-admin-all").click()
        assert count.text_content().strip() == "已选 %d/%d" % (total, total), "全选后计数不同步"
        assert checks.evaluate_all("els => els.every(x => x.checked)"), "全选没有勾满"
        assert btn.is_disabled(), "会重启项未确认时仍应禁用"
        reboot = panel.locator("#cf-reboot-ack")
        if reboot.count() and not reboot.first.is_hidden():
            reboot.first.check()
        assert btn.is_enabled(), "全选 + reboot ack 后执行按钮应可点"

        # 逐项取消一项：计数与按钮同步。
        checks.nth(7).uncheck()
        assert count.text_content().strip() == "已选 %d/%d" % (total - 1, total), "取消一项后计数没跟上"
        assert btn.is_disabled(), "缺一项时执行按钮必须禁用"
        checks.nth(7).check()
        assert count.text_content().strip() == "已选 %d/%d" % (total, total)
        assert btn.is_enabled()

        # 清空：回到未勾选，按钮禁用。
        panel.locator("#cf-admin-none").click()
        assert count.text_content().strip() == "已选 0/%d" % total, "清空后计数不对"
        assert checks.evaluate_all("els => els.every(x => !x.checked)"), "清空没有全部取消"
        assert btn.is_disabled(), "清空后执行按钮必须禁用"

        # 命令展开 / 收起：默认单行省略，展开后完整可见。
        cmd = panel.locator(".cf-admin-cmd").first
        row = panel.locator(".cf-admin-row").first
        assert cmd.evaluate("e => e.scrollWidth > e.clientWidth"), "长命令默认应单行省略"
        cmd.click()
        assert "is-open" in (row.get_attribute("class") or ""), "点击命令后该行未展开"
        assert cmd.evaluate("e => e.scrollWidth <= e.clientWidth + 1"), "展开后命令仍被截断"
        assert cmd.get_attribute("title"), "命令缺少 title 兜底"
        cmd.click()
        assert "is-open" not in (row.get_attribute("class") or ""), "再次点击未收起"

        # 1440 / 820：单列；390：上下排列。都不许横向溢出。
        for w, h, mode in ((1440, 900, "row"), (820, 900, "row"), (390, 844, "column")):
            page.set_viewport_size({"width": w, "height": h})
            page.wait_for_timeout(250)
            lay = row_layout(page)
            assert lay["scrollW"] <= lay["clientW"] + 1, "%d 宽度整页横向溢出: %s" % (w, lay)
            assert len(lay["lefts"]) == 1, "%d 宽度管理员行左边不唯一: %s" % (w, lay["lefts"])
            assert len(lay["widths"]) == 1, "%d 宽度管理员行宽度不唯一: %s" % (w, lay["widths"])
            assert lay["flexDir"] == mode, "%d 宽度行方向应为 %s，实际 %s" % (w, mode, lay["flexDir"])
            print("宽度 %d 行布局: %s" % (w, lay))
        page.screenshot(path=os.path.join(OUT, "decisions-390.png"), full_page=False)
        page.set_viewport_size({"width": 1440, "height": 900})
        page.wait_for_timeout(200)

        # 真实还原页（不拦截任何响应）回归：面板能正常生成，底部操作栏存在且无横向溢出。
        real = browser.new_page(viewport={"width": 1440, "height": 900})
        real.on("pageerror", lambda e: errors.append("pageerror(real): " + str(e)))
        real.goto(BASE)
        real.wait_for_load_state("networkidle")
        real.click("#tab-restore")
        real.click("#btn-plan")
        real.wait_for_selector("#confirm-panel", timeout=180000)
        real.wait_for_timeout(300)
        assert real.locator("#confirm-panel .cf-foot").count() == 1, "真实页面缺少底部操作栏"
        m = real.evaluate("() => ({s: document.scrollingElement.scrollWidth, c: document.scrollingElement.clientWidth})")
        assert m["s"] <= m["c"] + 1, "真实页面横向溢出: %s" % m
        print("真实还原页：面板生成、无横向溢出")
        real.screenshot(path=os.path.join(OUT, "decisions-real-1440.png"), full_page=False)
        real.close()

        # sticky 判定需要面板高于视口：真实 fixture 只有 2–3 个管理员项，
        # 因此用同一真实服务的长列表响应把面板撑高，再看底栏是否贴住视口底。
        # 视口要比面板矮，否则 320px 列表上限会让面板整体装得下，sticky 无从判定。
        sticky = browser.new_page(viewport={"width": 1440, "height": 600})
        sticky.on("pageerror", lambda e: errors.append("pageerror(sticky): " + str(e)))
        expand_plan(sticky)
        sticky.goto(BASE)
        sticky.wait_for_load_state("networkidle")
        sticky.click("#tab-restore")
        sticky.click("#btn-plan")
        sticky.wait_for_function(
            "n => document.querySelectorAll('.cf-admin-check').length === n",
            arg=LONG_ADMIN_COUNT,
            timeout=180000,
        )
        sticky.wait_for_timeout(300)
        foot = sticky.locator("#confirm-panel .cf-foot")
        assert foot.evaluate("e => getComputedStyle(e).position") == "sticky", ".cf-foot 未生效 sticky"
        sticky.evaluate(
            """() => { const s = document.querySelector('#confirm-panel');
                       window.scrollTo(0, s.getBoundingClientRect().top + window.scrollY); }"""
        )
        sticky.wait_for_timeout(350)
        stick = sticky.evaluate(
            """() => { const s = document.querySelector('#confirm-panel').getBoundingClientRect();
                       const f = document.querySelector('#confirm-panel .cf-foot').getBoundingClientRect();
                       return {panelBottom: Math.round(s.bottom), footBottom: Math.round(f.bottom),
                               vh: window.innerHeight}; }"""
        )
        assert stick["panelBottom"] > stick["vh"], "面板没有高过视口，sticky 无法判定: %s" % stick
        assert abs(stick["footBottom"] - stick["vh"]) <= 1, "底部操作栏没有贴在视口底: %s" % stick
        print("sticky 吸底:", stick)
        sticky.screenshot(path=os.path.join(OUT, "decisions-sticky-1440.png"), full_page=False)
        sticky.close()

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

        browser.close()

    if errors:
        raise SystemExit("控制台有错误:\n" + "\n".join(errors))
    print("④ 决策与二次确认自检：通过")


main()

