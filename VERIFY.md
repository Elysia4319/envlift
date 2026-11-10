# envlift GUI v3（Instrument Dossier）修复与复验

## 摘要（200 字以内）

本次只做 v3 皮肤的可交付修复：统一 muted 对比度、页头编号改黄铜、终端标题提亮、阶段轨 active 更醒目、780px 以下页头改三行、读数带换行去悬空线，并清理 11 处推右内联样式、统一“管理员运行”表头。未改视觉方向，未引入外部依赖、字体文件或组件库。

## 修改清单

- [x] **R1 对比度**：`--muted: #6a7078` → `#5f656c`。三组对比度均 ≥ 4.5（见下表）。
- [x] **R2 页头编号**：`.page-index` 的 `color: var(--oxide)` → `var(--brass)`；本机截图（未随仓库分发）中编号不再有红色告警感。
- [x] **R3 终端标题**：`.term-title` 从 `rgba(242,240,234,.32)` 提到 `.45`；合成色 `#797c7b` 对 `#161d21` 为 **4.05:1**（≥ 3）。
- [x] **R4 阶段轨**：`done` 改为透明底 + 薄荷深色描边、常规字重、`--text-soft` 文字；`active` 为薄荷实心点 + 600 字重 + `--mint-deep` 文字。DOM 实测见下方，本机截图（未随仓库分发）。
- [x] **R5 窄屏页头**：≤780px 改为「编号行 + 标题行 + 说明行」单列；`layout_check.py` 已加入 560/760 两档，390/560/760/820 四档均无整页横向溢出。
- [x] **R6 读数带换行**：`.tally` 改为 `gap: 1px` + 容器 `--rule-soft` 底色，子项用 `--surface` 卡面底色；560px 实测换 2 行、所有子项 `border-right: 0px`，无悬空竖线。
- [x] **R7 内联样式收口**：`app.js` 中全部 8 处含 `margin-left:auto` 的行内样式（清单中的 5 处精确写法 + 3 处带其他属性的写法）改为 `.push-right` / `.row-actions`；`index.html` 第 79/97/126 行三处改为 `.row-actions` / `.row-actions.spaced` / `.row-actions.push-right`。`grep` 已无 `style="margin-left:auto`。
- [x] **R8 表头与术语**：计划表 `<th>权限</th>` → `<th>管理员运行</th>`；PATH 系统级标题「需管理员」→「需管理员运行」。`run_ui_check.py` 的表头断言同步更新。

## 对比度数字（R1 / R3）

公式：`(L1 + 0.05) / (L2 + 0.05)`，按 sRGB 相对亮度计算。

| 前景 | 背景 | 对比度 | 结果 |
|---|---|---|---|
| `#5f656c` | `#eae7de` | **4.79:1** | ≥ 4.5 ✅ |
| `#5f656c` | `#fbfaf6` | **5.67:1** | ≥ 4.5 ✅ |
| `#5f656c` | `#f2f0ea` | **5.20:1** | ≥ 4.5 ✅ |
| `rgba(242,240,234,.45)` 合成 `#797c7b` | `#161d21` | **4.05:1** | ≥ 3 ✅ |

## 原始输出

### node --check

```text
node --check: exit 0 (no output)
```

### go build

```text
go build -trimpath -ldflags "-s -w" -o envlift.exe ./cmd/envlift
exit 0；envlift.exe = 8,705,536 bytes
```

### go vet

```text
go vet: exit 0 (no output)
```

### go test ./... -count=1

```text
ok  	envlift/cmd/envlift	5.887s
ok  	envlift/internal/api	5.961s
ok  	envlift/internal/apply	5.897s
ok  	envlift/internal/config	5.975s
ok  	envlift/internal/engine	5.864s
ok  	envlift/internal/match	5.801s
ok  	envlift/internal/pathenv	5.951s
?   	envlift/internal/pathmgmt	[no test files]
ok  	envlift/internal/plan	5.856s
ok  	envlift/internal/probe	5.782s
?   	envlift/internal/recipes	[no test files]
ok  	envlift/internal/report	5.899s
ok  	envlift/internal/scan	5.874s
ok  	envlift/internal/softscan	5.945s
ok  	envlift/internal/sysinfo	5.758s
ok  	envlift/internal/targetid	5.876s
ok  	envlift/internal/task	5.845s
ok  	envlift/internal/vcompare	5.770s
ok  	envlift/internal/webui	6.030s
?   	envlift/internal/winreg	[no test files]
```

### run_ui_check.py

