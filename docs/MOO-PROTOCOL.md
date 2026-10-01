# Moo 应用源协议

> **一句话**：在仓库根放一份 `moo.json`，Moo 就能把它添加为**应用源**，同步其中的应用并检测更新；
> 应用可以是 FPK（原生包）或 Docker 应用。
>
> 本文是**发布方照着就能写**的完整规范：协议定位、地址发现、字段全表、分类清单、版本与安装包、
> 6 份带逐行注释的示例、同步与安装流程、错误与缓存行为、FAQ 排查手册、生成工具与最佳实践。

| 项 | 值 |
|---|---|
| 协议文件 | `moo.json`（文件名固定；放仓库根或任意 HTTP 可访问路径） |
| 协议标识 | 文件里写 `"schema_version": "moo"` |
| 适用范围 | Moo **0.6.233+** |
| 包类型 | FPK（`app_type: fpk`/`native`）、Docker（`app_type: docker`） |
| 实现位置 | 解析 `internal/source/fndepot.go`；候选与扩展字段 `internal/source/mooindex.go`；搜索贴链接 `internal/source/source_key.go` + `frontend/src/lib/sourceKey.ts`；运行方式展示 `frontend/src/lib/appMeta.ts` |
| 生成工具 | `tools/gen-moo-json`（FPK 目录）、`tools/gen-repo-moo-json.py`（仓库 releases） |

---

## 目录

