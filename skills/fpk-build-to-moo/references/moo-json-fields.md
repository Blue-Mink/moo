# `moo.json` 字段速查（含源级安装向导）

> 目标：发布者不用翻完整协议也能写对字段。完整语义/错误缓存/FAQ 见
> `docs/Moo应用源协议.md`；本页只列“发布时最常用 + 最近新增”的字段。

## 1. 顶层
```jsonc
{
  "schema_version": "moo",                 // 固定字符串
  "source_info": {                         // 可选；源列表展示
    "name": "示例仓库",
    "distributor": "Example Org",
    "homepage": "https://example.com",
    "description": "一句话说明",
    "i18n": {}
  },
  "apps": { "<appname>": { /* 见下 */ } }
}
```
`apps` 的键 = 包内 `manifest.appname`，大小写敏感，发布后不要改。

## 2. 应用字段
| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `display_name` | string | 建议 | 缺省回退 appname |
| `version` | string | 单版本必填 | 多版本可省，从 `releases` 挑最新 |
| `download_url` | string | 单版本必填 | FPK 直链；相对路径按 `moo.json` 目录补全 |
| `platform` | string/array | 可选 | `x86`/`arm`/`all`；缺省 x86 |
| `sha256` | string | 强烈建议 | 64 位 hex；不匹配拒绝安装 |
| `size` | number/string | 可选 | **条目级 MB**（`12.3`） |
| `size_bytes` | number | 可选 | **条目级字节**；与 `size` 同时存在时优先 |
| `categories` / `labels` / `tags` | string/array | 可选 | 14 个固定分类，第一项=主分类 |
| `desc` | string | 可选 | 纯文本简介 |
| `desc_html` | string | 可选 | 富文本；0.6.269+，消毒后渲染，优先于 `desc` |
| `changelog` | string | 可选 | 本版更新日志；多版本写 `releases.<ver>.changelog` |
| `updated_at` | string | 可选 | 本版发布时间 |
| `first_release_at` | string | 可选 | 最早发布时间（生成器可填） |
| `icon_url` | string | 可选 | 商店卡片图标；建议 ≥512 |
| `preview_urls` | string[] | 可选 | 详情轮播；建议宽度 ≥800 |
| `readme_url` | string | 可选 | README；默认露约 10 行 |
| `homepage` | url | 可选 | 项目主页 |
| `bug_report_url` | url | 可选 | 详情页「问题反馈」；0.6.388+ |
| `download_count` | number | 可选 | 下载次数（生成器可汇总） |
| `license` | string | 可选 | 许可协议，如 `MIT`；0.6.269+ |
| `app_type` | string | 可选 | `fpk`/`docker`/`native` |
| `install_type` | string | 可选 | `root`/`package`/`用户空间`/`系统空间`/`存储空间`；详情页自适应标签 |
| `service_port` | number/string | 可选 | 应用服务端口 |
| `min_fnos` | string | 可选 | 最低 fnOS 版本；0.6.269+ 安装期平台校验 |
| `wizard` | object | 可选 | **源级安装向导**，见 §4 |
| `releases` | object | 多版本必填 | 版本集合，见 §3 |
| `arch_diff` | object | 可选 | 旧格式：按 x86/arm/all 分列下载链接 |

## 3. `releases` / `packages`
```jsonc
"releases": {
  "1.2.3": {
    "changelog": "修复 …",
    "updated_at": "2026-11-03T00:00:00+08:00",
    "packages": {
      "x86": { "download_url": "…x86.fpk", "sha256": "<64-hex>", "size": 12345678 },
      "arm": { "download_url": "…arm.fpk", "sha256": "<64-hex>", "size": 12000000 }
    }
  }
}
```
- 包级 `size` 是**字节**；条目级 `size` 是 MB；`size_bytes` 是条目级字节。
- `packages.<arch>` 只允许 `x86`/`arm`/`all`；缺当前架构时回退 `all`。
- 每个含 `download_url` 的分支都自带 `sha256` + `size`，不要跨分支继承。
- 多版本索引只留最近 2 版；顶层 `version/download_url/sha256/size/updated_at/changelog` 跟最新版。

