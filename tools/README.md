# tools/ —— 维护脚本登记表

> 新增脚本前先在本表登记一行（用途 / 触发方式 / 状态）。
> 2026-11-05 清理：移出 54 项历史调试产物（探针 `dcprobe`/`oauthprobe`/`panelappnames` 等、
> `verify_06118*`、`go_mobile*`、`mobile_scroll_test*`、29 张截图 PNG）到工作区
> `_tools_archive_1105/`，可从那里取回。判据：0 处文档/代码引用。

## 会写索引的（唯一写入器，改前先看测试）

| 脚本 | 作用 | 触发 | 状态 |
|---|---|---|---|
| `update_moo_json.py` | moo.json 跟版（读 FPK manifest → releases 节点 + 顶层下载字段，两版制） | 手动 / `moo-json.yml` | **对外契约**：20 处 skills/templates + 5 处 workflow 引用，CLI 不得随意改 |
| `update_fnpack.py` | FnDepot `fnpack.json`（V2）跟版 | 手动 | 复用 `update_moo_json.read_fpk` |
| `sync_mate_index.py` | Mate 发版一次更新三处：moo.json、fnpack.json、mate-changelog ×2 | 手动 | 有 `--check` 门禁模式 |

## 生成器（被上面或 cron 复用）

| 脚本 | 作用 | 触发 | 状态 |
|---|---|---|---|
| `update_about_changelog.py` | 生成 `changelog.md`（apps.moo 最近 N 版） | 被 sync 脚本 import；手动 `--write/--push` | 生成规则单一事实源 |
| `update_mate_changelog.py` | 生成 `mate-changelog.md`（apps.moo-mate） | 同上 | 与上者同构，用户决定暂不合并 |

## 常驻定时（crontab）

| 脚本 | 作用 | 触发 | 状态 |
|---|---|---|---|
| `sync_github_changelogs.py` | 拉 moo.json（多源取最高）→ 推 GitHub 仓根两份 changelog | `7,22,37,52 * * * *` | **生产消费方**（`about_lists_url` 未配置时读 GitHub 仓根） |
| `sync_about_changelog.py` | 同上，只推 Gitea `moo-lists` 的 `changelog.md` | `5 * * * *`（2026-11-05 由 */15 降频） | 灾难备份源，无生产消费方；带防回退守卫（只延迟不回退） |

## 对外工具（已随文档分发，改名/改参数会断外部使用）

| 脚本 | 作用 | 引用 |
|---|---|---|
| `moo_invite.py` | 邀请上游作者一键做成 Moo 源（自动识别 + 预填链接 + issue 正文） | skills 文档 |
| `gen-repo-moo-json.py` | 从仓库 releases 逆向生成 moo.json | skills / docs 共 15 处 |
| `gen-moo-json/`（Go） | 从 FPK 目录生成 moo.json | `docs/Moo应用源协议.md` |
| `wait_ci_green.py` | 打 tag 前等 GitHub main 的 Gate 绿（rc 0/1/2/3） | `skills/fpk-build-to-moo/references/release-gates.md` §5 |

## 辅助

| 脚本 | 作用 | 触发 |
|---|---|---|
| `gen_mate_default_index.py` | 生成 `mate-default-index.json`（mate 首装远程默认清单） | 手动（mate 索引变化后跑） |

## 约定

- 文档里引用本目录脚本时写完整相对路径（`tools/xxx.py`）；`skills/ci_check.py` 会校验
  `skills/**/*.md` 反引号里点名的 `tools/`、`.github/scripts/` 路径是否真实存在。
- 不在此表的 `.py` / `.sh` / `.mjs` 不应出现在本目录；一次性验证脚本放工作区，不入库。
