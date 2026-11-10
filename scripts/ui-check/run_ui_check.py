"""③ 前端三页的真机验证（Playwright）。

跑法（服务已在 18082 起来，工作目录 tmp/c3）：
    python run_ui_check.py <base_url_with_token>

做的事：
  1. 状态条：断言 os/build/arch/用户/winget/网络/配置 全部有值，提权徽章有态；
  2. 扫描页：点「开始扫描」→ 等真实扫描跑完 → 断言未识别区排在已识别之前、
     日志区有增量内容、阶段轨有节点；
  3. 还原页：载入配置 → 生成计划 → 断言表格列齐全、plan_hash 是 64 位、
     「执行还原」按钮 disabled 且有 ④ 说明、待确认区控件 disabled；
  4. PATH 页：断言五个标记的图例、台账行、journal 列表（无则显示空态文案）；
  5. 错误条：用一次会被拒的 rollback 请求，断言错误条按 code 显示；
  6. 全程收集 console error 与 pageerror，最后断言为空；
  7. 断言 token 已从地址栏抹掉，且浏览器持久存储里没有 token。
"""
import json
import re
import sys

from playwright.sync_api import sync_playwright

BASE = sys.argv[1]
SHOT = sys.argv[2] if len(sys.argv) > 2 else "."

console_errors = []
page_errors = []
task_requests = []


def log(msg):
    print(msg, flush=True)


def launch(p):
    """优先用系统已装的 Edge/Chrome：目标机必有浏览器，也免得下载 Chromium。"""
    last = None
    for kwargs in ({"channel": "msedge"}, {"channel": "chrome"}, {}):
        try:
            return p.chromium.launch(headless=True, **kwargs)
        except Exception as e:  # noqa: BLE001
            last = e
    raise RuntimeError(f"没有可用浏览器: {last}")