```text
状态条：系统 | Windows 11 · build 26300 | 架构 | amd64 | 用户 | olduser@OLD-PC | winget | v1.29.380 | 网络 | 可达 | 配置 | work\envlift.yaml
管理员运行徽章：未以管理员运行
阶段轨（扫描中）：读注册表与 PATH
扫描结果分区：本次扫描 → 系统信息 → PATH · 用户级 → PATH · 系统级 → 语言运行时 → 未识别 → 已识别 → 写入配置
扫描速览：软件条目 | 228 | 原始 | 521 | 滤除 | 393 | 条 | 配方已识别 | 18 | 未识别 | 210 | 其中 | 44 | 条有 | winget | id | 配方表 | 33 | 条
日志行数：2 → ['23:25:07  PATH 38 条 · 运行时 8 个', '23:25:07  配方 33 条 · 已识别 18 · 未识别 210']
增量轮询次数：8
软件条目可勾选数：38
配置概览：SCHEMA v1 软件条目 2 PATH 用户级 2 PATH 系统级 3 生成时间 2026-01-01T00:00:00+08:00
plan_hash=000169e56d713308930e9ea4a1f5dd387b8a868c1261f0f959d17d20bc35e363
执行顺序表列：# | 目标 | 期望 / 当前 | 层级 | 将执行的最终命令 | 管理员运行 | 重启 | 校验 | 失败降级
待确认区可用控件数：0
PATH 台账出现的标记：+ ~ ? = !
journal 列表：reports/ 下还没有 journal —— 说明这台机器上还没执行过还原。
错误条：code=bad_request msg=读取配置 no-such-config.yaml 失败: open no-such-config.yaml: The system cannot find th hint=请求内容不合法。
console errors：['Failed to load resource: the server responded with a status of 400 (Bad Request)']
page errors：[]
```

> 最后一条 400 是脚本故意用不存在的配置触发的错误条测试，脚本已按白名单放过。

### layout_check.py（含新增 560 / 760 档）

```text
desktop-1360: scrollWidth=1360 clientWidth=1360 height=1000
desktop-1360 / restore: scrollWidth=1360 clientWidth=1360 height=1000
tablet-820: scrollWidth=820 clientWidth=820 height=900
tablet-820 / restore: scrollWidth=820 clientWidth=820 height=900
tablet-760: scrollWidth=760 clientWidth=760 height=900
tablet-760 / restore: scrollWidth=760 clientWidth=760 height=900
phone-560: scrollWidth=560 clientWidth=560 height=900
phone-560 / restore: scrollWidth=560 clientWidth=560 height=900
phone-390: scrollWidth=390 clientWidth=390 height=844
phone-390 / restore: scrollWidth=390 clientWidth=390 height=844
布局自检：通过
```

### fake_poll_check.py

```text
since 序列： [0, 1, 2]
渲染出的日志行： ['12:00:01  frame-1', '12:00:02  frame-2', '12:00:03  frame-3']
阶段轨： ['读注册表与 PATH', '三源合并', '完成']
page errors： []
轮询增量逻辑：通过
```

## R4 阶段轨 DOM 实测

```json
[
  {
    "t": "读注册表与 PATH",
    "s": "done",
    "dot": "rgba(0, 0, 0, 0)",
    "border": "rgb(47, 125, 85)",
    "weight": "400",
    "color": "rgb(60, 70, 76)"
  },
  {
    "t": "三源合并",
    "s": "active",
    "dot": "rgb(123, 224, 168)",
    "border": "rgb(123, 224, 168)",
    "weight": "600",
    "color": "rgb(47, 125, 85)"
  }
]
```

一句话说明：done 是空心描边点 + 常规字重，active 是薄荷实心点 + 600 字重 + 薄荷深色文字，当前步骤第一眼可见。

## R6 读数带换行实测（560px）

```json
{"gap": "1px", "bg": "rgba(18, 24, 28, 0.09)", "rows": 2, "borders": ["0px", "0px", "0px", "0px", "0px", "0px", "0px"], "rowTops": [605, 698]}
```

2 行、6 个子项 `border-right` 全为 0，换行处由容器底色 + 1px gap 形成分隔，无悬空竖线。

## 截图

本机复验截图未随仓库分发（含真实身份信息）。

## 本轮涉及文件

- `internal/webui/static/app.css`
- `internal/webui/static/app.js`
- `internal/webui/static/index.html`
- `scripts/ui-check/layout_check.py`
- `scripts/ui-check/run_ui_check.py`
- `VERIFY.md`

未改动视觉方向、字体文件、外部依赖或组件库；未删除 `[hidden]`、ARIA 属性或既有静态断言。工作区在本轮之前已包含 v3 皮肤的未提交改动，本轮只修改上述文件，未重排无关代码。
