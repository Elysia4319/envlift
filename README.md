# envlift

Move a Windows development environment from one machine to another.

> **English summary:** envlift scans installed software, PATH entries, language runtimes, and conda/pip environments on Windows, then builds a reviewable restore plan. It can install software, restore conda/pip packages, and modify PATH, so use a VM or snapshot first and only run trusted configuration files. This is an early alpha for Windows 10/11 (amd64), released under the MIT License.

> 把一台 Windows 机器的开发环境「原样搬走」，在另一台上「原样落地」。

**Status:** Early alpha · Windows 10/11 (amd64) only · MIT · v0.1.0-alpha

## 这是什么、解决什么问题

换电脑或重装系统后，开发环境往往要靠记忆重建：PATH 条目要逐条找回，Python、Node、JDK、Go 的版本容易漏，常用软件要逐个下载安装。envlift 先只读扫描当前机器，生成一份可审阅的 `envlift.yaml`；再在目标机上生成还原计划，默认只预演，确认后才执行。

## 快速开始

### 下载 release

从 GitHub Releases 下载 `envlift.exe`（仓库发布后提供），放到一个工作目录：

```powershell
mkdir C:\envlift
cd C:\envlift
.\envlift.exe version
```

### 从源码构建

需要 Go 1.27+：

```powershell
git clone https://github.com/Elysia4319/envlift.git
cd envlift
go build -trimpath -ldflags "-s -w" -o envlift.exe ./cmd/envlift
.\envlift.exe version
```

发布后也可以：

```powershell
go install github.com/Elysia4319/envlift/cmd/envlift@latest
```

## CLI 用法

```powershell
# 只读扫描，预览确认后写配置；默认脱敏
.\envlift.exe scan --out envlift.yaml

# 保留 pip/setuptools/wheel 等基础包（默认过滤）
.\envlift.exe scan --out envlift.yaml --keep-pip-bootstrap

# 生成还原计划，默认 dry-run，不改系统
.\envlift.exe restore --config envlift.yaml

# 真实执行；需要输入大写的 APPLY 二次确认
.\envlift.exe restore --config envlift.yaml --apply

# 默认不恢复 conda/pip 包；必须显式选择范围
.\envlift.exe restore --config envlift.yaml --apply --conda-packages=conda
.\envlift.exe restore --config envlift.yaml --apply --conda-packages=pip

# 回滚某次 apply 的 PATH 变更
.\envlift.exe restore --rollback --journal reports\.envlift-journal-<时间>.json

.\envlift.exe version
.\envlift.exe help
```

`scan --raw` 会关闭脱敏，输出真实用户名和主机名；只应写入 `*.raw.yaml` 等本地文件，不要提交或外发。

## GUI 用法

```powershell
.\envlift.exe --serve --no-open --port 18080
```

按终端输出的带 token 地址在浏览器打开。GUI 复用 CLI 的扫描、计划、执行和报告路径；**不要同时运行两个 envlift 实例**，它们会争用同一份 `envlift.yaml` 与 `reports/`。

## conda 环境

`scan` 会读取本机可发现的 conda 安装，采集命名环境的 conda 包（name / version / build / channel）与 pip 包（name / version）。默认过滤 pip / setuptools / wheel 等基础包；GUI 或 `--keep-pip-bootstrap` 可保留。base 默认只读，不进入自动恢复列表；所有环境默认 `restore_conda=false`、`restore_pip=false`，必须在 GUI 勾选或显式传 `--conda-packages` 才会执行。GUI 的扫描结果里可以逐个环境开关 conda / pip，并选择命名镜像 profile。

CLI 的 `--conda-packages=conda|pip|both` 是**显式全局覆盖**：对非 base 环境忽略 `restore_conda` / `restore_pip`，直接按该范围执行；不传时默认 `none`，全部跳过。GUI 不使用全局覆盖，仍按每个环境的两个开关执行。

`restore` 对 conda 环境使用独立层级 `conda`：

- 环境不存在时生成 `environment.yml`，执行 `conda create --yes --prefix ... --override-channels -c ... --file environment.yml`。
- 环境已存在时生成 `specs.txt`，执行 `conda install --yes --prefix ... --override-channels -c ... --file specs.txt`；不使用不支持 `--yes` 的 `conda env update`，也不自动接受 conda ToS。
- pip 包单独执行 `<prefix>\python.exe -m pip install -r requirements.txt`。
- 临时 `environment.yml`、`specs.txt`、`CONDARC`、`PIP_CONFIG_FILE` 放在 `reports/conda/` 下，成功后清理；失败或未验证时保留，便于排查。
- 执行后用 `conda list` / `pip list` 校验；缺包或版本不符只标 `unverified`，不报 `succeeded`。
- 失败不会自动删除环境；journal 会给出 `conda env remove --prefix ...` 的手工清理提示。
- conda 安装的包会同时出现在 `pip list` 中；扫描会按 PEP 503 规范化名称剔除重叠，避免 `requirements.txt` 重复安装 numpy 等 conda 包。

镜像只使用配置里的命名 profile，不硬编码默认清华源；envlift **不会自动读取或迁移**源机器的 `~/.condarc` / pip 配置，也不会自动采集 channel_alias/default_channels。URL 中的 token / 密码不会被写入配置；带凭据的 URL 会被拒绝。默认只允许 HTTPS；HTTP 或 pip 的 `trusted-host` 必须显式配置。

## 安全边界

