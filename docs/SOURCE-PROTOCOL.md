# Moo 应用源协议（moo.json）v1

> 目标：让**任何** FPK / Docker 应用仓库都能用一份 `moo.json` 声明自己的应用清单，
> Moo 侧「添加应用源」粘一个地址即可同步；同时**向后兼容 FnDepot 协议**
> （`fnpack.json` / `fndepot.json`），老仓库无需改动也能被 Moo 读取。

- 适用范围：Moo 0.6.233+
- 关联实现：`internal/source/fndepot.go`（解析）、`internal/source/mooindex.go`（候选地址与扩展字段）、
  `tools/gen-moo-json`（从 FPK 目录一键生成 `moo.json`）

---

## 1. 地址发现（候选地址）

用户添加一个源地址后，Moo 会按顺序尝试下列候选（第一个返回 200 的胜出）：

| 用户填写的地址形态 | 候选顺序 |
|---|---|
| `https://host/path/moo.json`（`.json` 直链） | 该地址本身 |
| `https://github.com/<owner>/<repo>` | `…/main/moo.json`、`…/master/moo.json`、镜像前缀、`cdn.jsdelivr` |
| 其它仓库/目录地址 | `<url>/moo.json`、`<url>/raw/main/moo.json`，**再回退** `<url>/fnpack.json`、`<url>/raw/main/fnpack.json`（兼容 FnDepot） |

> 也就是说：**同一个仓库可以同时挂 `moo.json`（Moo 原生）与 `fnpack.json`（FnDepot 兼容）**，
> Moo 优先读 `moo.json`。

## 2. 文件结构

三种形态都支持：

| 形态 | 结构 | 说明 |
|---|---|---|
| **A. Moo v1（推荐）** | `{ schema_version, source_info, apps }` | 本文档主推，可携带扩展字段 |
| B. fnpack V2 包裹 | `{ schema_version, source_info, apps }` | 与 A 同构（A 是它的超集） |
| C. fnpack V1 平铺 | `{ "<appname>": { … } }` | FnDepot `fndepot.json` 那种，Moo 一直支持 |

```jsonc
{
  "schema_version": "moo/v1",          // 字符串即可；有该键 → 按 V2 包裹解析
  "source_info": {                     // 可选，取自 FnDepot 约定
    "name": "Blue-Mink 应用仓",
    "homepage": "https://github.com/Blue-Mink/FnDepot",
    "distributor": "Blue-Mink",
    "updated_at": "2026-10-01T21:00:00+08:00"
  },
  "apps": {
    "dockflare": { /* 见第 3 节，key = appname */ }
  }
}
```

## 3. 应用条目字段

**key = `appname`**（安装/更新匹配用，务必与 FPK 内 `manifest` 的 `appname` 一致）。

### 3.1 基础（必填）

| 字段 | 类型 | 说明 |
|---|---|---|
| `display_name` | string | 展示名（缺省回退 appname） |
| `version` | string | 版本号（建议与 FPK manifest 一致） |
| `download_url` | string | **可安装包直链**；相对路径按 `moo.json` 所在目录补全 |
| `platform` | string / array | `x86` / `arm` / `all`（多值用数组） |
| `sha256` | string | 包校验和（**强烈建议**：Moo 下载后校验，缺失则退化为结构校验） |
| `size` | number / string | 包体积（MB；数字或 `"12.3"` 均可） |

### 3.2 元数据（可选）

| 字段 | 说明 |
|---|---|
| `desc` | 简介（纯文本，列表/卡片展示） |
| `desc_html` | 富文本简介（详情页；Moo 会消毒后渲染） |
| `changelog` | 本版更新日志 |
| `updated_at` | 更新时间（ISO8601 或任意可读串） |
| `labels` / `categories` | 分类（字符串或数组） |
| `tags` | 同 labels（别名） |
| `icon_url` | 图标地址（相对路径按目录补全；缺省时 Moo 走内置图标通道） |
| `preview_urls` | 预览截图数组（详情页轮播；**支持双指缩放**） |
| `readme_url` | README 地址 |
| `homepage` | 项目主页 |
| `author` / `maintainer` | 开发者（两个名字都认） |
| `author_url` / `maintainer_url` | 开发者主页 |
| `distributor` | 发布者/分发者（与开发者同名时不显示） |
| `bug_report_url` | 反馈地址 |
| `license` | 许可协议（如 `MIT`） |
| `download_count` | 下载量（官方目录以此覆盖统计） |

### 3.3 安装行为（Moo 扩展）

