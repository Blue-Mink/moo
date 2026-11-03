# 发布门禁：脱敏 / 代码审计 / CI / E2E

> 顺序固定：**范围冻结 → 脱敏 → 独立代码审计 → CI → 本地 E2E → 真机 E2E → 发布**。
> 任何一步失败都回到对应步骤；变更集一变，后面的门禁全部重跑。**不得自审自批**。

## 0. 范围冻结
- 先列本轮允许改的文件（绝对路径/清单），`git diff --stat` 与清单逐项对账；出现范围外文件 = FAIL。
- 记录回滚点：上一版 commit / tag / FPK / 数据备份路径。
- 禁止 `git add -A`；提交用显式文件清单。

## 1. 脱敏门（0 命中才放行）
- 扫描对象：`SKILL.md`、`README.md`、`INSTALL.md`、`references/`、`templates/`、`scripts/` 以及要发布的 release 正文/索引。
- 模式至少覆盖：真实内网 IPv4、内网域名、账号/仓库名、个人邮箱、GitHub token 前缀（ghp / github_pat / gho / ghs，各自后接下划线）/ API key（sk- 前缀）、私钥块、`password/token/secret/api_key` 赋值、私有工作区路径、未发布构件的 64-hex 哈希。
- 允许：RFC5737 `192.0.2.x`、`example.com`、`127.0.0.1`、占位符 `<...>`；**真实 `moo.json` 里的可访问地址是例外，但不得把它贴进公开文档**。
- 本技能：`python3 skills/ci_check.py`（内含同源模式；从仓库根执行，根目录含 `skills/`、`tools/`、`docs/`）。
- 命中即修后重扫；不得用 `# sanitize:ignore` 掩盖真实值。

## 2. 独立代码审计门
由**独立子 agent/审计人**执行，主会话不代审。检查清单：
- **范围**：只改清单内文件；无调试残留（probe 脚本、`console.log`、`kbDebug`、临时开关、TODO/FIXME 入库）。
- **凭据/地址**：无硬编码口令、token、私钥、内网 IP/域名、个人邮箱；示例用占位符。
- **鉴权/加密**：新增/修改的鉴权、签名、验签、token 传递逐条复核；不得为调试放宽鉴权。
- **数据**：卸载/升级/迁移的删除边界；是否只删自己的目录；失败是否可回滚。
- **发布**：资产矩阵、`.sha256`/`.minisig`、tag、索引顺序、同版本规则。
- **文档**：命令与真实工具一致（`fnpack 1.2.4`、`appcenter-cli install-fpk --env`）；链接/锚点可达；无过期断言。
- 输出 `AUDIT_<date>.md`，结论只能是 **APPROVED / REJECTED**，附证据（命令输出/文件行号）。变更集一变必须重审。

## 3. CI 门
### M 技能（moo 仓内）
- `.github/workflows/skills-check.yml` → `python3 skills/ci_check.py`
- `ci_check.py` 已覆盖：frontmatter、链接、安装器 dry-run、各技能 unittest、**E2E local**、敏感模式扫描。
- 修改 `ci_check.py` 或新增技能测试后，必须本地跑一遍 `python3 skills/ci_check.py`。
- 当前本地结果：单测全绿；E2E PASS；sanitize 由 `ci_check.py` 覆盖；`git diff --check` 0（数量以实际输出为准，不写死）。

## 4. E2E 门
### 4.1 本地 E2E（每次 CI 必跑）
`python3 tests/e2e_local.py`：
1. 有 `fnpack` 时真实 `fnpack build -d` 产出 FPK；没有时用合成 FPK 兜底（CI 无 fnpack 也能跑）。
2. 校验 `manifest.checksum == md5(app.tgz)`、`app.tgz` 无外层 `app/` 前缀、根图标 ≥512。
3. 生成 `moo.json` + `fnpack.json`，本地 HTTP 服务后回读索引/FPK/图标，核对 sha256/size/字段。
4. 全程临时目录，不写设备、不联网。

### 4.2 真机 E2E（可选，仅一次性测试设备）
见 [../tests/e2e_device.md](../tests/e2e_device.md)。红线：
- 只在**可丢弃的测试设备**跑；不碰生产机/测试机上的全量生产版。
- 应用名用 `e2e-` 前缀；先备份 `@appdata`、快照数据指纹；跑完清理自己创建的测试应用/源。
- 不把真实口令/IP 写进脚本；凭据运行时注入。
- 至少覆盖：首装（含 `--env`）、配置改端口、卸载保留/删除数据、升级四步、失败路径、商店添加源/安装/更新。

## 5. 发布门
- 用户验收 + 明确推送口令；**推送是独立动作**，前置门禁通过 ≠ 授权推送。
- 自更新应用：每架构 `FPK + .sha256 + .minisig`；资产矩阵、tag、索引顺序、兜底三 URL、asset digest 全过。
- 发布后回读：`/api/about` 版本、bundle 指纹、数据零回归、icon 尺寸、release 资产 sha。

## 6. 证据记录
| 门 | 命令/产出 | 结果 |
|---|---|---|
| 范围 | `git diff --stat` + 清单 | 待填 |
| 脱敏 | `tests/sanitize.py` / `skills/ci_check.py` | 待填 |
| 审计 | `AUDIT_<date>.md` | 待填 |
| CI | workflow / `tests/ci.sh` | 待填 |
| E2E | `tests/e2e_local.py` + 真机记录 | 待填 |
| 发布 | release 资产/索引回读 | 待填 |
