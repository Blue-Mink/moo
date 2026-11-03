---
description: 从应用包到发布「Moo 应用源」的全流程：moo.json 协议（schema_version/apps/releases/packages、固定分类、单位约定）、用
  gen-moo-json / gen-repo-moo-json.py 生成索引、搜索框贴链接自测、添加为源与详情页验收、版本比较与更新判定、发布卫生（脱敏）。触发：发布到
  Moo、Moo 应用源、moo.json、上架 Moo 商店、Moo 源协议、把仓库做成 Moo 源。构建/打包 FPK 请用 fn-fpk-builder；发布到
  FnDepot 请用 fpk-build-to-fndepot。
name: fpk-build-to-moo
---

# 从构建 FPK 到发布「Moo 应用源」 — 全链路 Skill

> 全流程：**构建 FPK → 本地/测试机验证 → 写索引 → 放仓库 → 自测 → 添加为源 → 详情页验收 → 版本迭代**。
> 整合来源：**Moo 应用源协议**（`docs/Moo应用源协议.md`）+ 仓库内两个生成器
> （`tools/gen-moo-json`、`tools/gen-repo-moo-json.py`）+ Moo 仓库自身被当作应用源的完整实战。
>
> 深层构建规范（完整字段表 / 环境变量 / 生命周期）见姊妹技能 **`fn-fpk-builder`**；
> 发布到 **FnDepot** 开源商店请用姊妹技能 `fpk-build-to-fndepot`。

## 一、全链路总览

```
① 构建 FPK（源码 → 包）              → references/fpk-build.md
        ↓  判据：产出 *.fpk、manifest/ICON 齐（全槽位 ≥512）、记下 sha256
② 本地/测试机验证（装一次、启一次）    → references/fpk-build.md §七
        ↓  判据：能装能启、功能可用、版本号与 manifest 一致
③ 写 moo.json（手写 / 生成器一键生成） → 本文件 §2
        ↓  判据：json 合法、download_url + sha256 齐、单位正确、分类取自固定清单
④ 放到仓库（仓库根或任意 HTTP 路径）   → references/publish-workflow.md §4
        ↓  判据：curl -sI 链接 = 200，图标（≥512）/预览/README/包都能匿名取到
⑤ 自测：链接粘到 Moo 首页搜索框        → 本文件 §3
        ↓  判据：能即时列出该源应用且展示名/版本/图标（≥512）正确
⑥ 添加为源 + 详情页验收               → 本文件 §3
        ↓  判据：体积/校验和/预览/README/更新日志/分类/运行方式都对，且真装一次成功
⑦ 版本迭代与发布卫生                  → references/publish-workflow.md §7 + 本文件 §七
```

## 按需阅读（路由）

