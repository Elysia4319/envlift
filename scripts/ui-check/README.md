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
python scripts\ui-check\decisions_check.py 'http://127.0.0.1:18082/?token=...' shots
# 配置不在服务进程的工作目录时显式指定（服务端按自己的工作目录解析相对路径）
python scripts\ui-check\decisions_check.py 'http://127.0.0.1:18082/?token=...' shots --config=envlift.yaml
```

| 脚本 | 验证什么 |
|---|---|
| `run_ui_check.py` | 状态条六个事实 + 提权徽章；真实扫描的增量日志与分区顺序（未识别在前）；计划表每一列、`plan_hash` 格式、「执行还原」按钮禁用；PATH 台账五种标记；journal 空态；错误条按 `code` 显示；token 已从地址栏抹掉、浏览器持久存储为空；无 pageerror |
| `fake_poll_check.py` | 用 route 拦截伪造三帧任务快照，断言 `?since=` 序列是 `0,1,2`、三条日志不重不漏。**这条专治 off-by-one**：第一版用 `next_seq` 当 `since`，会永久漏掉第一条日志，而真机扫描太快，很难靠肉眼发现 |
| `layout_check.py` | 1360 / 820 / 390 三档宽度下页面无横向溢出 |
| `decisions_check.py` | ④ 决策链路（安装包歧义、PATH 改写、撤销后 hash 复原）；二次确认面板的全选/清空/已选计数/逐项取消/命令展开（在 820 视口断言省略），1440 与 820 单列、390 上下排列，以及真实页面 `.cf-foot` 吸底 |

## 注意

- `run_ui_check.py` 会**真的扫一遍本机**（只读：注册表 + `winget list`），耗时十几秒到一分钟。
- 这些脚本**不执行任何安装**：③ 步的「执行还原」按钮是禁用的，脚本也只断言它禁用。
- `decisions_check.py` 需要服务进程的工作目录里有可读的 `envlift.yaml`（仓库里 `envlift.yaml` 被 gitignore，worktree 默认没有）。脚本启动时先调 `/api/v1/config/load` 做前置检查，缺失会立刻报错并提示放置位置或改用 `--config=<路径>`，不会干等 180s 超时。
- `decisions_check.py` 的夹具**只改 `/api/v1/plan` 的响应**：把真实计划扩成 60 条 `needs_admin` 项，并在命令后追加 `--ui-check-ellipsis` 标记用于证明夹具生效。它不伪造 `/api/v1/apply`，不点「执行还原」，不执行任何安装。
- 1360 视口下侧栏占 262px，命令区约 762px，夹具命令能整行放下；因此「单行省略/展开」断言固定在 820 视口执行，1360 只验证布局与按钮状态。
- 截图落到 `shots/`，该目录不进版本库。
- **改完 `app.js` 先跑 `node --check internal/webui/static/app.js`**：embed 只关心字节，
  语法错了 `go test ./...` 一样全绿，只有真正打开页面才会炸。
  （Playwright 那三个脚本会以 pageerror 的形式抓到，但那已经是几十秒之后的事。）

