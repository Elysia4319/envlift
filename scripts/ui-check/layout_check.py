"""布局自检（替代肉眼评审的一小部分）：多档宽度下都不许出现页面级横向溢出。

表格本身允许容器内横向滚动（.table-wrap），但整页不能横向滚动，
否则窄窗口上会出现「内容跑到屏幕外」的经典问题。
"""
import sys

from playwright.sync_api import sync_playwright

BASE = sys.argv[1]


def launch(p):
    for kwargs in ({"channel": "msedge"}, {"channel": "chrome"}, {}):
        try:
            return p.chromium.launch(headless=True, **kwargs)
        except Exception:  # noqa: BLE001
            continue
    raise RuntimeError("没有可用浏览器")


def check(page, label):
    m = page.evaluate(
        """() => ({
            scrollW: document.scrollingElement.scrollWidth,
            clientW: document.scrollingElement.clientWidth,
            bodyH: document.body.getBoundingClientRect().height,
        })"""
    )
    print(f"{label}: scrollWidth={m['scrollW']} clientWidth={m['clientW']} height={int(m['bodyH'])}")
    assert m["scrollW"] <= m["clientW"] + 1, f"{label} 出现整页横向溢出: {m}"
    # 关键元素必须可见且有非零尺寸（可见的那个 panel 由调用方指定）
    for sel in ["#facts", "#badge-elev", ".tabs"]:
        box = page.locator(sel).first.bounding_box()
        assert box and box["width"] > 0 and box["height"] > 0, f"{label} {sel} 不可见"


def main():
    with sync_playwright() as p:
        browser = launch(p)
        for w, h, name in [
            (1360, 1000, "desktop-1360"),
            (820, 900, "tablet-820"),
            (760, 900, "tablet-760"),
            (560, 900, "phone-560"),
            (390, 844, "phone-390"),
        ]:
            page = browser.new_page(viewport={"width": w, "height": h})
            page.goto(BASE)
            page.wait_for_load_state("networkidle")
            check(page, name)
            page.screenshot(path=f"shots/layout-{name}.png", full_page=False)
            page.click("#tab-restore")
            page.wait_for_timeout(200)
            check(page, name + " / restore")
            page.close()
        browser.close()
        print("布局自检：通过")


main()