| 场景 | 读哪里 |
|------|--------|
| **构建阶段**：开发目录 / manifest 字段 / 图标 / 生命周期脚本 / privilege / build.sh / 本机安装与热替换 / 构建失败排查 | [references/fpk-build.md](references/fpk-build.md) |
| **构建 amd64 / arm64 / 双架构**（交叉编译、overlay、`--arch`、`packages.x86/arm`） | [references/fpk-build.md](references/fpk-build.md) §3.14 |
| **图标糊 / 商店卡片图标不是 512** | 本文件 §2.5 + 姊妹技能 `fn-fpk-builder` 的 `references/official-contract.md`「图标清晰度」 |
| **操作流程**（每步的"做完判据" / 多应用仓库维护 / 发布新版） | [references/publish-workflow.md](references/publish-workflow.md) |
| 协议全文（字段表 / 14 项固定分类 / 7 份带注释示例 / 错误与缓存 / FAQ） | [docs/Moo应用源协议.md](../../docs/Moo应用源协议.md) |
| `moo.json` 字段速查（bug_report_url / min_fnos / desc_html / size_bytes / 源级 wizard） | [references/moo-json-fields.md](references/moo-json-fields.md) |
| 安装/卸载/配置/升级向导、数据删除边界、190s 回调看门狗、标准四步 | [references/wizard-lifecycle.md](references/wizard-lifecycle.md) |
| 自更新 minisign 三件套、资产矩阵、tag、索引顺序、兜底通道 | [references/self-update-signing.md](references/self-update-signing.md) |
| Moo / Moo Mate 实战差异（api-scope/port-config/checkport/Mate 顶层二进制） | [references/moo-mate-notes.md](references/moo-mate-notes.md) |
| 脱敏 / 独立代码审计 / CI / E2E 发布门 | [references/release-gates.md](references/release-gates.md) |
| 只想抄一份能用的索引 | 本文件 §2 的两个生成器 + 协议文档 §6 示例合集 |
| 已有 `moo.json`，只想增量更新一个应用 | 本文件 §2.4 + [维护指南](../../templates/MOO_JSON_MAINTENANCE.md) |
| 排查「搜不到 / 没图标 / 体积 0.0MB / 更新红点不消」 | [docs/Moo应用源协议.md](../../docs/Moo应用源协议.md#10-faq--排查手册) |
| 构建 / 打包 / 安装 / 热替换 FPK | 姊妹技能 `fn-fpk-builder` |
| 发布到 FnDepot 商店（fnpack.json） | 姊妹技能 `fpk-build-to-fndepot` |

## 二、生成 `moo.json`

### 2.1 从 FPK 目录生成（最常用）

```bash
cd moo && go run ./tools/gen-moo-json \
  -dir /path/to/fpk-store \                 # 递归扫描 *.fpk
  -name "我的应用仓" -distributor "MyOrg" \
  -base-url "https://example.com/apps" \    # download_url 前缀（省略则写相对路径）
  -out moo.json
```

自动完成：读 FPK 内 `manifest`（appname / version / display_name / desc / author / platform / service_port…）
→ 算 **sha256 与字节体积** → FPK 旁有 `ICON.PNG` 自动带 `icon_url`
→ 同名应用按**版本比较**只留最高版。

### 2.2 从仓库 releases 生成（「以 release 发布 FPK」的仓库）

```bash
python3 tools/gen-repo-moo-json.py \
  --repo https://gitea.example.com/owner/repo --user <user> --password <token> \
  --app myapp --name "我的应用" --distributor "MyOrg" \
  --labels "系统工具" --install-type root \
  --preview "docs/shot-1.png,docs/shot-2.png" \
  --readme-file "README.md" --icon-file "icons/demo.png" \
  --out moo.json
```

遍历全部 release：生成器当前**每个 release 取一个 FPK**（默认按 x86）写 `packages.x86`（直链 + sha256 + 字节体积 + 发布时间），
自动汇总**真实下载数**与**最早发布时间**；`--preview/--readme-file/--icon-file` 传仓库内相对路径会自动转 raw 链接。
同一版本同时有 x86+arm 两个包时，用 §2.4 的 `update_moo_json.py --fpk a_x86.fpk a_arm.fpk` 合并成 `packages.x86` / `packages.arm`；
不要手工只补一个架构。

### 2.3 手写最小可用

```jsonc
{
  "schema_version": "moo",                     // 固定写 "moo"
  "source_info": { "name": "我的应用仓" },
  "apps": {
    "myapp": {                                 // key = appname，必须与包内 manifest 一致
      "display_name": "我的应用",
      "version": "1.0.0",
      "download_url": "https://example.com/myapp-1.0.0.fpk",
      "sha256": "<64 位十六进制>",
      "size": 3.2,                              // 条目级体积：MB
      "categories": ["实用效率"],                // 分类只能取协议清单里的值
      "desc": "一句话简介。"
    }
  }
}
```

### 2.4 已有索引的一键维护（持续发版推荐）

仓库里已经有 `moo.json` 时别重跑生成器——用零依赖脚本增量更新，只动目标应用的
`releases` 节点与顶层跟版字段，**其余应用逐字节不变**：

```bash
# 独立安装：脚本随 bundled/tools/ 一起装好；仓库内用 tools/update_moo_json.py
UPD="bundled/tools/update_moo_json.py"    # 仓库内改为 tools/update_moo_json.py
# 单架构 x86
python3 "$UPD" --version 1.2.3 --fpk dist/myapp_1.2.3_x86.fpk --repo OWNER/REPO
# 双架构：一次传两个包，分别写 packages.x86 / packages.arm
python3 "$UPD" --version 1.2.3 \
  --fpk dist/myapp_1.2.3_x86.fpk dist/myapp_1.2.3_arm.fpk --repo OWNER/REPO
python3 "$UPD" --check --version 1.2.3 \
  --fpk dist/myapp_1.2.3_x86.fpk dist/myapp_1.2.3_arm.fpk --repo OWNER/REPO
# 没有随技能副本时才回退到远程 raw（不推荐；版本不可控）
# curl -fsSL https://raw.githubusercontent.com/Blue-Mink/moo/main/tools/update_moo_json.py -o /tmp/update_moo_json.py
```

- 版本 / appname / 平台默认从包内 `manifest` 读，`--version` 与包内不一致时脚本直接拒绝；
- `--fpk` 可一次传多个包（x86+arm）：按各自 manifest 分别写 `packages.x86` / `packages.arm`；只传一个就只写一个架构，不要手工补另一个；
- 新架构会增补到应用级 `platform`（默认；`--no-platform-sync` 可关），否则 arm 客户端可能仍按旧架构过滤；
- 多应用索引必须能定位目标条目（包内 appname 或 `--appname`），脚本不会猜；
- `--tag` 处理非规范 tag（如 `release-1.2.3`）；`--keep` 控制保留版本数（默认 2）；
- 同版本换包默认拒绝（防已缓存用户校验失败），确需覆盖加 `--allow-republish`；
- 想让它跟着 Release 自动跑：把 `templates/moo-autoupdate.yml` 放进自己仓库的 `.github/workflows/`；
- 三种接入方式与排错表见 [`templates/MOO_JSON_MAINTENANCE.md`](../../templates/MOO_JSON_MAINTENANCE.md)。

### 2.5 图标：FPK 全槽位 + `moo.json` 商店索引（2026-11-02）

飞牛取图标有**三条独立通道**，任一通道拿到 64 小图就会糊：

1. **桌面 / 列表**：`ui/images` 下的槽位图（serviceicon，`?size=N`）；
2. **详情弹窗**：FPK 根目录 `ICON.PNG`；
3. **商店卡片**：`moo.json` 的 `icon_url` 指向仓库树内的 `<app>/ICON.PNG`，与装机图标是**两套独立文件**。

做法（完整口径见姊妹技能 `fn-fpk-builder` 的 `references/official-contract.md`「图标清晰度」）：

- 做一张 **≥512 母版**，原样铺满 FPK 的全部实际槽位，**不要**按 64/256 分别降采样：
  - `ui/config` 用 `images/icon_{0}.png` 的项目：`ui/images/icon_0.png` / `icon_64.png` / `icon_128.png` / `icon_256.png` / `icon_512.png` + 根 `ICON.PNG` / `ICON_256.PNG`；
  - `ui/config` 用 `images/ICON.PNG` 的项目（如 Moo）：`ui/images/ICON.PNG` / `ICON_64.PNG` / `ICON_256.PNG` + 根 `ICON.PNG` / `ICON_256.PNG`；**不要**额外造 5 个小写槽位。
- **商店索引**：`moo.json` 的 `icon_url` 路径不用改；但 `icon_url` 指向的仓库树内 `ICON.PNG` 与同目录 `ICON_256.PNG`（如有）都要换成同一张 ≥512。只改 FPK、不改树内文件 → 商店卡片仍糊。
- **验收**：`curl` 取 `icon_url` 与 FPK 根图标，尺寸都 ≥512；装机后详情弹窗/桌面强刷（图标 URL 有 7 天 immutable 缓存）；源码 == 包内 == 设备，三处 md5 一致。

### 2.6 `moo.json` 字段速查（别只写最小子集）

最小示例只覆盖 `display_name/version/download_url/sha256/size/categories/desc`；实际还常用：

| 字段 | 用途 | 版本/注意 |
|---|---|---|
| `desc_html` | 详情页富文本，优先于 `desc` | 0.6.269+，消毒后渲染 |
| `license` | 详情页许可协议行 | 0.6.269+ |
| `min_fnos` | 最低 fnOS 版本，安装期平台校验 | 0.6.269+；可从 manifest `os_min_version` 同步 |
| `bug_report_url` | 详情页「问题反馈」 | 0.6.388+；`gen-moo-json` 已支持 |
| `preview_urls` / `readme_url` | 预览轮播 / README | `gen-repo-moo-json.py` 支持 |
| `app_type` | `fpk`/`docker`/`native` | 影响列表/详情标记 |
| `size_bytes` | 条目级字节，优先于 `size`（MB） | 0.6.233+ |
| `source_info` | 源名称/分发者/主页/描述/i18n | 可选 |
| `wizard` | 源级安装向导 | 0.6.269+，见 §2.7 |

完整字段表、单位、示例与生成器能力边界见 [references/moo-json-fields.md](references/moo-json-fields.md)。

> **别把 FnDepot 的字段直接抄过来**：Moo 是 14 类、条目 `size`=MB；FnDepot 是 9 类、`packages.size`=字节；Moo `install_type` 自适应，FnDepot 用 `run_as`+`install_type`；`manifest.platform` 两边都是字符串。

### 2.7 源级安装向导 `wizard`

`moo.json` 可以声明安装前向用户收集的参数：

```jsonc
"wizard": {
  "fields": [
    { "key": "SITE_TITLE", "label": "站点标题", "default": "我的博客", "required": true },
    { "key": "PORT", "label": "服务端口", "default": "8090" }
  ]
}
```

- **0.6.269+**：Moo 安装前弹窗收集，值随安装请求下发给应用；键名由应用自己定义，不强制 `wizard_` 前缀。
- 这是 **Moo 源级** 一层；FPK 里的 `wizard/install` 是**平台级**另一层。两层可能同时存在，键名/合并顺序/优先级由应用安装逻辑处理；发布前用 Moo 客户端真装一次抓请求确认。
- `required`/`default` 只是客户端提示；安装回调/服务端必须再校验。
- `gen-moo-json` 目前不生成 `wizard`，需手写或由发布脚本合并。

## 三、自测与发布（别跳过第 4 步）

1. **搜索框自测**：把 `moo.json` 链接粘到 Moo **首页搜索框** —— 不添加源也能即时列出该源全部应用；
2. 能列出 → 设置 → **应用源设置** → 添加应用源（粘同一链接）；
3. 打开该应用详情页逐项验收：
   - 图标（≥512）、展示名、版本；
   - **安装包大小**与 **SHA256**（= 单位写对没）；
   - 预览图（可点开、**支持双指缩放**）、README（默认约 10 行，可展开）；
   - 更新日志（默认最近 3 条，可展开）、分类标签、运行方式/安装位置；
4. 真装一次（点「安装」）确认下载与校验通过。

### 3.5 安装 / 卸载 / 升级与装后四铁律

- 命令：`appcenter-cli install-fpk <fpk> --volume <N> --env /tmp/<app>.env`，**必须 root**；`install-fpk` 不是独立命令。
- `appcenter-cli install-fpk` 对已装应用是 **no-op**；换版走 停→卸→`appcenter-cli check` 确认没了→装→启，或 daemon `/rpc/v1/update/task`。
- `start` 报 **10500** 是“已在运行”，不是失败；`uninstall` 报 **10150** 是本地应用禁卸；常驻入口 `checkport=false`，否则停用后再启用报 **11000**。
- 安装回调约 **190 秒看门狗**：重活必须“秒回 + 后台 worker”。
- 装后四铁律：新 PID / 运行态二进制 sha == 包内 / 版本 + 前端 bundle 指纹 / 数据零回归（源数/卡数/已装数/禁用集合逐字一致）；冷启动前几秒偏低是瞬态。
- 旧版/失败回滚：保留上一版 FPK + `@appdata` 备份；失败时 stop→uninstall→装上一版→start。
- 向导、数据删除边界、config/upgrade 与更多错误码见 [references/wizard-lifecycle.md](references/wizard-lifecycle.md)。

## 四、核心坑点速览

| # | 坑点 | 现象 | 解决 |
|---|------|------|------|
| 1 | `appname` 与包内 manifest 不一致 | 安装失败 / 更新识别不到 | 以包内 `manifest` 的 `appname` 为准写 key |
| 2 | **单位写错** | 体积显示 `0.0 MB` 或异常大 | 条目级 `size` = **MB**；`releases.*.packages.<arch>.size` = **字节** |
| 3 | `sha256` 留空 | 只做结构校验，损坏包可能被装上 | **务必填**；两个生成器会自动算 |
| 4 | 仓库需登录才能读 raw | 索引/图标/预览/README 全取不到 | 服务端放开匿名读，或让 Moo 拿到访问令牌 |
| 5 | 索引用了「拆分详情」形态 | 应用被整条跳过 | 给 Moo 的索引**不要拆**（`details_url` 不拉） |
| 6 | 版本号规则不统一 | 装完更新红点不消 / 提示反向更新 | 统一成纯数字三段（可加固定后缀，如 `-panel`） |
| 7 | 分类自造名字 | 落到错误分类或不显示 | 只取协议 §4.3 清单里的 14 个值，第一项=主分类 |
| 8 | 内网地址写进公开文档 | 泄漏内网拓扑 | 文档用占位（如 `192.0.2.10:3000`）；**只有 moo.json 需要真实地址** |

## 五、版本与更新判定（会被坑的地方）

Moo 比较版本：去前导 `v` → 拆「数字前缀 + 后缀」→ 数字逐段数值比较 → 相同再比后缀（非空 > 空）。

| 例子 | 谁更新 |
|------|--------|
| `0.10.0` vs `0.9.0` | `0.10.0`（逐段数值，不是字符串） |
| `1.2.0-panel` vs `1.2.0` | `1.2.0-panel`（带后缀更大） |
| `1.0.0` vs `1.0.0-rc1` | `1.0.0-rc1`（与 semver 相反！） |
| `2.0` vs `2.0.0` | 相等 |

> 一条线路一个 `moo.json`：稳定版与测试版别混在同一份索引里，否则会把测试版当"最新"。

### 5.5 发布 / 自更新 / 签名（摘要）

- 自更新应用每架构三件套：`FPK` + `.sha256` + `.minisig`；缺一 fail-closed；老客户端缺 `.sha256` 会永远升不上来。
- 签名私钥离线，公钥 `go:embed`；CI 只构建/上传，签名只在发布机。
- 资产矩阵：Moo 7 件（6 手工 + CI 二进制），Mate 6 件；tag `vX.Y.Z` / `mate-vX.Y.Z`；release 必须 public。
- 索引永远晚于 release；`moo.json` 的 `releases` 只留最近 2 版；同版本不替换包。
- 兜底通道每条都要同时给 FPK + `.sha256` + `.minisig` 三个 URL；国内 API 限流时几乎恒走兜底。
- 完整清单见 [references/self-update-signing.md](references/self-update-signing.md)。

## 六、命令速查

| 操作 | 命令 |
|------|------|
| 构建 FPK（项目自带脚本） | `./build.sh x86` / `./build.sh arm`（或 `./build.sh both`，以项目脚本为准；没有就 `fnpack build -d <开发目录>`） |
| 手动打包 | `fnpack build -d <开发目录>` |
| 记录包校验和 | `sha256sum <appname>.fpk` |
| app.tgz 校验和（重压后） | `md5sum app.tgz`（回写 `manifest.checksum`；正常 `fnpack build` 自动写） |
| 装到测试机 | `scp <fpk> root@<NAS>:/tmp/ && ssh root@<NAS> 'appcenter-cli install-fpk /tmp/<fpk> --volume <N> --env /tmp/<app>.env'`（必须 root；env 文件每行 `KEY=VALUE`） |
| 卸载确认 | `appcenter-cli check <app>`（rc=0 也可能没卸） |
| 状态 | `appcenter-cli status <app>`（0=运行中 / 3=未运行；start 报 10500 是假错） |
| 热替换（只换二进制） | `ssh root@<NAS> 'appcenter-cli stop <app> && cp <bin> <安装目录>/ && appcenter-cli start <app>'` |
| 从 FPK 目录生成索引 | `go run ./tools/gen-moo-json -dir <fpk目录> -base-url <前缀> -out moo.json` |
| 从仓库 releases 生成索引 | `python3 tools/gen-repo-moo-json.py --repo <url> --app <appname> --out moo.json` |
| 校验 JSON 合法 | `python3 -c "import json;json.load(open('moo.json'))"` |
| 取包 sha256 | `sha256sum <file>.fpk` |
| 匿名可达性自测 | `curl -sI <moo.json 链接>`（应 200；图标/预览/包同样测一遍） |
| 源自测（不添加） | 把链接粘到 Moo **首页搜索框** |
| 添加源 | Moo → 设置 → 应用源设置 → 添加应用源 |
| 强制刷新该源 | 页面右上角「刷新页面」，或设置里对该源点同步 |

### 6.5 Moo / Moo Mate 实战差异

- Moo：`api-scope: ["trim.app.center"]`、`port-config` + `moo.sc`（`port_forward="no"`）、`checkport=true`、`micro_app=true`、`gatewaySocket=app.sock`、`gatewayPrefix=/app/moo`；7 资产。
- Mate：二进制在 `app.tgz` 顶层、`web/index.html` `go:embed`、`default_index.json.gz`、`datadir` 指针、`checkport=false`、`gatewaySocket=mate.sock`、`gatewayPrefix=/app/moo-mate`；6 资产、`mate-v` tag。
- 多应用索引维护：`update_moo_json.py --appname moo-mate --tag mate-v0.2.36 ...`，只动目标节点；不要用默认 `v` 前缀去猜 `mate-v`。
- 细节见 [references/moo-mate-notes.md](references/moo-mate-notes.md)。

## 七、发布卫生（脱敏红线）

**要公开的文档与仓库**（协议说明、README、技能文档）发布前必须过一遍：

- 设备地址写 `<NAS_IP>`，示例用 RFC5737 文档网段 `192.0.2.10:3000` 或 `gitea.example.com`；
- 不出现账号口令、token、私钥块、SSH 目标、私有工作区路径；
- **不出现未公开构件的 sha256**（公开 release 的哈希可以保留）；
- 品牌/组织名按需脱敏（示例用 `Example Org`）；
- **例外**：`moo.json` 里**必须**写真实可访问地址（它是应用源索引本身）——所以别把带内网地址的 moo.json 直接贴进公开文档；
  将来仓库要公开到外网时，先把索引里的地址换成公网域名/反代再生成。

## 八、引用与致谢

- **Moo 应用源协议**：本仓库 `docs/Moo应用源协议.md`
- 生成器：`tools/gen-moo-json`（Go）、`tools/gen-repo-moo-json.py`（Python）
- 姊妹技能：`fn-fpk-builder`（构建/打包/安装）、`fpk-build-to-fndepot`（发布到 FnDepot）
- 实战来源：Moo 仓库自身被添加为应用源的全过程（252+ 版本多版本索引、真实下载数、预览图与 README 拉取）

## 九、依赖与跨 Agent 安装

本技能可在仓库内直接使用，也可独立安装到其它 Agent：

```bash
sh skills/install-cross-agent.sh --target claude-code --apply        # ~/.claude/skills/
sh skills/install-cross-agent.sh --target codex --apply              # ~/.codex/prompts/
sh skills/install-cross-agent.sh --target cursor --apply             # .cursor/rules/
sh skills/install-cross-agent.sh --target agents-md --apply          # AGENTS.md
sh skills/install-cross-agent.sh --target skills-dir --dest <目录> --apply --skill fpk-build-to-moo
```

- **仓库相对引用**：`docs/Moo应用源协议.md`、`tools/gen-moo-json`、`tools/gen-repo-moo-json.py`、`tools/update_moo_json.py`
  在仓库内是相对路径；独立安装时安装器会复制到本技能的 `bundled/docs/` 与 `bundled/tools/`，
  文档中的 `../../docs/...` 链接在独立环境下按 `bundled/` 解析。
- **依赖**：`python3`（生成器与测试）；`tools/gen-moo-json` 是 Go 源码（需要 `go build` 才能跑）。
- **自检**：`sh tests/ci.sh`（脱敏 + compileall + 单测 + x86/arm 双架构 E2E）；仓库级再跑 `python3 skills/ci_check.py`（覆盖全部技能）。