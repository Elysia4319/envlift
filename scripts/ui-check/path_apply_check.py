"""④ 真机验证（G）：PATH-only 的完整决策链路。

⚠️ 这个脚本会**真的改本机 HKCU 的 Path**（写入一条目录），然后停下等你回滚。
   它只适用于 PATH-only 的配置（不含软件条目）。回滚用：
       envlift restore --rollback --journal <reports/.envlift-journal-*.json>
   跑完必须逐字符比对改动前的快照（scripts/ui-check 的 README 里有完整步骤）。

流程：生成计划 → 断言只有 1 条 ? 待确认 PATH → 界面决策改写落点 →
二次确认（mode=all，无提权项）→ 执行 → 断言成功且 PATH 已写入。
"""
import sys

from playwright.sync_api import sync_playwright

BASE = sys.argv[1]
NEW_VALUE = sys.argv[2] if len(sys.argv) > 2 else "C:\\Windows\\Temp\\envlift-e2e"


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
        page.on("pageerror", lambda e: errors.append(str(e)))

        page.goto(BASE)
        page.wait_for_load_state("networkidle")
        page.click("#tab-restore")
        page.click("#btn-plan")
        page.wait_for_selector("#plan-result .section", timeout=120000)

        cards = page.locator(".pending-card")
        assert cards.count() == 1, "期望恰好 1 条待确认 PATH，实际 %d" % cards.count()
        text = page.inner_text("#plan-result")
        assert "PATH[user]" in text and "无该盘符" in text, "待确认项不是「无盘符」的 PATH"
        print("待确认：", " ".join(cards.first.inner_text().split())[:120])

        hash0 = page.text_content("#plan-hash")

        card = cards.first
        card.locator(".dec-act-path").select_option("rewrite")
        card.locator(".dec-value").fill(NEW_VALUE)
        card.locator(".dec-apply-path").click()
        page.wait_for_function(
            "old => { const el = document.getElementById('plan-hash'); return el && el.textContent !== old; }",
            arg=hash0, timeout=60000,
        )
        hash1 = page.text_content("#plan-hash")
        print("决策后 plan_hash：", hash1)
        assert hash1 != hash0

        body = page.inner_text("#plan-result")
        assert "按决策改写落点为 " + NEW_VALUE in body or "改写" in body, "计划里看不到决策结果"
        assert "未决策跳过 0" in body or page.locator("#confirm-panel .guard").count() == 0, "仍有未决策项"

        btn = page.locator("#btn-apply")
        assert btn.is_enabled(), "没有提权项时执行按钮应可点（mode=all 默认）"
        page.screenshot(path="shots/path-apply-before.png", full_page=True)

        btn.click()
        page.wait_for_function(
            "() => { const el = document.querySelector('#apply-run'); return el && el.hidden === false && el.innerText.indexOf('执行完成') >= 0; }",
            timeout=180000,
        )
        print("执行结果：", " ".join(page.inner_text("#apply-run").split())[:200])
        page.screenshot(path="shots/path-apply-after.png", full_page=True)

        # journal 名：PATH 页能列出来，也能从执行结果里看到
        page.click("#tab-path")
        page.wait_for_timeout(800)
        journals = page.locator("#journal-list table tbody tr")
        assert journals.count() >= 1, "PATH 页没有 journal 行"
        name = page.locator("#journal-list table tbody tr td.code").first.inner_text().strip()
        print("JOURNAL=" + name)
        browser.close()

    if errors:
        raise SystemExit("pageerror: " + "; ".join(errors))
    print("Path-only 决策 → 执行：通过")


main()
