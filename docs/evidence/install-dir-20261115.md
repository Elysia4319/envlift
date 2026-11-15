# 安装目录功能 VM 验收（2026-11-15）

## 环境

- VM：`envlift-vm-20261102`
- 快照：`VM-CLEAN`；每个场景开始前回滚，验收结束后再次恢复
- Guest：Windows 11 Home，build `26300`，版本 `10.0.26300`
- winget：`v1.11.510`；`winget install --help` 含 `--force`
- 权限：普通会话 `ADMIN=False`；最高权限交互式计划任务 `ADMIN=True`、Session 1
- 最终验收二进制：`envlift 0.1.0-alpha (schema v1)`
- 验收代码提交：`2848e01`（P1 修复）；文档提交随后
- 二进制 SHA256：`6CFCA653D4CD931BA2E05917B47CE3D6C1703295D4D1AA87390C244151049765`

## 最终验收二进制

从 P1 修复提交 `2848e01` 重新构建：

```text
go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=0.1.0-alpha" -o tmp\envlift-accept.exe ./cmd/envlift
```

- 版本：`envlift 0.1.0-alpha (schema v1)`
- SHA256：`6CFCA653D4CD931BA2E05917B47CE3D6C1703295D4D1AA87390C244151049765`
- `go version -m` 显示 `-buildvcs=false`，模块版本 `(devel)`，无 `vcs.modified`。

历史二进制（**非最终验收二进制**）：

- `a416826` + dirty：SHA `3A5DDD63...`，`vcs.modified=true`。
- `61b9ffd` + `-buildvcs=false`、无版本注入：SHA `3A020C19...`，版本 `0.1.0-dev`。
- `ec4e7e9` 的旧 alpha：SHA `455C7A...`，已被 P1 修复替代。
- 旧结果仅作历史参考，不能作为最终 alpha 验收结论。

## 环境限制

- `D:` 是 UDF 光驱（DriveType=5），`E:` 是 CDFS 光驱；只有 `C:` 可写。
- 因此安装目录验收使用 `C:\Apps\...`，不再使用 `D:\Apps\...`。
- Git for Windows 2.56.0 在提权会话中未按 `/DIR` 落点，已单独记录，不作为本轮通过项。

## Git 配方核查（C）

官方 silent install 页面（`https://gitforwindows.org/silent-or-unattended-installation`）：

- 推荐的静默安装方式是 `/VERYSILENT /NORESTART /NOCANCEL /SP-`，目录通过
  `/LOADINF="git_options.ini"` 的 `[Setup] Dir=C:\Program Files\Git` 指定。
- 页面没有给出 `/ALLUSERS` 的官方说明；自定义参数表也没有 `ALLUSERS`。
- 官方 `installer/install.iss` 使用 `PrivilegesRequired=lowest`（另有 `none` 分支），
  未声明 `PrivilegesRequiredOverridesAllowed=commandline`。

结论：没有官方依据支持给 Git 配方追加 `/ALLUSERS`，本轮不修改 Git 配方。Git 的
`/DIR` 未落点问题保留为待查项，建议后续用官方 `/LOADINF` 方案单独验证。

## 场景结果

