# 工作流与约束

> 这份文档写给「下一个人」或「下一次会话」：不是讲 envlift 做什么（那是 `README` / `PRD`），
> 而是讲**在这个仓库里怎么干活**——顺序、粒度、边界、以及哪些步骤不许省。
>
> 立这份文档的原因很具体：上一轮交付里，我用了「先写断言 → 实现 → 自查 → 真机回归」这套流程，
> 却没有在任何地方写下来；换个人接手只能靠猜。规矩如果不落在仓库里，就等于没有。

---

## 1. 四份主线文档

| 文档 | 回答什么 | 什么时候读 |
|---|---|---|
| `README.md` | 这是什么、现在能做什么 | 第一次进仓库 |
| `docs/workflow.md`（本文） | 怎么改、怎么验、什么不许做 | 每次动手前 |
| `docs/decisions.md` | 为什么是这样（D1–D22，含取值表） | 改行为前，**冲突时以它为准** |
| `docs/PRD.md` | 具体规格（命令、schema、路由） | 实现细节拿不准时 |
| `docs/vm-e2e-checklist.md` | 哪些只能在 VM 上验、验到哪了 | 每次交付前核对 |

关系：`decisions` 是**为什么**，`PRD` 是**做什么**，本文是**怎么做**，`vm-e2e-checklist` 是**验到哪**。
四者冲突时优先级：`decisions` > `PRD` > 本文 > checklist。

## 2. 开发顺序（TDD 的三步半）

1. **先写断言**。能写成单测的就写单测；只能在界面上看的，先把「期望看到什么」写成 Playwright 断言。
   断言先行不是为了形式，而是为了先想清楚「怎样算对」。
2. **实现到断言变绿**。不要为了让测试过而改断言——除非能说清旧断言本身错了（那就单独说明）。
3. **提交前自查**（下一节），一次过完再提交。
4. **真机回归**：凡是碰真实系统的改动，必须在真机（本机或 VM）上跑一遍，贴真实输出。
   单测全绿 ≠ 功能可用：本项目至少有 5 个 bug 是「单测全绿、真机才现形」的
   （前端路由常量缺失、报告列表把 md/json 各列一行、`api()` 只返回 JSON、
   journal 被当报告解析、diff 单桶导致「层级变化」永远为空）。

## 3. 提交前自查清单（不许省）

- `go vet ./...` 干净；`go test ./...` 全绿（必要时 `-count=1` 绕缓存）。
- 改过 `internal/webui/static/app.js` → **必须** `node --check internal/webui/static/app.js`
  （embed 只存字节，语法错了 `go test` 一样绿，页面会白屏）。
- 碰过前端交互 → 跑对应的 Playwright 脚本（`scripts/ui-check/`）。
- 碰过真实系统（PATH / 注册表 / 进程）→ 真机跑一次，并做回滚与逐字符比对。
- 新增/改行为 → 同步 `docs/decisions.md`（新决策编号）或 `docs/PRD.md`；两边都改时以 decisions 为准。
- 测试与 fixture **不许出现真实用户名/主机名**（用 `olduser` / `OLD-PC` 这类假身份）。
- `git status` 干净：没有把临时脚本、截图、`envlift.yaml`、`reports/` 带进索引。

## 4. commit 粒度

- **一条 commit 只做一件事**。「顺手改的」要么单独一条，要么不做。
- 先修 bug 再加功能时，bug 单独一条（这样回滚功能不会带走修复）。
- 改行为的 commit 必须在 message 里写清：**改了什么 / 怎么验证的**（贴关键数字，
  例如「464→524 字符，rollback 后 byte_equal=true」）。
- 不 amend / rebase 已经跑通、已经验证过的提交（收益为零，风险是动到基线）。

## 5. 验证边界：本机 vs VM

| 操作 | 本机（开发机） | VM |
|---|---|---|
| 扫描（只读） | ✅ | ✅ |
| dry-run / 生成计划 / 生成报告 | ✅ | ✅ |
| PATH-only 的真实 apply + rollback | ✅（**必须**当场回滚并逐字符比对） | ✅ |
| 装软件的真实 apply（winget / 本地包） | ❌ **一律不允许** | ✅ |
| 触发重启 / `expect_reboot` | ❌ | ✅ |
| 写 HKLM 的系统级 PATH | ❌ 不做 | ✅（提权边界） |
| 卸载 winget 验证降级 | ❌ | ✅（做不到就模拟并标注） |

硬性禁止（单测层面）：真读注册表、真调 `winget`、真装软件、真改本机 PATH、真重启、起外网监听。
外部能力一律做成接口（`apply.Executor` / `pathmgmt.Manager` / `scan.Registry`），测试注入假实现。

本机跑 PATH-only apply 的收尾动作（缺一不可）：
`envlift restore --rollback --journal reports\.envlift-journal-*.json` → 再与改动前快照做
ordinal 比较 / SHA256 比对，并确认值类型（`REG_SZ` / `REG_EXPAND_SZ`）未变。

## 6. skill 加载不了怎么办

本仓库的操作者（人或会话）可能没有 skill 执行环境。规则：

- **按本文第 2、3 节手工执行同等步骤**（先断言 → 实现 → 自查 → 真机回归），不许跳过自查。
- 在 commit message 里**注明替代方式**（例如「本会话无 skill 环境，按 docs/workflow.md 手工执行」）。
  这一条之前漏过：14 个 commit 都没写，事后再补写历史的收益为零，所以从下一次开始执行。
- skill 的建议与 `docs/decisions.md` 冲突时，**以决策为准**，并在回复里明确指出冲突点。

## 7. 常用命令

```powershell
# 构建 / 测试（优先 PATH 中的 go；没有则用你的 Go 1.27+ 安装）
$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { throw '未找到 go，请安装 Go 1.27+ 或把 go.exe 加入 PATH' }
$env:GOPATH  = "$env:USERPROFILE\go"
$env:GOCACHE = "$env:LOCALAPPDATA\go-build"
$env:GOPROXY = 'https://proxy.golang.org,direct'
# 如需国内镜像，可改用：$env:GOPROXY = 'https://goproxy.cn,direct'
& $go vet ./... ; & $go test ./... ; & $go build -trimpath -ldflags "-s -w" -o envlift.exe ./cmd/envlift
node --check internal/webui/static/app.js
```

- 真机 GUI：`envlift.exe --serve --no-open --port 1808x`，工作目录放 `envlift.yaml` + `installers/` + `reports/`。
- 界面脚本（都要「带 token 的地址」作第一个参数）：
  `scripts/ui-check/` 下 `run_ui_check.py`（③ 三页）、`decisions_check.py`（④ 决策与二次确认）、
  `reports_check.py`（⑤ 报告页）、`path_apply_check.py` / `path_apply_two.py`（PATH-only apply，**会改本机 PATH**）、
  `reports_diff_apply_check.py`（两份真实报告的 diff）。
- 沙箱里 `curl` 访问不到 localhost，用 PowerShell `Invoke-WebRequest`；错误体取 `$_.ErrorDetails.Message`。
- Playwright 用系统 Edge（`channel="msedge"`），**不要** `playwright install chromium`（本机实测会卡十几分钟）。

## 8. 交付时要给什么

每次交付固定三段：**改了什么 / 怎么验证的（贴真实输出）/ 下一步**。
做不到的事直接说，并给替代方案——不要绕过，也不要写成「已完成」。
