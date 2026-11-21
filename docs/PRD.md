# envlift PRD 与技术方案（MVP）

> 本文是 `docs/decisions.md` 的可执行展开。决策理由不在本文重复，冲突时以 `decisions.md` 为准。
> 状态：已锁定，进入实现。

---

## 1. 目标与非目标

### 1.1 目标

把一台 Windows 机器上的开发环境**只读地**扫描成一份单一、人类可读、可 diff 的 `envlift.yaml`；在另一台机器上**先 dry-run、再经确认**地按这份配置还原。

三个必须成立的属性：

1. **扫描零副作用**：不写注册表、不改系统设置、不联网、不上传、不装任何东西。
2. **还原可预演**：`restore` 的默认行为是打印计划而非执行。
3. **诚实**：装不了的软件标 `manual` 并给出人工步骤，不假装能装。

### 1.2 非目标（MVP 明确不做）

GUI、代码签名、联网上传与同步、自动推断静默安装参数、安装需 license 或图形交互的软件、版本钉死、驱动迁移、增量同步、非 Windows 平台、自动卸载回滚。

### 1.3 已知能力上限

实测本机 234 个已装程序中仅 65 个（28%）有 winget 包映射。**本工具不承诺「一键装回全部软件」**，承诺的是「把环境说清楚，并把能自动的部分自动掉」。

---

## 2. 命令与参数

```
envlift scan    [--out <path>] [--raw] [--overwrite] [--yes] [--keep-pip-bootstrap]
envlift restore [--config <path>] [--apply] [--yes]
                [--plan-out <file>] [--installers <dir>]
                [--conda-packages conda|pip|both|none]
                [--decisions <file>]
                [--from-user <old>] [--from-host <old>]
envlift version
envlift help
```

| 命令 / 参数 | 默认 | 说明 |
|---|---|---|
| `scan` | — | 只读扫描 → 终端预览 → 交互确认 → 写文件 |
| `scan --out` | `./envlift.yaml` | 输出路径 |
| `scan --raw` | 关闭 | 关闭脱敏（用户名、主机名原样输出）。**启用时打印显式警告**，建议配 `--out *.raw.yaml` |
| `scan --overwrite` | 关闭 | 目标文件已存在时覆盖（先备份为 `.bak`）。**不加则拒绝写入** |
| `scan --yes` | 关闭 | 跳过「是否写入」确认，但**不绕过**已存在文件的保护 |
| `scan --keep-pip-bootstrap` | 关闭 | 保留 pip / setuptools / wheel / distribute 等基础 pip 包；默认过滤 |
| `restore` | — | **默认即 dry-run**：打印逐条命令、副作用、需提权项，然后退出 |
| `restore --apply` | 关闭 | 真正执行。**仅表示「不是 dry-run」，不代表已授权提权** |
| `restore --config` | `./envlift.yaml` | 配置文件路径 |
| `restore --yes` | 关闭 | 跳过**无需提权**的确认。**绝不静默批准提权** |
| `restore --plan-out` | 关闭 | 把计划写成 JSON（显式要求才落盘，dry-run 默认零写入） |
| `restore --installers` | `./installers` | 本地安装包目录 |
| `restore --conda-packages` | `none` | conda 环境恢复范围：只恢复 conda 包、只恢复 pip 包、两者或都不恢复。默认不恢复，必须显式选择 |
| `restore --decisions` | 配置同目录的 `envlift.decisions.yaml`（存在则加载） | 决策文件；显式指定但读不到 → 直接报错，不当成「没有决策」 |
| `restore --from-user` | 空 | `--raw` 配置中的原用户名，显式要求才重写 |
| `restore --from-host` | 空 | `--raw` 配置中的原主机名 |
| `version` / `help` | — | — |

**退出码**：`0` 全部完成；`1` 出错中止；`2` 完成但有降级 / 替代 / 跳过 / 待人工 / 未验证项（含**因未决策被跳过**的项），或计划被阻止（依赖环）。数据类待确认不再阻断整批执行（C2 / D17）。

---

## 3. 配置文件 schema v2

### 3.1 顶层字段

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `app` | string | 是 | 固定 `envlift` |
| `schema` | int | 是 | 当前为 `2`。规则见 §3.7；v1 配置可读并自动迁移 |
| `generated_at` | string (RFC3339) | 是 | 扫描时间 |
| `source_machine` | object | 是 | 来源机器信息，见下 |
| `drive_map` | map[string]object | 否 | 盘符 → `{label, purpose}`。**仅作还原判断的元信息，不承担脱敏职责** |
| `path` | object | 是 | `{user: []string, machine: []string}` |
| `runtimes` | []object | 是 | 语言运行时 |
| `software` | []object | 是 | 已装软件 |
| `installers` | []object | 否 | 安装配方 |
| `conda_envs` | []object | 否 | conda 命名环境快照，见 §3.8 |
| `mirrors` | map[string]object | 否 | 命名镜像 profile，见 §3.8 |
| `x-*` | any | 否 | 用户自定义字段，读端无条件忽略 |

**`x-excluded`（A2）**：扫描/写盘时被显式排除的软件条目**原样**写在这个区里。它们是完整快照的一部分，只是不参与还原；`x-` 前缀读端无条件忽略（D9），因此将来想找回某条被排除的软件，信息还在。**默认不存在**——不排除时行为与「忠实快照」完全一致。

`source_machine`：`os`（见下）、`arch`（`amd64`）、`hostname`、`username`。

`os` 子结构：