- envlift **会安装软件、修改用户级/系统级 PATH**，这不是纯只读工具。
- 首次使用请放在 VM、测试机或系统快照中；确认回滚路径后再用于真实机器。
- 只运行自己信任的 `envlift.yaml`。配置中的命令会按计划执行。
- conda/pip 包也是可执行代码；只恢复你信任的 manifest。envlift 不会自动接受 conda ToS，也不会自动添加 `trusted-host`。
- 提权项需要管理员权限；不要在来源不明的配置上提权运行。
- 本工具不负责验证第三方安装包来源，`installers/` 中的文件需自行确认可信。

## 隐私与数据

- envlift 自身不收集或上传 telemetry；winget/Windows 的网络与遥测行为由各自产品决定。
- `envlift.yaml`、`reports/`、`installers/` 可能包含软件清单、路径和主机信息，默认不应提交到版本库。
- 报告默认脱敏：用户名 → `${USER}`，主机名 → `${HOST}`；GUI 下载/预览复用同一脱敏路径。
- `scan --raw` / `--raw` 不脱敏；使用后请把产物当敏感文件处理。
- 本仓库不附带本机复验截图。

## 已知限制

- 软件识别率约 8%（18/221），内置配方 33 条；识别不出的条目会进入人工处理。
- 部分配方的 `silent_args` 仍不完整，本地安装包层级可能不可用。
- F6 的历史记录曾未执行真实重启；2026-11-15 alpha 场景 4 为 operator-reported，未由仓库工件独立确认（缺 `LastBootUpTime` 证据）。
- F8「缺 winget」是模拟场景，未真实卸载 App Installer。
- 不要同时开两个实例；MVP 未做单实例检测。
- 安装目录仅对配方显式声明参数的包生效；内置 7zip 本地目录参数已在 VM 验证。Git 与 Notepad++ 的 `install_dir_arg` 与 `winget_location_arg` 均暂不开放；VS Code / Python 未复验。Node / Go / PowerShell 目前仅支持 winget 目录参数。
- Git 的 `/DIR` 后续需用官方 `/LOADINF` 方案验证；Notepad++ 的 `/D` 也需要重新确认安装包支持情况。
- winget 的 `--location` 是包级 best-effort，是否真正生效取决于具体包，必须 VM 验证。
- winget 失败后若本地配方没有 `install_dir_arg`，不会保留可执行降级方案，避免静默装到默认目录。
- 指定目录会触发重装；winget 需要 `--force` 支持，否则计划会被阻止。
- 当前版本不支持 `select{installer}` / `select{recipe}` 与 `set_location` 组合。
- 安装目录变更不提供自动卸载回滚；回滚只覆盖 PATH。
- 安装后目录未验证时报告标 `unverified`，不会显示成“已验证成功”。
- `set_location` 成功后写入本地 `envlift.relocations.yaml`；同一 target_id + 归一化目录命中账本即跳过安装，不再依赖 verify。改目录或删除账本条目才会再次重装。
- 账本同时保存请求目录 `install_dir` 与实测目录 `actual_dir`；`verified=true` 时跳过前会重新读取 `InstallLocation`，目录被移动或读取失败时重新安装并标 `unverified`。`verified=false` 或没有读取器时仍按账本跳过，但报告标“账本记录未验证/未复核”。
- 同名软件的 `InstallLocation` 候选按归一化路径去重；仍有多个不同路径时标 `unverified` 并列出候选，不取第一条。
- 账本 `version` 高于当前程序支持时拒绝读取，不按旧格式猜测。
- 重装账本是本地状态，已被 `.gitignore` 忽略，不入库。
- runtime 条目暂不做注册表目录验证；同名多版本也可能只能标 `unverified`。
- winget 缺 `--force` 时当前实现会阻止整个计划，而不只是跳过单个条目。
- conda 恢复要求本机已有可用的 conda；envlift 不自动安装 Miniconda/Anaconda。
- conda create/install 使用 `--file` 与 `--override-channels`，要求 conda 4.6+；本轮只在 26.9.3 验证，旧版 conda 未验证，不提供自动回退到 `conda env create/update`。
- `environment.yml` 不写 pip 依赖，pip 包由 Phase 2 单独恢复；两阶段都失败时环境会保留，不自动删除。
- 带 token 的私有 conda channel 会被脱敏存储，因而不能直接用于还原；需要改用不含凭据的本地/企业源。
- conda 包版本/build 必须与清单匹配才算通过；只匹配名称不算。
- base 环境仍记录完整 conda/pip 包清单，作为只读快照；它不参与自动恢复。
- 这是 early alpha，接口、配置格式和 GUI 仍可能变化。

## 文档入口

- [docs/workflow.md](docs/workflow.md)：开发、验证与禁止事项
- [docs/decisions.md](docs/decisions.md)：已锁定决策（D1–D24）
- [docs/PRD.md](docs/PRD.md)：命令、schema 与路由规格
- [docs/vm-e2e-checklist.md](docs/vm-e2e-checklist.md)：VM 验证清单与已验范围
- [SECURITY.md](SECURITY.md)：信任模型与漏洞报告
- [CONTRIBUTING.md](CONTRIBUTING.md)：贡献流程

## 开发与测试

需要 Go 1.27+ 和 Node.js 20+（仅用于 JS 语法检查）：

```powershell
go vet ./...
go test ./... -count=1
node --check internal/webui/static/app.js
gofmt -l .
```

提交前请运行 `gofmt -w .` 统一 Go 格式，并确认 `gofmt -l .` 无输出。

## 贡献 / Security / License

- 贡献前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 与 [docs/workflow.md](docs/workflow.md)。
- 安全问题请按 [SECURITY.md](SECURITY.md) 通过 GitHub Security Advisories 报告，不要公开贴利用细节。
- 本项目使用 [MIT License](LICENSE)。

