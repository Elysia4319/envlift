# envlift

Move a Windows development environment from one machine to another.

> **English summary:** envlift scans installed software, PATH entries, and language runtimes on Windows, then builds a reviewable restore plan. It can install software and modify PATH, so use a VM or snapshot first and only run trusted configuration files. This is an early alpha for Windows 10/11 (amd64), released under the MIT License.

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

# 生成还原计划，默认 dry-run，不改系统
.\envlift.exe restore --config envlift.yaml

# 真实执行；需要输入大写的 APPLY 二次确认
.\envlift.exe restore --config envlift.yaml --apply

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

## 安全边界

- envlift **会安装软件、修改用户级/系统级 PATH**，这不是纯只读工具。
- 首次使用请放在 VM、测试机或系统快照中；确认回滚路径后再用于真实机器。
- 只运行自己信任的 `envlift.yaml`。配置中的命令会按计划执行。
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
- F6 的真实重启续跑尚未执行；状态机与幂等跳过已验证。
- F8「缺 winget」是模拟场景，未真实卸载 App Installer。
- 不要同时开两个实例；MVP 未做单实例检测。
- 这是 early alpha，接口、配置格式和 GUI 仍可能变化。

## 文档入口

- [docs/workflow.md](docs/workflow.md)：开发、验证与禁止事项
- [docs/decisions.md](docs/decisions.md)：已锁定决策（D1–D22）
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

