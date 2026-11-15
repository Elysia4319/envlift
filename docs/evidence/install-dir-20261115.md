# 安装目录功能 VM 验收（2026-11-15）

## 环境

- VM：`envlift-vm-20261102`
- 快照：`VM-CLEAN`；每个场景开始前回滚，验收结束后再次恢复
- Guest：Windows 11 Home，build `26300`，版本 `10.0.26300`
- winget：`v1.11.510`；`winget install --help` 含 `--force`
- 权限：普通会话 `ADMIN=False`；最高权限交互式计划任务 `ADMIN=True`、Session 1
- 测试二进制：`envlift 0.1.0-alpha (schema v1)`
- 二进制 SHA256：`3A5DDD63EA5949FC3A7689BF0C1B843F800A18D4F0FD69ABFCA2F5FC311A9E60`
- 代码基线：HEAD `a416826`，包含本轮未提交的 CLI `recipes.Merge` 修复

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
| 1. 本地包首次 apply | 通过 | 7-Zip NSIS：命令 `/S /D=C:\Apps\7zip`；报告 `install_dir_verified=true`、`actual_dir=C:\Apps\7zip\` | 使用真实 DisplayName `7-Zip 25.01 (x64)` |
| 2. 同目录第二次 apply | 通过 | `status=skipped`、`Reinstall=false`、未执行安装命令、账本保留 | verify `C:\Apps\7zip\7z.exe` 通过 |
| 3. 改目录第三次 apply | 通过 | 命令 `/D=C:\Apps\7zip2`；报告 verified=true；账本 `actual_dir=C:\Apps\7zip2\` | 同 target_id 账本更新 |
| 4. expect_reboot | 通过 | 第一次停在“完成后需要重启”，exit 2 并写账本；重启后第二次 `skipped` | 无害 noop 安装器 |
| 5. winget 包 | 通过（有 caveat） | 命令含 `--location "C:\Apps\7zip-winget" --force`；注册表 `InstallLocation=C:\Apps\7zip-winget\` | winget 实际装 26.06，配置期望 25.01；报告保守标 `unverified` |
| 6. 降级目录丢失 | 通过 | `fallback_plan=null`；`fallback=指定目录时不可降级...`；无 `[降级]`；标记文件 `MARKER=NO` | winget 失败后没有静默装到默认目录 |
| 7. 账本与实际不符 | 通过 | 篡改注册表后打印“账本记录与实际不符，重新安装”并执行安装；账本 `actual_dir` 更新 | 重装后 verified=true |
| 8. InstallLocation 读取失败 | 通过（安全行为） | 拒绝读取假卸载键后未按账本跳过，执行安装并标 `unverified` | 读取错误被折叠为“未找到”，日志显示“不符”而非“读取失败” |
| 9. 候选去重 | 通过 | 同路径重复键 -> 去重为 1 并 `skipped`；不同路径 -> `未验证（同名候选：...）` | 覆盖 HKLM/HKCU 同名候选 |
| 10. 账本异常 | 通过 | 损坏 YAML/version>1 均 warning 并按空账本继续；账本不可写 warning 但安装继续 | 见 `apply-ledger-dir` 输出 |
| 11. 无 LocationReader | 未验证 | CLI 固定注入 `RegistryLocationReader`，无法在不改代码/API 的情况下关闭 | 单测已覆盖 `Locations=nil` |
| 12. 回滚限制 | 部分通过 | 安装目录-only journal 无 PATH 快照，rollback 报错；7-Zip 仍安装 | PATH 回滚沿用 2026-11-03 F7 结果，本轮未重跑 |

## 关键输出

### 场景 1：7-Zip 首次安装

```text
命令 "C:\envlift\cases\7zip-local\installers\7z2501-x64.exe" /S /D=C:\Apps\7zip
[重装] 7-Zip 25.01 (x64) —— 指定了安装目录，跳过幂等检查
[执行] 7-Zip 25.01 (x64) —— "C:\envlift\cases\7zip-local\installers\7z2501-x64.exe" /S /D=C:\Apps\7zip
[目录] 7-Zip 25.01 (x64) —— 已验证（C:\Apps\7zip\）
执行完成: 成功 1 · 跳过 0 · 失败 0
```

报告 `restore-20261103-102726.json`：

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

报告 `restore-20261103-102804.json`：`status=skipped`，`reinstall` 为空（false），`install_dir_verified=true`。

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

报告 `restore-20261103-102856.json`：`status=skipped`。

### 场景 5：winget 7-Zip

```text
命令 winget install --id 7zip.7zip --exact --silent --accept-package-agreements --accept-source-agreements --location "C:\Apps\7zip-winget" --force
[目录] 7-Zip 25.01 (x64) —— 未验证（未找到 InstallLocation）
```

注册表实际：

```text
REQ_7ZIP=True
ENTRY=7-Zip|7-Zip 26.06 (x64)|26.06|LOC=C:\Apps\7zip-winget\
```

结论：winget 接受 `--location`，实际落点为请求目录；但 winget 安装了 26.06，配置期望 25.01，envlift 的 DisplayName 精确匹配未命中，报告保守标 `unverified`，没有把未验证写成已验证。

### 场景 6：降级目录丢失

计划：

```text
command       = winget install --id Vendor.DoesNotExist ... --location "C:\Apps\fallback" --force
fallback      = 指定目录时不可降级到本地包（配方未声明本地目录参数）
fallback_plan = null
```

apply：

```text
[执行] Fallback App —— winget install --id Vendor.DoesNotExist ...
执行完成: 成功 0 · 跳过 0 · 失败 1
```

没有 `[降级]` 行；`C:\envlift\fallback-ran.txt` 标记文件不存在（`MARKER=NO`）。

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

## 证据文件

报告与 journal 见 `docs/evidence/install-dir-20261115/`：

- `restore-20261103-102726.*`：场景 1
- `restore-20261103-102804.*`：场景 2
- `restore-20261103-102905.*`：场景 3
- `restore-20261103-102700.*` / `restore-20261103-102856.*`：场景 4
- `restore-20261103-102806.*`：场景 5
- `restore-20261103-102743.json`：场景 6
- `restore-20261103-102814.*`：场景 7
- `restore-20261103-103143.*`：场景 8
- `restore-20261103-103002.*`：场景 9
- `restore-20261103-103259.*`：场景 10
- `7zip-102646.json` / `7zip-102812.json` / `7zip-103001.json` / `7zip-103257.json` / `readfail-103135.json`：关键 journal

## 未完成

- 场景 11（无 LocationReader）无法从 CLI 关闭读取器，未做真实 VM 验证。
- 场景 12 的 PATH 回滚未在本轮重跑，沿用 2026-11-03 F7 结果。
- Git for Windows 2.56.0 的 `/DIR` 仍未生效；本轮改用 7-Zip NSIS 完成本地目录验收。
- winget 版本漂移导致的 `unverified` 是保守结果，不是安装目录失败；后续可考虑按配方 `match_display` 或 winget 包名辅助匹配。
