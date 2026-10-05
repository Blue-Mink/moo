# Moo HTTP API 全量文档

> 版本基线：0.6.282（2026-10-05）· 适用架构：fnOS x86_64（Go 后端 + React 前端单二进制服务）
>
> 本文档覆盖 Moo 对外提供的全部 91 个 HTTP 端点，含请求/响应结构、SSE 事件协议、认证与安全模型、构建部署流程。文中不出现任何真实主机地址、凭据或令牌；示例一律使用占位符。

---

## 目录

1. [总则与约定](#1-总则与约定)
2. [认证与可信通道](#2-认证与可信通道)
3. [通用类型](#3-通用类型)
4. [SSE 事件协议](#4-sse-事件协议)
5. [系统与状态](#5-系统与状态)
6. [应用目录](#6-应用目录)
7. [安装 / 更新 / 卸载 / 下载](#7-安装--更新--卸载--下载)
8. [应用控制与信息](#8-应用控制与信息)
9. [应用源管理](#9-应用源管理)
10. [收藏](#10-收藏)
11. [设置](#11-设置)
12. [备份与缓存清理](#12-备份与缓存清理)
13. [FPK 下载缓存](#13-fpk-下载缓存)
14. [后台任务](#14-后台任务)
15. [加速源（镜像）](#15-加速源镜像)
16. [面板账号](#16-面板账号)
17. [通知系统](#17-通知系统)
18. [Moo 自更新](#18-moo-自更新)
19. [错误处理](#19-错误处理)
20. [安全模型](#20-安全模型)
21. [构建与部署全流程](#21-构建与部署全流程)

---
- [22. 官方应用中心](#22-官方应用中心)

## 1. 总则与约定

### 1.1 基址

| 通道 | 基址 | 说明 |
|---|---|---|
| 面板 WebUI（推荐） | `http://<NAS_IP>:5666/app/moo/api/` | 经 fnOS 应用中心网关代理，自动携带面板会话 → **可信通道** |
| 直连（可信设备） | `http://<NAS_IP>:38101/api/` | 服务默认监听端口，可被 `MOO_WEB_PORT` 环境变量覆盖 |

- 所有端点路径以 `/api/` 开头；非 `/api` 路径回退 SPA 前端（`index.html`）。
- 服务仅监听**回环与本机面板代理**可达的地址面；写操作一律要求可信通道 + 管理员（见 §2）。
- 静态资产（`/app/moo/` 下 JS/CSS/图标）由同一服务提供。

### 1.2 编码

- 请求/响应均为 UTF-8 JSON（除明确标注的二进制/HTML/SSE 端点）。
- `Content-Type: application/json`（POST/PUT 必须）；响应 `Accept-Encoding: gzip` 时 JSON 透明压缩（SSE 与二进制直通不压缩）。
- 列表类大响应（如 `GET /api/apps`）带 **ETag**，支持 `If-None-Match` → `304` 协商缓存。

### 1.3 端点统计（2026-10-01 按代码重算，共 85 条）

| 域 | 数量 |
|---|---|
| 系统与状态 | 7 |
| 应用目录 | 6 |
| 安装/更新/卸载/下载（SSE） | 7 |
| 应用控制与信息 | 8 |
| 应用源管理 | 12 |
| 收藏 | 2 |
| 设置 | 5 |
| 备份与缓存 | 6 |
| FPK 下载缓存 | 3 |
| 后台任务 | 2 |
| 加速源 | 3 |
| 面板账号 | 1 |
| 通知系统 | 14 |
| Moo 自更新 | 2 |
| 官方应用中心 | 7 |
| **合计** | **85** |

### 1.4 上限与超时（2026-10-01 API 审计）

| 项 | 值 | 位置 |
|---|---|---|
| 操作历史 `history` | 最近 **20 条**（超出丢最旧） | `internal/operation/queue.go` |
| 官方商店列表缓存 | **10 分钟** TTL，拉取失败回退旧缓存 | `internal/api/official.go` |
| 请求头读取超时 | **10s**（网关 unix socket 与 TCP 调试口一致） | `cmd/server/main.go` |
| 空闲连接超时 | **120s** | `cmd/server/main.go` |
| 响应写超时 | **不设**（故意）：安装/更新/向导走 SSE 长流，向导单次最长 12 分钟 | `cmd/server/main.go` |
| 请求体大小上限 | **10MB**（`http.MaxBytesReader`，超出 400） | `internal/api/server.go` |

> 端点清单由 `internal/api/api_docs_test.go` 双向守护：新增/改路由而漏改本文档会直接测试失败。

---

## 2. 认证与可信通道

Moo 采用**两层防线**：

1. **可信通道判定**：请求必须来自 fnOS 面板代理（网关注入的可信标记）或本机回环。`GET /api/version` 的 `trusted` 字段反映当前请求是否走可信通道。
2. **管理员校验（requireAdmin）**：全部写操作与敏感读操作经 `requireAdmin` 中间件——要求可信通道**且**当前会话为 fnOS 管理员。非可信/非管理员访问写端点返回 `403`。
3. **变更请求校验头（0.6.207-panel D2，CSRF 纵深防御）**：`requireAdmin` 覆盖的端点里，
   凡**非 `GET`/`HEAD`/`OPTIONS`** 的请求必须携带自定义头 `X-Moo-Admin`（前端固定值 `1`，由 `apiFetch` 自动附加）；
   缺失返回 **400**：
   `{"error":"缺少请求校验头（X-Moo-Admin），请刷新 Moo 页面后重试"}`。
   机制：跨站 simple 请求（表单 POST / `no-cors` fetch）无法附加自定义头（需 CORS 预检，而网关无 CORS 头），
   即使平台网关不校验 `Origin`，CSRF 也在应用层被封死。**只读 GET 豁免**。
   手工 `curl` 调写接口：`curl -H 'X-Moo-Admin: 1' -H 'Content-Type: application/json' …`。

只读端点（目录浏览、详情、健康快照等）允许未登录访问（面板内浏览场景），因此**不要在只读端点泄露敏感数据**；配置类写端点永不开放匿名。

面板账号（`panel_enabled`/`panel_username`/`panel_password`）是 Moo 用来调用 fnOS 应用中心（appcenter daemon）的**本机回环**凭据，只存本机 `config.json`，永不通过 API 回显明文密码（`panel_has_password` 仅返回布尔）。

---

## 3. 通用类型

### 3.1 AppInfo（应用统一视图）

`GET /api/apps` 元素与 `GET /api/apps/{key}` 响应。

| 字段 | 类型 | 说明 |
|---|---|---|
| `key` | string | 应用唯一键（`appname` 或 `appname@源名`，同名多源共存时带后缀） |
| `appname` | string | 平台应用名（appcenter 标识） |
| `display_name` | string | 展示名 |
| `description` | string | 描述 |
| `installed` | bool | 本机是否已装 |
| `installed_version` / `latest_version` | string | 已装版本 / 源内最新版 |
| `installed_fpk_version` / `available_version` | string | FPK 包版本 / 可更新版本 |
| `update_from_source` | string | 更新策略（0.6.272）= origin/lineage 时更新目标的来源源名（跨源同宗）；空 = 安装源自身 |
| `has_update` | bool | 有可用更新（已忽略时为 false） |
| `update_ignored` | bool | 更新已被用户忽略 |
| `update_ignored_pending` | bool | 已忽略但确实压着新版本（角标计数含此类） |
| `platform` / `app_type` / `category` | string | 平台 / 类型 / 分类 |
| `release_url` / `release_notes` | string | 上游发布链接 / 说明 |
| `status` | string | 已装应用运行状态（平台提供） |
| `start_stop` / `uninstallable` | bool? | 可启停 / 可卸载能力位 |
| `web_protocol` / `web_url` / `web_port` / `web_path` | string/int | Web 入口 |
| `web_on_webui` | bool | Web 入口是否在 WebUI 内 |
| `web_service_name` / `service_port` | string/int | 服务名 / 服务端口 |
| `homepage` | string | 主页 |
| `icon_url` | string | 图标直链（可能指向源数据域，抓取受 SSRF 防护约束） |
| `updated_at` | string | 源更新时间 |
| `download_count` | int? | 上游下载量（第三方源无 → 用 `local_installs`） |
| `local_installs` | int | 本机经 Moo 安装/更新累计次数 |
| `post_install_note` | string | 安装后提示 |
| `source` | string | 来源源名 |
| `maintainer` / `maintainer_url` / `distributor` / `distributor_url` | string | 维护者 / 分发者 |
| `changelog` | string | 原始更新日志 |
| `changelog_entries` | [{version, text}] | 解析后的版本化条目（最新在前） |
| `size_bytes` / `sha256` | int64/string | FPK 包大小 / 校验 |
| `preview_count` / `preview_urls` | int/string[] | 预览图数量 / 直链 |
| `has_readme` | bool | 是否有 README |

### 3.2 SourceEntry（应用源）

```json
{
  "id": "src-xxxx", "name": "FnDepot", "url": "https://…/fndepot.json",
  "app_count": 123, "enabled": true, "favorite": false,
  "error": "", "last_fetched": "2026-09-29T12:00:00+08:00", "empty_streak": 0
}
```

- `error`：最近一次同步失败原因（成功时为空）。
- `empty_streak`：连续 0 应用同步轮次（自动监测据此停用空源）。

### 3.3 BackgroundTask（后台任务）

```json
{
  "id": "dl-xxxx", "appname": "someapp", "op": "download",
  "status": "running", "step": "下载中", "progress": 42.5,
  "message": "…", "new_version": "1.2.3",
  "downloaded": 1048576, "total": 4194304, "speed": 524288
}
```

`op` ∈ `install | update | download`；`status` ∈ `running | done | error`。

### 3.4 MirrorHealth（加速源健康快照）

```json
{
  "mirrors": [{"key": "gh-proxy", "url": "https://…/", "ok": true, "latency_ms": 120, "speed_mbps": 8.2}],
  "selected": "auto", "active": "gh-proxy",
  "last_probe": "2026-09-29T12:00:00+08:00",
  "last_switch": {"from": "jsdelivr", "to": "gh-proxy", "reason": "测速优选", "ts": "…"},
  "interval_s": 300
}
```

Docker 组同构（`/api/mirrors/docker/health`）。

### 3.5 Settings（设置页全量视图）

`GET /api/settings` 响应字段（全部为当前生效值）：

`check_interval_hours`, `mirror`, `mirror_options[]`, `docker_mirror`, `docker_mirror_options[]`, `custom_github_mirror`, `custom_docker_mirror`, `install_volume`, `volume_options[]`, `download_dir`, `auto_update`, `catalog_language`, `update_policy`, `source_auto_care_disabled`, `source_list_url`, `source_list_disabled`, `panel_enabled`, `panel_username`, `panel_base_url`, `panel_has_password`, `backup_dir`, `backup_auto`, `backup_interval_days`, `cache_clean_days`, `cache_clean_every_days`, `gh_probe_hours`, `gh_probe_minutes`, `dk_probe_hours`, `dk_probe_minutes`, `proxy_enabled`, `proxy_url`, `dock_order[]`, `settings_tab_order[]`。

字段语义与 `PUT /api/settings` 的指针语义见 §11。

### 3.6 FpkDownloadFile（下载缓存条目）

```json
{"name": "appname-x.y.z.fpk", "size": 7704517, "mod_at": "…",
 "appname": "appname", "display_name": "应用名", "installed": false}
```

---

## 4. SSE 事件协议

长操作（安装/更新/卸载/下载/自更新/目录重载）返回 `text/event-stream`。每条事件为一行：

```
data: {"step":"install","progress":42,"message":"下载中…"}
```

| step | 语义 |
|---|---|
| `<操作名>`（install/update/uninstall/download/downloading/verifying/installing…） | 进行中；`progress` 0–100（下载类为精确值，其它为阶段值），`message` 人类可读进度 |
| `done` | 成功终态，`progress=100`，`message` 结果摘要 |
| `error` | 失败终态，`error` 字段为原因 |

约定：
- **终态唯一**：流以 `done` 或 `error` 之一结束；客户端不得把「连接断开」当成功。
- 客户端断开后**操作在后台继续**（进度可经 `GET /api/operations` / `GET /api/tasks` 轮询恢复）。
- 轮询恢复路径的可靠性：终态按操作 ID 从 history 取回（防 running→error 窗口误报完成）。
- 自更新 SSE 另有阶段词：`downloading` → `verifying` → `installing` → `done`/`error`（§18）。

---

## 5. 系统与状态

### 5.1 `GET /api/version`（公开）
```json
{"version": "0.6.206", "trusted": true}
```
`trusted` = 当前请求是否来自可信通道（前端据此决定是否提示登录）。

### 5.2 `GET /api/status`（公开）
```json
{"version": "0.6.206", "platform": "fnos"}
```

### 5.3 `GET /api/about`（公开）
关于信息（版本、构建时间、许可等）。

### 5.4 `GET /api/daemon/status`（公开）
```json
{"available": true}
```
appcenter daemon（fnOS 应用中心 RPC，本机 unix socket）是否可达。不可达时安装/更新不可用，健康自检会推 `health_error` 事件。

### 5.5 `GET /api/operations`（公开）
```json
{"current": {BackgroundTask|操作视图} | null, "history": [操作视图…]}
```
当前操作 + 历史（SSE 断线后的进度恢复入口）。
**上限**：`history` 固定保留**最近 20 条**（超出丢弃最旧，见 `internal/operation/queue.go`），
因此单次响应体有界；`current` 至多 1 条。需要更长的操作留痕请走应用日志。

### 5.6 `GET /api/installed`（requireAdmin）
平台视角的已安装应用列表（appcenter 原始数据）。

### 5.7 `GET /api/icons/version`（公开）
图标缓存代次（前端判断是否需刷新图标映射）。

---

## 6. 应用目录

### 6.1 `GET /api/apps`（公开，ETag）
响应 `AppsResponse`：
```json
{"apps": [AppInfo…], "last_check": "2026-09-29T12:00:00+08:00", "upgrade_allowed": true}
```
目录合并所有已启用源 + 官方目录；同名应用多源共存（`key` 带 `@源名`）。支持 `If-None-Match`。

### 6.2 `GET /api/apps/{key}`（公开）
单个 AppInfo 详情。查询参数：
- `wizard=1`：附加安装向导信息（配置项表单 schema）。

### 6.3 `GET /api/recommended`（公开）
推荐应用列表（探索页）。

### 6.4 `GET /api/installed-detail`（公开）
已安装应用的平台详情（状态/启停/入口）。

### 6.5 `GET /api/search/source-keys`（公开）
源链接归一化检索（搜索贴源链接用）：
```json
[{"key": "github.com/owner/repo", "names": ["FnDepot", "某源名"]}]
```
把仓库根 / raw 前缀 / 路径 / v1v2 索引 / CDN 镜像等形式归一到 `github.com/owner/repo`（或 fndepot JSON 索引键）。

---

## 7. 安装 / 更新 / 卸载 / 下载

以下端点均为 **requireAdmin + SSE**。`{key}` 为 AppInfo.key（URL 编码）。

| 端点 | 说明 |
|---|---|
| `POST /api/apps/{key}/install` | 安装（下载 FPK → 校验 sha256 → appcenter 安装/升级） |
| `POST /api/apps/{key}/update` | 更新到 `available_version`（保留 @appdata 数据） |
| `POST /api/apps/{key}/uninstall` | 卸载（走平台标准管线） |
| `POST /api/apps/{key}/download-task` | 仅下载 FPK 到缓存（不安装），生成可暂停/续传任务 |
| `POST /api/apps/{key}/task/pause` | 暂停下载任务 |
| `POST /api/apps/{key}/task/resume` | 恢复下载任务 |
| `POST /api/apps/reload` | 重载目录（SSE；等价于全量刷新后重建目录） |
| `POST /api/check` | 检查更新（并发 8 全量源刷新 + 比对已装版本） |
| `PUT /api/apps/{key}/ignore-update` | 忽略该应用的更新（可重复忽略不同版本） |
| `DELETE /api/apps/{key}/ignore-update` | 取消忽略 |

安装/更新成功时 `local_installs[appname]` 累计 +1 并持久化。

`POST /api/check` 响应：
```json
{"updated": 155, "failed": 2, "total_apps": 2093, "updated_apps": 7}
```
（`updated`/`failed` 为源数；`updated_apps` 为有新版本的已装应用数。）

---

## 8. 应用控制与信息

| 端点 | 权限 | 说明 |
|---|---|---|
| `POST /api/apps/{key}/start` | admin | 启动已装应用（平台服务） |
| `POST /api/apps/{key}/stop` | admin | 停止 |
| `GET /api/apps/{key}/panel-detail?appname=…` | admin | 面板侧应用详情（账号/端口/状态） |
| `GET /api/apps/{key}/wizard` | 公开 | 取安装向导字段：非官方应用 `StageOnly` 暂存后 `FetchWizard`；**官方应用**走面板下载链（最长 12 分钟），返回 `{appname, has_wizard, fields…}`，失败时 `has_wizard=false` + `error` |
| `GET /api/apps/{key}/asset?type=…&index=N&u=…` | 公开 | 远端资源代理透传（见下） |
| `GET /api/apps/{key}/diagnostic?step=…&error=…` | 公开 | 应用诊断（0.6.269 起启用）：只读收集 report（应用/版本/架构/失败步骤/错误/日志尾部 ≤50 行/平台）+ `issue_url`，供前端上报页一键生成 issue 内容，不改动任何状态 |

`asset` 查询参数：
- `type=icon`：应用图标（本地缓存优先，miss 时代理抓取；源数据 URL 受 SSRF 防护：仅公共地址）。
- `type=preview&index=N`：第 N 张预览图（同样缓存 + 防护）。
- `type=readme`：README 渲染源（`u` 传原始 URL，30s 超时透传，失败回退占位）。

---

## 9. 应用源管理

全部 **requireAdmin**。`{id}` 为 SourceEntry.id。

| 端点 | 请求体 | 说明 |
|---|---|---|
| `GET /api/sources` | — | 源列表 `[SourceEntry]` |
| `POST /api/sources` | `{"name":"…","url":"https://…"}` | 添加单源（url 必填；支持 fndepot JSON 索引 / conversun 目录 / 官方源列表形式） |
| `POST /api/sources/batch` | `{"items":[{"name":"…","url":"…"}]}` | 批量添加（逐条校验；重复 URL 去重并回 `deduped` 友好计数） |
| `POST /api/sources/reorder` | `{"order":["id1","id2",…]}` | 调整排序（须全量排列） |
| `POST /api/sources/sync-list` | — | 按内置源列表地址同步源清单（开启自动同步时周期执行） |
| `POST /api/sources/restore-defaults` | — | 一键恢复默认源列表（补齐缺失 + 同 URL 去重保一；官方源不删） |
| `POST /api/sources/{id}/sync` | — | 刷新单源 |
| `POST /api/sources/sync-all` | — | 一键刷新所有**已启用**源（并发 8；手动触发，不计自动监测轮次） |
| `POST /api/sources/{id}/toggle` | — | 启用/停用切换 |
| `POST /api/sources/{id}/rename` | `{"name":"新名"}` | 重命名 |
| `POST /api/sources/{id}/favorite` | — | 关注（星标）切换；关注源新增应用/报表变化触发通知 |
| `DELETE /api/sources/{id}` | — | 删除源（已装应用不受影响） |

自动监测策略（`source_auto_care_disabled` 可整体停用）：每轮全量刷新后，「成功但 0 应用」连续 5 次的源自动停用并沉底。

---

## 10. 收藏

| 端点 | 权限 | 说明 |
|---|---|---|
| `GET /api/favorites` | admin | 收藏列表（`[key]`，顺序 = 收藏先后） |
| `POST /api/favorites` | admin | 切换收藏：`{"key":"appname@源名"}`；上限 500 条 |

收藏持久化于 `config.json`，重启保留。发现页「收藏列表」按此渲染；收藏应用有更新触发 `favorite_update` 通知。

---

## 11. 设置

### 11.1 `GET /api/settings`（admin）
返回 §3.5 Settings 全量视图。

### 11.2 `PUT /api/settings`（admin）
局部提交 + **指针语义**：
- 字符串字段（如 `custom_github_mirror`、`proxy_url`、`panel_base_url`、`download_dir`、`backup_dir`）：JSON 中**存在**（含空串）= 显式设置/清除；**缺省** = 不改动。
- 布尔字段（`auto_update`、`proxy_enabled`、`backup_auto`、`panel_enabled`、`source_list_disabled`…）：指针语义同上（缺省 = 不改动）。
- 数值字段（`gh_probe_hours`…、`cache_clean_days`…）：越界整单拒绝。
- `dock_order` / `settings_tab_order`：非空提交必须是**完整排列**（全量提交防脏序）。
- `update_policy`（0.6.272 跨源更新策略）：仅接受 `strict`（默认，只认安装源）/ `origin`（同发布仓库）/ `lineage`（同作者或同发布仓库），其余整单拒绝；变化后目录缓存失效、下次 `/api/apps` 重建。

常用片段示例：

```json
{"proxy_enabled": true, "proxy_url": "socks5://127.0.0.1:1080"}
{"mirror": "auto", "docker_mirror": "daocloud", "auto_update": true}
{"backup_dir": "", "backup_auto": true, "backup_interval_days": 7}
```

校验规则摘要：
- `mirror`/`docker_mirror` ∈ 选项键集合（`auto`/镜像 key/`direct`/`custom`）；`custom` 须配对应自定义地址。
- `proxy_url` 非空时 scheme 必须 ∈ `http|https|socks4|socks5|socks5h` 且带 host；`proxy_enabled=true` 时地址必填（整单拒绝）。代理配置**保存即生效**（运行时动态 transport），仅 GitHub 域名改道。
- `panel_base_url` 必须是回环地址（fnOS 面板 127.0.0.1:5666 形态）。
- `download_dir` 必须是可访问的 /volN 目录；切换会迁移缓存，失败整单拒绝。
- 备份目录须可写（空串 = 回本机默认数据目录）。

### 11.3 `POST /api/settings/proxy-test`（admin）
科学加速代理连通性测试（**不保存**、不影响当前生效配置，单独校验给定地址）：
```json
请求：{"proxy_url": "http://127.0.0.1:7890"}
响应：{"ok": true, "latency_ms": 350}          // 或
      {"ok": false, "error": "…connection refused", "latency_ms": 12}
```
经代理 HEAD `https://github.com`（12s 超时）。

### 11.4 `GET /api/settings/download-dirs`（admin）
可选下载目录列表（卷根 + 卷下顶层共享目录）。

### 11.5 `GET /api/settings/download-dirs/browse?path=/vol1`（admin）
目录浏览（目录选择器用；逐层下钻，返回子目录名）。

### 11.6 `GET /api/settings/docker-mirror/status`（admin）
Docker 优选镜像源状态：读 daemon.json 的 `registry-mirrors` 当前值。
```json
响应：{"mirrors": ["docker.fnnas.com"], "docker_active": true,
       "applied_at": "…", "mirror": "…", "backup_file": "…"}   // 末三项仅应用过时有值
```

### 11.7 `POST /api/settings/docker-mirror/apply`（admin）
应用优选镜像源：备份 daemon.json（滚动 5 代）→ 合并写 `registry-mirrors`
（原子 rename）→ `systemctl restart docker` → 读回验证，失败自动回滚；
daemon.json 损坏时拒绝写入。
```json
请求：{"mirror": "aliyun", "custom_url": ""}      // mirror=预设名或 "custom"
响应：{"ok": true, "mirrors": ["…"], "backup_file": "…"}   // 或 {"ok": false, "error": "…"}
```

---

## 12. 备份与缓存清理

| 端点 | 权限 | 说明 |
|---|---|---|
| `GET /api/backups` | admin | 备份列表（config.json 全量快照 + 数据目录归档） |
| `POST /api/backups` | admin | 立即创建一份备份（`backup_auto` 开启时另有周期备份） |
| `DELETE /api/backups/{name}` | admin | 删除指定备份 |
| `GET /api/backups/{name}/download` | admin | 下载备份文件（二进制） |
| `POST /api/backups/{name}/restore` | admin | 从备份恢复（先自动兜底再覆盖，失败保留现场） |
| `POST /api/backups/clean` | admin | 清理应用缓存（图标/README 等）中过期文件 |
| `GET /api/logs` | admin | 在线查看应用日志（moo.log 末尾 N 行，`?lines=` 默认 200、上限 2000；含文件大小与轮转归档清单） |

`POST /api/backups/clean` 请求体：
```json
{"days": 7}      // 清理 >7 天缓存（days 缺省用 cache_clean_days 配置）
{"force": true}  // 强制清空全部缓存文件（仍只动 cache 目录，绝不碰已下载 FPK/已装应用）
```
响应：`{"removed": 3, "bytes_freed": 6291456, "remaining": 154}`。

备份目录语义：`backup_dir` 空 = 本机数据目录 `backups/`；可为 /volN 下任意目录（手机/电脑可同步）。

---

## 13. FPK 下载缓存

| 端点 | 权限 | 说明 |
|---|---|---|
| `GET /api/fpk-downloads` | admin | 已下载 FPK 列表 `[FpkDownloadFile]` |
| `DELETE /api/fpk-downloads/{name}` | admin | 删除缓存包 |
| `POST /api/fpk-downloads/{name}/install` | admin + SSE | 用缓存包安装/升级（跳过下载阶段） |
| `GET /api/fpk-downloads/{name}/wizard` | admin | 探测缓存包安装向导定义（直读 FPK 内 wizard/install，无需 staging） |

---

## 14. 后台任务

| 端点 | 权限 | 说明 |
|---|---|---|
| `GET /api/tasks` | 公开 | 任务列表 `[BackgroundTask]`（下载任务带 id，可区分新旧终态） |
| `DELETE /api/tasks/{id}` | admin | 清除终态任务记录 |

---

## 15. 加速源（镜像）

GitHub 加速组（源同步/图标/readme/自更新下载的前缀镜像）与 Docker 镜像组（系统级拉取参考）独立配置。

| 端点 | 权限 | 说明 |
|---|---|---|
| `GET /api/mirrors/health` | 公开 | GitHub 组健康快照（§3.4） |
| `GET /api/mirrors/docker/health` | 公开 | Docker 组健康快照（含本地 KSpeeder 缓存） |
| `POST /api/mirrors/check` | admin | 立即手动测速（两组各自全量候选） |

语义：
- `mirror=auto`：按测速优选，切换触发 `mirror_switched` 通知（30 分钟冷却防抖）。
- 测速**直连**各镜像（不经科学加速代理）——测的是镜像本身质量。
- 自动测速间隔由 `gh_probe_*` / `dk_probe_*` 齿轮配置（0h0m = 默认 5 分钟）。
- 全部候选失败 → `mirrors_all_failed`（Docker 组对偶 `docker_mirrors_all_failed`），恢复 → `mirrors_recovered`。

---

## 16. 官方应用中心连接（OAuth）

> 0.6.255 起**面板账号已从设置中彻底移除**，官方应用中心改为纯 OAuth 免登录连接。
> 原面板账号连通性测试端点已下线。官方源授权入口见
> `POST /api/official/authorize-headless`（请求体临时携带面板账号，不落盘）
> 与前端「应用源 → 飞牛应用中心 → 🔑」对话框（fnOS 1.2.0800+ 走 iframe 内嵌授权页）。

---

## 17. 通知系统

形式对齐 fn-knock 事件中心：**渠道**（外部推送通道）+ **规则**（逐事件开关）+ **记录**（流水）。

> 本节全部端点 **requireAdmin**，唯 `GET /api/notify-view/{id}` 例外（卡片跳转目标，公开 + 一次性令牌）。

### 17.1 渠道 CRUD

| 端点 | 说明 |
|---|---|
| `GET /api/notify-channels` | 渠道列表（敏感参数脱敏回显） |
| `POST /api/notify-channels` | 新建渠道 |
| `PUT /api/notify-channels/{id}` | 更新渠道 |
| `DELETE /api/notify-channels/{id}` | 删除渠道 |
| `POST /api/notify-channels/{id}/test` | 对已存渠道发测试消息 |
| `POST /api/notify-channels/test` | 测试**未保存草稿**（弹窗内即时验证） |

渠道结构（`type` ∈ `wecom | dingtalk | feishu | serverchan | pushplus | bark | webhook`）：
```json
{
  "id": "ch-xxxx", "type": "wecom", "name": "企微群", "enabled": true,
  "params": {"webhook_url": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=<KEY>"},
  "timeout": 10,
  "format": "card"
}
```
`params` schema 随 type 变化（webhook_url / sendkey / token / devicekey…）；`format` ∈ `""/markdown`（默认）、`markdown_v2`（表格/富文本）、`card`（卡片，需 `view_base` 详情页地址，未配置自动降级）。

### 17.2 设置与规则

`GET /api/notify-settings`（admin）：
```json
{
  "enabled": true,
  "events": {"install_success": true, "welcome": true, "…": true},
  "catalog": [{"key": "install_success", "label": "安装成功", "desc": "…", "group": "应用生命周期", "default": true}],
  "mem_alert_mb": 256, "cpu_alert_pct": 80, "view_base": ""
}
```
`PUT /api/notify-settings`：局部提交（`enabled`、`events` 部分键、阈值、`view_base`）。
规则语义：**事件 key 缺失 = 目录缺省；仅显式 false 为关**。关闭的事件既不推送也不入记录。

事件目录（30 类，5 组）：

| 组 | 事件 key |
|---|---|
| 应用生命周期 | `install_success` `install_error` `update_success` `update_error` `uninstall_success` `uninstall_error` `download_done` `download_error` `backup_done` `backup_error` |
| 应用更新 | `updates_available` `favorite_update` `auto_update_round` `favorite_source_apps` `favorite_source_report` |
| Moo 自身 | `welcome` `store_update_available` `store_update_success` `store_update_error` `resource_mem_alert` `resource_cpu_alert` `health_error` `health_recovered` `disk_space_alert` |
| 源与网络 | `source_sync_failed` `source_recovered` `source_sync_summary` `mirror_switched` `mirrors_recovered` `mirrors_all_failed` `docker_mirrors_all_failed` `docker_mirrors_recovered` |
| 数据面 | `catalog_drop` |

### 17.3 记录与触发

| 端点 | 说明 |
|---|---|
| `GET /api/notify-log` | 通知流水（含各渠道发送结果；上限 `NotifyLogCap` 条） |
| `POST /api/notify-log` | 手动写入一条记录：`{"event":"…","msg":"…","ok":true}` |
| `POST /api/notify-log/clear` | 清空记录 |
| `POST /api/notify/fire` | 手动触发任意事件（规则行门铃调试）：`{"event":"install_success"}` |
| `POST /api/notify/welcome` | 重发欢迎语（默认测试渠道） |
| `GET /api/notify-view/{id}?t=<token>` | 通知详情页（卡片跳转目标；HTML，一次性令牌） |

---

## 18. Moo 自更新

### 18.1 `GET /api/store-update`（公开）
```json
{"current_version": "0.6.206", "has_update": false, "last_check": "…", "error": ""}
{"current_version": "0.6.206", "has_update": true, "available_version": "0.6.207", "…": "…"}
```
探测源：GitHub Releases（走加速源候选顺序：优选镜像 → 自定义 → 直连；直连候选经科学加速代理）。**非 amd64 架构不提供自更新**（官方仅发 x86 FPK）。私有仓库探测恒 404 → 仓库转 public 后红点才生效。

### 18.2 `POST /api/store-update`（admin + SSE）
就地自更新管线（daemon 升级）：
```
data: {"step":"downloading","message":"下载 v0.6.207 安装包…","progress":0}
data: {"step":"verifying","message":"校验安装包…"}
data: {"step":"installing","message":"提交飞牛应用中心就地升级…"}
data: {"step":"done","progress":100,"message":"…"}      // 或 {"step":"error","error":"…"}
```
错误分支：已有任务进行中 / 探测失败 / 已是最新 / release 缺 FPK 资产 / 下载失败 / sha256 校验失败 / 提交升级失败。成功后推 `store_update_success` 通知。

---

## 19. 错误处理

统一错误体（非 2xx）：
```json
{"error": "人类可读原因（中文）"}
```

| 状态码 | 语义 |
|---|---|
| 400 | 请求体/参数非法（指针语义冲突、越界、URL 不合法、地址校验失败） |
| 403 | 非可信通道或非管理员访问写/敏感端点 |
| 404 | 端点/资源不存在（源 id、备份名、下载文件名、notify-view 令牌无效） |
| 409 | 资源冲突（同名源、重复任务等） |
| 501 | 端点未实现（如 `diagnostic`） |
| 502 | 上游失败（面板 RPC、代理抓取失败） |
| 500 | 内部错误 |

约定：**整单拒绝**——一个字段非法则整个请求 400，不落任何部分状态。

---

## 20. 安全模型

| 层 | 机制 |
|---|---|
| 网络面 | 服务监听面最小化：写操作全部 requireAdmin（可信通道 + 管理员）；面板地址强制回环 |
| 凭据 | 面板密码仅存本机 `config.json`（文件权限 700），API 只回 `panel_has_password` 布尔；通知渠道参数脱敏回显 |
| SSRF 防护 | 源数据驱动的 URL（icon_url/readme_url/preview_urls）抓取前经公共地址校验：拒绝内网/回环/元数据地址 |
| 供应链 | FPK 安装前 sha256 校验；自更新校验 release 资产哈希 |
| 科学加速 | 代理地址只存本机；仅 GitHub 域名改道，镜像/内网流量不经过代理 |
| 日志 | 非 GET 请求打审计日志（方法/路径/remote）；不记录凭据 |
| 备份 | 快照含全量 config（含密码字段）——备份文件权限与目录须受控 |

---

## 21. 构建与部署全流程

### 21.1 仓库结构

```
moo/
├── cmd/server/          # Go 主程序（HTTP 服务 + 后台协程）
│   └── main.go          # 入口：config.Load → netx.SetProxy → source.Manager → api.Server
├── internal/
│   ├── api/             # HTTP 路由与处理器（85 端点）+ SSE + 缓存
│   ├── config/          # config.json 结构/加载/保存
│   ├── source/          # 源同步引擎（fndepot 索引 / conversun 目录 / 官方列表）
│   ├── netx/            # 科学加速出网代理（0.6.206）
│   ├── netguard/        # SSRF 公共地址校验
│   ├── notify/          # 通知渠道 fanout（7 类渠道）
│   ├── operation/       # 长操作状态机（current/history 原子切换）
│   ├── pipeline/        # 安装/更新/卸载管线（下载→校验→appcenter）
│   └── platform/        # fnOS appcenter daemon RPC（unix socket）
├── frontend/            # React + Vite（构建产物 → web/）
├── fnos/                # FPK 描述：manifest + lifecycle（安装/卸载/启停脚本）
├── web/                 # 前端构建产物（随 FPK 分发）
└── docs/API.md          # 本文档
```

### 21.2 构建管线（x86 FPK）

```bash
# 1. 前端（Node 22 + npm；产物输出到 ../web/dist → 拷贝为 web/）
cd frontend && npm run build        # tsc -b && vite build

# 2. 后端（Go 1.25+；版本注入 main.Version）
export PATH=<go>/bin:$PATH
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.Version=0.6.206" \
    -o build/moo-server-x86 ./cmd/server/

# 3. 暂存目录（manifest + 二进制 + web）
STAGING=build/tmp/fnos-x86
rm -rf "$STAGING"; cp -a fnos "$STAGING"
cp build/moo-server-x86 "$STAGING/app/moo-server"
cp -r web "$STAGING/app/web"

# 4. 打包（fnpack）
./build/fnpack build --directory "$STAGING"
mv moo.fpk moo_0.6.206_x86.fpk
sha256sum moo_0.6.206_x86.fpk     # 发布时记录到 Release 尾行
```

### 21.3 安装与运行时目录

| 路径 | 内容 | 升级行为 |
|---|---|---|
| `/vol1/@appcenter/moo/` | 二进制 `moo-server` + `web/` 前端 | FPK 升级覆盖（@appcenter 随包更新） |
| `/vol1/@appdata/moo/` | `config.json`（权限 700）、`cache/`（catalog 快照/图标/readme 缓存）、`backups/`、`moo.log` | **升级保留**（@appdata 数据卷） |

环境变量：
- `MOO_WEB_PORT`：覆盖默认监听端口（默认 38101）。
- `TRIM_PKGVAR`：数据目录根（fnOS 应用框架注入 @appdata 绝对路径）。

生命周期（`fnos/` lifecycle 脚本）：安装 = 释放包 + 起服务；升级 = 停 → 换二进制/web → 起（config.json 不动）；卸载 = 停服务（数据卷默认保留，供重装恢复）。

### 21.4 热替换（开发调试）

```bash
appcenter-cli stop moo
cp <staging>/moo-server /vol1/@appcenter/moo/moo-server
rm -rf /vol1/@appcenter/moo/web && cp -r web /vol1/@appcenter/moo/web
appcenter-cli start moo
curl -s http://127.0.0.1:38101/api/version    # 验证版本
```

### 21.5 发布流程

1. 版本号（`fnos/manifest` + README 徽章）bump。
2. 构建 FPK + sha256 记录。
3. 源码同步到发布仓库（git push main）。
4. GitHub Release：`v0.6.206` + FPK 资产 + 更新日志（一行亮点 + 一句说明 + FPK sha 尾行模板）。
5. 仓库须为 **public**，自更新红点探测才生效（私有仓库探测恒 404）。

### 21.6 后台协程（服务内常驻）

| 协程 | 职责 | 周期 |
|---|---|---|
| 源自动同步 | 全量刷新已启用源（并发 8） | `check_interval_hours`（默认 24h） |
| 源自动监测 | 空源停用/沉底 | 随每轮同步 |
| GitHub 加速组测速 | 优选镜像切换 | `gh_probe_*`（默认 5m） |
| Docker 加速组测速 | 优选 + KSpeeder 参考 | `dk_probe_*`（默认 5m） |
| 自更新探测 | GitHub Releases 探测 | 周期性 + 手动「立即检查」 |
| 自动更新应用 | `auto_update` 开启时对「有更新」应用走标准管线（排除 Moo 自身防自更新环） | 随源刷新轮次 |
| 自动备份 | config 快照写入备份目录 | `backup_interval_days`（默认 7d） |
| 缓存自动清理 | 删除超龄缓存文件 | `cache_clean_every_days` |
| 健康自检 | daemon RPC 可达性 + 5 分钟告警/恢复 | 5m |
| 资源告警 | 内存 >256MB / CPU >80% 持续 5 分钟告警 | 30m 冷却 |
| 磁盘告警 | 数据目录使用率 >90% | 30m 冷却 |

---

## 22. 官方应用中心（`/api/official/*`）

飞牛官方应用中心（面板 `/ogh/ac/h` 代理）的 OAuth 连接与商店浏览。Token 由 `internal/official`
管理（PKCE 授权码 + 自动刷新 + 落盘持久化），商店列表 **10 分钟缓存**（拉取失败回退旧缓存）。

| 端点 | 权限 | 说明 |
|---|---|---|
| `GET /api/official/status` | 公开 | 连接状态（是否已授权、token 过期时间等）。0.6.254 起含 `ui_supported`/`ui_known`：本机面板前端是否支持 /signin PKCE 授权页（仅 fnOS 1.2.0800+ 前端支持，旧版只渲染普通登录页）；`ui_known=false` = 检测中 |
| `GET /api/official/authorize` | admin | 开始授权：返回 `{url}`，前端打开该 URL 完成登录后带 `code` 回调。0.6.253 起支持 `?base=http(s)://<主机:端口>` 指定用户浏览器可达的面板地址（仅 http/https、无 URL 凭据；缺省用本机回环） |
| `POST /api/official/callback` | admin | 完成授权：body `{code}`，成功 `{ok:true}` |
| `POST /api/official/cancel` | admin | 取消本次授权（丢弃待交换的 PKCE 对），成功 `{ok:true}` |
| `POST /api/official/authorize-headless` | admin | 0.6.254 无头授权（无浏览器，旧版 fnOS 前端无授权页时的替代路径）：复用面板账号 WS 登录 → `GET /oauthapi/authorize` 取一次性 code → 换 token。成功 `{ok:true}`；面板未配置 400；登录失败/限流 502（附原因）；响应无 code 502（附原始响应片段） |
| `POST /api/official/logout` | admin | 断开授权：清 token，成功 `{ok:true}` |
| `GET /api/official/apps` | admin | 商店全量列表 `{total, list}`（10 分钟缓存） |
| `GET /api/official/search?keyword=…` | admin | 关键词搜索 `{total, list}`；`keyword` 为空返回 **400** |
| `GET /api/official/apps/{name}` | admin | 单个应用详情（面板 StoreDetail 透传） |

约定：

- 上游不可达/未授权统一 **502**（`writeErr`），`status` 除外（它只回报本地状态）。
- 列表缓存命中时不打上游；`POST /api/official/callback`/`logout` 会改变授权态，之后首次 `apps` 重新拉取。
- **0.6.253 通道接线**：OAuth 会话有效时，官方目录（`Panel.Apps`）、详情回填、安装前 sourceID 查询（`panelApp`）优先走本 OAuth 通道（免面板登录、不受面板限流影响）；会话缺失或通道故障时自动回退面板 WS 通道（含 0.6.252 失败退避）。接线实现见 `internal/api/official_oauth_wiring.go`。
- **0.6.254 版本门控**：/signin 的 PKCE 授权页仅 fnOS 1.2.0800+ 面板前端有（懒加载 oauth chunk；旧版前端零 OAuth 逻辑，浏览器打开授权链接只见普通登录页）。启动时探测本机面板前端（`internal/official/support_probe.go`，进程内缓存），前端按 `ui_supported` 分流：支持 → iframe 授权页流程；不支持 → 无头授权（`authorize-headless`，面板账号一次性登录取码，授权后目录仍免面板登录）。
- 该域原为**主线（moo-w）独有**；2026-10-01 两条线合并后（面板线为基线 + 摘入官方模块），
  发布线同样具备这 7 个端点。