## 4. 源级安装向导 `wizard`
```jsonc
"wizard": {
  "fields": [
    { "key": "SITE_TITLE",   "label": "站点标题", "default": "我的博客", "required": true },
    { "key": "ADMIN_EMAIL",  "label": "管理员邮箱", "required": true },
    { "key": "PORT",         "label": "服务端口", "default": "8090" }
  ]
}
```
- **0.6.269+**：Moo 在安装前弹窗收集，值随安装请求下发给应用；键名由应用自己定义，不强制 `wizard_` 前缀。
- 这是 **Moo 源级** 的一层；FPK 里的 `wizard/install` 是**平台级**的另一层。两层可能同时存在，键名/合并顺序/优先级由应用安装逻辑处理；发布前用 Moo 客户端真实安装一次抓请求确认。
- `required`/`default` 只是客户端提示；服务端/安装回调必须再校验（端口、路径、必填）。
- `gen-moo-json` 目前不生成 `wizard`；需要手写进 `moo.json`，或由你自己的发布脚本合并。

## 5. 生成器能力边界
| 工具 | 已支持 | 需要手写/补 |
|---|---|---|
| `gen-moo-json` | 从 FPK manifest 读 appname/version/display_name/desc/author/platform/service_port；`--homepage`/`--name`/`--distributor`；`bug_report_url`/`app_type`/`size_bytes`/`install_type`/`labels`/`updated_at` 等字段（以当前源码为准） | `wizard`、`desc_html`、`license`、`min_fnos`、`preview_urls`、`readme_url` 可能需要手写或补参数 |
| `gen-repo-moo-json.py` | 遍历 Release 生成 `releases`；`--preview`/`--readme-file`/`--icon-file`/`--labels`/`--install-type`/`--homepage` | `wizard`、`bug_report_url`、`min_fnos` 需手写/补 |
| `update_moo_json.py` | 增量更新 `releases` + 顶层跟版；`--tag` 处理 `mate-v` 等非规范 tag；`--sync-meta` 同步 display_name/desc/license/install_type/os_min_version→min_fnos | `wizard`、`bug_report_url`、`desc_html`、预览/README 需手写或另补 |

## 6. 最小示例与检查
```jsonc
{
  "schema_version": "moo",
  "source_info": { "name": "我的应用仓" },
  "apps": {
    "myapp": {
      "display_name": "我的应用",
      "version": "1.2.3",
      "download_url": "https://example.com/myapp-1.2.3.fpk",
      "sha256": "<64-hex>",
      "size": 12.3,
      "size_bytes": 12900000,
      "categories": ["实用效率"],
      "desc": "一句话简介。",
      "desc_html": "<p>富文本简介</p>",
      "icon_url": "https://example.com/myapp/ICON.PNG",
      "preview_urls": ["https://example.com/myapp/1.png"],
      "readme_url": "https://example.com/myapp/README.md",
      "homepage": "https://example.com/myapp",
      "bug_report_url": "https://example.com/myapp/issues",
      "license": "MIT",
      "min_fnos": "0.9.24",
      "install_type": "root",
      "service_port": 8090,
      "wizard": { "fields": [
        { "key": "PORT", "label": "服务端口", "default": "8090", "required": true }
      ] }
    }
  }
}
```

发布前检查：
- [ ] `schema_version` 是 `"moo"`；`apps` 键 = manifest `appname`
- [ ] `download_url` + `sha256` + `size` 三者一致；条目 `size` 是 MB，`packages.*.size` 是字节
- [ ] `categories` 只取 14 个固定值；第一项是主分类
- [ ] `icon_url`/`preview_urls`/`readme_url` 匿名可达；图标 ≥512
- [ ] `bug_report_url`/`license`/`min_fnos`/`desc_html` 按需填写
- [ ] `wizard` 键名与应用安装逻辑一致；真机安装验证
- [ ] 多版本 `releases` 只留 2 版；顶层字段跟最新版

