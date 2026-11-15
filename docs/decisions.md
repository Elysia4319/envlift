# envlift 决策记录

本文件记录已锁定的决策。带编号（D*）的条目一经确认即生效，不再重新讨论；如后续需要修订，追加新编号并注明取代关系，不删除原文。
**配套文档**：怎么改 / 怎么验 / 什么不许做 → `docs/workflow.md`；具体规格 → `docs/PRD.md`；VM 验收 → `docs/vm-e2e-checklist.md`。四者冲突时以本文为准。

---

## D1 形态：Go 单文件原生 exe，CLI 优先

`scan` 与 `restore` 是同一程序的两个子命令。零运行时依赖、免安装、裸系统双击即跑。后续若要 GUI，在 CLI 内核之上另套一层，内核不变。

**为什么这么定**：`restore` 阶段运行在刚装好的裸系统上，那里没有 Node、没有 Python、没有 .NET SDK。这一条直接排除 Electron（需 200MB+ 运行时，而目标恰恰是新系统啥都没有）；Tauri 依赖 WebView2，Win11 自带但 Win10 不一定有，存在风险。Go 单文件约 10MB、零依赖、交叉编译简单，是裸系统自举这条硬需求上最省事的选择。`scan` 与 `restore` 合为一个程序，是因为 `scan` 阶段本机有运行时也无所谓，拆成两个产物只会增加分发负担。

**已知并接受的风险**：未签名 exe 会触发 SmartScreen 警告。MVP 不引入代码签名。

---

## D1-R1 修订 D1：同一个 exe 的双形态

同一个 `envlift.exe`：带子命令（`scan` / `restore` / `version` / `help`）走 CLI；无参数或只带 `--serve` / `--no-open` / `--port` 走 GUI。**CLI 是内核，GUI 是外壳**——GUI 不重写任何业务逻辑，只通过 `internal/api` 的 `Deps` 调用内核函数。

**为什么这么定**：做成两个 exe 就要分发两件产物并保证两者版本同步；而两者的差别只是入口形态，不是能力差异。合一的代价只是入口分派那十几行，收益是「拷一个文件就能用」。内核函数签名与语义不得为了让 GUI 好写而改动。

---

## D2 扫描边界

- **PATH**：只取 `HKCU\Environment` 与 `HKLM\...\Session Manager\Environment` 的持久值，不取当前会话 PATH。
- **语言运行时**：探测优先（实际调用 `python --version` / `node -v` / `java -version`），同时识别版本管理器（fnm / nvm / pyenv / conda / rustup / sdkman），注册表仅作兜底。
- **软件清单**：三源合并去重——注册表 Uninstall（过滤 `SystemComponent=1`、无 `UninstallString`、KB 补丁）+ `winget list` + `App Paths`。
- 不记录从注册表推导出的安装参数，只记录「是否已知可静默安装」。

**为什么这么定**：会话 PATH 里常混有 VS Developer Prompt 临时注入的路径，以及指向已卸载软件的僵尸路径，照搬会把垃圾带进新机器。语言运行时方面注册表基本不可靠——多版本 Python、由 fnm/nvm 管理的 Node 根本不进注册表，只能靠实际探测加版本管理器识别。软件清单必须三源合并：实测本机 457 条 Uninstall 项中 334 条（73%）是 `SystemComponent=1` 的隐藏噪声，而 `winget list` 列出的 234 个包里只有 65 个（28%）有 winget 源映射，单靠任一来源都会漏或脏。安装参数不记录，是因为注册表 `UninstallString` 里的静默参数大多不可靠，记下来反而误导还原阶段。

---

## D3 安装策略：三级降级

`winget` → 本地安装包目录 → `manual`。断网或源不可用时立即停止自动阶段，生成报告（已完成 / 失败 / 待人工），绝不跳过继续。

**为什么这么定**：实测本机仅 28% 的已装软件能被 winget 重装，剩下 72%（SOLIDWORKS、Adobe 全家桶、各类行业软件）在 winget 中查无此包。若只做 `winget → manual` 两级，七成软件仍需人工逐个点击，工具就失去意义；本地安装包目录是这套东西能否真正省事的关键。断网即停而非跳过继续，是因为半装完的环境比没装更糟——用户无法判断哪些装成功了。

---

## D4 跨机器兼容：尽力还原

用户名与盘符在已知软件路径上自动重写为目标机器值；PATH 中指向旧盘符或旧用户名、无法可靠映射的条目默认不重写，标记为待确认失效项。版本冲突以「能跑起来」为准，所有偏差写入三态差异报告（降级 / 替代 / 跳过）。

**为什么这么定**：严格一致在跨机器场景下几乎必然卡死——目标机的用户名、盘符、系统版本只要有一项不同就报错停下，工具不可用。PATH 条目之所以不自动重写，是因为自动猜盘符很容易猜错，而 PATH 猜错的后果是新机器上一批命令静默失效，比明确标记出来更糟。

---

## D5 隐私与脱敏