| 字段 | 类型 | 说明 |
|---|---|---|
| `version` | string | **按 build number 判定**的主版本：`Windows 10` / `Windows 11` / `unknown` |
| `build` | string | 注册表 `CurrentBuildNumber` 原值，保留以便事后核对 |
| `product_name` | string | 注册表 `ProductName` 原值，**不可靠，仅作参考** |
| `display_version` | string | 如 `24H2` / `26H2` |

> 判定依据：Windows 10 为 build 10240–19045；Windows 11 自 22000 起。区间外一律 `unknown`，不猜。
> 实测本机 `ProductName` 报 `Windows 10 Home China` 而 build 为 26300，故不能拿 ProductName 兜底。

### 3.2 `path`

两个 `PathEntry` 数组，分别对应 `HKCU\Environment` 与 `HKLM\...\Session Manager\Environment` 的 `Path` 持久值。**不采集当前会话 PATH**（D2）。

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `value` | string | 是 | **注册表原始字面值**，不展开 `%SystemRoot%` 这类变量 |
| `expand` | bool | 否 | 来源类型为 `REG_EXPAND_SZ` 时为 `true`，缺省即 `REG_SZ` |

保留原始顺序与来源分段；重复项、空项、多余引号、尾部反斜杠、失效路径**只在终端预览里标注，不静默改写、不删除**。

> **schema v1 修订记录**：本字段在实现第 ② 步时由 `[]string` 改为 `[]PathEntry`，因为需要逐条记录 `REG_EXPAND_SZ` 标记。v1 尚未对外发布、无存量配置文件，故未升 schema 版本；v2 起 `conda_envs` / `mirrors` 为新增可选字段，`docs/envlift.example.yaml` 与本表已同步。

### 3.3 `runtimes`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `name` | string | 是 | `python` / `node` / `java` / `go` / `rustc` … |
| `command` | string | 是 | 探测所用命令名 |
| `version` | string | 否 | 探测结果，探测失败为 `null` |
| `manager` | string | 否 | `fnm` / `nvm` / `pyenv` / `conda` / `rustup` / `sdkman` / `null` |
| `executable` | string | 否 | 解析到的绝对路径（按注册表 PATH 解析，会话 PATH 仅兜底） |
| `resolved_from` | string | 否 | 来源段：`machine` / `user` / `session` / `literal` |
| `resolved_index` | int | 否 | 该段内的序号（从 0 开始），用于回答「为什么解析到这个」 |

> **可执行文件解析规则**：按注册表 PATH 顺序（实测 Windows 的拼接顺序是**系统段在前、用户段在后**）逐目录查找 `name` + `PATHEXT`，外层循环目录、内层循环扩展名；会话 PATH 只补充前两段未覆盖的目录。**探测执行时必须使用解析出的绝对路径**，不能交回 `exec.LookPath` 重新解析，否则会出现「记的是 A 的路径、报的是 B 的版本」。

### 3.4 `software`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `name` | string | 是 | 显示名 |
| `version` | string | 否 | 注册表 `DisplayVersion` |
| `install_location` | string | 否 | `InstallLocation`，无则 `null` |
| `winget_id` | string | 否 | 有源映射时为 winget 包 Id，否则 `null` |
| `sources` | []string | 是 | 命中的来源，取值 `registry` / `winget` / `app_paths` |
| `silent_capable` | bool | 是 | **是否已知可静默安装**。不从注册表反推，仅由内置知识表判定 |
| `manual_hint` | string | 否 | 已知必须人工处置时的说明，否则 `null` |

**注意**：`software` 只记录**事实**，不记录安装命令、不记录安装参数（D2）。

### 3.5 `installers`（配方）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `name` | string | 是 | 配方名，`depends_on` 引用此值 |
| `match` | string | 是 | 文件名 glob，如 `Git-*-64-bit.exe` |
| `silent_args` | string | 是 | 静默参数 |
| `needs_admin` | bool | 是 | 是否需管理员权限 |
| `order` | int | 否 | 书写顺序为主序，`order` 仅作同批次内的次级排序 |
| `depends_on` | []string | 否 | 依赖的配方名 |
| `verify` | object | 否 | `{command, expect_contains}` |
| `expect_reboot` | bool | 是 | 装完是否需重启才能继续 |
| `source` | string | 是 | `builtin` / `user` |
| `verified_at` | string | 否 | 配方验证日期，`builtin` 必填 |

**匹配规则**：遍历 `installers\` 目录，按 `match` glob 匹配文件名；`source: user` 的同名配方覆盖 `builtin`。匹配不到 → 该软件降级为 `manual`。

### 3.6 示例

```yaml
app: envlift
schema: 2
generated_at: '2026-10-27T10:05:00+08:00'

source_machine:
  os:
    version: 'Windows 11'        # 按 build 判定，可能为 unknown
    build: '26100'               # 注册表原值
    product_name: 'Windows 11 Pro'  # 注册表原值，仅作参考
    display_version: '24H2'
  arch: amd64
  hostname: '${HOST}'
  username: '${USER}'

drive_map:
  C: { label: 'System', purpose: '系统盘' }
  D: { label: 'Data', purpose: '软件安装盘' }

path:
  user:
    - value: 'C:\Users\${USER}\AppData\Local\Microsoft\WindowsApps'
    - value: '%USERPROFILE%\scoop\shims'
      expand: true
  machine:
    - value: 'C:\Program Files\Git\cmd'
    - value: 'D:\Program Files\nodejs'

runtimes:
  - name: python
    command: python
    version: '3.13.12'
    manager: conda
    executable: 'C:\Users\${USER}\miniconda3\python.exe'
  - name: node
    command: node
    version: '22.22.2'
    manager: fnm
    executable: 'C:\Users\${USER}\AppData\Roaming\fnm\node-versions\v22.22.2\installation\node.exe'

