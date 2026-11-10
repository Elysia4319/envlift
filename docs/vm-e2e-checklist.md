# VM 端到端验证清单

> 用途：把**只能在一台全新 VM 上验证**的项目固化下来，避免上下文丢失后重跑一遍
> 或误以为已经验过。
> 本机（开发机）**永远不跑装软件的真实 apply**——本机装软件属于不可回滚的副作用。
>
> 配套文档：怎么改 / 怎么验 → `docs/workflow.md`；已锁定决策（D1–D22）→ `docs/decisions.md`。

## 0. 环境准备

- [ ] 干净 Windows VM（建议 Win11 24H2，另备一台 Win10 22H2 验证降级）
- [ ] 快照点建好再开始；每一项验证完可回滚快照重来
- [ ] 拷贝便携三件套到同一目录：`envlift.exe` / `envlift.yaml` / `installers\`
- [ ] 记录基线：`HKLM\SYSTEM\...\Environment\Path` 与 `HKCU\Environment\Path` 的
      原始值与值类型（`REG_SZ` / `REG_EXPAND_SZ`），回滚要逐字符比对
- [ ] 记录 VM 是否已装 winget（`winget --version`）

## 1. 装软件 apply 成功路径（**两项都要做**）

- [ ] **winget 级**：配置里只放 1–2 个 winget 包（建议 `Git.Git` + `Microsoft.PowerShell`），
      走 `/api/v1/plan` → `/api/v1/apply`（`mode=all`，`admin_confirmed` 覆盖全部项）。
      期望：任务终态 `succeeded`；每个包在报告里记了「走的层级 = winget」与**实际安装版本**。
- [ ] **本地包级**：`installers\` 里放一个真实安装包（建议 Git for Windows 离线 exe），
      配方带正确的 `silent_args` + `verify`。期望：静默安装成功、`verify` 事后校验通过。
- [ ] 两者都要确认：`plan_hash` 在 preview 与 apply 之间一致才允许启动（改配置后
      重发旧 hash 必须 409 `plan_stale`）。

## 2. journal 与报告完整性

- [ ] journal 的 `steps` 覆盖**每一个**执行过的动作：软件条目有 step，PATH 写入有
      `kind=path` 的 step（B1 已修，需在真实装软件场景复核）
- [ ] 每个 step 有 `exit_code` / `output` / `started_at` / `finished_at` / `status`
- [ ] 失败时 `finished=true` 且 `stopped_reason` 说明停在哪个条目
- [ ] `reports/` 下 md + json 双份同时生成，md §2 逐项结果与 journal 一致
- [ ] 报告已统一脱敏（C5），由 `report.Build` + `TestReportIsAlwaysRedacted` 覆盖；
      GUI 下载/预览复用同一路径；VM 上抽查一份报告，确认只出现 `${USER}` / `${HOST}`。

## 3. 真实重启后续跑（D13-P2）

- [ ] 挑一个 `expect_reboot: true` 的真实安装器（如 .NET Runtime / VS Build Tools）
- [ ] 第一次 apply 期望停在 `awaiting_reboot`，不自动续跑、不写状态文件
- [ ] 重启 → 再跑一次 apply：已完成的项靠 `verify` 被**跳过**，未完成项继续
- [ ] 报告两次都要落盘，且第二次能看出「跳过」而不是「重装」

## 4. 缺 winget 降级（D10）

- [ ] 卸载 App Installer（或改名 `winget.exe`）后跑 apply
- [ ] 期望：winget 级**整级**标 `unavailable` 并跳过，不逐包超时；本地包级照常执行
- [ ] 提示文案给出 `https://aka.ms/getwinget`，且不自动安装 winget

## 5. 断网整级跳过（D7）

- [ ] 拔网线 / 关网卡后跑 apply（本机可用 `ENVLIFT_NET_PROBE=127.0.0.1:1` 模拟，
      但 VM 要真断网验一次）
- [ ] 期望：计划生成时即判定 `network_ok=false`，winget 级整级 `skipped-by-network`
- [ ] apply 时重探网络与计划不一致 → 409 `plan_stale_network`，不自行改道

## 6. manual 项的人工步骤可读性

- [ ] 配置里包含确定性 manual 项（如 SOLIDWORKS / 需要 license 的软件）
- [ ] 报告 §4「待人工处理」给出的步骤是**可直接照做**的（下载地址 / 账号要求 /
      安装选项），不是「请手动安装」这种废话
- [ ] 版本管理器托管的运行时（fnm / nvm / pyenv / conda / rustup / sdkman）给出对应
      命令而不是 winget 命令（A6）

## 7. 回滚与 PATH 逐字符一致