脱敏默认开启（用户名→`${USER}`、主机名→`${HOST}`、盘符→`${DRIVE}`）；提供 `--raw` 原样导出并打印显式警告；密钥类（token、私钥、.env 内容）永不导出，不提供开关，仅记录「该文件存在」的标记。

**为什么这么定**：配置文件必然包含 `C:\Users\<用户名>\...`、主机名、有时还有公司内部源地址，而这类文件大概率要进 git 或发给别人，默认脱敏才能避免默认泄露。`--raw` 留给确实需要原样比对的场景，但必须显式警告。密钥类不给开关，是因为一旦写进配置文件又被 commit，git 历史里撤不回来——这类风险不该由用户的一次误操作决定。

---

## D4-R1 修订 D4：取消盘符自动重写

配置中非系统盘路径保留原盘符字面值。还原时对涉及非系统盘的路径做存在性检查：目标机有同盘符 → 直接用；无 → 进「待确认」清单，由用户决定（跳过 / 指定新盘符 / 按原盘符建目录）。仅用户名重写保留自动（`C:\Users\old` → `C:\Users\new`）。

**为什么这么定**：用户名重写是**结构性**替换——路径层级不变、可预测、可验证；盘符重写是**语义性**替换——软件自己记着绝对路径，改了等于欺骗它，而且引用点无法枚举（部分埋在二进制里、部分在配置文件里用了不同转义方式），改一半比不改更糟。

---

## D5-R1 修订 D5：脱敏范围收窄

脱敏只保留 用户名→`${USER}`、主机名→`${HOST}`。**取消 `${DRIVE}`**。配置顶部保留 `drive_map`，但只作还原判断的元信息（盘符 → label / purpose），不承担脱敏与映射职责。`--raw` 开关、密钥类（token / 私钥 / .env 内容）永不导出且不提供开关，两条不变。

**为什么这么定**：盘符不属敏感信息（`C` `D` `E` 不含任何能定位到人的信息），却是还原必需的事实。先把它抹成 `${DRIVE}` 再让用户在目标机映射回来，等于先丢信息再猜回来——只有成本，没有收益。

---

## D6 配方（C1 结论）

配方进主配置 `installers:` 节，单一文件约束保住；exe 内置默认配方表，用户同名条目覆盖内置；包目录 `installers\` 不放任何元数据文件，靠文件名 glob 匹配。配方字段：`name` / `match`(glob) / `silent_args` / `needs_admin` / `order` / `depends_on` / `verify` / `expect_reboot` / `source` / `verified_at`。内置配方需带 `source` 与 `verified_at`，过期配方在 dry-run 阶段打警告。

**为什么这么定**：从注册表 `UninstallString` 反推静默参数不可靠，但本地包级要静默安装又必须知道参数，所以把参数从「推导产物」改为「显式声明」；配方进主配置而非独立文件，是为守住「单一配置文件」约束；包目录不放元数据、靠文件名 glob 匹配，是少一个会失效的环节。`verify` / `expect_reboot` 两项是因为静默参数不是唯一变量——很多安装器静默装完仍会弹窗、要 license、或必须重启才写注册表。

---

## D7 断网与降级（C2 结论）

断网是全局条件，整级跳过 winget 并标记 `skipped-by-network`，不逐包超时等待。非断网但单包 winget 失败时，该包单独降级到本地包级，不整级降级。报告必须记录每个包本次走的层级与实际安装版本。

**为什么这么定**：断网是全局条件，逐包超时等待只会白等几分钟且日志全是噪声；单包失败不是全局条件，所以按包降级而非整级降级。报告记录层级与实际版本，是因为断网走本地包会带来版本漂移，不记录则下次对比时会困惑为什么版本对不上。

---

## D8 MVP 边界

**做**：`envlift scan`（只读 → 预览 → 确认后写 `envlift.yaml`）、`envlift restore`（默认即 dry-run，`--apply` 才执行）、三级降级、三态差异报告、脱敏与 `--raw`、PATH 备份与回滚、`installers:` 配方节与内置表。

**不做**：GUI、代码签名、联网上传同步、自动推断静默参数、安装需 license 或图形交互的软件、版本钉死、驱动迁移、增量同步、非 Windows 平台、自动卸载回滚。

**为什么这么定**：MVP 的目标是跑通「扫描 → 导出 → 还原」最小闭环；GUI、签名、同步都不影响闭环是否成立，推迟到闭环验证之后。版本钉死、驱动迁移、自动卸载回滚同理——它们的失败模式比不做的成本更高。

---

## D9 Schema

`schema: 1` 整数主版本，配 `app: envlift` 与 `generated_at`。新增可选字段不升版本（读端忽略未知字段）；仅破坏性变更 +1。用户自定义字段一律 `x-` 前缀，读端无条件忽略。低于自身版本 → 链式迁移（v1→v2→…），迁移前备份原文件；高于自身版本 → 拒绝并提示升级 `envlift`，不做降级读取。

**为什么这么定**：高版本拒绝而非降级读取，是因为降级读取会静默丢失语义——比如 v2 的 manual 项多了 `blocked_reason` 字段，v1 读端忽略它，就可能把「不可装」误判成「可装」。这种错误无声无息，比直接拒绝危险得多。

---

## D10 自举

便携目录约定 `envlift-portable\`，固定三件套 `envlift.exe` / `envlift.yaml` / `installers\`，整体拷 U 盘或网盘。不取自解压包、不把 yaml 内嵌进 exe。缺 winget 时（`winget --version` 探测失败）整级标记 `unavailable`，提示可手动装 App Installer（`https://aka.ms/getwinget`），然后继续走本地包级；不自动安装 winget。

