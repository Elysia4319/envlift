"""⑤ 真机验证（H1 收尾）：两份真实 apply 报告的 diff + 回滚弹窗。

两份报告由 path_apply_two.py 生成：一次两条 PATH 都写、一次跳过其中一条。
只读；弹窗看完就取消，不真的回滚（PATH 已由 CLI 回滚并逐字符比对过）。
"""
import os
import sys

from playwright.sync_api import sync_playwright

BASE = sys.argv[1]
OUT = "shots"
os.makedirs(OUT, exist_ok=True)
errors = []


def launch(p):
    for kwargs in ({"channel": "msedge"}, {"channel": "chrome"}, {}):
        try:
            return p.chromium.launch(headless=True, **kwargs)
        except Exception:  # noqa: BLE001
            continue
    raise RuntimeError("没有可用浏览器")


def main():
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
        page.click("#tab-report")
        page.wait_for_selector("#report-result .rep-pick", timeout=30000)

        apply_rows = page.locator("#report-result tbody tr", has_text="apply")
        assert apply_rows.count() == 2, "应有 2 份 apply 报告，实际 %d" % apply_rows.count()
        apply_rows.nth(0).locator(".rep-pick").check()
        apply_rows.nth(1).locator(".rep-pick").check()
        page.locator("#btn-rep-diff").click()
        page.wait_for_selector("#btn-diff-back", timeout=30000)

        dtext = page.inner_text("#report-result")
        assert "状态变化" in dtext and "层级变化" in dtext and "版本变化" in dtext
        assert "环境一致" in dtext, "两次 apply 的环境应当一致（不该报 env 差异）"
        status_rows = page.locator("#report-result .section", has_text="状态变化").locator("tbody tr")
        assert status_rows.count() == 1, "应恰好 1 条状态变化（一条 PATH 被跳过），实际 %d" % status_rows.count()
        row = status_rows.first.inner_text()
        assert "succeeded" in row and "skipped" in row, "状态变化应是 succeeded → skipped: %r" % row
        print("状态变化行:", " ".join(row.split())[:120])
        page.screenshot(path=os.path.join(OUT, "reports-diff-apply.png"), full_page=True)

        # ---- 回滚弹窗（F3）：要看得到将恢复的原值 ----
        page.click("#btn-diff-back")
        page.wait_for_selector("#report-result .rep-pick", timeout=30000)
        page.locator("#report-result .rep-open").first.click()
        page.wait_for_selector("#btn-rep-rollback", timeout=30000)
        assert page.locator("#btn-rep-rollback").is_enabled(), "有 journal 时应可回滚"
        page.locator("#btn-rep-rollback").click()
        page.wait_for_selector("#dlg[open]", timeout=30000)
        dlg = page.inner_text("#dlg")
        assert "回滚 PATH" in dlg and "用户级" in dlg and "系统级" in dlg, "回滚弹窗内容不对"
        assert "REG_EXPAND_SZ" in dlg or "REG_SZ" in dlg, "回滚弹窗应显示值类型"
        print("回滚弹窗:", " ".join(dlg.split())[:160])
        page.screenshot(path=os.path.join(OUT, "reports-rollback-dialog.png"), full_page=False)
        # 只看不点：PATH 已经回滚并逐字符比对过
        page.click("#dlg-foot button:has-text('取消')")

        browser.close()

    if errors:
        raise SystemExit("控制台有错误:\n" + "\n".join(errors))
    print("两份 apply 报告 diff + 回滚弹窗：通过")


main()
