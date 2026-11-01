# moo.json 一键维护指南（给应用源作者）

> 适用：仓库根目录已有 `moo.json`（Moo 应用源协议），需要随每次发版更新。
> 目标：**发完 Release 跑一条命令**（或什么都不用做，交给 CI），索引自动跟到新版本。

## 为什么必须维护

Moo 只做三件事：读你仓库里的 `moo.json` → 按 `download_url` 直连下载 → 用 `sha256` 校验字节。

- 只发 Release 不改索引 → 用户端**看不到更新**（一直停在旧版）；
- 版本号改了、`sha256`/`size` 没跟 → 用户装包时**校验失败被拒**（fail-closed）；
- 版本号与 Release tag 不一致 → 详情页与更新判断错乱。

## 方式一：本地一条命令（推荐）

```bash
# 1) 构建出 FPK，例如 dist/myapp_1.2.3_x86.fpk（双架构就构建两个）
# 2) 更新索引（脚本自己算 sha256、字节数，并从包内 manifest 读 appname/platform）
python3 tools/update_moo_json.py --version 1.2.3 --fpk dist/myapp_1.2.3_x86.fpk \
        --changelog "本版更新内容…"

# 双架构一次搞定（manifest.platform 的 x86_64/aarch64 会自动归一成 x86 / arm）
python3 tools/update_moo_json.py --version 1.2.3 \
        --fpk dist/myapp_1.2.3_x86.fpk dist/myapp_1.2.3_arm.fpk --changelog "本版更新内容…"

# 3) 检查 diff 后提交
git diff moo.json && git add moo.json && git commit -m "chore(moo.json): 1.2.3"
```

没有脚本时先取一份（零依赖，Python 3.8+）：

```bash
curl -fsSL https://raw.githubusercontent.com/Blue-Mink/moo/main/tools/update_moo_json.py -o /tmp/update_moo_json.py
```

默认行为：按 `https://github.com/<owner>/<repo>/releases/download/v<version>/<文件名>` 拼直链
（仓库与 tag 前缀可用 `--repo`、`--tag-prefix` 改），并**只保留最近 2 个版本节点**（`--keep 0` 全留）。

安全约束：`--version` 必须与包内 `manifest.version` 一致，不一致会直接报错
（确实要写不同版本号时加 `--allow-version-mismatch`）；同一架构在一次调用里出现两次也会报错。

两个辅助开关：`--dry-run` 只打印 diff 不落盘；`--check` 只校验（文件已是最新则退出码 0，需要更新则 1，适合放进 CI 做「索引是否跟版」的巡检）。脚本幂等——同一输入重复运行不会产生改动（内容没变时不刷新 `updated_at`），写入用「临时文件 + 原子替换」，中途被杀也不会留下半截 JSON。

另外两个安全开关：

- **`--allow-downgrade`**：默认拒绝「写入比索引里已有版本更旧」的版本。防的是手动 `workflow_dispatch` 传错旧 tag、给旧分支补发版时把索引写回旧版（顶层虽取最大值不降级，但旧节点会被重新加回）。确实要给老版本线补一次包时才加。
- **`--sync-meta`**：把包内 `manifest` 的展示字段（`display_name` / `desc` / `license` / `install_type` / `os_min_version`→`min_fnos`）同步进索引。默认**不动**这些字段（它们常被作者手工润色过）；改了 manifest 里的简介/显示名又想让它跟到索引时才加。`manifest.desc` 是多行三引号块时脚本只能读首行片段，会自动跳过并在输出里提示。

## 方式二：交给 GitHub Actions（发版后全自动）

把 `moo-autoupdate.yml` 复制到自己仓库的 `.github/workflows/moo-json.yml`，
按需改 `FPK_GLOB` / `KEEP` / `BRANCH`。之后每次 **published release**：

1. 从 release 下载 FPK；2. 跑同一个更新脚本；3. 有变化就自动提交 `moo.json`。

工作流自带的两个门禁：

- **预发布不进门**：`release.prerelease == true` 时整个 job 跳过（Moo 收录稳定版，否则用户会被提示更新却装到测试包）。
- **下载字节对账**：下载后用 `gh api` 取该 release 的 `asset.digest`，与本地 `sha256sum` 逐包比对，不一致直接 fail（防中间镜像换包/截断）。老 release 没有 digest 字段时只告警，不阻断。

### CI 侧兜底（三层，任一可用即可）