**为什么这么定**：便携目录零工具链、无需额外解压步骤、yaml 可直接编辑；自解压包多一步且杀软更易误报，yaml 内嵌进 exe 则破坏「可编辑」。不自动装 winget，因为那是系统级改动，违反逐项询问的约束。

---

## D11 顺序与依赖

执行顺序即 `installers:` 列表书写顺序；依赖用 `depends_on` 声明，执行前拓扑排序 + 环检测，检测到环报错拒绝执行。装到一半失败 → 停止并保留已装部分，不做自动卸载回滚（卸载风险大于保留）；唯一例外是 PATH 改动必须可回滚。

**为什么这么定**：列表书写顺序显式、可读、可 diff，不引入隐式排序；环检测放在执行前，是为了在产生任何副作用之前失败。失败保留已装部分而不自动卸载，是因为卸载风险大于保留（可能删掉用户原有东西、卸不干净、触发重启），而「已装但可能不完整」可以靠报告说清楚。

---

## D12 校验与报告

三级校验——① 命令可执行性 ② 版本比对（只报差异不阻断）③ 关键路径存在性。报告双份落 `reports/restore-<YYYYMMDD-HHMMSS>.md` 与同名 `.json`，内容三态（降级 / 替代 / 跳过），每项附走了哪一级、实际版本、失败原因。

**为什么这么定**：三级校验覆盖三种不同失败模式——命令不可执行是「没装上」、版本不符是「装错了」、路径缺失是「装了一半」。双份输出是因为 md 给人看、json 给下次 diff 用。三态而非成功/失败，是为了让「尽力还原」的结果可审计。

---

## D13 实现细则（P1–P6）