| 字段 | 取值 | 说明 |
|---|---|---|
| `app_type` | `fpk` / `docker` / `native` | Moo 原生类型；**与 `isdocker` 二选一或并存**（并存时以 `app_type` 为准） |
| `isdocker` | `"true"` / `"false"`（字符串）或 bool | FnDepot 兼容写法（`is_docker` 亦认） |
| `install_type` | 如 `用户空间` / `系统空间` | 安装位置说明 |
| `service_port` | number / string | 应用服务端口（展示与跳转用） |
| `min_fnos` | string | 最低 fnOS 版本（不满足时 Moo 会提示） |
| `wizard` | object | 安装向导参数（`{"fields":[{"key","label","default","required"}]}`） |

### 3.4 多版本（可选，兼容 FnDepot）

```jsonc
"releases": {
  "1.2.0": {
    "changelog": "…",
    "updated_at": "2026-09-30",         // 该版发布时间（列表/详情显示「最近更新时间」）
    "packages": {
      // size = **字节数**（与 FnDepot/RROrg 一致）；size_mb 仅供人读、可省略
      "x86":   { "download_url": "…x86.fpk", "sha256": "…", "size": 12903424, "size_mb": 12.3 },
      "arm":   { "download_url": "…arm.fpk", "sha256": "…", "size": 12373197 },
      "all":   { "download_url": "…",        "sha256": "…" }
    }
  }
}
```
顶层 `version`/`download_url` 缺省时，Moo 会按**版本降序**挑当前架构（`x86` → `all` 回退）第一个可用包，
并用它的 `sha256`/`size` 补齐校验和与体积；`updated_at` 取该版本的发布时间。

## 4. 最小示例

```json
{
  "schema_version": "moo/v1",
  "source_info": { "name": "示例仓库", "distributor": "Blue-Mink" },
  "apps": {
    "dockflare": {
      "display_name": "DockFlare",
      "version": "3.1.3",
      "app_type": "docker",
      "platform": ["x86", "arm"],
      "size": 12.3,
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "download_url": "https://example.com/dockflare-v3.1.3-fnos-x86.fpk",
      "icon_url": "icon.png",
      "preview_urls": ["shot-1.png", "shot-2.png"],
      "desc": "Cloudflare Tunnel 的自动 DNS/隧道管理面板。",
      "labels": ["网络工具"],
      "author": "ChrispyBacon-dev",
      "distributor": "Blue-Mink",
      "license": "GPL-3.0"
    }
  }
}
```

## 5. 与 FnDepot 的关系（兼容矩阵）

| FnDepot 字段 | Moo 是否识别 | 备注 |
|---|---|---|
| `display_name` / `version` / `platform` / `desc` / `size` | ✅ | 直接可用 |
| `labels` / `categories` | ✅ | 二者皆认 |
| `author` / `maintainer`（含 `_url`） | ✅ | 优先 author |
| `distributor` / `bug_report_url` / `homepage` / `readme_url` | ✅ | 直接可用 |
| `isdocker`（字符串）/ `is_docker`（bool） | ✅ | 与 `app_type` 等效 |
| `install_type` / `service_port` / `download_count` | ✅ | |
| `download_url` / `sha256` / `size`（条目级） | ✅ | |
| `releases.*.packages.<arch>` | ✅ | 多版本源（RROrg 等） |
| `fndepot.json`（V1 平铺） | ✅ | 老仓库原样可用 |

**Moo 新增（FnDepot 没有的）**：`app_type`、`desc_html`、`preview_urls`、`license`、
`min_fnos`、`wizard`、`tags`（别名）、`sha256` 条目级校验、`source_info` 摘要。

## 6. 生成 `moo.json`

内置工具从**一堆 FPK** 直接生成（自动读 FPK 内 `manifest` + 计算 sha256/体积）：

```bash
cd moo && go run ./tools/gen-moo-json \
  -dir /path/to/fpk-store \          # 递归扫描 *.fpk
  -name "Blue-Mink 应用仓" \
  -distributor "Blue-Mink" \
  -base-url "https://example.com/apps" \   # 生成 download_url 前缀（可省略 → 相对路径）
  -out moo.json
```

生成的 `moo.json` 放在仓库根、与应用包同目录即可被 Moo 添加为源。

## 7. 版本与演进

- `schema_version` 目前为 `moo/v1`；解析按「有 `schema_version` 即走包裹格式」判定，未知版本号
  仍按 v1 字段尽力解析（前向兼容：新增字段一律可选）。
- 后续大改（如源签名、增量索引）会升 `moo/v2`，并保持 v1 可读。
