"""轮询增量逻辑的假 API 测试（E2 第 4 项）。

为什么单独做：真机扫描太快，`?since=` 的 off-by-one 很容易被掩盖——
第一版就是这样漏掉第一条日志的（用 next_seq 当 since，等于永久跳过 seq=1）。
这里用 route 拦截伪造三帧任务快照，直接断言请求的 since 序列与渲染结果。
"""
import json
import sys

from playwright.sync_api import sync_playwright

BASE = sys.argv[1]
SHOT = sys.argv[2] if len(sys.argv) > 2 else "."


def launch(p):
    for kwargs in ({"channel": "msedge"}, {"channel": "chrome"}, {}):
        try:
            return p.chromium.launch(headless=True, **kwargs)
        except Exception:  # noqa: BLE001
            continue
    raise RuntimeError("没有可用浏览器")


STATUS = {
    "os": "Windows 11", "build": "26300", "product_name": "Windows 10 Home China",
    "arch": "amd64", "username": "fakeuser", "hostname": "FAKE-PC",
    "elevated": False, "winget_ok": True, "winget_version": "v1.0",
    "network_ok": True, "network_note": "", "installer_dir": "installers",
    "installer_files": 0, "config_path": "envlift.yaml", "recipes": 33,
}

EMPTY_RESULT = {
    "config": {
        "app": "envlift", "schema": 1, "generated_at": "2026-10-27T00:00:00Z",
        "source_machine": {"os": {"version": "Windows 11", "build": "26300", "product_name": "x"},
                           "arch": "amd64", "hostname": "FAKE-PC", "username": "fakeuser"},
        "path": {"user": [], "machine": []}, "runtimes": [], "software": [],
    },
    "stats": {"software_total": 0, "raw": 0, "filtered": 0,
              "by_source": {"uninstall": 0, "winget": 0, "app_paths": 0}, "winget_via": "table"},
    "recognition": {"recipes": 33, "recognized": 0, "unrecognized": 0,
                    "unrecognized_with_winget": 0, "entries": []},
}

# 三帧：seq 1/2/3 逐帧到达，最后一帧结束任务。
FRAMES = [
    {"state": "running", "phase": "读注册表与 PATH", "done": 0, "total": 3,
     "logs": [{"seq": 1, "time": "2026-10-27T12:00:01Z", "level": "info", "message": "frame-1"}],
     "next_seq": 2},
    {"state": "running", "phase": "三源合并", "done": 1, "total": 3,
     "logs": [{"seq": 2, "time": "2026-10-27T12:00:02Z", "level": "warn", "message": "frame-2"}],
     "next_seq": 3},
    {"state": "succeeded", "phase": "完成", "done": 3, "total": 3,
     "logs": [{"seq": 3, "time": "2026-10-27T12:00:03Z", "level": "info", "message": "frame-3"}],
     "next_seq": 4, "result": EMPTY_RESULT},
]

sincelog = []
frame_idx = {"i": 0}


def main():
    with sync_playwright() as p:
        browser = launch(p)
        page = browser.new_page(viewport={"width": 1200, "height": 900})
        errors = []
        page.on("pageerror", lambda e: errors.append(str(e)))

        def handle(route, request):
            url = request.url
            if "/api/v1/status" in url:
                return route.fulfill(status=200, content_type="application/json", body=json.dumps(STATUS))
            if url.endswith("/api/v1/scan") and request.method == "POST":
                return route.fulfill(status=202, content_type="application/json", body='{"task_id":"t1"}')
            if "/api/v1/task/t1" in url:
                since = int(url.split("since=")[1])
                sincelog.append(since)
                i = frame_idx["i"]
                frame = FRAMES[min(i, len(FRAMES) - 1)]
                frame_idx["i"] = i + 1
                # 按后端口径过滤：只返回 seq > since 的日志
                snap = dict(frame)
                snap["id"] = "t1"
                snap["kind"] = "scan"
                snap["logs"] = [l for l in frame["logs"] if l["seq"] > since]
                return route.fulfill(status=200, content_type="application/json", body=json.dumps(snap))
            return route.continue_()

        page.route("**/api/**", handle)
        page.goto(BASE)
        page.wait_for_load_state("networkidle")
        page.click("#btn-scan")
        page.wait_for_selector("#scan-result:not([hidden])", timeout=30000)
        page.wait_for_timeout(300)
        page.screenshot(path=f"{SHOT}/07-fake-poll.png", full_page=True)

        log_lines = page.eval_on_selector_all("#scan-log div", "els => els.map(e => e.textContent)")
        rail = page.eval_on_selector_all("#scan-rail li", "els => els.map(e => e.textContent)")

        print("since 序列：", sincelog, flush=True)
        print("渲染出的日志行：", log_lines, flush=True)
        print("阶段轨：", rail, flush=True)
        print("page errors：", errors, flush=True)

        assert sincelog == [0, 1, 2], f"since 序列不对（off-by-one？）: {sincelog}"
        assert len(log_lines) == 3, f"应渲染 3 行日志、不重不漏，实际 {len(log_lines)}"
        for i, needle in enumerate(["frame-1", "frame-2", "frame-3"]):
            assert needle in log_lines[i], f"第 {i+1} 行不是 {needle}: {log_lines[i]}"
        assert rail == ["读注册表与 PATH", "三源合并", "完成"], f"阶段轨不对: {rail}"
        assert not errors, errors
        browser.close()
        print("轮询增量逻辑：通过", flush=True)


main()
