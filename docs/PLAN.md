# Moo MVP 模块拆分与排期

## 1. 定位与约束

- 飞牛 fnOS 第三方应用中心，**clean-room 重写**，UI 交互参照 New Store
- 后端 **Go 1.25**（单二进制 + web embed），前端 **React + TypeScript**
- **仅 x86_64** 单架构 FPK（不出 arm）
- 同步三大平台：**FnDepot**（fnpack.json 扁平/V1/V2）/ **fnos-apps**（conversun）/ **飞牛官方应用中心**（面板 app-center API）
- 默认端口 **38100**（避开 New Store 38011）
- 仓库名/appname：`moo`（GitHub 已验证可用）

## 2. 架构

```
moo/
├── cmd/server/            # 入口：daemon + HTTP + 静态 UI 服务
├── internal/
│   ├── appmodel/          # 统一应用模型 + 版本比较 + 同名去重
│   ├── source/            # ★ 三平台适配层（核心新设计）
│   │   ├── fndepot/       #   FnDepot 源（fnpack.json 解析，多版本兼容）
│   │   ├── fnosapps/      #   conversun/fnos-apps 源
│   │   └── official/      #   飞牛官方（面板登录 + app-center API，355 应用）
│   ├── downloader/        # 下载任务（后台化 / 暂停 / .part 断点续传）
│   ├── mirror/            # GitHub/Docker 镜像健康监测（可后置简化）
│   ├── installer/         # FPK 安装 pipeline（appcenter RPC + 向导参数）
│   ├── panel/             # 飞牛面板登录 / 官方通道客户端
│   ├── task/              # 任务系统（教训：非重入锁自死锁 → 全部带回归测试）
│   └── store/             # 元数据存储（SQLite，appcenter 库同步端口）
├── frontend/              # React+TS（从 New Store MIT 代码移植）
├── web/                   # 构建产物（embed）
└── fnos/                  # FPK 模板（x86 only）
```

**Source Adapter 接口**（三平台统一）：

```go
type Source interface {
    Name() string
    List(ctx context.Context) ([]appmodel.App, error)   // 拉取目录
    Detail(ctx context.Context, name string) (*appmodel.App, error)
    DownloadURL(app *appmodel.App) string
    Refresh(ctx context.Context) error                   // 手动刷新
}
```

## 3. 里程碑

| 里程碑 | 内容 | 验收标准 |
|---|---|---|
| **M0 骨架** ✅ | 本仓库：Go module + daemon 桩 + x86 构建链 + FPK 模板 + 本文档 | `go build` 通过；build.sh 出 x86 FPK |
| **M1 商店骨架** ✅ | 前端 UI（MVP：发现/下载/源 三视图 + 详情弹窗；完整 5 页移植并入 M4 打磨）；FnDepot 源 adapter（V1/V2 + 仓库 URL 自动解析）；下载任务系统（后台/暂停/续传） | 加源 → 浏览列表 → 详情页 → 下载 FPK 落盘（含暂停/继续）——2026-09-21 测试机全过（11 应用目录、kspeeder/dockflare 下载、paused→resume→done） |
| **M2 安装管道** ✅ | FPK 安装 pipeline（appcenter daemon RPC：stage/install/upgrade/uninstall/start/stop + 向导 initValue 自动填充 + 任务三态语义）；单操作队列互斥；X-Trim-Isadmin 鉴权（TCP 直连恒 403） | kspeeder 测试机全过（2026-09-21）：全新安装 3.7min（向导自动填端口 5003✓）/升级 25s/卸载保留 @appdata/停用→stopped/启动→running；端口闭环=网关入口无端口段，.sc 已关 LAN 转发（port_forward=no） |
| **M3 官方通道** | 面板账密登录（ad-hoc，只填一次）+ 官方 355 应用目录 + cloud 安装 + 依赖选择弹窗 | 官方数独到 355；数独 e2e 安装 |
| **M4 打磨发版** | 镜像监测（简化：GitHub 源自动探测）；移动端/主题收尾；首版 FPK | 主 NAS 真机全流程；发版（GitHub Release + 源条目） |

排期参考（按 New Store 实际开发节奏折算）：M1 ≈ 1-2 周，M2 ≈ 1 周，M3 ≈ 1 周，M4 ≈ 3-5 天。**首版 FPK 预计 4-6 周**。