software:
  - name: 'Git'
    version: '2.47.1'
    install_location: 'C:\Program Files\Git'
    winget_id: 'Git.Git'
    sources: [registry, winget]
    silent_capable: true
    manual_hint: null
  - name: 'Adobe Illustrator 2026'
    version: '30.8.2'
    install_location: 'D:\Program Files\Adobe\Adobe Illustrator 2026'
    winget_id: null
    sources: [registry]
    silent_capable: false
    manual_hint: '需 Creative Cloud 登录后安装'

installers:
  - name: git
    match: 'Git-*-64-bit.exe'
    silent_args: '/VERYSILENT /NORESTART'
    needs_admin: true
    order: 10
    depends_on: []
    verify:
      command: 'git --version'
      expect_contains: 'git version'
    expect_reboot: false
    source: builtin
    verified_at: '2026-01-15'
```

### 3.7 版本与迁移（D9）

- `schema` 为整数主版本，**仅破坏性变更 +1**；新增可选字段通常不升版本。v2 是例外：conda 环境是可执行恢复面，旧二进制会静默忽略，按 D24 升版本。
- 读端忽略未知字段，并在回写时**保留**未知字段（含 `x-*`）。
- `schema` < 本程序支持版本 → 链式迁移，**迁移前备份原文件**为 `envlift.yaml.bak`。
- `schema` > 本程序支持版本 → **拒绝加载**，提示升级 `envlift`。

### 3.8 `conda_envs` 与 `mirrors`（schema v2）

`conda_envs` 记录只读扫描到的 conda 环境。base 默认 `restore_conda=false`、`restore_pip=false`，只作只读记录。

base 仍保存完整 conda/pip 包清单，用于跨机器对比与审计；它不参与自动恢复，不因清单较大而截断。

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `name` | string | 是 | 环境名；base 固定为 `base` |
| `prefix` | string | 是 | 环境前缀；导出时脱敏 `${USER}` / `${HOST}` |
| `manager` | string | 否 | conda 安装根目录，用于区分多个 conda 安装 |
| `conda_exe` | string | 否 | 扫描到的 conda 可执行文件 |
| `conda_version` | string | 否 | `conda info --json` 的版本 |
| `python_version` | string | 否 | conda 包列表中 `python` 的版本 |
| `base` | bool | 否 | 是否 base 环境 |
| `restore_conda` | bool | 否 | 是否恢复 conda 包；缺省 false，必须显式开启 |
| `restore_pip` | bool | 否 | 是否恢复 pip 包；缺省 false，必须显式开启 |
| `mirror` | string | 否 | 引用 `mirrors` 的 profile 名；空表示默认源 |
| `packages` | []object | 否 | `{name, version, build, channel}`；channel URL 中的凭据会被脱敏 |
| `pip_packages` | []object | 否 | `{name, version}`；默认过滤 pip / setuptools / wheel / distribute |

`mirrors` 的键是 profile 名，值支持 `conda_channels`、`pip_index_url`、`pip_extra_index_url`、`pip_trusted_host`。envlift **不硬编码**任何公共镜像；profile 由用户显式配置。envlift 也不会自动读取或迁移源机器的 `~/.condarc` / pip 配置，不会自动采集 `channel_alias` / `default_channels`。URL 中出现 userinfo 或敏感查询参数（token / password / key 等）时拒绝保存；默认只允许 HTTPS，HTTP 必须显式加入 `pip_trusted_host`。

临时 `pip.ini` 的 `extra-index-url` / `trusted-host` 写成单键多值（空格分隔），不使用重复键；pip 的 append 选项会按空白拆分。

`CondaChannels` 只传递配置里的 channel 名或完整 URL，不展开自定义 `channel_alias`；依赖自定义 alias 的环境需在目标机保留 conda 配置，或把 channel 写成完整 URL。

---

## 4. 扫描流程（只读边界）

### 4.1 数据来源

| 目标 | 来源 | 只读方式 |
|---|---|---|
| PATH | `HKCU\Environment`、`HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment` | 读注册表值 |
| 软件清单 | `Uninstall` ×3（HKLM 64/32、HKCU）、`winget list`、`App Paths` | 读注册表 + 只读命令 |
| 运行时 | 执行 `--version` 探测 + 版本管理器目录识别 | 执行命令，超时 3s |
| 系统信息 | `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion` | 读注册表值 |

### 4.2 Uninstall 项过滤规则

丢弃满足任一条件的条目：`SystemComponent = 1`、`UninstallString` 为空、`DisplayName` 匹配 `KB\d{6,}`、`ParentKeyName` 非空。

### 4.3 三源合并

按归一化名称（小写、去版本号、去 `(x64)` 类后缀）+ 版本去重；命中多个来源时合并进 `sources`，`winget_id` 取非空值。

### 4.4 预览与写盘

`scan` 在写文件**之前**于终端输出：

1. 分节统计（PATH 条数 / 运行时数 / 软件数 / 其中可静默数）
2. **待备包清单**：`silent_capable: true` 但 `installers\` 中暂无匹配安装包的软件（D13-P5）
3. 完整 yaml 预览（脱敏后）

交互确认后写盘；`--yes` 跳过确认。

### 4.5 副作用声明

`scan` 唯一写操作是写 `envlift.yaml`。执行 `--version` 探测会启动进程，但只查询版本、不产生配置变更。**任何情况下不写注册表。**

---

## 5. 还原流程（三级降级）

### 5.1 主流程

```
加载配置 → 校验 schema → 展开 ${USER}/${HOST}
  → 构建计划（逐包定级）→ 拓扑排序 + 环检测
  → [dry-run] 打印计划并退出
  → [--apply] 执行 → 生成报告