- **P1 幂等**：`restore` 可重复执行，每步执行前用配方 `verify`（无则回落全局三级校验）判断是否已完成，已完成则跳过、不重装也不报错。
- **P2 expect_reboot**：遇到需重启的项，完成该项后停止，打印「重启后重新运行 `envlift restore --apply`」，不做自动续跑、不引入状态文件。
- **P3 verify 优先级**：配方自带 `verify` 优先；缺失时回落全局三级校验。
- **P4 提权边界**：`--apply` 只表示「不是 dry-run」；需要管理员权限或会触发 UAC 的项仍逐项询问；`--yes` 仅对无需提权的确认生效，绝不静默批准提权。
- **P5 scan 产物**：`scan` 默认写 `./envlift.yaml`（`--out` 可改），并输出「待备包清单」（已知可静默安装但 `installers\` 中暂无匹配安装包的软件）。便携目录由用户手动装配，`scan` 不自动拷贝安装包。
- **P6 术语**：「禁止自动推断静默参数」禁的是从本机注册表 `UninstallString` 或安装包现场反推参数；按文件名 glob 查内置配方表属声明式匹配，允许。

**为什么这么定**：这些是复核实现可行性时发现的空白，缺任一条 MVP 都无法正确执行——尤其 P1 幂等是 P2 能落地的前提，没有它，重启后重跑会把已装项重来一遍。

---

## D14 图形界面形态（C-1 结论）

GUI = **纯标准库 + `embed` 静态前端 + 本地 HTTP + 系统默认浏览器**。零第三方依赖、不引 CDN、不用 Tauri/Electron。服务只绑 `127.0.0.1`，端口由系统分配（`--port` 可固定），一次性随机 token 只经请求头 `X-Envlift-Token` 传递，`/api/quit` 用于退出，进程退出即释放端口、不留后台常驻。

**为什么这么定**：
- Electron 需 200MB+ 运行时，而 GUI 恰恰要在裸系统上跑；Tauri 依赖 WebView2，Win10 不保证有。
- 已否决 `walk`（原生窗口库）方案：引入纯 Go 三方库只为省掉「用浏览器打开」这一步，收益不抵成本，而目标机必有浏览器。
- 不引 CDN 是硬要求：目标机可能断网，任何外链都会导致白屏。

**推论**：前端必须能纯 vanilla 运行、无构建步骤、无 npm；所有数据来自 `/api/v1`，前端不拼命令字符串、不实现业务逻辑；错误按稳定 `code` 分支，不匹配自然语言。

---

## D15 apply 入口防线：confirmations 改为显式三态

**背景**：GUI 第 ② 步真机验证时踩到一个真 bug——`confirmations` 为空对象时，旧实现把它当成「可以跑」，于是 apply 直接执行了需要提权的 `winget install`。根因不是某个字段没校验，而是**空载荷被默认解释成允许**。

**决定**：把 `confirmations` 从「数组有没有值」升级为显式 `mode`，**不提供任何默认行为**。

| `mode` | 语义 | 校验 |
|---|---|---|
| `all` | 全部执行 | 计划中每个 `needs_admin` 项都必须出现在 `admin_confirmed` 里，缺一个就 400 `admin_confirmation_missing`，`details.missing` 逐项列出 |
| `skip_admin` | 只跑非提权部分 | `admin_confirmed` 允许为空，但必须 `ack_skipped_admin: true`，否则 400 `skipped_admin_ack_missing`；执行时提权项标 `blocked-needs-admin` 并进报告 |
| 缺失 / 其他 | — | 400 `invalid_mode` |

`reboot_ack`：计划中存在 `expect_reboot` 项而 `reboot_ack` 不为 true → 400 `reboot_ack_required`（`skip_admin` 下被阻止的提权项不计入，否则「只跑非提权部分」会被一个根本没机会执行的项卡死）。

**校验顺序**（在启动任务之前全部通过才入队，任何一步失败都不产生 `task_id`）：
token → 请求体结构 → `mode` → 加载配置并生成计划 → `admin_confirmed` / `ack_skipped_admin` / `reboot_ack` → `plan_hash` → 网络重探测。

**两处实现口径**（原提案未覆盖，落地时补上）：
1. **系统级 PATH 写入计入提权集合**，名字形如 `PATH[system] <值>`。它不在 `plan.items` 里，只统计 items 会留下「`needs_admin` 计数为 0 但执行时仍要写 HKLM」的缝。
2. `skip_admin` 下系统级 PATH 与提权条目一样「不写、不中断、只记账」，用户级 PATH 照常写。

**错误消息必须含具体项名**：前端直接把 `details` 渲染给人看，不允许用户在「哪里没确认」上再猜一次。

**为什么不是「数组非空即放行」**：那会误伤 D13-P4 已承诺的「只执行无需提权部分」——用户想只跑非提权项时没有合法表达方式，只能被迫全确认或干脆不跑。

---

## D16 砍掉 GUI 的 cookie 鉴权

静态资源**不鉴权**（HTML / CSS / JS 里没有任何数据）；`/api/` 下所有路由（含 `/api/health`、`/api/quit`）**只认 `X-Envlift-Token` 请求头**。任何响应都不得下发 `Set-Cookie`。启动 URL 仍带一次性 token，前端读入内存后立刻 `history.replaceState` 抹掉，此后全部走请求头。

**为什么这么定**：② 那版给静态资源套了一层「query token → Set-Cookie → 后续靠 cookie」的引导鉴权，与「token 只经请求头、只放内存」（D14 / ③-C2）直接冲突——cookie 一下发，token 就同时活在内存、地址栏历史和浏览器存储三处，那句话再也没法写成断言。而静态资源本身不含任何数据，为它承担一条鉴权链路是纯粹的净成本。

**代价（明确接受）**：同一个 localhost 上的其它进程也能读到前端页面。页面里只有代码，没有凭据、也没有本机信息；真正的数据全在 `/api/v1` 后面。

**断言化**（`internal/webui/server_test.go`）：`TestNoResponseSetsCookie`（任何响应不许 `Set-Cookie`）、`TestCookieIsNotACredential`（只带 cookie 的 API 请求 401）、`TestStaticIsOpenButAPIIsNot`（静态 200 / API 401）、`TestAPIV1SetsNoCookie`。

---


---

## D17 ④ 决策：`decisions` 是本机侧覆盖层，不是环境快照

**背景**：④ 要让用户对「歧义怎么处置、PATH 待确认项落到哪」逐项拍板。这需要一个新的持久化产物 `envlift.decisions.yaml`。

**定位**：`envlift.yaml` 是**可版本化的单一环境快照**（D3）；`envlift.decisions.yaml` 是**机器相关的选择**——同一份快照在 A 机与 B 机上可以有不同决策。因此：

- 决策文件**不进主配置**、**不进 schema**、**进 `.gitignore`**；
- 与配置里的 `x-excluded` 同属「本机侧可丢弃覆盖层」——丢了只需重新决策，不会损坏快照；
- 读写放在 `internal/engine`（配置同目录），`internal/config` 只管 yaml schema，**不认识「决策」这个领域概念**；
- 类型 `plan.Decisions` / `plan.Decision` 定义在 `internal/plan`，api 层只做 JSON 反序列化与错误映射，**禁止另造一套 pending / decision 结构**。

**取值表**（前端与后端逐字一致；`= ` 已存在的 PATH 条目不需要决策，不出现在待确认区）：

| kind | action | params | 说明 |
|---|---|---|---|
| software | `select` | `{version}` / `{installer}` / `{recipe}` | 多版本选一个 / 一配方命中多包 / 一条目命中多配方（M4） |
| software | `override_representative` | — | 把被覆盖条目提为代表（shared_recipe） |
| software | `skip` | — | 本次不装 |
| software | `mark_manual` | — | 转人工，报告给人工步骤 |
| runtime | `select` | `{version}` | 多版本共存选装哪个 |
| runtime | `skip` / `mark_manual` | — | 版本管理器托管的走 A6，给 fnm/nvm 命令 |
| path | `add` | — | `+` 默认 |
| path | `accept` | — | `~` 用户名重写，D4-R1 唯一自动项，默认接受 |
| path | `rewrite` | `{new_value}` | `?` 无同盘符：指定新盘符或按原盘符建目录 |
| path | `skip` | — | 不写入 |
| path | `mark_invalid` | — | `!` 失效，默认 skip，显式标记便于下次识别 |

**参数校验**（不校验就会在执行时才炸）：

- `rewrite.new_value`：非空、不含换行/控制字符、不含双引号、无未配对单引号、长度 ≤ 260、必须是盘符开头的绝对路径或 UNC，且盘符必须在目标机存在（错误里列出可用盘符）；
- `select.version` / `select.installer`：必须落在该条目的候选列表里，不在则 400 并列出合法候选；
- `params` 该有必须有、不该有必须没有：`select` 缺 params 或同时给 version + installer → 400；`skip` / `mark_manual` / `add` / `accept` / `mark_invalid` 带了 params → 400。

**错误处理（B4）**：未知 `target_id`、未知 `kind`、未知 `action`、参数不合法、重复决策、引用 `x-excluded` 条目——一律 400，`details.issues` 逐条列出「第 n 条决策（target_id）：原因」。**不允许「加载了但没生效」**：决策文件里引用的 id 在当前计划里找不到，同样报错。

**两类闸口（C1/C2）**：

- 安全类（`plan_hash` / `admin` / `reboot`）缺任一 → 入口拒绝，不启动任务（D15 不变）；
- 数据类待确认（歧义 / PATH `?` / shared_recipe）**不阻断整体**：未决策项标 `blocked-needs-decision`，跳过执行并进报告，前端二次确认面板顶部显示「N 项因未决策将被跳过」；
- manual 项一次性 ack，不逐项（C3）。

**`select` 的三个参数互斥**：有且只能给一个（version / installer / recipe），多给或少给都是 400。

- `recipe` 用于「一个条目命中多个配方」（M4），候选就是该条目 `matched_by` 命中的配方名。选定后按该配方重算：有安装包走本地包，没有就标 manual。若该配方本身命中多个安装包，条目退回 `needs-disambiguation`（同一个 target_id 只能有一条决策，所以这一步必须先在 installers/ 里收敛）。

**一处已知边界（明确记下，不静默绕过）**：`select{version}` 的候选来自「同配方、不同版本」的条目组，且只对 `multi_version=false`（会被去重）的组有意义；`multi_version=true` 的产品各版本都要装，没有「选哪个」这回事。

**`plan_hash` 会变**：`target_id`、`candidates`、`decision` 等字段进 JSON 后哈希必然变。任何一次决策都会让前端已确认状态作废（D1：重新生成计划 → 新 hash → 重新二次确认）。

---

## D18 报告：结构化产物 + 以 target_id 为键的 diff（⑤）

**定位**：报告是**跨机器对比与外发的产物**，不是调试日志。因此它自带格式版本号、自带脱敏、并且把「条目身份」固定在 `target_id` 上——diff 全靠它。

### 格式

- `report_schema: 1`；读到更高版本 → **拒绝渲染**，提示「由更高版本生成，请升级后查看」，不做降级猜测（与 D9 同一理由）；没有版本号 = 旧格式，同样拒绝并要求重新生成。
- 字段：`report_schema / kind / generated_at / config_path / journal / env / plan_hash / mode / summary / items[] / path_changes[] / warnings[] / decisions`。
- `env`：`os / build / arch / username / hostname / winget_ok / network_ok / elevated`，其中 username / hostname **只有占位符**。
- `summary`：`total / succeeded / skipped / manual / blocked_needs_decision / failed / path_writes / awaiting_reboot`，条目与 PATH 合并计数（沿用计划汇总的口径：`?` 未决策计入 blocked，`!` 失效与 `=` 已存在计入 skipped）。
- `items[]`：`target_id / kind / name / expected_version / before_version / actual_version / version_unparsed / version_raw / tier / status / command / exit_code / verify_result / failure_reason / needs_admin / expect_reboot / decision / reason / manual_hint`。
  - 三个版本互不覆盖：`expected_version`（配置要的）、`before_version`（计划生成时目标机上的）、`actual_version`（执行后从 verify 输出解析出来的）。
  - `actual_version` 来自 verify 的 stdout/stderr（C1）；解析不出来就留空，并标 `version_unparsed: true` + 附 `version_raw` 前缀。**留空可以，编不行**（C2）。
- `decisions`：`apply_mode / actions{action: 条数} / blocked[]`（C4）。两台机器报告对不上时，最常见的原因就是决策不同。

### 脱敏（C5）

一律脱敏：用户名 → `${USER}`、主机名 → `${HOST}`，**不提供 raw 开关**。报告是要外发/对比的东西，与 `--plan-out --raw-plan` 的取舍不同。`plan_hash` 在脱敏**之前**算，所以它仍然对应 apply 校验过的那份计划。

### md 与 json

同一份 `Document` 渲染两份产物：json 是结构化主体，md 是可读渲染。前端只解析 json，md 原文只以等宽 `<pre>` 展示——**不引入 md→HTML 渲染器**，也就没有注入面。

### diff 语义（D1–D5）

- 以 `target_id` 为键做集合比较；条目与 PATH 变更放在同一张键空间里（PATH 也是 `target_id`）。
- 五类：新增 / 消失 / 状态变化 / 层级变化 / 版本变化。重点展示后三类，前两类折叠进「其他」。
- 一条条目可以**同时**出现在「状态」与「层级」两组里：`planned → manual` 与 `winget → manual` 是两条独立信息（断网时两者往往一起发生，只报一个会把另一个信号藏起来）。
- 状态变化优先标出：`succeeded → failed`、`succeeded → manual`、`blocked-needs-decision → succeeded`。
- 层级变化（`winget → 本地包`、`winget → manual`）是断网或源异常的信号（D7）。
- 版本变化按三层看：`expected_version` / `before_version`（执行前）/ `actual_version`（执行后），detail 里说明是哪一层变了。
  **注意口径**：`target_id` 本身包含版本，所以「配置里把期望版本改掉」= 换了一个条目，在 diff 里表现为「新增 + 消失」；
  「版本变化」分组实际捕捉的是同一条目在目标机上实际版本不同（目前只有 runtime 有 actual_version）。
  两种呈现都不丢信息，但不要指望在「版本变化」里找「改配置换了版本」。
- **环境差异先于条目差异展示**：`os / build / arch / winget_ok / network_ok / elevated / mode` 任一不同就在顶部告警，并写明「条目差异可能由此产生」；主机名差异不算（两侧都是 `${HOST}`）。
- diff 是纯函数（入参两份 `Document`），单测注入构造数据，不碰文件系统之外的环境。
- 只支持两份对比，默认选中最新两份。

### API（E）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/reports` | 列表（名、时间、mode、摘要、env 简版），时间倒序；单份超过 5MB 时只给 stat 摘要 |
| GET | `/api/v1/reports/{name}?format=json` | 结构化报告；`?offset=&limit=`（默认 50）分页 items；schema 不支持 → 200 + `supported:false`；>5MB → 400 `report_too_large` |
| GET | `/api/v1/reports/{name}?format=md` | 原文 |
| GET | `/api/v1/reports/diff?a=&b=` | 两份报告的差异（D1–D3） |