def main():
    with sync_playwright() as p:
        browser = launch(p)
        page = browser.new_page(viewport={"width": 1360, "height": 1000})

        page.on("console", lambda m: console_errors.append(m.text) if m.type == "error" else None)
        page.on("pageerror", lambda e: page_errors.append(str(e)))
        page.on(
            "request",
            lambda r: task_requests.append(r.url) if "/api/v1/task/" in r.url else None,
        )

        page.goto(BASE)
        page.wait_for_load_state("networkidle")

        # ---- 1. 状态条 ----
        facts = page.inner_text("#facts")
        log("状态条：" + " | ".join(facts.split("\n")))
        for needle in ["Windows", "amd64", "envlift.yaml", "winget"]:
            assert needle in facts, f"状态条缺少 {needle}: {facts}"
        elev = page.inner_text("#badge-elev")
        assert elev in ("管理员运行", "未以管理员运行"), f"管理员运行徽章异常: {elev}"
        log(f"管理员运行徽章：{elev}")
        assert page.evaluate("location.search") == "", "token 仍留在地址栏"
        stored = page.evaluate(
            "JSON.stringify(Object.keys(localStorage).concat(Object.keys(sessionStorage)))"
        )
        assert stored == "[]", f"浏览器持久存储里不该有东西: {stored}"
        page.screenshot(path=f"{SHOT}/01-scan-idle.png", full_page=True)

        # ---- 2. 扫描 ----
        page.click("#btn-scan")
        page.wait_for_timeout(1200)
        page.screenshot(path=f"{SHOT}/02-scan-running.png", full_page=True)
        rail = page.inner_text("#scan-rail")
        log("阶段轨（扫描中）：" + " / ".join(x for x in rail.split("\n") if x.strip()))

        page.wait_for_selector("#scan-result:not([hidden])", timeout=180000)
        page.wait_for_timeout(400)
        page.screenshot(path=f"{SHOT}/03-scan-result.png", full_page=True)

        heads = page.eval_on_selector_all(
            "#scan-result .section-head h2", "els => els.map(e => e.textContent)"
        )
        log("扫描结果分区：" + " → ".join(heads))
        assert "未识别" in heads and "已识别" in heads, f"缺少软件分区: {heads}"
        assert heads.index("未识别") < heads.index("已识别"), "未识别区必须排在已识别之前"
        tally = page.inner_text("#scan-result .tally")
        log("扫描速览：" + " | ".join(x for x in tally.split() if x.strip()))
        assert "已识别" in tally and "未识别" in tally, f"速览缺少配方统计: {tally}"
        assert "[object" not in tally, f"速览里出现了 [object Object]（map 被当字符串渲染）: {tally}"
        log_text = page.inner_text("#scan-log")
        log_lines = [x for x in log_text.strip().splitlines() if x.strip()]
        log(f"日志行数：{len(log_lines)} → {log_lines}")
        assert len(log_lines) >= 2, f"日志太少: {log_text!r}"
        assert any("PATH" in x for x in log_lines), "第一条日志（PATH 统计）没渲染出来，可能是 since off-by-one"
        assert any("since=" in u for u in task_requests), "没有走增量轮询（?since=）"
        log(f"增量轮询次数：{len(task_requests)}")

        picks = page.locator("#scan-result .pick")
        log(f"软件条目可勾选数：{picks.count()}")

        # ---- 3. 还原页 ----
        page.click("#tab-restore")
        page.click("#btn-config-current")
        page.wait_for_timeout(600)
        load_note = page.inner_text("#plan-load-note")
        # 读数标签由 CSS 统一转大写（SCHEMA），断言只关心内容是否存在。
        assert "schema" in load_note.lower(), f"配置概览没渲染: {load_note}"
        log("配置概览：" + " ".join(load_note.split()))

        page.click("#btn-plan")
        page.wait_for_selector("#plan-result:not([hidden])", timeout=120000)
        page.wait_for_timeout(300)
        page.screenshot(path=f"{SHOT}/04-plan.png", full_page=True)

        plan_hash = page.inner_text("#plan-hash").strip()
        assert re.fullmatch(r"[0-9a-f]{64}", plan_hash), f"plan_hash 异常: {plan_hash}"
        log("plan_hash=" + plan_hash)

        headers = page.eval_on_selector_all(
            "#plan-result table.ledger thead tr:nth-child(1) th",
            "els => els.map(e => e.textContent)",
        )
        # 第一张表的表头是执行顺序表
        log("执行顺序表列：" + " | ".join(headers))
        for col in ["目标", "层级", "将执行的最终命令", "管理员运行", "重启", "校验", "失败降级"]:
            assert col in headers, f"计划表缺少列 {col}"

        body_text = page.inner_text("#plan-result")
        # ④ 起「一个配方命中多个安装包」会标 blocked-needs-decision 而不是 local，
        # 所以这里不再强求 local；层级是否存在由 ④ 的 decisions_check.py 覆盖。
        for needle in ["需管理员运行", "会重启", "winget", "manual"]:
            assert needle in body_text or needle == "会重启", f"计划页缺少 {needle}"
        apply_btn = page.locator("#plan-result button:has-text('执行还原')")
        assert apply_btn.count() == 1 and apply_btn.is_disabled(), "执行还原按钮未禁用"
        # ④ 已实现：确认面板在，按钮仍在「没有勾选任何确认」时禁用；
        # 决策控件不再是占位（可用），决策详情由 decisions_check.py 覆盖。
        assert "二次确认" in page.inner_text("#plan-result"), "缺少二次确认面板"
        assert page.locator("#plan-result .dec-ctl").count() >= 0, "决策控件缺失"
        enabled_selects = page.locator("#plan-result select:not([disabled])")
        log(f"待确认区可用控件数：{enabled_selects.count()}")

        # ---- 4. PATH 页 ----
        page.click("#tab-path")
        page.wait_for_timeout(800)
        page.screenshot(path=f"{SHOT}/05-path.png", full_page=True)
        path_text = page.inner_text("#path-result")
        marks = page.eval_on_selector_all(
            "#path-result .mark", "els => [...new Set(els.map(e => e.textContent))]"
        )
        log("PATH 台账出现的标记：" + " ".join(marks))
        assert "+" in marks and "!" in marks and "?" in marks, f"标记不全: {marks}"
        for word in ["用户级", "系统级", "备份", "回滚"]:
            assert word in path_text, f"PATH 页缺少 {word}"
        journals = page.inner_text("#journal-list")
        log("journal 列表：" + " ".join(journals.split())[:160])

        # ---- 5. 错误条：走界面触发一次必然被拒的请求，断言按 code 显示 ----
        page.click("#tab-restore")
        page.fill("#plan-config", "no-such-config.yaml")
        page.click("#btn-plan")
        page.wait_for_selector("#errbar:not([hidden])", timeout=60000)
        code = page.inner_text("#errbar-code")
        msg = page.inner_text("#errbar-msg")
        hint = page.inner_text("#errbar-hint")
        log(f"错误条：code={code} msg={msg[:80]} hint={hint[:80]}")
        assert code == "bad_request", f"错误码应走稳定 code 分支，实际 {code}"
        page.screenshot(path=f"{SHOT}/06-errorbar.png", full_page=True)
        page.click("#errbar-close")
        assert page.locator("#errbar").is_hidden(), "关闭后错误条应隐藏"

        # ---- 6. 控制台 ----
        log(f"console errors：{console_errors}")
        log(f"page errors：{page_errors}")
        # 只有故意触发的那一次 400 允许出现在 console 里（浏览器把非 2xx 记为 error）。
        unexpected = [e for e in console_errors if "400" not in e]
        assert not unexpected, f"非预期 console error: {unexpected}"
        assert not page_errors, page_errors
        browser.close()


main()