- [ ] 改前快照 → apply（含用户级 + 系统级 PATH 变更）→ 记录改后值
- [ ] `envlift restore --rollback --journal <file>` → 值类型与内容与快照**逐字符相同**
- [ ] 回滚后不残留：系统级 PATH 未被多写一次；广播生效（新开终端能看到旧 PATH）

## 8. 提权边界（D13-P4 / D15）

- [ ] 非提权启动 + `mode=all` 但 `admin_confirmed` 缺项 → 400 `admin_confirmation_missing`
- [ ] `mode=skip_admin` + `ack_skipped_admin=true` → 提权项标 `blocked`，非提权项照跑，
      且**没有任何 UAC 弹窗**（不做半成品提权链）
- [ ] 以管理员重开后再跑 → 提权项正常执行

## 9. 本机已覆盖、不需 VM 复验（留作回归）

- 扫描（只读三源合并）、计划生成、脱敏写入、报告双份落盘
- PATH-only apply → journal → 报告 → rollback 全链路（B1，2026-10-27 本机通过）
- `awaiting_reboot` 状态机与幂等跳过（B2，2026-10-27 本机用无害命令通过）
- D15 入口防线全部拒绝分支（单测 + 本机 HTTP 实测）

## 10. 已知问题（VM 验证时注意）

1. **报告脱敏（C5）**：报告已统一脱敏（C5），由 `report.Build` +
   `TestReportIsAlwaysRedacted` 覆盖；GUI 下载/预览复用同一路径；VM 上抽查一份报告。
2. **`silent_args` 覆盖率仍低**：内置 33 条配方里只有一部分有依据的静默参数；
   留空的配方只作识别，本地包层级自动不可用（见 decisions.md D6 / PRD §3.5）。
3. **识别率约 8%**（18/221）：剩余 42 条「有 winget id 但无配方」是配方表太薄，
   不是规则问题。
4. **本机 `rundll32` 被安全策略拦**：GUI 开浏览器走 `shell32!ShellExecuteW`，VM 上
   若被拦会打印提示并需手动粘贴 URL（不算失败）。
- **④ 决策链路（PATH-only，2026-10-27 本机通过）**：1 条 `?` 待确认 PATH → GUI 选「改写落点」
  → plan_hash 变化 → 二次确认（mode=all，无提权项）→ apply → `[PATH] HKCU 新增 1 条`
  → `envlift restore --rollback` → HKCU Path 长度 464→492→464、值类型仍是 `REG_EXPAND_SZ`、
  与改动前快照 **ordinal 相等**（SHA256 前 32 位 `8C5A4BDF51186C1A178446C74A09904A` 前后一致）。
  同一次运行还验了：撤销决策后 plan_hash 回到原值、非法路径回显 `invalid_decision_params`、
  确认面板在未勾选时禁用「执行还原」。


---

## 11. VM 执行顺序（F0–F8，按序勾选，每场景单独记录）

> 前置：E1–E5 完成（干净快照 `VM-CLEAN`、便携目录、2–3 个小安装包、网络可切换、winget 可用）。
> 每场景结束回到 `VM-CLEAN`；记录表列：场景 / 结果（通过·未通过·模拟）/ 关键输出 / 备注。

- [x] **F0 dry-run 先行**：VM 上跑 `restore`，确认 `${USER}/${HOST}` 代入后的命令正确、盘符判定正确
      （本机 D 盘在 VM 上可能不存在 → 应出现 `?` 待确认 PATH）。对照本机预览（本机 229 条 = winget 55 / manual 168 / skipped 6）。
- [x] **F1 winget 级成功**：1–2 个小体积软件装成功；核对 verify 通过、`actual_version` 回填、
      journal 有 step、报告 md+json 双份落盘。
- [x] **F2 本地包级**：E3 放的安装包走通 glob 匹配 + `silent_args`；核对命令与 dry-run 完全一致。
- [x] **F3 断网整级跳过**：断网重跑，winget 级整级 `skipped-by-network`、本地包级继续（D7），
      不逐包超时；报告里能看到网络标记。
- [x] **F4 单包失败降级**：一个 winget 装不上（不存在的 id）+ 本地包存在 → **逐包**降级，不是整级。
- [x] **F5 manual 可读**：无法自动安装的项标 manual，人工步骤可复制（含 `fnm install 21` 这类命令）。
- [x] **F6 expect_reboot 续跑**：无害配方 + `expect_reboot: true` → `awaiting_reboot`、文案正确、
      重启后重跑靠幂等跳过。VS 2026 真装单列为可选长任务，**不阻塞验收**。
- [x] **F7 PATH 与回滚**：1–2 个软件产生的 PATH 变更；备份落点正确、值类型不变、
      rollback 后与本机快照 SHA256 一致。