```

### 5.2 逐包定级

| 条件 | 层级 |
|---|---|
| `winget_id` 非空 且 winget 可用 且 网络可用 | `winget` |
| `installers\` 中有文件匹配某配方 `match` | `local` |
| 以上都不满足 | `manual` |

### 5.2.1 conda 环境定级

conda 环境使用独立层级 `conda`，不复用 local/winget：

默认 `--conda-packages=none`，所有 conda 环境条目跳过；GUI 的 conda/pip 复选框默认不勾选。CLI 显式传 `--conda-packages=conda|pip|both` 时，对非 base 环境视为全局覆盖，忽略 `restore_conda` / `restore_pip`；GUI 不使用全局覆盖，仍按每个环境的两个开关执行。

| 条件 | 状态 |
|---|---|
| `base=true` | `manual`，只读，不自动修改 |
| 配置里同名环境多于一个 | `needs-disambiguation`，不自动执行 |
| 目标机不存在 | `planned`，执行 `conda create --yes --prefix ... --override-channels -c ... --file environment.yml` |
| 目标机存在且有缺包 | `planned`，执行 `conda install --yes --prefix ... --override-channels -c ... --file specs.txt`（不用 `--prune`，不自动接受 ToS） |
| conda/pip 包都已匹配 | `skipped` |
| 只启用 pip 且环境不存在 | `manual`，因为不能只靠 pip 创建环境 |

恢复 conda 包时生成 `environment.yml` 与 `specs.txt`；恢复 pip 包时生成 `requirements.txt`。临时 `environment.yml` / `specs.txt` / `CONDARC` / `PIP_CONFIG_FILE` 放在 `reports/conda/<环境名>/` 下，成功后清理；失败或未验证时保留并写 warning。executor 通过环境变量注入临时配置，**不修改全局 `.condarc` / pip 配置**。

执行后用 `conda list --json --prefix ...` 与 `<prefix>\python.exe -m pip list --format=json` 校验。缺包或版本/build 不符时状态为 `unverified`，不能报 `succeeded`。conda 环境的 `actual_version` 取 `conda list` 中 `python` 包的版本。失败不自动删除环境；journal 记录手工清理命令。

`conda create --file` / `conda install --file` / `--override-channels` **只验证 conda 26.9.3**；不声明旧版 conda 支持，旧版未验证，不自动回退到 `conda env create/update`，因为后者会重新引入默认频道 ToS 与 `env update --yes` 兼容问题。

### 5.3 断网与源不可用（D7）

- **断网是全局条件**：整级跳过 winget，全部标记 `skipped-by-network`，**不逐包超时等待**。
- **winget 不存在**（`winget --version` 失败）：整级标记 `unavailable`，提示 `https://aka.ms/getwinget`，继续走 `local` 级。
- **非断网但单包失败**：该包单独降级到 `local` 级，不整级降级。

### 5.4 幂等（D13-P1）

每个包执行前先做「已完成」判断：配方 `verify` 优先，缺失时回落全局三级校验（§6.1）。已判定完成 → 标记 `skipped: already-present`，**不重装、不报错**。

这是 `expect_reboot` 能落地的前提。

### 5.5 执行与失败

- 顺序：`installers:` 书写顺序为主序；`depends_on` 拓扑排序；**检测到环 → 立即报错退出，不执行任何安装**。
- 失败：**停止并保留已装部分**，不自动卸载（D11）。
- `expect_reboot: true` 的项完成后：停止，打印「重启后重新运行 `envlift restore --apply`」，不自动续跑、不引入状态文件（D13-P2）。

### 5.6 提权边界（D13-P4）

`--apply` 只表示「不是 dry-run」。`needs_admin: true` 的项**逐项询问**，用户拒绝则该包标 `skipped: user-declined`。`--yes` 不覆盖提权询问。

### 5.7 计划生成（plan）

`restore` 的全部输出由 `internal/plan.Build` 产生，**dry-run 与 `--apply` 共用同一个函数**（C1）。它是纯函数：入参是配置 + 目标机探测结果 + 网络状态 + 安装包清单 + 配方表，不做任何 IO（C2），因此可以完全用构造数据单测。

`--plan-out` 写出的是这份计划结构体的产物；`reports/*.json` 是
`internal/report.Document`，见 §7.3。

**dry-run 零副作用**（C3）：不写文件、不执行命令，**包括不执行 verify**——verify 只在 apply 阶段跑，dry-run 里只打印「将用 X 校验」。唯一例外是环境预检的只读探测（`winget --version`、3 秒 TCP 可达性）。

**层级判定**（C4）：逐包判断 winget（可用 + 有 id + 网络可达）→ 本地包（配方 glob 命中）→ manual。断网是全局条件，整级跳过 winget 并标 `skipped-by-network`，不逐包超时。

**顺序**（C5）：配方列表书写顺序为主序，`depends_on` 拓扑排序，检测到环即报错且不产出可执行计划；被移动的条目在计划里注明原因。

**歧义不猜**（C6）：一个包命中多个配方、一个配方命中多个安装包、多个包共用同一配方，三种情况一律标 `needs-disambiguation`，不进执行计划，退出码非 0。

**`--raw` 配置的还原**（C8）：配置中不含占位符即判定为 `--raw` 导出，打印警告、按字面值还原、**不猜测旧用户名**；需要重写时显式给 `--from-user` / `--from-host`。

