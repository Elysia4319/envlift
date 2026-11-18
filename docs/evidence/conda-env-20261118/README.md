# conda VM 原始证据（2026-11-18）

- 分支：`feature/conda-env`
- 二进制 SHA256：`2C116A484762C126C5EED2D7AB502423805B9E579AC7E4BE0FC68B785D409C50`
- CLI 语义补验二进制 SHA256：`905EDD55DB041B49E2AE069E9184DCC83619E7849BC0C26E66CFC3C41C0FEC12`
- VM：`envlift-vm-20261102`，快照 `VM-CLEAN`
- Miniconda：`26.9.3`，`C:\Miniconda3`
- 测试 prefix：`C:\My Apps\conda测试`、`C:\My Apps\conda-create测试`
- 文本输出中的用户名/主机名已替换为 `${USER}` / `${HOST}`；报告 JSON 由 envlift 统一脱敏。

## 文件

| 文件 | 内容 |
|---|---|
| `scan.txt` | 真实扫描：base + 命名环境，默认 restore 开关 false |
| `conda-envs.json` | `conda env list --json` 最终结果 |
| `dryrun-scan.txt` | 扫描配置 dry-run：base manual、命名环境默认 skipped |
| `dryrun-default.txt` | 不传 `--conda-packages` 时 skipped |
| `create1.txt` / `create-journal-1.json` / `create-report-1.json` | create + pip 成功 |
| `create2.txt` / `create-journal-2.json` / `create-report-2.json` | 第二次 apply skipped |
| `update1.txt` / `update-journal-1.json` / `update-report-1.json` | conda install pandas + pip rich 成功 |
| `update2.txt` / `update-journal-2.json` / `update-report-2.json` | 第二次 apply skipped |
| `fail1.txt` / `fail-journal.json` / `fail-report.json` / `fail-env-check.txt` | 无效包失败；环境仍可 import numpy/pandas |
| `unverified1.txt` / `unverified-journal.json` / `unverified-report.json` | 包装器让 `conda list` 返回空；真实装好 tabulate 后仍标 unverified |
| `mirror1.txt` / `mirror-files.txt` / `mirror-journal.json` / `mirror-report.json` | 镜像 profile 的临时 condarc / pip.ini |
| `try-create.txt` / `try-update.txt` | 手工确认 `conda create --override-channels` 与 `conda install --override-channels` 可用 |
| `binary.sha256` | 本次 VM 使用二进制校验值 |
| `binary-905e.sha256` | CLI 语义补验使用的重新构建二进制 |
| `explicit-none.txt` / `explicit-conda.txt` | fresh-scan 风格配置：不传 flag 时 skipped；显式 `--conda-packages=conda` 时生成 install 计划 |

## 说明

- 缺包 unverified 使用 VM 内包装器注入：`conda list` 返回 `[]`，create/install 仍委托真实 `C:\Miniconda3\Scripts\conda.exe`。
- 其余 create/install/pip 均为真实 conda 操作。
- 未验证项见 `docs/vm-e2e-checklist.md` §13，未勾选项不得视为通过。