路径穿越沿用 D3 的 `safeReportName`。回滚仍走 `POST /api/v1/rollback`（传该报告的 journal 文件名）。

---

## D19 VM 实测修复：winget 幂等、PATH 刷新、逐包降级、manual 报告、CLI 管道确认

**背景**：2026-11-03 在全新 Win11 VM 上跑 F0–F8，暴露了五处「单测全绿、真机才现形」的缺口。以下为已锁定行为，后续实现不得回退。

1. **winget 层级继承配方元数据**：只要配方被命中（winget id / alias / display name / name），`verify`、`verify_expect_contains`、`expect_reboot`、`recipe_source`、`verified_at` 都必须带到 winget 层级。否则装完后无法用 verify 判断「已完成」，重跑会再次 `winget install` 并因「已安装」失败。`expect_contains` 必须真正参与判定（退出码 0 且输出包含该子串），不能只看退出码。
2. **verify 前重建进程 PATH**：安装器通常只改注册表 PATH，不改父进程环境。`apply` 在每次 verify 前从 HKLM/HKCU 的 `Path` 重建当前进程 PATH（仅内存，不写注册表），否则刚装好的 `node` / `git` 仍会「找不到」。重建必须幂等：注册表段在前、会话独有段保留、按大小写不敏感去重，重复调用不得让 PATH 变长。
3. **winget 单包失败逐包降级**：`plan.Item` 增加 `fallback_plan`（tier / command / needs_admin / expect_reboot / verify / recipe / installer_file）。`apply` 在 winget 命令失败时只对该包执行 fallback 本地命令；fallback 成功后再跑 fallback 的 verify。不得整级改道，也不得只留 `fallback` 文案。
4. **manual 步骤进入报告**：报告 §4 与 JSON 的 `manual_hint` 必须输出配置里的具体人工步骤（账号、下载入口、安装选项）；只有 hint 为空时才回落「需按官方方式手动安装」。
5. **CLI 管道确认**：`restore --apply` 用管道批量喂 `APPLY` + `y` 时，每次确认都新建 `bufio.NewReader(os.Stdin)` 会预读并丢弃后续行，表现为整批 skipped。已修：`APPLY` 与逐项 `[y/N]` 共用同一个 `prompter`（单个 reader），EOF 时若已读到内容仍按该行处理，并忽略首行 UTF-8 BOM（PowerShell 管道会给原生程序加 BOM）。交互式终端行为不变；自动化仍建议走 GUI `/api/v1/apply`（`mode=all` + `admin_confirmed`）。