- [x] **F8 缺 winget（模拟）**：无法真卸载 App Installer 时用环境变量模拟，
      **记录里必须写「模拟」**；真缺 winget 仍标「未真实验证」。
- [x] **G2 真实 diff**：用 F1 与 F3 两份真实报告在界面做一次对比，确认「层级变化」组非空。
- [x] **G3 回填本清单**：勾选已完成项；把 F6 的 VS 2026 与 F8 的模拟项明确标为未真实验证。
- [x] **G4 汇总**：把 VM 上暴露的真 bug / 意外行为单独列一节（沿用「真机才现形」的记录方式）。

> 注意：`F1` 与 `F3` 的报告是「同一份配置、不同网络状态」——这是 D2 里「层级变化」信号
> 第一次有真实数据的场景，别漏。

### 2026-11-03 实测结果（VM：`envlift-vm-20261102`）

| 场景 | 结果 | 关键证据 | 备注 |
|---|---|---|---|
| F0 | 通过（有环境限制） | 229 项：winget 55 / manual 172 / skipped 2；断开光驱后 D: 路径出现 17 条 `?` 待确认 | D:/E: 在本 VM 被光驱占用；计数与交接文档差 4 条（全新系统、无已装软件） |
| F1 | 通过 | `restore-20261103-115152.md`：Node.js 26.13.0 / winget / succeeded / verify pass | 修复了 winget 层级未继承配方 verify 的缺口 |
| F2 | 通过 | `restore-20261103-115430.md`：Git 2.56.0 / local / succeeded / verify pass | 本地 Git 离线包 |
| F3 | 通过 | `restore-20261103-115627.md`：Git / local / succeeded / “网络不可达，winget 级整级跳过（D7）” | `ENVLIFT_NET_PROBE=127.0.0.1:1` |
| F4 | 通过（修复后） | `restore-20261103-125430.md`：fake winget id 失败后 `[降级]` 本地 Git / local / succeeded | 新增结构化 `fallback_plan` |
| F5 | 通过（修复后） | `restore-20261103-123332.md` §4 输出 SOLIDWORKS 的账号/安装器/许可步骤 | 报告 §4 原先只写“需按官方方式手动安装” |
| F6 | 部分通过 | `restore-20261103-121853.md`：等待重启=是；`restore-20261103-122449.md`：复跑 skipped 1 | **真实重启未执行**（VM 停在锁屏，避免锁屏无法回桌面）；状态机与幂等已验证 |
| F7 | 通过 | HKCU 86→107→86 字符；HKLM 188→212→188；回滚前后 SHA256 完全一致，类型仍为 `ExpandString` | journal `…-122745.json` |
| F8 | 通过（模拟） | `restore-20261103-123004.md`：Git / local / succeeded / “winget 不可用，整级跳过（D7）” | **模拟**，未真卸载 App Installer |
| G2 | 通过 | F1 Git(winget) vs F3 Git(local)：层级变化 1 条、版本变化 1 条、状态变化 0 条 | UI diff 截图（本机复验产物，未随仓库分发） |

> CLI 管道确认（G4-5 的修复）已在开发机用空计划验证：`"APPLY`ny`n" | envlift.exe restore --apply`
> 能读到 `APPLY` 并落 journal + 报告；`n` 仍按取消处理。真实装软件场景尚未在 VM 复跑。

### G4 真机才现形的问题

1. **winget 层级不继承配方 verify/expect_reboot**：装完后无法幂等跳过，重跑会再次 `winget install` 并因“已安装”失败。已修（`internal/plan/plan.go`）。
2. **安装器更新 PATH 后当前进程环境不刷新**：`node -v` / `git --version` 在安装后 verify 失败。已修（`internal/apply/apply.go` 从注册表重建进程 PATH；重复刷新幂等，不累加）。
3. **单包 winget 失败没有可执行降级**：原实现只有 `fallback` 文案，没有本地命令。已修（`FallbackPlan` + apply 降级执行）。
4. **报告 §4 丢失 manual_hint**：只写“需按官方方式手动安装”。已修（报告输出与 JSON 增加 `manual_hint`）。
5. **CLI `restore --apply` 用管道喂确认会被 `bufio` 预读**：后续 `[y/N]` 读不到输入，表现为整批 skipped。已修（`APPLY` 与逐项确认共享单个 reader，并忽略首行 UTF-8 BOM，`cmd/envlift/main.go` + `restore.go`）；交互式终端不受影响。
6. **App Installer 别名指向旧版本**：重启后 `winget.exe` 别名仍指向 `1.26.509.0`，实际包是 `1.29.379.0`，导致 winget 无法启动。已通过回到登录态快照绕过；属镜像/更新环境问题，非 envlift 代码。