### 5.8 PATH 合并在软件阶段之后

先装软件、后合并 PATH，保证写入的目录真实存在。

---

## 6. 校验、PATH 与回滚

### 6.1 三级校验（D12）

1. **命令可执行性**：`command` 能启动并返回。
2. **版本比对**：与配置记录值对比，**只报差异不阻断**。
3. **关键路径存在性**：`install_location` 存在。

### 6.2 PATH 合并规则

- **只增不减**：仅追加目标机不存在的条目，绝不删除、绝不重排已有条目。
- 去重按大小写不敏感 + 忽略尾部分隔符。
- 含无法展开变量的条目（旧盘符、旧用户名）→ 进「待确认」清单，**不写入**（D4-R1）。
- 修改系统级 PATH 需管理员权限，**逐次询问**。

### 6.3 备份与回滚

修改前写两份：

- `reports/path-backup-<ts>.json`：修改前后的完整值（审计用）
- `reports/path-backup-<ts>.reg`：可直接双击导入的注册表回滚文件

回滚不引入新子命令，靠 `.reg` 手动导入完成。

---

## 7. 报告

### 7.1 文件

`reports/restore-<YYYYMMDD-HHMMSS>.md` 与同名 `.json`。均在 `.gitignore` 中。

### 7.2 md 结构

1. 头部：时间、目标机、配置来源与 schema、是否 `--apply`
2. 概要：总数 / 已装 / 降级 / 替代 / 跳过 / 待人工 / 中止原因
3. 逐项表：名称、层级、预期版本、实际版本、状态、说明
4. PATH 变更：新增条目、待确认条目、备份文件路径
5. 待人工清单：每项附人工步骤
6. 后续建议（如「重启后重跑」）

### 7.3 json 结构

```json
{
  "report_schema": 1,
  "kind": "restore-report",
  "generated_at": "2026-10-27T10:05:00+08:00",
  "config_path": "envlift.yaml",
  "journal": "reports/.envlift-journal-<时间>.json",
  "env": {
    "os": "Windows 11", "build": "26100", "arch": "amd64",
    "username": "${USER}", "hostname": "${HOST}",
    "winget_ok": true, "network_ok": true, "elevated": false
  },
  "plan_hash": "...",
  "mode": "dry-run",
  "summary": {
    "total": 0, "succeeded": 0, "skipped": 0, "manual": 0,
    "unverified": 0, "blocked_needs_decision": 0, "failed": 0, "path_writes": 0,
    "awaiting_reboot": false
  },
  "items": [
    { "target_id": "...", "kind": "software", "name": "Git",
      "expected_version": "2.47.1", "before_version": "2.40.0",
      "actual_version": "2.47.1", "tier": "winget", "status": "succeeded",
      "command": "winget install --id Git.Git", "verify_result": "pass",
      "needs_admin": false, "expect_reboot": false, "manual_hint": "" }
  ],
  "path_changes": [
    { "target_id": "...", "section": "user", "mark": "+",
      "value": "C:\\Users\\${USER}\\bin", "status": "planned" }
  ],
  "warnings": [],
  "decisions": { "apply_mode": "all", "actions": {}, "blocked": [] }
}
```

**状态取值**：计划态 `planned` / `manual` / `covered` /
`blocked_needs_decision`；执行态 `succeeded` / `failed` / `skipped` /
`blocked`。报告统一脱敏，`env.username` / `env.hostname` 固定为
`${USER}` / `${HOST}`；`plan_hash` 在脱敏前计算。

---

## 8. 目录约定

### 8.1 便携目录（D10）

```
envlift-portable\
├── envlift.exe
├── envlift.yaml
├── installers\
│   └── Git-2.47.1-64-bit.exe
└── reports\            # 运行时生成
```

