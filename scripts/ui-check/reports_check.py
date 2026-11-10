"""⑤ 报告页自检（Playwright + 系统 Edge）：列表 / 详情 / md 原文 / 两份对比。

不执行任何安装、不改 PATH：只读 reports/ 下的文件。
"""
import os
import sys

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
        page.wait_for_selector("#report-result .section", timeout=30000)

        rows = page.locator("#report-result table.ledger tbody tr")
        assert rows.count() >= 2, "报告列表至少应有 2 行，实际 %d" % rows.count()
        print("列表行数:", rows.count())
        page.screenshot(path=os.path.join(OUT, "reports-list.png"), full_page=True)

        # ---- 详情 ----
        page.locator("#report-result .rep-open").first.click()
        page.wait_for_selector("#report-result #btn-rep-back", timeout=30000)
        text = page.inner_text("#report-result")
        for needle in ["报告详情", "plan_hash", "逐项结果", "PATH 变更"]:
            assert needle in text, "详情缺少 %s" % needle
        print("详情摘要:", " ".join(page.inner_text("#report-result .tally").split()))

        n_all = page.locator("#report-result table.ledger tbody tr").count()
        page.click("#report-result .rep-filter[data-filter='manual']")
        page.wait_for_timeout(300)
        n_manual = page.locator("#report-result table.ledger tbody tr").count()
        assert n_manual <= n_all, "筛选后行数不该变多"
        print("条目行数: 全部 %d / 仅 manual %d" % (n_all, n_manual))
        page.click("#report-result .rep-filter[data-filter='all']")
        page.wait_for_timeout(200)

        # ---- md 原文（<pre>，不渲染）----
        page.click("#btn-rep-md")
        page.wait_for_selector("#report-result #rep-md", timeout=30000)
        page.wait_for_function("() => (document.getElementById('rep-md')||{}).textContent.length > 100", timeout=30000)
        md = page.inner_text("#rep-md")
        assert "# envlift 还原报告" in md, "md 原文不对: %r" % md[:80]
        assert page.locator("#report-result .md-raw").count() == 1, "md 应以等宽 <pre> 展示"
        page.screenshot(path=os.path.join(OUT, "reports-detail-md.png"), full_page=True)
        page.click("#btn-rep-md")
        page.wait_for_timeout(300)

        # ---- 对比 ----
        page.click("#btn-rep-back")
        page.wait_for_selector("#report-result .rep-pick", timeout=30000)
        # 用两份 dry-run 对比：它们的网络状态不同（一份 ENVLIFT_NET_PROBE 指到死端口），
        # 正好验证「环境差异先于条目差异」。
        dry = page.locator("#report-result tbody tr", has_text="dry-run")
        assert dry.count() >= 2, "需要两份 dry-run 报告"
        dry.nth(0).locator(".rep-pick").check()
        dry.nth(1).locator(".rep-pick").check()
        btn = page.locator("#btn-rep-diff")
        assert btn.is_enabled(), "勾选两份后对比按钮应可用"
        btn.click()
        page.wait_for_selector("#btn-diff-back", timeout=30000)
        dtext = page.inner_text("#report-result")
        assert "报告对比" in dtext
        assert "环境不同" in dtext, "这两份报告网络状态不同，应给出环境告警"
        for group in ["状态变化", "层级变化", "版本变化"]:
            assert group in dtext, "对比页缺少分组 %s" % group
        print("对比页分组行数：",
              " / ".join(
                  "%s %d" % (g, page.locator("#report-result .section", has_text=g)
                             .locator("table.ledger tbody tr").count())
                  for g in ["状态变化", "层级变化", "版本变化"]
              ))
        page.screenshot(path=os.path.join(OUT, "reports-diff.png"), full_page=True)
        page.click("#btn-diff-back")

        # ---- 三档宽度 ----
        page.click("#tab-report")
        for w, h in ((1360, 1000), (820, 900), (390, 844)):
            page.set_viewport_size({"width": w, "height": h})
            page.wait_for_timeout(250)
            m = page.evaluate("() => ({s: document.scrollingElement.scrollWidth, c: document.scrollingElement.clientWidth})")
            assert m["s"] <= m["c"] + 1, "%d 宽度横向溢出: %s" % (w, m)
            print("宽度 %d: %d/%d" % (w, m["s"], m["c"]))

        browser.close()

    if errors:
        raise SystemExit("控制台有错误:\n" + "\n".join(errors))
    print("⑤ 报告页自检：通过")


main()
