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
        ↓  判据：产出 *.fpk、manifest/ICON 齐、记下 sha256
② 本地/测试机验证（装一次、启一次）    → references/fpk-build.md §七
        ↓  判据：能装能启、功能可用、版本号与 manifest 一致
③ 写 moo.json（手写 / 生成器一键生成） → 本文件 §2
        ↓  判据：json 合法、download_url + sha256 齐、单位正确、分类取自固定清单
④ 放到仓库（仓库根或任意 HTTP 路径）   → references/publish-workflow.md §4
        ↓  判据：curl -sI 链接 = 200，图标/预览/README/包都能匿名取到
⑤ 自测：链接粘到 Moo 首页搜索框        → 本文件 §3
        ↓  判据：能即时列出该源应用且展示名/版本/图标正确
⑥ 添加为源 + 详情页验收               → 本文件 §3
        ↓  判据：体积/校验和/预览/README/更新日志/分类/运行方式都对，且真装一次成功
⑦ 版本迭代与发布卫生                  → references/publish-workflow.md §7 + 本文件 §七
```

## 按需阅读（路由）

| 场景 | 读哪里 |
|------|--------|
| **构建阶段**：开发目录 / manifest 字段 / 图标 / 生命周期脚本 / privilege / build.sh / 本机安装与热替换 / 构建失败排查 | [references/fpk-build.md](references/fpk-build.md) |
| **操作流程**（每步的"做完判据" / 多应用仓库维护 / 发布新版） | [references/publish-workflow.md](references/publish-workflow.md) |
| 协议全文（字段表 / 14 项固定分类 / 7 份带注释示例 / 错误与缓存 / FAQ） | [docs/Moo应用源协议.md](../../docs/Moo应用源协议.md) |
| 只想抄一份能用的索引 | 本文件 §2 的两个生成器 + 协议文档 §6 示例合集 |
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

遍历全部 release：每个 FPK 附件写成 `releases.<版本>.packages.x86`（直链 + sha256 + 字节体积 + 发布时间），
自动汇总**真实下载数**与**最早发布时间**；`--preview/--readme-file/--icon-file` 传仓库内相对路径会自动转 raw 链接。

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

## 三、自测与发布（别跳过第 4 步）

1. **搜索框自测**：把 `moo.json` 链接粘到 Moo **首页搜索框** —— 不添加源也能即时列出该源全部应用；
2. 能列出 → 设置 → **应用源设置** → 添加应用源（粘同一链接）；
3. 打开该应用详情页逐项验收：
   - 图标、展示名、版本；
   - **安装包大小**与 **SHA256**（= 单位写对没）；
   - 预览图（可点开、**支持双指缩放**）、README（默认约 10 行，可展开）；
   - 更新日志（默认最近 3 条，可展开）、分类标签、运行方式/安装位置；
4. 真装一次（点「安装」）确认下载与校验通过。

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

## 六、命令速查

| 操作 | 命令 |
|------|------|
| 构建 FPK（项目自带脚本） | `./build.sh x86` |
| 手动打包 | `fnpack build -d <开发目录>` |
| 记录包校验和 | `sha256sum <appname>.fpk` |
| 装到测试机 | `scp <fpk> root@<NAS>:/tmp/ && ssh root@<NAS> 'appcenter-cli install /tmp/<fpk>'` |
| 热替换（只换二进制） | `ssh root@<NAS> 'appcenter-cli stop <app> && cp <bin> <安装目录>/ && appcenter-cli start <app>'` |
| 从 FPK 目录生成索引 | `go run ./tools/gen-moo-json -dir <fpk目录> -base-url <前缀> -out moo.json` |
| 从仓库 releases 生成索引 | `python3 tools/gen-repo-moo-json.py --repo <url> --app <appname> --out moo.json` |
| 校验 JSON 合法 | `python3 -c "import json;json.load(open('moo.json'))"` |
| 取包 sha256 | `sha256sum <file>.fpk` |
| 匿名可达性自测 | `curl -sI <moo.json 链接>`（应 200；图标/预览/包同样测一遍） |
| 源自测（不添加） | 把链接粘到 Moo **首页搜索框** |
| 添加源 | Moo → 设置 → 应用源设置 → 添加应用源 |
| 强制刷新该源 | 页面右上角「刷新页面」，或设置里对该源点同步 |

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