| 场景 | 结果 | 关键证据 | 备注 |
|---|---|---|---|
| 1. 本地包首次 apply | 通过（alpha 6CFCA6） | builtin 7zip，无 verify，含空格目录：`/S /D=C:\My Apps\7zip`；报告 `install_dir_verified=true`、`actual_dir=C:\My Apps\7zip\` | `verify_command_present=false` |
| 2. 同目录第二次 apply | 通过（alpha 6CFCA6） | `status=skipped`、`Reinstall=false`、未执行安装命令；账本命中即跳过 | 不依赖 verify |
| 3. 改目录第三次 apply | 通过（alpha 6CFCA6） | `/D=C:\My Apps\7zip2`；报告 verified=true；账本 `actual_dir=C:\My Apps\7zip2\` | 同 target_id 账本更新 |
| 4. expect_reboot | 通过（alpha 6CFCA6） | 第一次停在“完成后需要重启”，exit 2 并写账本；重启后第二次账本命中跳过并标 unverified | 账本 verified=false 也跳过 |
| 5. winget 包 | 通过（alpha 6CFCA6，版本漂移 unverified） | 第一次 apply 完成、注册表落点为请求目录；第二次账本命中跳过 | 两次报告均 unverified；winget 装 26.06、配置 25.01 |
| 6. 降级目录丢失 | 历史通过（alpha 455C，未用 6CFCA6 复跑） | `fallback_plan=null`；无 `[降级]`；标记文件 `MARKER=NO` | 不阻塞本轮 1–5 |
| 7. 账本与实际不符 | 历史通过（旧二进制，非最终） | 篡改注册表后打印“账本记录与实际不符，重新安装”并执行安装；账本 `actual_dir` 更新 | 未用 alpha 复跑 |
| 8. InstallLocation 读取失败 | 历史通过（旧二进制，非最终） | 拒绝读取假卸载键后未按账本跳过，执行安装并标 `unverified` | 读取错误被折叠为“未找到” |
| 9. 候选去重 | 历史通过（旧二进制，非最终） | 同路径重复键 -> 去重为 1 并 `skipped`；不同路径 -> `未验证（同名候选：...）` | 未用 alpha 复跑 |
| 10. 账本异常 | 历史通过（旧二进制，非最终） | 损坏 YAML/version>1 均 warning 并按空账本继续；账本不可写 warning 但安装继续 | 未用 alpha 复跑 |
| 11. 无 LocationReader | 未验证 | CLI 固定注入 `RegistryLocationReader`，无法在不改代码/API 的情况下关闭 | 单测已覆盖 `Locations=nil` |
| 12. 回滚限制 | 部分通过（旧二进制，非最终） | 安装目录-only journal 无 PATH 快照，rollback 报错；7-Zip 仍安装 | PATH 回滚沿用 2026-11-03 F7 结果，本轮未重跑 |
| 13. 重装失败报告 | 历史通过（alpha 455C，未用 6CFCA6 复跑） | `fail-setup.exe` 退出 1；报告 `status=failed`、`failure_reason=simulated installer failure: exit 1`；无账本 | 未出现 `succeeded` |

最终 alpha 6CFCA6 验收通过项为场景 1–5；场景 6/13 为 alpha 455C 历史结果；
场景 7–10 为旧二进制历史结果；场景 11 未验证；场景 12 部分通过。

## P1 修复复验（alpha 6CFCA6）

### 账本命中即跳过

builtin 7zip 没有 `Verify`，使用含空格目录 `C:\My Apps\7zip`：

- 第一次 apply：安装成功，报告 `install_dir_verified=true`、`actual_dir=C:\My Apps\7zip\`，`verify_command_present=false`。
- 第二次 apply：`[跳过] 7-Zip 25.01 (x64) —— 账本命中，跳过安装`，没有执行安装命令。
- 第三次 apply：改目录到 `C:\My Apps\7zip2`，重新安装，账本 `actual_dir` 更新。

expect_reboot 场景的账本 `verified=false`：

```text
[目录] Noop Reboot —— 未验证（账本记录未验证）
[跳过] Noop Reboot —— 账本命中，跳过安装
```

winget 场景：

```text
第一次：执行 winget install ... --location "C:\Apps\7zip-winget" --force，报告 unverified
第二次：[目录] 7-Zip 25.01 (x64) —— 未验证（账本记录未验证）
        [跳过] 7-Zip 25.01 (x64) —— 账本命中，跳过安装
