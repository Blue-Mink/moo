# 2026-10-01 两线合并记录（main 线 × 面板线）

## 背景

同一项目此前分裂成两条开发线，功能互有缺失（46 个文件内容不同）：

| | 主线（`moo-w`） | 面板线（`moo-panel-207`） |
|---|---|---|
| 独有 | 官方应用中心 OAuth 模块（`internal/official/`、`/api/official/*`）、`docs/API.md` | 207–217 全部安全加固与体验迭代（残留自清理、日志轮转、`X-Moo-Admin`、body 上限、键盘 dock、通知记录等） |
| 体量 | 332 文件 / 21,002 行 Go / 85 路由 | 309 文件 / 20,746 行 Go / 78 路由 |

## 合并方向

**以面板线为基线**（它是实际发布线、独有能力 10 : 3、版本号更高），摘入主线的官方模块与文档。

## 本次摘入的内容

1. `internal/official/`（`oauth.go` 426 行 + `client.go` 153 行）——PKCE 授权码 + 自动刷新 + 会话落盘。
2. `internal/api/official.go` —— 7 个 `/api/official/*` 端点（`status` 公开，其余 admin）。
3. `internal/api/server.go` —— `Official` 字段 + 路由注册块。
4. `cmd/server/main.go` —— `official.NewManager(dataDir)` + `LoadSession()` 接线；
   同时带上**服务端超时修复**：网关 unix socket 入口由零超时的 `http.Serve` 改为专用
   `http.Server`（`ReadHeaderTimeout 10s` + `IdleTimeout 120s`），TCP 口补 `IdleTimeout`，
   `WriteTimeout` 保持不设（SSE 安装流最长 12 分钟）。
5. `docs/API.md` —— 全量 API 文档（85 端点，含 §22 官方应用中心、§1.4 上限与超时表）。
6. `internal/api/api_docs_test.go` —— 文档覆盖**双向守护**测试（代码路由 ⇄ 文档端点）。

## 同步修正

- `internal/notify/send_formats_test.go`：断言不再要求 full 模式带详情链接（对齐 0.6.213「通知去链接」）。
- `internal/notify/send_verbosity_test.go`：截断尾措辞断言改为「其余条目」（对齐 0.6.214 定稿）。
- 这两个用例在合并前就是红的——行为按用户要求改了，测试没跟。

## 脱敏

- 19 个历史文件取主线**已脱敏版**（设备地址 → `MOO_BASE` / `NAS_HOST` 环境变量注入、`<NAS_IP>` 占位、测试 fixture 改 RFC1918 `172.16.0.x`）。
- `internal/official/oauth.go` 注释里的测试机地址已去除。
- 工作区运维脚本（`sync_gitea_releases*.py` 等，含凭据）加入 `.gitignore`，不入库。

## 另一并摘入：Agent-Moo skill（原只存在于 GitHub）

`skills/`（`skills/README.md` + `skills/moo/`：SKILL.md、references×6、scripts×2）此前**只在 GitHub**，
本地三棵树都没有。本次一并从 GitHub 快照取回并入，两个仓库（GitHub/Gitea）从此内容对齐。

同步修正：

- `skills/moo/references/api-full.md` ← `docs/API.md`（镜像关系，逐字节同步）。
- skill 里写死的端点数 **78 → 85**（`SKILL.md` 描述与加载指引、`install-deploy.md` 目录树注释）。
- `references/feature-map.md` 新增 **Official App Center (OAuth)** 一节（7 个端点 + 502 语义）。
- `internal/api/api_docs_test.go` 新增 `TestSkillAPIMirrorMatchesRootDoc`：两份文档不一致即测试失败
  （负向验证过：故意加一行漂移 → 立刻报错并指出同步要求）。

## Agent skill 体检与完善（同日）

对 `skills/moo/` 做了一次「对照合并后代码」的实测体检，修掉 3 个真问题：

1. **`X-Moo-Admin` 契约缺失（最要紧）**：合并后的代码里，`requireAdmin` 覆盖的端点凡非
   `GET/HEAD/OPTIONS` 都必须带 `X-Moo-Admin`（缺 → **400**）。skill 完全没写这条，自带的
   `scripts/moo-api.mjs` 也不发送该头 → 照 skill 执行写操作必然失败。
   已修：`docs/API.md` §2 补契约（含 400 报文与 `curl` 示例）、`SKILL.md` 硬规则、`references/safety.md`
   新增「Request header contract」、`moo-api.mjs` 对非 GET 自动附加（GET/HEAD 豁免）。
2. **feature-map 索引不全**：85 条路由里索引只覆盖 78。补齐 `GET /api/apps/{key}/wizard`、
   `GET /api/apps/{key}/panel-detail`、`GET /api/apps/{key}/diagnostic`、`DELETE /api/tasks/{id}`，
   并把 `…/rename`、`…/toggle`、`DELETE …/{id}` 之类省略写法展开为完整路径 → 现覆盖 **85/85**。
3. **0.6.207/216 运维特性未记录**：`references/install-deploy.md` 新增「Runtime Self-Maintenance」，
   说明**启动残留自清理**（`residual_cleanup.go`，会删自己 `@appdata` 里的旧凭据/备份残留——文件重启后
   消失即此因）与**日志轮转**（`logrotate.go`，满盘会阻塞 FPK 暂存），两者均无关闭开关。

新增两条守望测试（共 4 条文档/skill 守卫，全绿）：

- `TestSkillAPIMirrorMatchesRootDoc` —— `docs/API.md` 与 skill 镜像 `api-full.md` 必须逐字节一致。
- `TestFeatureMapEndpointsExistInCode` —— feature-map 里提到的端点必须都在代码中存在。

## 验证

- `go build ./...` ✓ · `go vet` ✓ · `go test ./...` **0 FAIL** ✓
- 文档覆盖双向 + 镜像一致 + feature-map 无过期端点：四条测试正反均验证 ✓
- 敏感串复扫：非 `node_modules/build` 路径下无内网地址/凭据残留 ✓