> 2026-09-21 浏览器实测修复记录（0.3.1→0.3.5，测试机面板实机验证）：
> 1. **空白页根因**：入口 URL `/app/moo` 无尾斜杠 → 相对资源 `./assets/…` 解析到 `/app/assets/…`（404）。修复 = 网关通道根路径 301 → `/app/moo/` + ui/config 入口加尾斜杠（v0.3.1）。
> 2. **源抓取串行试错 60s+**（GitHub 挂起时每候选各等 30s 超时，网关层响应丢失表现为页面卡「加载中」）。修复 = 候选 URL 并发竞速 + 12s 总上限（v0.3.3）。
> 3. **首屏阻塞**：缓存空时同步刷源改后台刷新 + 完成后自动重载（v0.3.2）。
> 4. **相对资源直链**：icon/readme 的 `./xxx` 对 GitHub 源补全为 raw.githubusercontent（网页路径返回 HTML 非图片）（v0.3.4/0.3.5）。

> 2026-09-21 **v0.4.0 UI 全量对齐 New Store**（用户要求「UI 交互页面和现在的 New Store 页面一样」）：
> - 前端整套移植（frontend/ = New Store 前端，品牌改 Moo）：发现（推荐 hero×3 + 热门横滑）/全部（分类药丸+排序）/已安装/有更新 四 tab、搜索、主题切换、详情弹窗（运行中/打开/停用/卸载/下载fpk 四态按钮）、设置弹窗（系统设置+应用源设置：源列表/开关/自动监测/批量添加）。
> - 后端 API 全量按 New Store 前端契约重写（internal/api/store_*.go）：/api/apps（合并目录+daemon 安装状态+has_update 版本比较）、/api/apps/{key}/*（SSE 安装/更新/卸载/下载任务+暂停继续/wizard/asset/启停）、/api/sources（增删/开关/排序/sync）、/api/tasks、/api/fpk-downloads、/api/settings、/api/check、/api/recommended、/api/store-update；M3/M4 未接入端点（panel-detail/diagnostic/mirrors）返回明确降级。
> - 图标统一走后端代理 `asset?type=icon`：应用声明 icon_url → FnDepot 仓库布局候选（<app>/ICON.PNG 等）→ 本机 /vol1/@appcenter/<app>/ui/images/ 回退；raw.githubusercontent 附 jsDelivr 镜像并发竞速（0.1s 命中）。已装系统应用直接用 daemon 的 /app-center-static/icon/<app>/icon.png 面板直链。
> - 分类映射 mapCategory（与 New Store mapFndepotCategory 同表）：FnDepot labels「工具，网络」→ system、「娱乐」→ media 等，前端分类药丸计数生效。
> - 部署坑：飞牛卸载重装会**丢失应用配置目录**（源缓存清空，需触发一次「立即检查」）；二进制在 /vol1/@appcenter/moo/moo-server（**平铺**，非 app/ 子目录）；热替换用 `appcenter-cli stop → 替换 → start`（pkill 会让 daemon 状态脏住，start 报 already started 但不拉起）。
> - 测试机浏览器实测：四 tab/搜索过滤/详情弹窗/设置页/暗色模式/真实图标全过。

## 4. 从 New Store 移植清单

| 模块 | New Store 位置 | 处理 |
|---|---|---|
| 前端 5 页 + 组件 | `frontend/src/`（App.tsx 等） | MIT 部分直接移植；无 LICENSE 部分 clean-room 重实现 |
| 下载任务系统 | `internal/api/task.go`、`download_task.go`、`internal/core/downloader.go` | 逻辑参照重实现（1.21.1 设计：后台化/暂停/.part 续传） |
| FPK 安装 pipeline | `internal/api/install.go`、`pipeline.go` | 参照重实现 |
| 官方通道 | `internal/api/`（app-center 相关）+ `internal/panel/`（如有） | 参照重实现（协议是逆向的，必须独立验证） |
| 端口闭环 | `fnos/cmd/common`（native-web-port-flow） | MIT 增量部分直接移植 |
| 镜像监测 | `internal/mirror/monitor.go` | M4 简化版移植 |

## 5. 明确不做（本期）

- ❌ arm 架构
- ❌ 与 conversun 代码同步/merge（独立项目，无 fork 关系）
- ❌ 飞牛 App dock / microApp 桥嵌入（后置，等 micro_app 桥稳定后再评估）
- ❌ 用户体系 / 多账号（**改：多用户靠统一网关身份 Header，见 §8**）

## 8. 飞牛统一网关 + 开放 API 集成（2026-09-21 调研结论）

### 8.1 统一网关（官方文档 developer.fnnas.com/docs/core-concepts/gateway-registration）

- 应用注册 `gatewayPrefix`（`/app/{appname}`）+ `gatewaySocket`（target/ 下的 unix socket 文件名）
- 面板 nginx `/app/` → `trim_http_cgi` → 校验会话 → 转发到 `/var/apps/moo/target/app.sock`
- 转发请求带可信身份 Header：`X-Trim-Userid` / `X-Trim-Isadmin` / `X-Trim-Username`（不要信任客户端传的用户 ID）
- 支持 WebSocket（同前缀同 socket，如 `/app/moo/ws`）
- 网关入口下 `protocol`/`port` 字段被忽略——不再需要独立端口暴露
- 对比旧模型 `index.cgi`（`/cgi/ThirdParty/{appname}/`，每请求起进程、无 WS、CGI 前校验登录）：常驻服务必须走统一网关

### 8.2 开放 API（system ≥1.2.0401 / App ≥1.34.0）

- 后端：`POST /api/v1/trimapp` via unix socket `/var/run/trim_open_gateway_apiscope.socket`
- 认证：`Authorization: Bearer ${TRIM_API_TOKEN}`（系统启动 cmd/main 时注入环境变量，**每次现读，不持久化**）
- `config/resource` 声明 api-scope（Moo 需要：`trim.system.getPlatformConfig` 读语言/版本；文件类 scope 按需）
- JS SDK：`@trimjs/web-app`（npm），需 manifest `micro_app=true`（已声明）
- 能力：文件授权（共享/个人）、ACL 检查、路径转换、页面路由、平台配置

### 8.3 对 Moo 的落地改动

| 里程碑 | 改动 |
|---|---|
| M1 ✅ | `cmd/server` 增加 unix socket 监听（`app.sock`，**主入口**）；TCP 38100 **绑 127.0.0.1**（仅本机调试/健康检查，不出 LAN）；前端 vite base + router basename = `/app/moo`（SPA 网关前缀坑，E2）——2026-09-21 完成，e2e 经面板网关 `trusted:true` 验证 |
| M2 ✅(提前) | 入口 `ui/config` 增加 `gatewayPrefix:"/app/moo"` + `gatewaySocket:"app.sock"` + `type:"iframe"`（参照 fndepot 真实模板；系统自动导入 app_service 表已验证）；**X-Trim-\* 只认 socket 通道请求，直连端口伪造的一律不信**（已实现：TCP 通道 trusted:false）；安装/卸载/源管理按 `X-Trim-Isadmin` 鉴权 → 已落地（requireAdmin 中间件，TCP 直连 403 实测） |
| M4 | 开放 API 接入：读平台语言/版本（主题跟随面板）；`config/resource` 加 api-scope |

**访问模型决策（2026-09-21 定案）**：统一网关为主入口（常驻 daemon + 登录态 + 用户身份 + WS + 零 LAN 端口暴露）；CGI 一票否决（每请求起进程，与有状态 daemon 架构不兼容）；独立端口降级为本机调试通道。

### 8.4 关于 32600 端口（调研结论）

- 测试机 + 主 NAS 实测：无 32600 监听、二进制/配置/面板前端均无该字符串、官方文档无记载
- 当前统一网关走 **unix socket 而非 TCP 端口**，不存在"网关 TCP 端口"
- 待办：向用户确认 32600 出处（旧版本/第三方文章/其他产品？）

## 6. 风险与对策

| 风险 | 对策 |
|---|---|
| 官方 app-center 是逆向闭源协议，面板升级可能破坏接口 | 版本探测 + 优雅降级（官方通道挂了不影响第三方源） |
| conversun 无 LICENSE 代码混入 | 移植时逐文件过授权边界（见 frontend/README.md 表格），拿不准的重写 |
| 面板 API 需要账密自登（跨域 iframe + HttpOnly cookie 三墙已实锤） | 沿用 New Store 方案：后端自登 127.0.0.1:5666，账密只填一次 |
| GitHub 网络抖动（构建/CI） | 重试循环 + 加速源（沿用 17 源监测成果） |

## 7. 发版链路（M4 固化）

```
go build (x86) → build.sh 打 FPK → 主 NAS 真机验证 →
git commit + tag v0.x.y → push → CI Release（x86 二进制）→
手动挂 x86 FPK（uploads.github.com）→ FnDepot/fnos-apps 源条目 → jsDelivr 刷新
```