- **脚本来源**：本仓 `tools/update_moo_json.py` 优先 → GitHub raw → jsDelivr → gh-proxy；三个网络通道都带超时，不会把 job 挂死。想完全离线：把脚本 vendor 进自己仓库的 `tools/`，workflow 会直接用本地副本。
- **推送**：首次 `git push` 失败会自动 `git pull --rebase` 重试一次；仍失败会明确提示是分支保护还是冲突（分支保护请放行 bot，或改成提 PR）。
- **资产缺失**：release 里没有匹配 `FPK_GLOB` 的包、或同架构出现两个包（full/lite），脚本会**报错停止**而不是猜一个写进去——这时改 `FPK_GLOB` 收窄到主包即可。

## 方式三：手改（最少依赖）

在 `apps.<appname>.releases` 里加一个节点（其余字段不用动）：

```json
"1.2.3": {
  "changelog": "本版更新内容",
  "updated_at": "2026-11-01T15:00:00+08:00",
  "packages": {
    "x86": {
      "download_url": "https://github.com/OWNER/REPO/releases/download/v1.2.3/myapp_1.2.3_x86.fpk",
      "sha256": "<sha256sum 结果>",
      "size": 12345678
    }
  }
}
```

> 注意：**包级 `size` 单位是字节**；条目级 `size` 才是 MB。写反了会显示成 `0.0 MB` 或 `12345678.0 MB`。

## 发版三步曲（建议写进项目 README）

1. 改 `VERSION` / `CHANGELOG`；
2. 发布 GitHub Release，上传 FPK（文件名带版本号）；
3. 跑一次 `update_moo_json.py`（或让 CI 跑），确认 `moo.json` 已提交。

## 排查

| 现象 | 原因 |
|---|---|
| 用户端一直显示旧版本 | `moo.json` 的 `releases` 没加新节点，或没提交/没推 |
| 安装报 sha256 不匹配 | 索引里的 sha 不是这个包的（重跑脚本，别手抄） |
| 详情页体积是 0.0 MB / 天文数字 | 条目级 `size`（MB）与包级 `size`（字节）写混了 |
| 「应用不存在」/ 装完不更新 | `apps` 的键必须与包内 `manifest` 的 `appname` 完全一致 |
| 脚本报「多个 FPK 解析出同一架构」 | release 里同时有 full / lite 两个包 → 只传主包（CI 里收窄 `FPK_GLOB`） |
| CI 推送失败（protected branch） | 分支保护不允许 Actions 直接推 → 放行该 bot，或改成提 PR |
| 第一次跑 diff 很大 | 脚本会把 JSON 统一重排成 2 空格缩进，属正常；之后 diff 只含版本节点 |
| 版本号带 `latest-` / `release-` 前缀 | 这类串不可比，脚本只当历史最旧，永远不会被选成「最新」——发版请用数字版本号 |
| 报 `manifest.appname 不一致` | 一次调用只能更新一个应用；确认 `--appname` 写对（索引键必须等于包内 appname）。确实要写到别的键下才加 `--allow-appname-mismatch` |
| 自建 Gitea / GitLab 源直链拼错 | `--repo` 传完整地址（`https://git.example.com/owner/repo`），脚本按 `<repo>/releases/download/<tag>/<文件>` 生成，不再硬编码 github.com |
| 看到 `moo.json.tmp.<pid>` 残留 | 上次写入被强杀；原文件仍完整，删掉残留临时文件即可（正常路径会自动清理） |
| CI 报「取不到 update_moo_json.py」 | 三个网络通道都不通；把脚本 vendor 到本仓 `tools/` 即走本地副本，完全离线可用 |
| Release tag 不规律（`release-1.2.3`） | 加 `--tag release-1.2.3`，索引版本仍取包内 `manifest.version` |
| 报「同版本换包被拒」 | 已发布版本不能静默换包（用户缓存校验和会失配）→ 递增版本号；确认要覆盖才加 `--allow-republish` |
| Windows 上索引是 UTF-16 / 整文件 diff | 索引必须是 UTF-8（BOM 会自动去除）；CRLF 会原样保留，不会整文件重排 |
| `--fpk "dist/*.fpk"` 带引号没展开 | 脚本会自动展开通配符；无匹配会直接报错，不会静默跳过 |
| 报「索引里已有更高版本 X」 | 这次要写的版本更旧（手动传错 tag、给旧分支补发版）→ 确实要给老版本线补包才加 `--allow-downgrade` |
| 预发布 release 没触发工作流 | 设计如此：`prerelease == true` 时 job 直接跳过，只有正式发布才进索引 |
| CI 报「下载字节与 GitHub 记录的 sha256 不一致」 | 下载被代理/镜像替换或截断 → 重跑一次；持续不一致就换通道，别用该代理 |
| 改了 manifest 的简介/显示名，索引里没变 | 默认不动展示字段（作者常手工润色）→ 加 `--sync-meta` 才同步 |