---

## D20 GUI 视觉语言：参考 Watermelon UI，但不引入它的技术栈

> 视觉方向已由 D21 取代；D20 里的工程约束（无外部依赖、系统字体、内联 SVG、改动边界）继续有效。

**背景**：2026-11-03 要求「用 `https://ui.watermelon.sh/home` 的风格组件改造 Web UI」。该站是 React + Tailwind + shadcn 组件库，而 D14 已锁定「纯标准库 + embed 静态前端 + 无 CDN + 断网可用」。直接引组件库会同时打破单 exe、零依赖、离线可用三条约束。

**决定**：只复刻它的**视觉语言**，不引入任何外部依赖。

1. **外壳**：桌面为「左侧栏 220px + 右侧内容区」；`≤820px` 时侧栏降级为顶部横排区域（同一份 DOM，只改 CSS 与 `aria-orientation`）。内容区 `max-width: 1320px`、左右 28px。
2. **色板**：品牌/操作绿 `#83D10E`（取自参考站实测取色），只用于主按钮、当前导航项、焦点环；成功语义另用墨绿 `#15803D`，避免「按钮绿」与「已完成绿」撞车。主按钮是**亮绿底 + 深墨字** `#14210B`：白字在该底色上对比度只有 1.90:1，不达 WCAG AA；深墨字为 8.27:1。
3. **形状**：卡片 18px、输入框 13px、按钮胶囊、芯片 8px；**表格保持直角**——数据面不卡片化，否则 9 列计划表与 PATH 台账的横向比对能力会被牺牲。
4. **字体**：只用系统栈（`Segoe UI Variable Text/Display` + `Cascadia Mono`）。参考站的几何无衬线无法离线获得，禁 `@font-face` 与外链字体。
5. **图标**：4 个导航图标为内联 SVG（`currentColor` 描边），不引图标库、不含 `<image>`/外部 `href`。
6. **主题**：浅色先行。色板全部走 CSS 变量（`--bg` / `--surface` / `--border` / `--accent` / …），后续加深色只需覆盖 `:root`；**不加切换按钮**，因为浏览器持久存储被 D16 的断言锁死为空。
7. **改动边界**：只改 `index.html` 与 `app.css`；`app.js` 仅改 `initTabs()`（竖向键盘 + `aria-orientation` 同步）。`#facts` / `#badge-elev` / `#btn-quit` / `#tab-*` / `#panel-*` 的 id 与 ARIA 语义原样保留，现有 Playwright 脚本继续有效。
8. **错误条位置**：从跨全宽吸顶横幅改为内容区顶部的卡片式提示，避免遮挡侧栏导航。