```

### 含空格 /D

`SplitCommand` 对 `/D=` 取剩余整行。VM 实际命令：

```text
"...7z2501-x64.exe" /S /D=C:\My Apps\7zip
```

argv 中 `/D=C:\My Apps\7zip` 是单个参数，7-Zip 安装到请求目录。

### NPP builtin 无 verify

builtin `notepadplusplus` 没有 `Verify`，安装包 `npp.8.8.1.Installer.exe`，请求目录
`C:\My Apps\Notepad++`：

- 第一次 apply：命令为 `/S /D=C:\My Apps\Notepad++`，报告 `unverified`；
  实际安装到 `C:\Program Files\Notepad++`，请求目录未生效。
- 第二次 apply：账本 `verified=false`，命中账本跳过安装并标 `unverified`。

结论：NPP 的 builtin `/D` 在当前安装包上未生效，不能宣称本地目录可用；本轮不改配方。

### 模板校验

`validateInstallDirTemplate` 现在要求非空模板恰好包含一个 `{install_dir}`；
单测覆盖 0 个、1 个、2 个占位符。

## 关键输出

### 场景 1：7-Zip 首次安装

```text
命令 "C:\envlift\cases\7zip-local\installers\7z2501-x64.exe" /S /D=C:\Apps\7zip
[重装] 7-Zip 25.01 (x64) —— 指定了安装目录，跳过幂等检查
[执行] 7-Zip 25.01 (x64) —— "C:\envlift\cases\7zip-local\installers\7z2501-x64.exe" /S /D=C:\Apps\7zip
[目录] 7-Zip 25.01 (x64) —— 已验证（C:\Apps\7zip\）
执行完成: 成功 1 · 跳过 0 · 失败 0
```

alpha 报告 `alpha-7zip-first.json`：

```text
status               = succeeded
tier                 = local
command              = "...7z2501-x64.exe" /S /D=C:\Apps\7zip
install_dir          = C:\Apps\7zip
install_dir_verified = true
install_dir_actual   = C:\Apps\7zip\
reinstall            = true
```

账本：

```yaml
version: 1
relocations:
  - target_id: d9b22a3aae5c
    install_dir: C:\Apps\7zip
    actual_dir: C:\Apps\7zip\
    verified: true