三件套整体拷到 U 盘或网盘。`installers\` 由用户手动装配，`scan` 不自动拷贝安装包。

### 8.2 源码目录

```
envlift\
├── go.mod
├── cmd\envlift\main.go
├── internal\
│   ├── config\         # schema 定义、读写、脱敏、迁移
│   ├── scan\           # 只读扫描
│   ├── restore\        # 计划、执行、降级
│   └── report\         # md / json 输出
├── docs\
│   ├── decisions.md
│   └── PRD.md
└── .gitignore
```

---

## 9. 硬性约束核对表

| 约束 | 落点 |
|---|---|
| 扫描只读、不写注册表 | §4.1 全部读值；§4.5 唯一写操作是写 yaml |
| 不联网、不上传、不安装 | §4 无网络调用；D8 不做同步 |
| 可随时中断 | `scan` 写盘前预览；`restore` 默认 dry-run |
| 安装必须 dry-run 且确认后执行 | §2 `restore` 默认 dry-run，`--apply` 才执行 |
| UAC / 系统级改动逐项询问 | §5.6、§6.2；GUI 侧由 **D15** 在入口强制（`mode` 三态 + `admin_confirmed` 逐项） |
| 配置单一文件、带 schema 版本 | §3，配方并入 `installers:` 节 |
| 不覆盖用户已有配置 | §6.2 只增不减 |
| PATH 合并先备份、可回滚 | §6.3 |
| 装不了的标 manual 给步骤 | §5.2、§7.2 第 5 节 |

---

## 10. 软件清单与配方映射（第 ④ 步）

### 10.1 software 字段扩充

在 §3.4 基础上新增（**全部可选，缺则留空不报错，schema 仍为 1**）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `publisher` | string | 注册表 `Publisher` 原值 |
| `uninstall_string` | string | 注册表 `UninstallString` 原值。**只作事实记录，不用它反推静默参数**（D2） |
| `arch_view` | string | 注册表视图：`64` / `32`（HKCU 为空） |

`silent_capable` 的判定收窄为：**存在可用的 winget 包 id**。ARP 直挂条目（`ARP\Machine\...`）的 Id 不是可安装包 id，不算。

### 10.2 配方字段扩充

在 §3.5 基础上新增（全部可选）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `winget_ids` | []string | **最强锚点**（M1）。winget id 规范化、稳定、不带版本与厂商噪声 |
| `aliases` | []string | 规范化后精确匹配的别名 |
| `match_display` | string | 对 DisplayName 的 Go RE2 正则，建议带 `(?i)` 并锚定 |
| `install_dir_arg` | string | 本地安装包追加的目录参数模板，`{install_dir}` 表示用户选择的目录；空表示不支持 |
| `winget_location_arg` | string | winget 追加的目录参数模板（如 `--location "{install_dir}"`）；空表示不支持 |
| `install_dir_source` | string | 上述模板的依据：官方文档链接或本机 `--help` 实测；模板非空时必填 |

### 10.3 四级锚点（M2）

对每条 software 依次尝试，**命中即停**：

1. `winget_ids` — 大小写不敏感精确匹配
2. `aliases` — 双方规范化后精确匹配
3. `match_display` — 对原始 DisplayName 跑正则
4. `name` — 双方规范化后精确匹配（兜底）

配方的 `name` 只作条目标识，**不参与 DisplayName 匹配**——两件事分开，避免「配方改名就匹配不上」。

命中依据写入计划的 `matched_by`（`winget_id` / `alias` / `display_regex` / `name` / `none`），这是 M5 要求的可审计字段。

### 10.2.1 `set_location` 决策

对 `planned` 且层级为 `local` / `winget` 的条目，可用决策
`{action: "set_location", params: {install_dir: "D:\\Apps\\Git"}}` 指定安装目录。

- 只使用配方显式声明的模板，不猜参数；两个模板都为空时拒绝。
- `winget` 条目要求 `winget_location_arg` 非空；`local` 条目要求 `install_dir_arg` 非空。
- `install_dir` 与 `path.rewrite` 共用路径校验：必须是盘符开头的绝对路径或 UNC，盘符必须存在。
- 安装目录只是决策参数，不进入 `target_id`（D23）；不设置时计划与命令保持原样。
- 指定目录视为重装：即使 verify 已通过也继续执行安装；winget 重装命令追加 `--force`。
- 若本机 winget 的 `install --help` 不含 `--force`，计划标 blocked，不猜测替代参数。
- 当前版本不支持 `select{installer}` / `select{recipe}` 与 `set_location` 组合；请先选安装包/配方，再单独指定目录。
- Git 与 Notepad++ 的本地目录参数尚未验证，内置配方不声明 `install_dir_arg` / `winget_location_arg`；`set_location` 会被拒绝，GUI 目录输入禁用。
- winget `--location` 是包级 best-effort，是否生效取决于包本身，必须 VM 验证。
- winget 失败后若本地配方没有 `install_dir_arg`，不保留可执行降级方案，避免装到默认目录。
- 安装目录变更不提供自动卸载回滚；回滚只覆盖 PATH。
- 安装后读取注册表 `InstallLocation` 校验落点；读不到或不一致时报告标 `unverified`，不把安装判失败。
- `set_location` 成功后写入本地 `envlift.relocations.yaml`；同一 target_id + 归一化目录命中账本即跳过安装，不再依赖 verify。
- 账本条目保存 `install_dir` 与 `actual_dir`；`actual_dir` 是安装后实测值，读不到时留空，不猜。
- 账本 `verified=true` 只代表上次验证结果。apply 在跳过前重新读取 `InstallLocation`：与账本目录一致才跳过；不一致时打印“账本记录与实际不符，重新安装”并重新安装；读取失败时也重新安装并标 `unverified`。
- 账本 `verified=false` 或未配置 `LocationReader` 时仍按账本跳过，但报告必须标 `unverified` /“账本记录未验证/未复核”，不能显示成已验证。
- 同名软件的 `InstallLocation` 候选按归一化路径去重，保留首个原始值；去重后仍有多个不同路径时标 `unverified` 并列出候选。
- 账本 `version > 1` 时 `relocation.Load` 返回“账本版本高于本程序支持”，不静默读取。
- 账本是本地状态，不入库；账本读取失败按空账本继续并 warning，写入失败不把安装判失败。
- 改目录或删除账本条目才会再次重装；账本 `verified=false` 不会自动重试。
- runtime 条目暂不支持注册表目录验证；同名多版本可能标 `unverified` 并列出候选。
- winget 缺 `--force` 时当前实现是整批 blocked，不只是跳过单个条目。

### 10.4 规范化（M3）

依次：Unicode NFC → trim → 小写折叠 → 去 `®™©` → 去括号/裸架构后缀 → 去尾部版本号 → 去尾部 `version` → 去尾部年份 → 去尾部标点 → 折叠空白；后缀剥离循环到稳定。

**已知副作用**：年份会被剥掉，所以 `Visual Studio Community 2022` 与 `... 2026` 归一后相同。以年份区分版本的产品**不能写别名**，只能靠 `match_display` 的原始正则区分。内置表里 `vs2022` 因此不设别名。

### 10.5 三源采集（C1–C5）

- **Uninstall**：`HKLM` 64 视图、`HKLM` 32 视图（`KEY_WOW64_32KEY`）、`HKCU` 默认视图，逻辑路径只写一次，视图由标志控制。一律 `READ`。单键读取失败记 warning 跳过。
- **过滤**（计数写进摘要，不静默丢弃）：`SystemComponent=1`、无 DisplayName 且无 UninstallString、**无 DisplayName**、`ParentKeyName` 非空、`ReleaseType` 属更新类、DisplayName 命中 `^KB\d+` / `Update for` / `Security Update for` / `Hotfix for`。
- **winget list**：按 JSON → CSV → 表格顺序尝试结构化输出；实测该 winget 版本不支持 `--output`，实际走表格解析。解析假设写在 `ParseWingetTable` 注释里；整体解析失败则整源标记不可用并继续。
- **App Paths**：只用来补 `install_location`，**不产生新条目**。
- **合并**：注册表条目以「规范化 name + publisher + version」为键；同名不同版本保留多条。winget 包按「规范化 name 唯一匹配」挂到已有条目上补 `winget_id`，挂不上的独立成条。

### 10.6 歧义（C6 / M4）

三种情况一律标 `needs-disambiguation`、退出码 2、不进执行计划：一个条目命中多个配方（列出配方名与命中依据）、一个配方命中多个安装包、多个条目共用同一配方。

> **已知副作用（需复核）**：真实机器上同一产品常有多个版本/安装范围（本机有 3 个 Temurin JDK、2 个 VSCode），按当前规则会被判为「多条目共用同一配方」而全部搁置。是否改为「保留一条、其余标 covered」需要决策。

---

## 11. 执行器与 PATH 回滚（第 ⑤ 步）

### 11.1 命令

```
envlift restore --apply [--journal-dir reports]
envlift restore --rollback [--journal <file>] [--journal-dir reports]
```

### 11.2 二次确认（C1）

`--apply` 有独立于 dry-run 的确认：打印「即将执行 N 项，其中 K 项需管理员，M 项将触发重启」，要求**输入 `APPLY`**（区分大小写）才继续。`--yes` 不能绕过这一层。`APPLY` 与后续逐项 `[y/N]` 共用同一个输入读取器，因此支持 `printf 'APPLY\ny\ny\n' | envlift restore --apply` 这类管道输入；首行 UTF-8 BOM（PowerShell 管道）会被忽略。

需要提权的条目**逐项询问**；当前进程未提权时**阻止该项并停止**，不静默跳过、也不自动提权（不实现半成品的 UAC 提升链）。

### 11.3 事务日志（C2）

`.envlift-journal-<YYYYMMDD-HHMMSS>.json`，落在 `--journal-dir`（默认 `reports/`）。每执行完一步立即整体重写落盘，不缓冲。字段：条目、层级、命令、退出码、输出摘要、状态、起止时间、PATH 快照。

`finished` 与 `stopped_reason` 标明这次是否跑完。`--rollback` 缺省取该目录下最新的 journal。

### 11.4 PATH 备份与回滚（C3）

- 任何 PATH 写入**之前**先快照 `HKCU` 与 `HKLM` 两处 Path 的**原始值与类型**，写入 journal，并另存人类可读的 `<journal>.path.bak`。快照失败即中止，不做任何改动。
- 值类型原样保留：`REG_EXPAND_SZ` 必须写回 `REG_EXPAND_SZ`，写成 `REG_SZ` 会让 `%VAR%` 变成字面量。
- 写入后广播 `WM_SETTINGCHANGE`（`SendMessageTimeoutW` + `HWND_BROADCAST`），否则新开进程读不到新 PATH。
- 回滚只做一件事：把快照值原样写回。**不做软件卸载**（D11）。
- 回滚时**与快照一致的键跳过写入**——写没改过的键既没必要，还会平白要求管理员权限，让「只改过用户级 PATH」的场景在非提权下回滚失败。

### 11.5 执行循环（C4）

逐项：幂等检查（verify 通过即跳过）→ 提权确认 → 执行（带超时）→ verify 判定 → 写 journal。失败即停并保留现状；`expect_reboot` 完成后停止并提示重启后重跑。

### 11.6 环境一致性（C5/C6）

apply 开始时**重新探测** winget 可用性与网络状态；与 dry-run 时不一致就停止并提示重跑 dry-run，不自行改道。

### 11.7 可注入性（C8）

命令执行（`apply.Executor`）与 PATH 读写（`pathmgmt.Manager`）都是接口，单测全部注入假实现——**不装任何软件、不改本机 PATH、不调 `winget install`**。

---

## 12. GUI 与本地 API（D1-R1 / D14）

### 12.1 形态

同一个 exe：带子命令走 CLI，无参数或只带 `--serve` / `--no-open` / `--port` 走 GUI。GUI 只绑 `127.0.0.1`，端口由系统分配（`--port` 可固定），一次性 token 只经请求头 `X-Envlift-Token` 传递；静态资源全部 `embed` 进 exe，**不引任何外链**。

### 12.2 路由（共 14 条，前端不得要求新增）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/status` | 系统信息、提权、winget、网络、配置路径、配方数 |
| POST | `/api/v1/scan` | 起扫描任务（只读） |
| POST | `/api/v1/scan/commit` | 写配置，`on_exist` = `rename` / `backup_overwrite`（缺省拒绝覆盖）；`exclude: [target_id]` 把条目挪进 `x-excluded`（缺省空 = 全量）。回执 `{written,total,excluded}`，`total` 仍是采到的条数 |
| GET | `/api/v1/config/current` | 读当前目录 `envlift.yaml` |
| POST | `/api/v1/config/load` | 读指定路径配置 |
| POST | `/api/v1/plan` | **同步**返回 `{plan_hash, plan}`（不建任务）；接受可选 `decisions`，非法决策当场 400 + `details.issues` |
| POST | `/api/v1/apply` | 起执行任务（**入口受 D15 强制**，见 12.4）；接受 `decisions` |
| GET | `/api/v1/decisions` | 读 `envlift.decisions.yaml`，返回 `{decisions: […]}`（不存在 = 空数组） |
| POST | `/api/v1/decisions` | 写决策文件，返回 `{saved: n}`；结构不合法 400 |
| GET | `/api/v1/task/{id}?since=N` | 任务快照，`N` = 已收到的最大日志 `seq` |
| POST | `/api/v1/task/{id}/cancel` | 尽力取消 |
| GET | `/api/v1/reports` | reports 目录列表，`kind` = `report` \| `journal` |
| GET | `/api/v1/reports/{name}?format=md\|json` | 读单份报告；journal 也走这条读快照 |
| POST | `/api/v1/rollback` | 按 journal 快照回滚 PATH |