**已知待办（本次不做）**：`app.js` 里仍有 48 处内联 `style="..."`（布局类，如 `margin-left:auto` / `display:flex`）。它们会压过 CSS 变量，做深色主题或再调间距时必须先改成 class。已在 `app.js` 顶部留 `TODO(ui)` 注释，可用 `grep -n 'style="' internal/webui/static/app.js` 定位。

**验证**：`go vet` / `go test ./...` 全绿；`node --check app.js` 通过；`layout_check.py` 在 1360 / 820 / 390 三档无整页横向溢出；真实扫描 + 计划 + PATH 页在 1440 宽下渲染正常，控制台零错误。新增静态断言见 `internal/webui/static_test.go`（`TestNavIconsAreInlineSVG`、`TestCSSUsesSystemFontsOnly`）。

---

## D21 GUI 视觉语言 v2：Vercel / Linear 方向的克制产品界面

**背景**：D20 的 Watermelon 方向偏亮绿、圆角与纯色块，与「克制、专业、有质感」的目标有距离。改为参考 Vercel / Linear 的产品界面语言，同时保留 D14 的全部离线约束。

**决定**：

1. **底色**：页面 `#F9FAFB`（极弱冷调灰白）；卡面纯白；侧栏 `#FBFBFC`。不再使用大面积彩色底。
2. **卡片**：`1px solid rgba(15,23,42,.07)` 半透明边框 + 极弱弥散阴影 `0 1px 2px rgba(16,24,40,.03), 0 8px 24px -18px rgba(16,24,40,.18)`；不靠重阴影分层。
3. **主色**：沉稳深蓝 `#1E40AF`（hover `#1E3A8A`），只用于主按钮、当前导航、焦点环；成功态改用翠绿 `#047857`。状态一律「淡底 + 细边」，避免大片纯色。
4. **形状**：卡片 12px、输入框 8px、按钮 8px（不再用胶囊）、芯片 6px；表格保持直角。
5. **终端**：`.term` + `.term-bar` 组成 Mac 风格深灰终端（`#15171C` 底、`#1C1F26` 头、红黄绿三个圆点），日志正文单独滚动。字体优先本机已安装的 `JetBrains Mono`；没装就直接回退 Windows 自带的 `Cascadia Mono` / `Consolas`，不内嵌、不下载字体文件。
6. **表格**：表头吸顶；偶数行 `#FCFCFD` 斑马纹；悬停行 `#F3F6FF`；命令/路径继续等宽。
7. **侧栏与信息架构**：沿用 D20 的左侧栏 220px、≤820px 顶部横排、页头「标题 + 说明」。
8. **字体**：UI 与标题优先 `Inter`，未安装则回退 `Segoe UI Variable`；数据字体优先 `JetBrains Mono`，未安装则回退 Windows 自带等宽字体。