```

### 场景 2：同目录第二次

```text
[跳过] 7-Zip 25.01 (x64) —— 已完成
执行完成: 成功 0 · 跳过 1 · 失败 0
```

alpha 报告 `alpha-7zip-second.json`：`status=skipped`，`reinstall` 为空（false），`install_dir_verified=true`。

### 场景 3：改目录

```text
命令 "...7z2501-x64.exe" /S /D=C:\Apps\7zip2
[重装] 7-Zip 25.01 (x64) —— 指定了安装目录，跳过幂等检查
[目录] 7-Zip 25.01 (x64) —— 已验证（C:\Apps\7zip2\）
执行完成: 成功 1 · 跳过 0 · 失败 0
```

账本更新为：

```yaml
install_dir: C:\Apps\7zip2
actual_dir: C:\Apps\7zip2\
verified: true
```

### 场景 4：expect_reboot

第一次 apply：

```text
[执行] Noop Reboot —— "...noop-reboot-setup.exe" /noop /DIR=C:\Apps\noop
停止原因: 条目 Noop Reboot 完成后需要重启，请重启后重新运行 envlift restore --apply
```

账本已写入 `install_dir=C:\Apps\noop`、`actual_dir` 为空、`verified=false`。

重启后第二次 apply：

```text
[跳过] Noop Reboot —— 已完成
执行完成: 成功 0 · 跳过 1 · 失败 0
```

alpha 报告 `alpha-noop-second.json`：`status=skipped`。

### 场景 5：winget 7-Zip

alpha 二进制 dry-run 命令：

```text
命令 winget install --id 7zip.7zip --exact --silent --accept-package-agreements --accept-source-agreements --location "C:\Apps\7zip-winget" --force
```

alpha 二进制 envlift apply 成功完成：

```text
[重装] 7-Zip 25.01 (x64) —— 指定了安装目录，跳过幂等检查
[执行] 7-Zip 25.01 (x64) —— winget install --id 7zip.7zip ... --location "C:\Apps\7zip-winget" --force
[目录] 7-Zip 25.01 (x64) —— 未验证（未找到 InstallLocation）
执行完成: 成功 1 · 跳过 0 · 失败 0 · 阻止 0 · PATH 新增 0
```

报告 `alpha-winget-apply.json`：

```text
status               = succeeded
tier                 = winget
install_dir          = C:\Apps\7zip-winget
install_dir_verified = false
install_dir_actual   = 空
reinstall            = true
```

注册表实际：

```text
REQ_7ZIP=True
ENTRY=7-Zip|7-Zip 26.06 (x64)|26.06|LOC=C:\Apps\7zip-winget\
```

账本 `alpha-winget-ledger.yaml`：`install_dir=C:\Apps\7zip-winget`、`verified=false`、无 `actual_dir`。

结论：

- winget 接受 `--location`，实际落点为请求目录，`--force` 也在命令中。
- envlift apply 完成，报告 `unverified`；原因是 winget 装 26.06、配置期望 25.01，
  DisplayName 精确匹配未命中，`install_dir_actual` 为空。这是保守结果，不是把未验证写成已验证。
- 场景 5 记为“通过（alpha，版本漂移 unverified）”。

### 场景 6：降级目录丢失

alpha 计划 `alpha-fallback-plan.json`：

```text
command       = winget install --id Vendor.DoesNotExist ... --location "C:\Apps\fallback" --force
fallback      = 指定目录时不可降级到本地包（配方未声明本地目录参数）
fallback_plan = null
```

alpha apply：

```text
[执行] Fallback App —— winget install --id Vendor.DoesNotExist ...
执行完成: 成功 0 · 跳过 0 · 失败 1
```

没有 `[降级]` 行；`C:\envlift\fallback-ran.txt` 标记文件不存在（`MARKER=NO`）。
报告 `alpha-fallback-report.json`：`status=failed`、`tier=winget`，失败原因是 winget 找不到
`Vendor.DoesNotExist`，没有执行本地降级。

### 场景 7：账本与实际不符

篡改注册表后：

```text
[目录] 7-Zip 25.01 (x64) —— 账本记录与实际不符，重新安装
[执行] 7-Zip 25.01 (x64) —— "...7z2501-x64.exe" /S /D=C:\Apps\7zip
[目录] 7-Zip 25.01 (x64) —— 已验证（C:\Apps\7zip\）
```

账本 `actual_dir` 更新为 `C:\Apps\7zip\`。

### 场景 8：读取失败

拒绝读取假卸载键后：

```text
[目录] Noop Readfail —— 账本记录与实际不符，重新安装
[执行] Noop Readfail —— "...noop-readfail-setup.exe" /noop /DIR=C:\Apps\readfail
[目录] Noop Readfail —— 未验证（未找到 InstallLocation）
```

安全行为正确：没有按账本跳过，执行了安装并标 `unverified`。诊断信息有限：`InstallLocations` 把读取错误折叠为零候选，所以显示“不符”而不是“读取失败”。

### 场景 9：候选去重

同一路径重复键：

```text
[跳过] 7-Zip 25.01 (x64) —— 已完成
```

不同路径：

```text
[目录] 7-Zip 25.01 (x64) —— 账本记录与实际不符，重新安装
[目录] 7-Zip 25.01 (x64) —— 未验证（同名候选：C:\Apps\7zip\ | C:\Apps\other）
```

### 场景 10：账本异常

损坏 YAML：

```text
警告: 解析重装账本 ledger-corrupt.yaml 失败: yaml: line 1: did not find expected ',' or ']'（按空账本继续）
```

version>1：

```text
警告: 账本 ledger-version2.yaml 版本为 2，账本版本高于本程序支持（最高 1），请升级 envlift（按空账本继续）
```

不可写：

```text
警告: 写入重装账本失败: 替换重装账本失败: rename ledger-dir.tmp ledger-dir: Access is denied.
执行完成: 成功 1 · 跳过 0 · 失败 0
```

### 场景 12：回滚限制

```text
journal 中没有 PATH 快照，无法回滚: reports\.envlift-journal-20261103-103257.json
EXIT=1
SEVENZIP=YES
```

安装目录变更没有被卸载回滚；7-Zip 仍安装。PATH 回滚在 2026-11-03 F7 已验证，本轮未重跑。

### 场景 13：重装失败报告

本地失败安装器输出：

```text
[重装] Fail Install —— 指定了安装目录，跳过幂等检查
[执行] Fail Install —— "C:\envlift\cases\fail-install\installers\fail-setup.exe" /S /D=C:\Apps\fail
执行完成: 成功 0 · 跳过 0 · 失败 1 · 阻止 0 · PATH 新增 0
停止原因: 条目 Fail Install 执行失败（退出码 1），已停止并保留现状
```

报告 `restore-20261103-104510.json`（alpha 二进制）：

```text
name           = Fail Install
status         = failed
tier           = local
failure_reason = simulated installer failure: exit 1
install_dir    = C:\Apps\fail
reinstall      = true
```

账本文件不存在（失败不写入成功记录）。

旧的 `restore-20261103-102644.*` / `fail-102643.json` 来自 `0.1.0-dev` 二进制，
仅作历史参考，不是最终验收结果。

## 证据文件

报告与 journal 见 `docs/evidence/install-dir-20261115/`：

- `p1-7zip-first.json` / `p1-7zip-second.json` / `p1-7zip-dir2-apply.txt`：场景 1–3（alpha 6CFCA6）
- `p1-noop-first.json` / `p1-noop-second.json`：场景 4（alpha 6CFCA6）
- `p1-winget-apply1.txt` / `p1-winget-apply2.txt` / `p1-winget-ledger.yaml`：场景 5（alpha 6CFCA6）
- `p1-npp-first.json` / `p1-npp-second.json` / `p1-npp-check.txt`：NPP builtin 无 verify 复跑
- `alpha-7zip-first.json` / `alpha-7zip-second.json` / `alpha-7zip-dir2.json` / `alpha-noop-first.json` / `alpha-noop-second.json`：alpha 455C 历史结果
- `alpha-winget-dryrun.txt` / `alpha-winget-apply.json` / `alpha-winget-apply.md` / `alpha-winget-check.txt` / `alpha-winget-ledger.yaml` / `alpha-winget-source-update.txt`：场景 5 alpha 455C 历史结果
- `alpha-winget-apply-failure.txt` / `alpha-winget-net.txt`：场景 5 早期网络失败记录（历史）
- `alpha-fallback-plan.json` / `alpha-fallback-report.json` / `alpha-fallback-apply.txt` / `alpha-fallback-check.txt`：场景 6（alpha）
- `restore-20261103-102726.*` / `102804.*` / `102905.*` / `102700.*` / `102856.*` / `102806.*`：旧二进制历史结果（非最终）
- `restore-20261103-102743.json`：场景 6
- `restore-20261103-102814.*`：场景 7
- `restore-20261103-103143.*`：场景 8
- `restore-20261103-103002.*`：场景 9
- `restore-20261103-103259.*`：场景 10
- `restore-20261103-104510.*`：场景 13（alpha 455C 历史，未用 6CFCA6 复跑）
- `restore-20261103-102644.*`：场景 13 历史 dev 结果（非最终）
- `7zip-102646.json` / `7zip-102812.json` / `7zip-103001.json` / `7zip-103257.json` / `readfail-103135.json`：关键 journal
- `fail-104507.json`：场景 13 alpha journal
- `fail-102643.json`：场景 13 历史 dev journal（非最终）

## 未完成

- 场景 11（无 LocationReader）：CLI 固定注入 `RegistryLocationReader`，无法在不改代码/API 的情况下关闭；
  未做真实 VM 验证，仅单测覆盖 `Locations=nil`。checklist 第 11 项保持未勾选。
- 场景 12（回滚限制）：已确认安装目录变更不提供自动卸载回滚；PATH 回滚本轮未重跑，
  沿用 2026-11-03 F7 结果。checklist 第 124 行保持未勾选。
- Git for Windows 2.56.0 的 `/DIR` 仍未生效；Notepad++ 8.8.1 的 `/D` 在 VM 中也未按请求目录落点。
  本轮改用 7-Zip NSIS 完成本地目录验收。
- 场景 5 alpha 已完成；winget 版本漂移（装 26.06、配置 25.01）导致报告 `unverified`。
  这是保守结果，不是安装目录失败；后续可考虑按配方 `match_display` 或 winget 包名辅助匹配。
- 可选诊断改进（E）未做：`InstallLocations` 仍会把读取错误折叠为零候选；
  场景 8 的安全行为正确，但日志显示“不符”而不是“读取失败”。