1. [五分钟上手](#1-五分钟上手)
2. [源地址：写在哪、怎么被找到](#2-源地址写在哪怎么被找到)
3. [文件结构](#3-文件结构)
4. [应用字段](#4-应用字段)
5. [版本与安装包](#5-版本与安装包)
6. [示例合集（逐行注释）](#6-示例合集逐行注释)
7. [Moo 如何处理你的源（同步 · 展示 · 安装）](#7-moo-如何处理你的源同步--展示--安装)
8. [校验、错误与缓存行为](#8-校验错误与缓存行为)
9. [生成 `moo.json`](#9-生成-moojson)
10. [FAQ / 排查手册](#10-faq--排查手册)
11. [最佳实践](#11-最佳实践)
12. [附录 A · 字段速查总表](#附录-a--字段速查总表)
13. [附录 B · 术语表](#附录-b--术语表)

---

## 1. 五分钟上手

1. 在仓库根新建 `moo.json`，粘下面的最小内容（把 `download_url` 换成你的包地址）；
2. 推到仓库（或任意 HTTP 服务，能 200 返回即可）；
3. Moo → 设置 → **应用源设置** → 添加应用源，粘 **JSON 直链**；
4. **先自测**：把同一链接粘到 **首页搜索框** —— 不添加源也能即时列出该源的全部应用。

```jsonc
{
  // 协议标识：固定写 "moo"
  "schema_version": "moo",

  // 源自身信息（可选，仅用于源列表展示）
  "source_info": { "name": "示例仓库", "distributor": "Example Org" },

  // 应用表：key = appname（必须与包内 manifest 的 appname 完全一致）
  "apps": {
    "dockflare": {
      "display_name": "DockFlare",                  // 展示名（缺省回退 appname）
      "version": "3.1.3",                           // 版本号
      "download_url": "https://example.com/dockflare-3.1.3.fpk",
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "size": 12.3,                                 // 单位 MB（见 §4.1）
      "categories": ["网络工具"],                    // 分类（见 §4.3）
      "desc": "Cloudflare Tunnel 的自动 DNS/隧道管理面板。"
    }
  }
}
```

---

## 2. 源地址：写在哪、怎么被找到

### 2.1 三种被识别的地址形态

| 形态 | 示例 | 何时用 |
|---|---|---|
| **JSON 直链** | `https://example.com/apps/moo.json` | 最推荐，任何 HTTP 服务都行 |
| **仓库地址** | `https://github.com/<owner>/<repo>`、`http://192.0.2.10:3000/<owner>/<repo>` | Moo 自动拼候选路径找 `moo.json` |
| **搜索框贴链接** | 上面任一种粘到首页搜索框 | 只想看看、不想先加源 |

### 2.2 候选路径（按顺序请求，第一个 200 的胜出）

| 你填的地址 | Moo 依次尝试 |
|---|---|
| 以 `.json` 结尾 | 只用该地址本身（不追加任何后缀） |
| GitHub 仓库 | `raw.githubusercontent.com/<owner>/<repo>/main/moo.json` → `…/master/moo.json` → 各 GitHub 镜像前缀 → `cdn.jsdelivr.net/gh/<owner>/<repo>/moo.json` |
| 其它仓库 / 目录 | `<url>/moo.json` → `<url>/raw/main/moo.json` |
| 自建 Gitea | **建议直接填 raw 直链**：`http://<host>/<owner>/<repo>/raw/branch/main/moo.json` |

- **主机支持域名或 IPv4**（内网 `192.0.2.10:3000` 一样可用）。
- 同目录若还有其它索引文件，**Moo 优先读 `moo.json`**，读不到才回退（见 §3 说明）。

### 2.3 搜索框「贴链接直搜」

可识别的链接形态（前端 `sourceKey.ts` 与后端 `source_key.go` 同语义）：

- 仓库根：`https://github.com/<owner>/<repo>`、`http://192.0.2.10:3000/<owner>/<repo>`
- raw / 镜像：`raw.githubusercontent.com/...`、`cdn.jsdelivr.net/gh/...`、gh-proxy 等镜像前缀
- 索引直链：`moo.json`（以及 Moo 兼容读取的其它索引文件名）
- Gitea raw：`http://<host>/<owner>/<repo>/raw/branch/<branch>/moo.json`

**归一化规则**：转小写 → 去协议与 `?`/`#` → 剥 `/raw/<分支>/` 段 → 剥索引文件名 →
GitHub 系统一成 `github.com/owner/repo` → 得到「源 key」与已配置源匹配。
**裸词 / 裸文件名**（例如只写 `moo.json`）**不算链接**，按普通关键词搜索处理。

---

## 3. 文件结构

```jsonc
{
  "schema_version": "moo",        // 有该键 → 按「包裹结构」解析（本文档结构）
  "source_info": { … },           // §3.2
  "apps": { "<appname>": { … } }  // §4
}
```

- `schema_version` **值固定写 `"moo"`**；Moo 只判断「这个键是否存在」，写成别的字符串也不会失败。
- 没有该键时，Moo 会按「应用表直接铺在顶层」（`{"<appname>": {…}}`）解析 —— 兼容历史索引，**但新源请用包裹结构**。

### 3.2 `source_info`（源自身信息）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `name` | string | 建议 | 源在 Moo 中的显示名（缺省用仓库名） |
| `author` / `distributor` | string | 建议 | **源维护者**；不会替代应用自身的开发者/发布者 |
| `homepage` | url | 可选 | 源或作者主页 |
| `description` | string | 可选 | 源简介 |
| `updated_at` | string | 可选 | 源更新时间（任意可读串，如 `2026-10-01T21:00:00+08:00`） |

---

## 4. 应用字段

**`<appname>` = 应用唯一标识**，必须与包内 `manifest` 的 `appname` 一致。

> **命名建议**：小写字母 / 数字 / `.` `-` `_`，例如 `dockflare`、`com.trek.app`。
> 它是安装、更新匹配与「同名合并」的键，**一旦发布不要再改**。

### 4.1 基础字段

| 字段 | 类型 | 必填 | 缺省行为 | 说明 |
|---|---|---|---|---|
| `display_name` | string | 建议 | 回退 `appname` | 展示名 |
| `version` | string | 建议（多版本可省） | 从 `releases` 挑最新版 | 版本号，见 §5.3 比较规则 |
| `download_url` | string | **是**（多版本可省） | — | 安装包直链；**相对路径按 `moo.json` 所在目录补全** |
| `platform` | string / array | 可选 | `x86` | `x86` / `arm` / `all` |
| `sha256` | string | **强烈建议** | 仅做结构校验 | 64 位十六进制；不匹配**拒绝安装** |
| `size` | number / string | 可选 | 不显示体积 | **条目级体积，单位 MB**（`12.3` 或 `"12.3"`） |

> ⚠️ **两个单位**：条目级 `size` = **MB**；`releases.*.packages.<arch>.size` = **字节**（§5.2）。
> 把字节数写进条目级 `size` 会显示成 `7784783.0 MB`；把 MB 写进包级会显示成 `0.0 MB`。

### 4.2 元数据字段

| 字段 | 类型 | 展示位置 | 说明 |
|---|---|---|---|
| `desc` | string | 列表卡片 / 详情页 | 纯文本简介 |
| `desc_html` | string | 详情页 | 富文本（Moo 会消毒后渲染） |
| `changelog` | string | 详情页「更新日志」 | 本版更新日志；多版本请在 `releases.<ver>.changelog` 里写 |
| `updated_at` | string | 详情页「最近更新」 | 本版发布时间 |
| `first_release_at` | string | 详情页「最早发布」 | 该应用最早发布时间 |
| `icon_url` | string | 列表 / 详情 | 图标；**建议方形 ≥128×128**；缺省走内置图标通道 |
| `preview_urls` | string[] | 详情页「预览」轮播 | 截图；**建议宽度 ≥800px**；**支持双指缩放** |
| `readme_url` | string | 详情页「README」 | Markdown 或 HTML；**默认只露出约 10 行**，点「展开」看全文 |
| `homepage` | url | 详情页链接 | 项目主页 |
| `bug_report_url` | url | 详情页链接 | 问题反馈 |
| `download_count` | number | 详情页「下载次数」 | 下载量 |
| `license` | string | 详情页 | 许可协议，如 `MIT` |
| `categories` / `labels` / `tags` | string / string[] | 卡片徽章 / 详情页标签 | 三者等价，**推荐 `categories`**，取值见 §4.3 |

### 4.3 分类取值（固定清单，不能自造）

- **多值**写数组或逗号分隔；**第一项视为「主分类」**（界面筛选以它为准）。
- 客户端顶部「全部」是界面筛选项，**不要写进分类**。
- 拿不准就**留空**（Moo 会尽力归类，但写错会把人带到错误分类）。

| 推荐写法 | 等价 key | 适用 |
|---|---|---|
| `AI` | `ai` | AI / 大模型相关 |
| `影音娱乐` | `media` | 影音、音乐、视频 |
| `媒体自动化` | `automation` | 媒体库、自动化整理 |
| `游戏` | `game` | 游戏与游戏服务 |
| `摄影摄像` | `photo` | 相册、监控、影像 |
| `实用效率` | `efficiency` | 效率工具 |
| `开发工具` | `devtools` | 开发、运维、代码 |
| `生活服务` | `lifestyle` | 生活类服务 |
| `备份同步` | `backup` | 备份、同步、网盘 |
| `下载` | `download` | 下载器、BT/PT |
| `网络工具` | `network` | 网络、代理、DNS |
| `浏览器` | `browser` | 浏览器类 |
| `驱动` | `driver` | 硬件驱动 |
| `系统工具` | `system` | 系统、安全、容器管理 |

### 4.4 作者 / 发布者 / 源作者（三者不同）

| 字段 | 谁 | 展示位置 |
|---|---|---|
| `author` / `maintainer`（含 `_url` 变体） | **开发者**（两个名字都认，优先 `author`） | 详情页徽章 |
| `distributor` | **发布者 / 分发者**（转包、二次打包方） | 详情页徽章（与开发者同名时**不显示**） |
| `source_info.author` | **源维护者**（与具体应用无关） | 源列表 |

### 4.5 安装行为字段

| 字段 | 取值 | 说明 |
|---|---|---|
| `app_type` | `fpk` / `docker` / `native` | 应用类型；`docker` 会在列表/详情显示容器标记 |
| `install_type` | `root` / `package` / `用户空间` / `系统空间` / `存储空间` | **运行身份**（root/package）或**安装位置**（系统空间/存储空间）。详情页**自适应标签**：运行身份 → 「运行方式」（`package` 显示为 **用户空间**，`root` 原样）；安装位置 → 「安装位置」；**认不出的值整行不显示** |
| `service_port` | number / string | 应用服务端口（展示、跳转） |
| `min_fnos` | string | 最低 fnOS 版本（不满足会提示） |
| `wizard` | object | 安装向导参数，见示例 7 |

---

## 5. 版本与安装包

### 5.1 单版本

顶层写 `version` + `download_url` + `sha256` 即可（§1 示例）。

### 5.2 多版本：`releases`

```jsonc
"releases": {
  "1.2.0": {
    "changelog": "新增「双指缩放」；修复 Dock 点击反馈。",   // 该版更新日志
    "updated_at": "2026-10-01T21:19:25+08:00",            // 该版发布时间
    "packages": {
      // size = 字节数（另有可选的 size_mb 仅供人读）
      "x86": { "download_url": "https://example.com/app-1.2.0-x86.fpk",
               "sha256": "…", "size": 7784783, "size_mb": 7.42 },
      "arm": { "download_url": "https://example.com/app-1.2.0-arm.fpk", "sha256": "…", "size": 7500000 },
      "all": { "download_url": "https://example.com/app-1.2.0-all.fpk", "sha256": "…", "size": 7600000 }
    }
  },
  "1.1.0": { "packages": { "x86": { "download_url": "…", "sha256": "…" } } }
}
```

**Moo 的挑选规则（4 步）**

1. 按**版本降序**取最新版本（比较规则见 §5.3）；
2. 在该版本 `packages` 里按 **当前架构 → `all`** 回退取第一个可用包；
3. 用它的 `download_url` / `sha256` / `size` 补齐详情页的下载链接、校验和、体积；
4. 全部版本（含每版 `changelog`）渲染到详情页「更新日志」——**默认只显示最近 3 条**，点右侧按钮展开全部。

### 5.3 版本比较规则（重要，影响"谁是最新版/有没有更新"）

Moo 的比较算法（`versionLess`）：

1. 去掉**前导 `v`**（`v1.2.3` 与 `1.2.3` 等价）；
2. 把版本切成「**数字前缀**」与「**后缀**」：`1.2.0-panel` → 数字 `1.2.0`、后缀 `-panel`；
3. 数字前缀按 `.` 分段**逐段数值比较**（所以 `0.10.0 > 0.9.0` ✓，字符串比较会错）；
4. 数字前缀相同再看**后缀字符串**比较：后缀非空 > 后缀为空（所以 `1.2.0-panel > 1.2.0`）。

| 例子 | 谁更新 | 为什么 |
|---|---|---|
| `0.10.0` vs `0.9.0` | `0.10.0` | 第 2 段 10 > 9 |
| `1.2.0-panel` vs `1.2.0` | `1.2.0-panel` | 数字相同，后缀非空更大 |
| `1.0.0` vs `1.0.0-rc1` | `1.0.0-rc1` | 同上（**注意与 semver 相反**：Moo 把带后缀视为更大） |
| `2.0` vs `2.0.0` | 相等 | 缺段按 0 补齐 |

> 结论：**版本号尽量用纯数字三段式** `x.y.z`；需要区分线路（如 `-panel`）就统一加后缀，
> 不要同一条线路里一会儿带后缀一会儿不带。

---

## 6. 示例合集（逐行注释）

### 示例 1 · 最小可用（一个应用、一个版本）

```jsonc
{
  "schema_version": "moo",
  "source_info": { "name": "我的小仓库" },
  "apps": {
    "myapp": {
      "display_name": "我的应用",
      "version": "1.0.0",
      "download_url": "https://example.com/myapp-1.0.0.fpk",
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "size": 3.2,
      "categories": ["实用效率"],
      "desc": "一个示例应用。"
    }
  }
}
```

### 示例 2 · 全字段（可选字段都写上）

```jsonc
{
  "schema_version": "moo",

  "source_info": {
    "name": "示例应用仓",                       // 源显示名
    "author": "Example Org",                            // 源维护者
    "homepage": "https://github.com/example-org/apps",
    "description": "社区维护的 fnOS 应用源",
    "updated_at": "2026-10-01T21:00:00+08:00"
  },

  "apps": {
    "dockflare": {
      "display_name": "DockFlare",                    // 展示名
      "version": "3.1.3",                             // 版本
      "app_type": "docker",                           // fpk | docker | native
      "platform": ["x86", "arm"],                     // 可用架构
      "size": 12.3,                                   // 条目级体积：MB
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "download_url": "https://example.com/dockflare-3.1.3.fpk",

      "icon_url": "icons/dockflare.png",              // 相对路径按 moo.json 目录补全
      "preview_urls": ["shots/1.png", "shots/2.png"], // 详情页轮播（支持双指缩放）
      "readme_url": "docs/dockflare.md",              // 详情页 README（默认露约 10 行）

      "desc": "Cloudflare Tunnel 的自动 DNS/隧道管理面板。",
      "desc_html": "<p>支持多云隧道、自动 DNS…</p>",    // 富文本（会消毒）
      "changelog": "新增 IPv6 隧道支持。",
      "updated_at": "2026-10-01T21:19:25+08:00",
      "first_release_at": "2025-03-11T10:00:00+08:00",

      "categories": ["网络工具", "实用效率"],           // 第一项为主分类
      "author": "ChrispyBacon-dev",
      "author_url": "https://github.com/ChrispyBacon-dev",
      "distributor": "Example Org",
      "homepage": "https://github.com/ChrispyBacon-dev/dockflare",
      "bug_report_url": "https://github.com/ChrispyBacon-dev/dockflare/issues",
      "license": "GPL-3.0",
      "download_count": 1200,

      "install_type": "root",                          // 运行方式：root → 显示 root
      "service_port": 5000,                            // 服务端口
      "min_fnos": "0.9.24"                             // 最低系统版本
    }
  }
}
```

### 示例 3 · 应用集合（多应用 + 分类 + 图标/README/预览）

```jsonc
{
  "schema_version": "moo",
  "source_info": { "name": "家庭 NAS 工具集", "distributor": "example-org" },
  "apps": {
    "alist": {
      "display_name": "AList 网盘聚合",
      "version": "3.29.0",
      "download_url": "pkgs/alist-3.29.0-x86.fpk",
      "sha256": "aaaa…（64 位）",
      "size": 21.4,
      "categories": ["备份同步"],
      "icon_url": "pkgs/alist.png",
      "preview_urls": ["pkgs/alist-1.png"],
      "readme_url": "pkgs/alist.md",
      "desc": "多存储网盘聚合列表程序。",
      "author": "AlistGo",
      "license": "MIT"
    },
    "qbittorrent": {
      "display_name": "qBittorrent",
      "version": "5.2.2",
      "app_type": "docker",
      "download_url": "pkgs/qbittorrent-5.2.2-x86.fpk",
      "sha256": "bbbb…（64 位）",
      "size": 33.1,
      "categories": ["下载", "网络工具"],
      "install_type": "package",
      "service_port": 8080,
      "desc": "带内置 Web 面板的 BT 客户端。"
    },
    "nettools": {
      "display_name": "网络工具箱",
      "version": "1.4.0",
      "download_url": "pkgs/nettools-1.4.0-x86.fpk",
      "sha256": "cccc…（64 位）",
      "size": 2.0,
      "categories": ["网络工具"],
      "desc": "DNS 查询、端口探测、测速小工具合集。"
    }
  }
}
```

### 示例 4 · 多版本 + 多架构（以 release 发布的仓库）

```jsonc
{
  "schema_version": "moo",
  "source_info": { "name": "Demo 应用仓", "distributor": "Example Org" },

  "apps": {
    "demo-app": {
      "display_name": "示例应用",
      "app_type": "fpk",
      "platform": ["x86"],
      "categories": ["系统工具"],
      "install_type": "root",
      "icon_url": "icons/demo.png",
      "readme_url": "README.md",
      "download_count": 1200,                 // 真实下载数（可由生成器自动汇总）
      "first_release_at": "2026-10-01T08:49:56+08:00",
      "sha256": "…（= releases 最新版 x86 包的 sha256，可省）",

      "releases": {
        "1.2.0": {
          "changelog": "🎨 **示例**——这一版改了 xxx。",
          "updated_at": "2026-10-02T00:10:00+08:00",
          "packages": {
            "x86": { "download_url": "https://host/owner/repo/releases/download/v1.2.0/moo_1.2.0_x86.fpk",
                     "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "size": 7600000, "size_mb": 7.25 }
          }
        },
        "1.1.0": {
          "changelog": "🧩 **新增**——这一版新增了 xxx 功能。",
          "updated_at": "2026-10-01T23:56:04+08:00",
          "packages": {
            "x86": { "download_url": "https://host/…/v1.1.0/…fpk", "sha256": "…", "size": 7550000 }
          }
        }
      }
    }
  }
}
```

> 只写 `releases`、不写顶层 `version`/`download_url` 时，Moo 自动挑最新版并按当前架构取包（§5.2）。

### 示例 5 · Docker 应用

```jsonc
{
  "schema_version": "moo",
  "apps": {
    "vaultwarden": {
      "display_name": "Vaultwarden",
      "version": "1.32.7",
      "app_type": "docker",                      // 详情页显示容器标记
      "install_type": "package",                 // 运行方式 → 显示「用户空间」
      "service_port": 8080,
      "platform": ["x86", "arm"],
      "download_url": "https://example.com/vaultwarden-1.32.7.tar.gz",
      "sha256": "dddd…（64 位）",
      "size": 48.6,
      "categories": ["实用效率", "系统工具"],
      "desc": "轻量级 Bitwarden 服务端实现。",
      "author": "dani-garcia",
      "license": "AGPL-3.0"
    }
  }
}
```

### 示例 6 · 自建 / 内网（相对路径）

```jsonc
{
  "schema_version": "moo",
  "source_info": { "name": "内网应用仓", "homepage": "http://192.0.2.10:3000/owner/apps" },
  "apps": {
    "intranet-tool": {
      "display_name": "内网工具",
      "version": "0.9.0",
      // 相对路径 → Moo 用 moo.json 的地址补全；换域名/端口不用改文件
      "download_url": "pkgs/intranet-tool-0.9.0.fpk",
      "icon_url": "pkgs/intranet-tool.png",
      "sha256": "eeee…（64 位）",
      "size": 5.1,
      "categories": ["实用效率"],
      "desc": "仅内网可用的小工具。"
    }
  }
}
```

源地址填任一种即可（等价）：

```text
http://192.0.2.10:3000/owner/apps/moo.json
http://192.0.2.10:3000/owner/apps/raw/branch/main/moo.json     ← Gitea 仓库形态
```

### 示例 7 · 带安装向导参数（`wizard`）

```jsonc
{
  "schema_version": "moo",
  "apps": {
    "blog": {
      "display_name": "博客系统",
      "version": "2.1.0",
      "download_url": "pkgs/blog-2.1.0.fpk",
      "sha256": "ffff…（64 位）",
      "size": 18.0,
      "categories": ["开发工具"],
      "desc": "带后台的轻量博客。",
      // 安装时向用户收集的参数（键名由应用自身定义）
      "wizard": {
        "fields": [
          { "key": "SITE_TITLE", "label": "站点标题", "default": "我的博客", "required": true },
          { "key": "ADMIN_EMAIL", "label": "管理员邮箱", "required": true },
          { "key": "PORT",        "label": "服务端口",  "default": "8090" }
        ]
      }
    }
  }
}
```

---

## 7. Moo 如何处理你的源（同步 · 展示 · 安装）

### 7.1 同步

1. 按 §2.2 候选取索引（**moo.json 优先**），解析成应用条目；
2. 与已装应用、平台官方目录**合并**：同一 `appname` 视为同一应用 —— 状态（已安装/运行中）以平台为准，
   展示会保留各来源徽章；**社区源的同名更高版本不会造成「假更新」**（更新判断以已安装来源为准）；
3. 周期性刷新：默认 **1 小时**（设置 → 系统设置 → 自动检查更新间隔可改 1/6/24 小时）；
   也可在页面右上角点「**刷新页面**」手动触发；
4. 源状态：连续 **5 次**同步为空 → 自动停用（源列表里手动重新启用）。

### 7.2 展示（源里的字段会出现在哪）

| 详情页位置 | 取值来源 |
|---|---|
| 图标 / 展示名 / 版本 | `icon_url` / `display_name` / `version`（或从 `releases` 挑出的版本） |
| 安装包大小 / SHA256 | 条目级 `size`(MB) / `sha256`，或所挑包的 `size`(字节) / `sha256` |
| 下载次数 | `download_count`（缺失时显示本机安装次数） |
| 运行方式 / 安装位置 | `install_type`（自适应标签，见 §4.5） |
| 最近更新 / 最早发布 | `updated_at` / `first_release_at`（或 `releases` 内对应值） |
| 预览（轮播，可双指缩放） | `preview_urls` |
| README（默认约 10 行，可展开） | `readme_url` |
| 更新日志（默认 3 条，可展开） | `releases.<ver>.changelog`（单版本用 `changelog`） |
| 分类标签 | `categories` / `labels` / `tags`（§4.3） |
| 开发者 / 发布者徽章 | `author`/`maintainer`、`distributor` |

> 图标 / 预览图 / README 由 Moo **代理拉取**并缓存；取不到时显示占位（不影响其余信息）。
> 若你的仓库需要登录才能读 raw 文件，这些资源会取不到 —— 请放开匿名读，或让 Moo 拿到访问令牌。

### 7.3 安装 / 更新

1. 用户点「安装」→ Moo 取所挑包的 `download_url` 下载；
2. **校验 `sha256`**（不匹配直接拒绝，并报错）；
3. 交给平台安装（FPK）/ 走容器流程（docker）；
4. 更新判断：拿当前安装版本与源里的最新版按 §5.3 比较，**更高才提示「有更新」**；
5. 同名多源时，更新包来自**当初安装的那个源**，避免被别的源带偏。

---

## 8. 校验、错误与缓存行为

| 层级 | 情形 | Moo 的行为 |
|---|---|---|
| 源级 | 候选地址全部失败 / 非 JSON / 结构错误 | 本次同步记「空」，**保留上次成功缓存**；连续 5 次空 → 自动停用该源 |
| 应用级 | 缺 `download_url`（且 `releases` 无可用包） | **跳过该应用**，其余照常同步 |
| 应用级 | `appname` 与包内 manifest 不一致 | 安装失败 / 更新识别不到 —— **务必对齐** |
| 版本级 | `releases` 某版缺包 | 跳过该版，继续看下一版 |
| 安装包级 | `sha256` 不匹配 | **拒绝安装**并报错（不填则退化为结构校验） |
| 安装包级 | 下载 404 / 超时 | 报错可重试；不影响其他应用 |
| 资源级 | 图标 / 预览 / README 拉取失败 | 显示占位；文字信息正常 |

**URL 与资源要求**

- `download_url` / `icon_url` / `preview_urls` / `readme_url` 支持 **http(s) 直链**与**相对路径**；
  相对路径按 `moo.json` 所在目录解析。
- 图标建议**方形、≥128×128**；预览图建议**宽度 ≥800px**；README 用 Markdown（图片用绝对链接最稳）。
- 私有仓库请放开匿名读，否则上述资源都无法拉取。

---

## 9. 生成 `moo.json`

### 9.1 从 FPK 目录生成

```bash
cd moo && go run ./tools/gen-moo-json \
  -dir /path/to/fpk-store \                 # 递归扫描 *.fpk
  -name "示例应用仓" \
  -distributor "Example Org" \
  -base-url "https://example.com/apps" \    # download_url 前缀（省略则写相对路径）
  -out moo.json
```

自动完成：读 FPK 内 `manifest`（appname/version/display_name/desc/author/platform/service_port…）→
算 **sha256 与字节体积** → FPK 旁有 `ICON.PNG` 自动带 `icon_url` → 同名应用按**版本比较**（§5.3）只留最高版。

### 9.2 从仓库 releases 生成

```bash
python3 tools/gen-repo-moo-json.py \
  --repo https://gitea.example.com/owner/repo --user <user> --password <token> \
  --app demo-app --name "示例应用仓" --distributor "Example Org" \
  --labels "系统工具" --install-type root \
  --preview "docs/shot-1.png,docs/shot-2.png" \
  --readme-file "README.md" --icon-file "icons/demo.png" \
  --out moo.json
```

遍历全部 release：每个 FPK 附件写成 `releases.<版本>.packages.x86`（直链 + sha256 + 字节体积 + 发布时间），
自动汇总**真实下载数**与**最早发布时间**；`--preview/--readme-file/--icon-file` 传仓库内相对路径会自动转 raw 链接。

---

## 10. FAQ / 排查手册

| 现象 | 原因 | 解决 |
|---|---|---|
| 添加源报「无候选地址」 | 候选全部请求失败（常见：文件没推上去、服务刚启动、内网不可达、仓库需登录） | 先在浏览器直接打开 `moo.json` 确认能 200；内网 Gitea 用 raw 直链；必要时放开匿名读 |
| 搜索框贴链接搜不到应用 | 链接形态不被识别（写了裸文件名、或主机是 IP 但格式不对）、或该源尚未被 Moo 读过一次 | 用完整链接（含 `http://`）；先添加为源再搜 |
| 应用出现了但没图标 | `icon_url` 缺失或取不到 | 填 `icon_url`；确认该地址匿名可访问；建议方形 ≥128×128 |
| 「安装包大小」显示 0.0 MB | **单位写错**（把字节写进条目级 `size`，或把 MB 写进包级 `size`） | 条目级用 MB，`packages.<arch>.size` 用字节（§4.1/§5.2） |
| 没有「最近更新 / 最早发布」 | 没填 `updated_at` / `first_release_at`（多版本源看 `releases.<ver>.updated_at`） | 补上字段（生成器会自动填） |
| 预览图是空的 | `preview_urls` 为空或图片取不到 | 补 `preview_urls`；确认图片匿名可访问 |
| README 里图片显示「图片不可用」 | README 内嵌图片地址取不到（常见：私有仓库 raw 需登录） | 放开匿名读，或用绝对可访问的图片地址 |
| 更新红点一直亮，装完还亮 | 版本号写法不统一（如一会 `1.0` 一会 `1.0.0-panel`），或 `appname` 与已装包不一致 | 统一版本号规则（§5.3）；核对 `appname` |
| 同名应用出现两条 | 不同源里 `appname` 不同（例如 `foo` 与 `foo.fpk`） | 统一 `appname`；Moo 视**同一 appname** 为同一应用并合并展示 |
| 源自动被停用了 | 连续 5 次同步为空 | 检查源地址/网络后在源列表重新启用 |
| 包下载失败 | `download_url` 404、需登录、或 `sha256` 不匹配 | 直链可匿名访问；`sha256` 与实际包一致 |

---

## 11. 最佳实践

1. **`appname` 与包内 manifest 严格一致** —— 最常见的坑，直接影响安装与更新识别。
2. **一定填 `sha256`** —— 唯一的包完整性校验手段；生成器会自动算。
3. **单位别写错**：条目级 `size` = MB；`packages.<arch>.size` = 字节。
4. **相对路径优先** —— 换域名/端口无需改文件（`download_url`/`icon_url`/`preview_urls`）。
5. **分类从 §4.3 清单里选**，第一项放主分类；不确定就留空。
6. **多版本用 `releases`**，把 `changelog`/`updated_at` 填上（详情页直接读它）。
7. **版本号统一规则**（建议纯数字三段 + 固定后缀），避免更新判断异常。
8. **发布前自测**：把链接粘到首页搜索框 → 能搜到、图标/预览/README 正常、体积与版本无误，再添加为源。
9. **私有仓库要么放开匿名读，要么让 Moo 能拿到访问令牌**，否则索引/图标/预览/README/包都取不到。
10. **一条线路一个 `moo.json`**：不要把「稳定版」和「测试版」混在同一份索引里（版本比较会挑到测试版）。

---

## 附录 A · 字段速查总表

| 字段 | 层级 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `schema_version` | 顶层 | string | 是 | 固定 `"moo"` |
| `source_info.name` | 顶层 | string | 建议 | 源显示名 |
| `source_info.author` / `distributor` | 顶层 | string | 建议 | 源维护者 |
| `source_info.homepage` / `description` / `updated_at` | 顶层 | string | 可选 | 源元信息 |
| `apps.<appname>.display_name` | 应用 | string | 建议 | 展示名 |
| `apps.<appname>.version` | 应用 | string | 建议 | 版本号 |
| `apps.<appname>.download_url` | 应用 | string | 是* | 包直链（*有 `releases` 时可省） |
| `apps.<appname>.sha256` | 应用 | string | 强烈建议 | 包校验和 |
| `apps.<appname>.size` | 应用 | number/string | 可选 | 体积 **MB** |
| `apps.<appname>.platform` | 应用 | string/array | 可选 | `x86` / `arm` / `all` |
| `apps.<appname>.app_type` | 应用 | string | 可选 | `fpk` / `docker` / `native` |
| `apps.<appname>.install_type` | 应用 | string | 可选 | 运行方式 / 安装位置（§4.5） |
| `apps.<appname>.categories` / `labels` / `tags` | 应用 | string/array | 可选 | 分类（§4.3） |
| `apps.<appname>.desc` / `desc_html` | 应用 | string | 可选 | 简介 |
| `apps.<appname>.changelog` | 应用 | string | 可选 | 本版更新日志 |
| `apps.<appname>.updated_at` / `first_release_at` | 应用 | string | 可选 | 最近更新 / 最早发布 |
| `apps.<appname>.icon_url` / `preview_urls` / `readme_url` | 应用 | string / string[] | 可选 | 图标 / 预览 / README |
| `apps.<appname>.author` / `maintainer`(+`_url`) | 应用 | string | 可选 | 开发者 |
| `apps.<appname>.distributor` | 应用 | string | 可选 | 发布者 |
| `apps.<appname>.homepage` / `bug_report_url` / `license` | 应用 | string | 可选 | 链接与许可 |
| `apps.<appname>.download_count` | 应用 | number | 可选 | 下载量 |
| `apps.<appname>.service_port` / `min_fnos` / `wizard` | 应用 | number/string/object | 可选 | 端口 / 最低版本 / 向导 |
| `apps.<appname>.releases.<ver>.changelog` / `updated_at` | 版本 | string | 可选 | 该版信息 |
| `apps.<appname>.releases.<ver>.packages.<arch>.download_url` | 包 | string | 是 | 该架构包直链 |
| `apps.<appname>.releases.<ver>.packages.<arch>.sha256` | 包 | string | 强烈建议 | 该包校验和 |
| `apps.<appname>.releases.<ver>.packages.<arch>.size` | 包 | number | 可选 | 体积 **字节**（另可加 `size_mb`） |

## 附录 B · 术语表

| 术语 | 含义 |
|---|---|
| **源 / 应用源** | 一份 `moo.json` + 它引用的应用与包；在 Moo 里以名称出现 |
| **appname** | 应用唯一标识（`apps` 的 key），须与包内 manifest 一致 |
| **条目级字段** | 直接写在 `apps.<appname>` 下的字段（`size` 单位是 MB） |
| **包级字段** | 写在 `releases.<ver>.packages.<arch>` 下的字段（`size` 单位是字节） |
| **主分类** | `categories` 数组第一项，界面筛选用它 |
| **运行方式 vs 安装位置** | 前者是运行身份（root / package→用户空间），后者是装在哪（系统空间 / 存储空间） |
| **贴链接直搜** | 首页搜索框直接粘贴源链接即可列出该源应用 |
| **同名合并** | 同一 `appname` 在多源出现时，Moo 合并为一条展示、更新跟随已装来源 |