**为什么不是直接抄 Vercel/Linear**：它们的组件依赖 React/Tailwind（Vercel 还有 Geist 字体）；本项目必须单 exe、断网可用、无构建步骤，所以只复刻视觉语言，不引技术栈。

**验证**：`go vet` / `go test ./...`、`node --check app.js`、三档宽度 Playwright 布局自检；新增 `TestTerminalUsesMacChrome` 断言终端头与 JetBrains Mono 字体栈存在。

---

## D22 GUI 视觉语言 v3：仪器校准档案（Instrument Dossier）

**背景**：D21 的 Vercel / Linear 方向干净，但辨识度不足。按「先生成随机字符串、再据其结构定创意方向」的方式重做整套 UI。种子是一串 96 位字母数字，内部分析出三组结构性线索：`96 = 12 × 8`、重复出现的 `LA / TL` 回环、以及密集的 `9 / 5 / 3` 与若干可用作色对的十六进制片段（如 `5B`、`93`、`AD`、`EE`）。**种子字符串本身不进入界面**，只作为结构灵感。

**决定**：

1. **概念**：仪器校准档案。界面像一份装订好的技术档案：暖纸底、墨黑侧栏、细刻度线、目录编号、读数带。
2. **网格与节奏**：8px 基频；统计读数按 12 栏连续排布；背景是 16px 间距的极淡点阵（远看是纸纹，近看是网格）。
3. **色彩**：暖纸 `#F2F0EA`、卡面 `#FBFAF6`、墨 `#12181C`、薄荷信号 `#7BE0A8`（深版 `#2F7D55`）、氧化红 `#B8442A`、黄铜 `#9A6B1F`、灰蓝 `#3E6F8E`。薄荷只给当前导航、进度、焦点与成功；氧化红只给风险与破坏性动作。
4. **字体**：标题用 Windows 自带的 `Bahnschrift`（DIN 风，像仪表刻度）；正文 `Segoe UI Variable Text`；数据 `JetBrains Mono` → `Cascadia Mono` → `Consolas`（未安装即回退，不内嵌字体文件）。
5. **侧栏**：墨黑底 + 99° 极淡拉丝纹；导航是目录式编号（01–04）+ 线性图标；当前项用薄荷左标与 8% 薄荷底。
6. **页头**：左侧编号 + 标题 + 说明，底部压一条 8px 周期的刻度尺线。
7. **卡片**：3px 直角、1px 墨线、极轻纸叠阴影；卡片标题前有一小段薄荷标记。
8. **表格**：暖色斑马纹 + 薄荷悬停；表头 9.5px 等宽大写、字距 0.1em；数据继续等宽。
9. **终端**：Mac 风格深墨终端（红黄绿圆点改为氧化红/黄铜/薄荷），头部显示 `scan.log` / `apply.log`。
10. **按钮**：墨底纸字的直角块，次级按钮用 1px 墨线；不再使用胶囊与大圆角。

**为什么这么定**：`96 = 12 × 8` 给出可执行的网格与读数带；重复的 `LA/TL` 回环落在「目录编号 + 刻度线」的重复节奏上；`9/5/3` 让界面保持 3px 直角、9.5px 小标签、5px 终止线这类克制尺度。整套东西有明确来源，但不是把随机串贴在界面上。

**验证**：`go vet` / `go test ./...`、`node --check app.js`；1440 / 820 / 390 三档无整页横向溢出；真实扫描、计划、PATH 页截图目检。

---

## D23 安装目录是决策参数，不进 target_id

**决定**：安装目录通过 `set_location` 决策传入，只改变命令与报告中的展示字段；`target_id` 的哈希输入保持不变。

**原因**：`target_id` 是条目身份，必须跨机器、跨重跑稳定。把目录纳入哈希会让同一条软件在指定目录后变成另一个条目，已有决策、报告 diff 与排除引用全部失效。目录是本次执行的参数，不是软件身份的一部分。

**边界**：只使用配方显式声明的 `install_dir_arg` / `winget_location_arg`；不设置目录时，计划、命令与报告保持原样。指定目录视为重装，跳过 verify 幂等检查；winget 重装要求本机支持 `--force`，否则计划 blocked。当前版本不支持 `select{installer}` 与 `set_location` 组合。

