# GUI 三页的自动化验证（③ 步）

三个脚本都用 Playwright 驱动**系统已装的 Edge/Chrome**（不下载 Chromium：
目标机本来就有浏览器，也省掉一次 150MB 下载）。

## 前置

```powershell
python -m pip install playwright
```

## 跑法

```powershell
# 1) 准备一个工作目录（GUI 的配置/安装包/报告路径都相对于工作目录）
$wd = Join-Path $env:TEMP "envlift-ui"; New-Item -ItemType Directory -Force $wd
Copy-Item docs\..\scripts\ui-check\fixtures\demo-envlift.yaml "$wd\envlift.yaml"
New-Item -ItemType Directory -Force "$wd\installers","$wd\reports"
Set-Content "$wd\installers\ExampleEditorSetup-1.0.exe" "dummy"

# 2) 起服务，抄下打印出来的带 token 地址
Start-Process envlift.exe -ArgumentList '--serve','--no-open','--port','18082' -WorkingDirectory $wd

# 3) 跑三个脚本（都接受「带 token 的地址」作为第一个参数）
python scripts\ui-check\run_ui_check.py  'http://127.0.0.1:18082/?token=...' shots
python scripts\ui-check\fake_poll_check.py 'http://127.0.0.1:18082/?token=...' shots
python scripts\ui-check\layout_check.py  'http://127.0.0.1:18082/?token=...'
```

| 脚本 | 验证什么 |
|---|---|
| `run_ui_check.py` | 状态条六个事实 + 提权徽章；真实扫描的增量日志与分区顺序（未识别在前）；计划表每一列、`plan_hash` 格式、「执行还原」按钮禁用；PATH 台账五种标记；journal 空态；错误条按 `code` 显示；token 已从地址栏抹掉、浏览器持久存储为空；无 pageerror |
| `fake_poll_check.py` | 用 route 拦截伪造三帧任务快照，断言 `?since=` 序列是 `0,1,2`、三条日志不重不漏。**这条专治 off-by-one**：第一版用 `next_seq` 当 `since`，会永久漏掉第一条日志，而真机扫描太快，很难靠肉眼发现 |
| `layout_check.py` | 1360 / 820 / 390 三档宽度下页面无横向溢出 |

## 注意

- `run_ui_check.py` 会**真的扫一遍本机**（只读：注册表 + `winget list`），耗时十几秒到一分钟。
- 这些脚本**不执行任何安装**：③ 步的「执行还原」按钮是禁用的，脚本也只断言它禁用。
- 截图落到 `shots/`，该目录不进版本库。
- **改完 `app.js` 先跑 `node --check internal/webui/static/app.js`**：embed 只关心字节，
  语法错了 `go test ./...` 一样全绿，只有真正打开页面才会炸。
  （Playwright 那三个脚本会以 pageerror 的形式抓到，但那已经是几十秒之后的事。）