长任务一律「POST 起任务 → 轮询 `GET /api/v1/task/{id}?since=`」，不提供同步阻塞接口。任务终态：`succeeded` / `failed` / `cancelled` / `awaiting_reboot`（待重启是独立终态，不是失败）。

### 12.3 载荷约定

- 配置结构体同时带 `yaml` 与 `json` 标签且取值一致：GUI 直接吃这份结构的 JSON，键名与 `envlift.yaml` 相同（`schema` / `source_machine` / `winget_id` …），不允许出现 Go 字段名。
- `scan` 任务结果 = `config` + `stats` + `recognition`。`recognition` 是**预览辅助**（哪条软件被哪个配方覆盖、靠哪一级锚点），由引擎用同一份 `match.Matcher` 与内置配方表算出；它**不写进配置文件**（M6），前端也不得自己实现匹配。
- 错误一律为统一信封 `{"error":{"code","message","details"}}`，`code` 是稳定字符串，前端按 `code` 分支，不匹配自然语言。
- `decisions` 在 plan / apply 载荷里是**数组**：`[{target_id, kind, action, params?, name?, version?, value?}]`。`name/version/value` 只给人读，校验一律以 `target_id` 为准（D17）。
- 计划里未决策的待确认项状态为 `blocked-needs-decision`（C2：跳过执行、进报告，不阻断整批）；`summary.blocked_needs_decision` 是条目与未决策 PATH 的合计，就是确认面板上「N 项因未决策将被跳过」的 N。

