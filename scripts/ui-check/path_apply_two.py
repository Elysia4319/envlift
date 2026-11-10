"""⑤ 真机验证（H1）：PATH-only 连跑两次 apply，制造「第二次跳过其中一条」。

用法：python path_apply_two.py <带 token 的地址> rewrite|skip-second

⚠️ 会真的改本机 HKCU Path。跑完必须 `envlift restore --rollback --journal <reports/...>`，
   并逐字符比对改动前的快照（README §H2）。
"""
import os
import sys

from playwright.sync_api import sync_playwright

BASE = sys.argv[1]
MODE = sys.argv[2] if len(sys.argv) > 2 else "rewrite"
OUT = "shots"
os.makedirs(OUT, exist_ok=True)

A_VALUE = r"C:\Windows\Temp\envlift-e2e\a"
B_VALUE = r"C:\Windows\Temp\envlift-e2e\b"


def launch(p):
    for kwargs in ({"channel": "msedge"}, {"channel": "chrome"}, {}):
        try:
            return p.chromium.launch(headless=True, **kwargs)
        except Exception:  # noqa: BLE001
            continue
    raise RuntimeError("没有可用浏览器")


def main():
    errors = []
    with sync_playwright() as p:
        browser = launch(p)
        page = browser.new_page(viewport={"width": 1360, "height": 1000})
        page.on("pageerror", lambda e: errors.append("pageerror: " + str(e)))
        page.on(
            "console",
            lambda m: errors.append(f"console.{m.type}: {m.text}")
            if m.type == "error" and "Failed to load resource" not in m.text
            else None,
        )

        page.goto(BASE)
        page.wait_for_load_state("networkidle")
        page.click("#tab-restore")
        page.click("#btn-plan")
        page.wait_for_selector("#plan-result .section", timeout=120000)

        cards = page.locator(".pending-card")
        assert cards.count() == 2, "期望 2 条待确认 PATH，实际 %d" % cards.count()

        # 第一条：改写落点到 a
        cards.nth(0).locator(".dec-act-path").select_option("rewrite")
        cards.nth(0).locator(".dec-value").fill(A_VALUE)
        cards.nth(0).locator(".dec-apply-path").click()
        page.wait_for_function(
            "() => document.querySelectorAll('.chip').length > 0 && "
            "document.body.innerText.indexOf('决策 · rewrite') >= 0", timeout=60000)
        page.wait_for_timeout(800)

        # 第二条：按模式改写或跳过
        cards = page.locator(".pending-card")
        second = [c for c in [cards.nth(i) for i in range(cards.count())] if "Y:" in c.inner_text()][0]
        if MODE == "skip-second":
            second.locator(".dec-act-path").select_option("skip")
            second.locator(".dec-apply-path").click()
            page.wait_for_function("() => document.body.innerText.indexOf('决策 · skip') >= 0", timeout=60000)
        else:
            second.locator(".dec-act-path").select_option("rewrite")
            second.locator(".dec-value").fill(B_VALUE)
            second.locator(".dec-apply-path").click()
            page.wait_for_function(
                "() => (document.body.innerText.match(/决策 · rewrite/g) || []).length >= 2", timeout=60000)
        page.wait_for_timeout(800)

        # 两条都决策完了：确认面板顶部不该再有「N 项因未决策将被跳过」
        assert page.locator("#confirm-panel .guard").count() == 0, "还有未决策项"
        btn = page.locator("#btn-apply")
        assert btn.is_enabled(), "没有提权项时执行按钮应可点"
        page.screenshot(path=os.path.join(OUT, "two-before-apply-%s.png" % MODE), full_page=True)

        btn.click()
        page.wait_for_function(
            "() => { const el = document.querySelector('#apply-run'); return el && el.hidden === false; }",
            timeout=60000)
        page.wait_for_function(
            "() => { const el = document.querySelector('#apply-run'); return el && el.innerText.indexOf('执行完成') >= 0; }",
            timeout=180000)

        # E5：执行完成后应已经跳到「报告」页并打开详情
        page.wait_for_timeout(1500)
        assert page.get_attribute("#tab-report", "aria-selected") == "true", "执行完成后没有跳到报告页"
        page.wait_for_selector("#btn-rep-back", timeout=60000)
        detail = page.inner_text("#report-result")
        assert "报告详情" in detail, "报告详情没渲染"
        assert page.locator("#btn-rep-rollback").is_enabled(), "有 journal 时回滚按钮应可用"
        page.screenshot(path=os.path.join(OUT, "two-report-%s.png" % MODE), full_page=True)

        import re
        m = re.search(r"journal\s+(\S+\.json)", detail)
        print("JOURNAL=" + (m.group(1) if m else "?"))
        print("MODE=" + MODE)
        browser.close()

    if errors:
        raise SystemExit("控制台有错误:\n" + "\n".join(errors))
    print("两次 apply 链路：通过（%s）" % MODE)


main()
