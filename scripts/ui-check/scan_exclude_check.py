"""A2 排除导出的界面回归：勾掉两条 → 写盘 → 校验 x-excluded。

跑法：python scan_exclude_check.py <base_url_with_token> [输出文件名]

断言：
1. 取消勾选后 `#commit-state` 显示「已排除 N 条 → …x-excluded」；
2. 写盘回执里 total 是采到的总数、excluded 是排除数（两者不互相吞掉）；
3. 落盘文件里出现 `x-excluded:`，被排除的条目名在里面，`software:` 段里没有它们。
"""
import re
import sys
from pathlib import Path

from playwright.sync_api import sync_playwright

BASE = sys.argv[1]
OUT = sys.argv[2] if len(sys.argv) > 2 else "envlift-excluded.yaml"

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
        page.on("pageerror", lambda e: errors.append(str(e)))

        page.goto(BASE)
        page.wait_for_load_state("networkidle")
        page.click("#btn-scan")
        page.wait_for_selector("#scan-result:not([hidden])", timeout=180000)

        picks = page.locator("#scan-result .pick")
        assert picks.count() >= 2, f"勾选框太少: {picks.count()}"
        # 记下这两条的显示名，稍后去文件里对账
        names = []
        for i in range(2):
            row = picks.nth(i).locator("xpath=ancestor::tr")
            names.append(row.locator("td.name").inner_text().strip())
            picks.nth(i).uncheck()
        print("排除:", names, flush=True)

        state = page.inner_text("#commit-state")
        print("勾选状态文案:", state, flush=True)
        assert "已排除 2 条" in state, f"计数文案不对: {state}"
        assert "x-excluded" in state, "文案应指出写到 x-excluded 区"

        page.fill("#commit-out", OUT)
        page.click("#btn-commit-rename")
        page.wait_for_function(
            "() => document.querySelector('#commit-result') && document.querySelector('#commit-result').textContent.indexOf('已写入') >= 0",
            timeout=60000,
        )
        result = page.inner_text("#commit-result")
        print("写盘回执:", result, flush=True)

        m = re.search(r"采到 (\d+) 条，其中已排除 (\d+) 条", result)
        assert m, f"回执里应有「采到 N / 排除 M」: {result}"
        total, excluded = int(m.group(1)), int(m.group(2))
        assert excluded == 2, f"排除数应为 2，实际 {excluded}"
        assert total > excluded, "总数应仍按采到的算"

        assert not errors, errors
        browser.close()

    # 落盘文件核对
    path = Path(OUT)
    if not path.is_absolute():
        print(f"（文件应在服务的工作目录下：{OUT}）", flush=True)
    text = path.read_text(encoding="utf-8")
    assert "x-excluded:" in text, "配置里应有 x-excluded 区"
    sw_part = text.split("x-excluded:")[0]
    ex_part = text.split("x-excluded:")[1]
    for n in names:
        assert n in ex_part, f"被排除的 {n} 应在 x-excluded 区里"
        assert n not in sw_part, f"被排除的 {n} 不该还在 software 段里"
    print("落盘核对：通过（x-excluded 有、software 段已剔除）", flush=True)
    print("A2 排除导出：通过", flush=True)


main()