### 12.4 apply 入口（D15）

载荷两种等价形态都接受：嵌套 `confirmations`（D15 原文）与扁平 `{mode, ack_skipped_admin, confirmed_items, admin_confirmed, reboot_ack}`；扁平优先。`confirmations.mode` 必填（`all` / `skip_admin`），无默认值。校验顺序：token → 请求体 → `mode` → 计划 → `admin_confirmed` / `ack_skipped_admin` / `reboot_ack` → `plan_hash` → 网络重探测；任何一步不过都不产生 `task_id`。

系统级 PATH 写入计入提权集合，条目名形如 `PATH[system] <值>`。

**二次确认面板**：管理员项默认全部未勾选，逐项确认；「全选 / 清空」只是逐条勾选/取消的批量操作，不改变 `mode=all` 必须全部勾选的要求，也不会自动提交——全选后仍需显式点击「执行还原」。

### 12.5 前端约束

纯 vanilla JS + 本地 CSS（`app.css` / `app.js` 与 `index.html` 一起 `embed` 进 exe，**没有任何外部样式表或脚本**），无框架、无构建、无 npm、无 CDN、无外部字体/图标；token 只存内存并立即从地址栏抹掉；不拼命令字符串、不做版本比较等业务判断。这些约束有回归断言兜底（`internal/webui/static_test.go`：外链扫描、路由白名单、禁止命令拼接、禁止浏览器持久存储、asset Content-Type）。

**视觉语言（D22，取代 D21 的视觉方向）**：仪器校准档案。暖纸底 `#F2F0EA` + 极淡 16px 点阵、墨黑侧栏、目录式编号导航、8px 节奏与 12 栏读数带；薄荷信号 `#7BE0A8` 只给当前导航/进度/焦点/成功，氧化红只给风险。标题用 Windows 自带 Bahnschrift，正文 Segoe UI，数据优先 JetBrains Mono（未安装回退 Cascadia Mono / Consolas，不内嵌字体文件）。卡片 3px 直角 + 1px 墨线 + 极轻阴影；表格暖色斑马纹 + 薄荷悬停；日志为 Mac 风格深墨终端（红黄绿圆点改为氧化红/黄铜/薄荷），头部显示 `scan.log` / `apply.log`。桌面左侧栏 232px，`≤820px` 降级为顶部横排。

**鉴权边界（D16）**：静态资源不鉴权，`/api/` 一律只认 `X-Envlift-Token` 请求头，任何响应都不下发 `Set-Cookie`（断言见 `internal/webui/server_test.go`）。

自动化验证脚本在 `scripts/ui-check/`（Playwright 驱动系统已装的 Edge/Chrome）：三页冒烟、轮询增量（假 API）、三档宽度布局自检。

**conda 面板**：扫描结果新增 conda 环境表，显示环境名、Python 版本、前缀、conda/pip 包数量、两个恢复开关与镜像选择。base 行只读；命名环境的 conda/pip 默认都不勾选，必须显式开启。提交扫描时，GUI 只回传开关与 mirror 名，不回传路径或包列表；服务端按 `name + manager` 匹配，匹配不到直接报错。

