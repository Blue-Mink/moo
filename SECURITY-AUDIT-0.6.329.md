# Moo（moo）安全审核报告 — 源码 + 最新安装包

> 审核对象：**Moo（moo）v0.6.329**，来源 `Blue-Mink/moo`（GitHub 开源，自维护，非搬运）
> 审核范围：① GitHub 源码（tag `v0.6.329` 全树 + `main@dedbf765` 增量）② GitHub Release 最新安装包 `moo_0.6.329_x86.fpk` / `moo_0.6.329_arm.fpk`
> 审核时间：2026-10-18
> 审核方法：三层静态分析（元数据 → 源码 → FPK 解包）+ 发布完整性核验（minisign / sha256），全程只读，未安装、未运行、未改动运行实例
> 安装包校验：`moo_0.6.329_x86.fpk` **5,083,007 B**，sha256 `7049ec34c3715a7f59c9c23ad13236fbc0908c9ed4ce74bb1fc50477a8ad1eb6`（与 GitHub Release API digest 一致）；
> `moo_0.6.329_arm.fpk` **4,627,761 B**，sha256 `f51bcd20458c4b0789e59d4a793077e1e9874ba4778f13f48486bd43bcd4b854`（一致）
> 签名：两个架构的 `.minisig` 均用**源码内嵌公钥**验签 **VALID**（key id 5886639878564360297）
>
> 发布说明：本报告为公开文档，文中代码路径均相对仓库根目录；审计所用安装包副本、解包目录与扫描脚本不随仓库分发（复现方法见文末附录）。

---

## ✅ 评分口径（请先读）

本报告所有分数——八维分、综合分——**都是「安全分」**：

- **分数越高 = 越安全，分数越低 = 越危险**
- 100 分 = 该维度无风险 / 防护到位；0 分 = 该维度极高风险
- 等级：🟢 **安全（≥70）** = 可放心安装；🟡 **中（40-70）** = 了解风险后可装；🔴 **危险（<40）** = 不建议安装

一句话：**分高的地方说明做得好，分低的那一维就是你要盯的风险点。**

---

## 综合安全分：68 分 · 🟡 中（短板＝root 运行权限，不是代码缺陷）

| 维度 | 安全分 | 说明 |
|---|---|---|
| 安全性（恶意风险） | 95 | M1–M6 全部未命中；无后门/挖矿/反弹 shell/数据破坏/窃取；唯一命中的 `curl ... \| sh` 文本来自内嵌的**第三方 README 快照**，非执行逻辑 |
| 权限 | 60 | `run-as: root` + `api-scope: trim.app.center`（装卸/启停应用能力）；回调脚本按官方契约以 root 执行。属**设计必需**，但一旦被攻破等同 root |
| 漏洞 | 85 | 无硬编码凭据；密钥 0600 + AES-256-GCM（`enc:v1`）；自更新 minisign + sha256 双 fail-closed；SSRF 防护齐备；仅少量低危项（见「六、发现清单」） |
| 暴露面 | 72 | TCP 仅绑 `127.0.0.1:38100`（不出 LAN）；网关 unix socket `chmod 0700`；pprof 仅回环；admin API 需可信通道 + 管理员 |
| 供应链 | 88 | 单一自维护仓、无 fork 放大；Release gate 含整树脱敏 + vet/test；`release.yml` 的 actions 全部钉 SHA；发布附 sha256 + minisign |
| 未知风险 | 75 | 闭源二进制（stripped）静态可见性有限；依赖 CVE 未深扫；运行时行为未动态观测 |
| 混淆/反分析 | 95 | Go 原生二进制，**无 UPX/无加壳**（熵 6.411，`UPX!` 不存在），源码可复现 |
| 数据隐私/外联 | 80 | 无遥测/统计；外联仅 GitHub API、镜像站与**用户自配**通知渠道；通知内容含应用名与事件明细（用户可配），日志本地留存 |

> 综合安全分 = min(八维) × 0.6 + avg(八维) × 0.4 = 60 × 0.6 + 81.25 × 0.4 = **68.5 → 68**
> 说明：🟡 的唯一来源是「权限」维度（作为应用商店必须 root 调应用中心 RPC）；若只看代码质量与发布安全，各项均在 85–95。

---

## 一、元数据层（FPK manifest / config）

| 项 | 值 | 判定 |
|---|---|---|
| appname / version | `moo` / `0.6.329` | 与 Release 资产名一致 |
| install_type | 系统空间（`micro_app = true`） | ⚠️ 见权限维度 |
| config/privilege run-as | **root** | ⚠️ 设计必需（调 appcenter daemon / docker / 系统路径） |
| api-scope | **`trim.app.center`** | ⚠️ 可装卸/启停应用（飞牛官方 scope） |
| service_port | 38100（实现仅绑 127.0.0.1） | ✅ 不出 LAN |
| source / maintainer | `thirdparty` / `Blue-Mink` | ✅ 自维护、非搬运 |
| 上游仓库 | `github.com/Blue-Mink/moo`（公开） | ✅ 源码可审计 |
| 被搬运源数 | 0（自身即上游） | ✅ 无供应链放大 |

## 二、源码层（GitHub 公开源码）

**审计基线**：tag `v0.6.329`（GitHub 树 485 个文件 + 65 个目录）与 `main@dedbf765`（526 个文件 + 77 个目录）；两者**运行时目录差异仅 1 行**（`cmd/notifyfire/main.go` 的 detailURL 默认端口 38101→38100），其余为 `skills/`、`docs/`、`downloads/` 等非运行内容 —— 源码结论对安装包成立。

| 类别 | 核查结论 | 证据 |
|---|---|---|
| 凭据硬编码 | ✅ 未发现 token / 口令 / 私钥；前端全量扫描 0 命中 | `frontend/src` 全量 |
| 敏感数据存储 | ✅ 口令/渠道密钥 AES-256-GCM（`enc:v1:`）；配置 `0600`；密钥文件 `0600`（存量 0700 自动修正） | `internal/config/config.go:429`、`internal/secret/secret.go:66-85` |
| 执行链 | ✅ Go 侧仅 9 处 `exec.Command`，全部为固定程序 + 固定/受控参数（`appcenter-cli`、`df`、`systemctl`、`service`、`setsid`、`aria2c`）；无 `sh -c` 拼接 | `internal/platform/volume.go:26`、`internal/api/docker_mirror_apply.go:420`、`internal/task/engine.go:200` |
| 命令注入 | ✅ aria2 候选 URL 先过 `netguard.Public`（必须 http/https），`-` 开头无法成为选项 | `internal/task/engine.go:104` |
| SSRF | ✅ 源数据驱动的抓取统一 `netguard`：仅公网、禁环回/私网/链路本地，DNS 失败即拒；管理员显式配置地址进豁免清单 | `internal/netguard/netguard.go` |
| 路径穿越 | ✅ 资产/文件接口 `path.Clean`；日志路径只取配置项（安装期校验无 `..`）；FPK 解包按固定成员名读取，写入固定临时目录 | `internal/api/store_ops.go:1684`、`internal/api/self_update.go:900-985` |
| HTML/XSS | ✅ `desc_html` / README 渲染先经 DOMPurify；`rehype-sanitize` 兜底 | `frontend/src/components/AppDetailDialog.tsx:148,1289,1303` |
| 鉴权模型 | ✅ 双通道：网关 unix socket（可信，读身份头，`0700`）+ TCP `127.0.0.1`（恒不信任，admin API 403）；写操作另需 `X-Moo-Admin` 头（CSRF 纵深） | `internal/api/server.go:404-418`、`cmd/server/main.go:330` |
| 自更新 | ✅ 仅 TLS 直连 GitHub；**必须**有 `.minisig`（内嵌公钥验签，不联网取钥）与 `.sha256`，任一缺失/失败即拒绝（fail-closed） | `internal/api/self_update.go:796-880` |
| 第三方安装 | ✅ 下载后按源声明校验 sha256（无 sha256 时校验精确大小）；版本守卫拒绝降级/同版本 | `internal/pipeline/pipeline.go:96-105`（版本守卫）、`:269-289`（下载核验） |
| 卸载/清理 | ✅ 仅删除 `/vol*/@app{data,conf,meta,home}/moo`；`/etc/hosts` 只删自有标记行；`daemon.json` 只移除自家镜像条目并备份 | `cmd/uninstall_callback` |
| 依赖 | `aead.dev/minisign v0.3.0`、`golang.org/x/net v0.47.0`、`x/crypto v0.44.0`；前端 React 19.2 / DOMPurify 3.4；有 `package-lock.json` | `go.mod` / `frontend/package-lock.json` |
| CI | Release gate：整树脱敏（25 条公开模式 + 扩展 secrets）+ `go vet` + `go test`；三个 action 均**钉到 commit SHA** | `.github/workflows/release.yml:31-43` |

> 说明：`M3` 扫描命中的 `curl -fsSL ... | sudo bash` 文本来自 `internal/reco/snapshots.json` 中**第三方应用 fn-knock 的 README 快照**（用于推荐卡片展示），不是 Moo 的执行逻辑 —— 人工核验为误报。

---

## 三、包层（FPK 解包 + fpk_audit.py）

**解包结构**：`manifest` + `app.tgz`（内含 `moo-server` 12,169,479 B、`ui/`、`config/`）+ `cmd/`（10 个生命周期脚本）+ `wizard/`（install/config/uninstall）+ `config/`（privilege、resource）+ 双尺寸图标。无额外夹带文件（对照 0.6.329 源码树与构建脚本）。

`fpk_audit.py` 关键输出与人工核验：

| 输出 | 人工核验 |
|---|---|
| `[manifest] run-as = root ⚠️` | 真实：见权限维度；回调脚本仅调用 appcenter/docker，未做越权写 |
| `cmd/* 全部 ✅` | 已逐脚本复核：端口校验（1024–65535）、`ports.conf` 单一事实源、psql 参数化拼接常量、无下载执行/无 `eval` |
| `binary 外联 286 …` | **误报为主**：字符串里的局域网地址是界面文案占位符；`pushplus` / `qyapi.weixin.qq.com` 等是用户可配通知渠道；其余为 GitHub / jsDelivr / 镜像站 |

**二进制核验**：

| 项 | 值 | 结论 |
|---|---|---|
| 文件类型 | ELF 64-bit x86-64, **stripped**（Go BuildID 正常） | ✅ 正常发布构建 |
| 熵 | 6.411（满值 8.0） | ✅ 未加密/未压缩壳 |
| `UPX!` 魔数 | 不存在 | ✅ 无加壳 |
| 可疑串 | 无矿池/反弹 shell/`authorized_keys`/`nc -e`；命中的下载执行文本均来自内嵌第三方 README | ✅ |
| 外联域名 | github.com / raw.githubusercontent.com / api.github.com / jsdelivr / 通知渠道（企微、飞书、钉钉、ServerChan）/ docker 镜像站 | ✅ 无未知 C2 |

**发布完整性（重点）**：

| 项 | 结果 |
|---|---|
| x86 sha256 vs Release API digest | ✅ 一致 `7049ec34…` |
| arm sha256 vs Release API digest | ✅ 一致 `f51bcd20…` |
| `.sha256` 侧车内容 | ✅ 与文件哈希逐字一致 |
| `.minisig` 验签（源码内嵌公钥） | ✅ x86 / arm 均 **SIGNATURE_VALID: true** |
| FPK 内 `moo-server` sha256 | `9a057aa6…`（与 Release 附带的 CI 二进制 `0838f94a…` 不同，属预期：FPK 内为 `-s -w` strip 版） |
| 运行组件一致性 | `app.tgz` 内二进制与 FPK 内声明一致；无运行时二次下载可执行文件的逻辑 |

> `manifest.checksum` 为 `app.tgz` 的 **MD5**（实测 `d83e3f9e…`，与包内字段逐字一致，可复现）；平台安装不依赖该值，完整性以 **minisign + sha256 侧车**为准。字段算法未写进仓库文档，见发现清单 F2。

## 四、恶意代码与数据破坏专项（M1–M6）

| 编号 | 类别 | 结论 | 证据 / 说明 |
|---|---|---|---|
| M1 | 病毒 / 蠕虫 | **未命中** | 无自我复制、无遍历 `@appdata/*` 改写；源码/脚本命中项均为技能文档与审计规则文本 |
| M2 | 木马 / 后门 | **未命中** | 无反弹 shell / C2 / 隐藏账号 / `authorized_keys`；`frpc` 仅出现在分类标签表；无未声明的隧道进程 |
| M3 | 恶意脚本 | **未命中** | 生命周期脚本无下载执行、无 `base64 -d \| sh`、无 eval 拼接；命中的 `curl\|sh` 仅为第三方 README 快照文本 |
| M4 | 数据破坏 | **未命中** | 卸载仅删自己四类目录；备份/日志清理限于自有目录；无 `find / -delete`、`dd`、`mkfs`、DB 破坏语句 |
| M5 | 数据窃取 | **未命中** | 不读 `/etc/shadow`、`~/.ssh`、浏览器 Cookie、云凭据；仅读本机应用图标/自身配置；无外传行为 |
| M6 | 反取证 | **未命中** | 无 `history -c`、无删系统日志；日志轮转只处理自己 `moo.log`（可配置路径，安装期校验） |

## 五、未知风险（审计看不到的部分）

| 风险点 | 说明 |
|---|---|
| 闭源二进制 | `moo-server` 已 strip，静态只能看常量与调用；条件触发逻辑不可 100% 排除（但源码公开、可自行构建比对） |
| 依赖 CVE | 未跑 `govulncheck`/`npm audit` 深扫；`x/net v0.47.0`、`x/crypto v0.44.0` 为近期版本，仍建议发布流程加依赖扫描 |
| 运行时行为 | 未在独立实例做网络抓包；外联清单来自静态字符串 + 代码路径 |
| 镜像漂移 | Docker 加速源写的是用户选择的 registry mirror；`/etc/docker/daemon.json` 改动有备份与回滚路径 |
| 供应链投毒窗口 | 结论对 `v0.6.329` 这一版有效；后续版本需重新验签（客户端已强制 minisign fail-closed） |

---

## 六、发现清单

| # | 级别 | 发现 | 影响 | 建议 |
|---|---|---|---|---|
| R1 | 中（结构性） | `run-as: root` + `api-scope: trim.app.center` | Moo 被攻破 ≈ 宿主机 root；可装卸任意应用 | 属应用商店设计必需。保持「仅回环 + 网关鉴权」、自更新强制签名；不要对公网暴露 38100；第三方源脚本风险由本审核技能逐应用把关 |
| F1 | 低 | `.github/workflows/skills-check.yml` 使用浮动 action 标签（`actions/checkout@v4`、`setup-python@v5`），同仓 `release.yml` 已钉 SHA | CI 供应链可被上游 tag 重指影响 | 统一钉到 commit SHA |
| F2 | 信息 | FPK `manifest.checksum` 实为 `app.tgz` 的 **MD5**（实测 `d83e3f9e…` 与包内字段逐字一致，可复现）；仓库文档未标注该字段算法 | 外部复核者若按 CRC32 判读会误以为「不可复现」 | 在构建说明中补一行「`checksum` = MD5(`app.tgz`)」 |
| F3 | 低 | `internal/reco/snapshots.json` 内嵌第三方 README 原文（含 `curl … \| sh` 安装示例） | 会让通用扫描器误报；也把第三方文本固化进二进制 | 保持现状可接受；如需降噪可在快照中裁剪「安装命令」段 |
| F4 | 信息 | Release 附带的 `moo-server-linux-amd64` 为 CI 构建（未 strip），与 FPK 内 strip 二进制哈希不同 | 无法直接二进制对账 | 可在 release 说明标注；或 CI 改用同一 `-s -w` 构建参数便于比对 |
| F5 | 信息 | 未做依赖 CVE 深扫（govulncheck / npm audit） | 未知 | 纳入 CI gate |

---

## 结论

| 问题 | 结论 |
|---|---|
| 有没有恶意代码？ | **没有**（M1–M6 全部未命中；命中项均为文档/README 文本误报） |
| 有没有偷数据？ | **没有**；无遥测，外联仅 GitHub、镜像站与用户自配通知渠道 |
| 有没有默认弱口令？ | **没有**（无内置账号体系，复用飞牛面板身份；密钥运行时随机生成、`0600`） |
| 为什么是这个等级？ | 唯一的短板是「root 运行 + 应用中心 scope」这一结构性权限面，不是代码缺陷 |
| 装还是不装？ | **可继续使用/升级**；只从 `Blue-Mink/moo` 官方 Release 更新（客户端已强制 minisign + sha256 fail-closed） |

**综合判定：🟡 中（68 分）** —— 代码与发布链干净、可审计、可验证；风险集中在「应用商店必须 root」这一固有权限面与依赖/CI 的少量加固项。若你在意最高等级，可关注两点：① 不把 38100 暴露到 LAN/公网；② 只安装经过本审核技能过一遍的第三方应用。

---

## 附录：复现方法

本报告全部结论可用下列步骤独立复现（全程只读，不需要安装或运行 Moo）：

| 步骤 | 操作 |
|---|---|
| 1 | **取源码**：`git clone https://github.com/Blue-Mink/moo`，`git checkout v0.6.329`（485 个文件）；再对 `main` 做一次运行时目录 diff（`internal/`、`cmd/`、`fnos/`、`frontend/src/`）确认增量 |
| 2 | **取安装包**：GitHub Release `v0.6.329` 下载 `moo_0.6.329_x86.fpk`（5,083,007 B）与 `moo_0.6.329_arm.fpk`（4,627,761 B），连同 `.sha256` / `.minisig` 三个资产 |
| 3 | **校验真伪**：`sha256sum -c` 核对两个架构；用二进制内嵌公钥（`internal/api/self_update_pubkey.txt`）对 `.minisig` 验签，两架构均应 `VALID` |
| 4 | **解包**：FPK 即 tar.gz —— 读取 `manifest`、解 `app.tgz`（内含 `moo-server`、`ui/`、`config/`），核对 `cmd/` 十个生命周期脚本与 `wizard/` |
| 5 | **静态扫描**：用仓库技能 `skills/moo-app-security-audit/scripts/fpk_audit.py` 跑一遍包层检查，再对 `moo-server` 做字符串 / 熵 / 加壳面核验 |
| 6 | **交叉核对**：`grep` 源码确认执行链（`exec.Command`）、抓取防护（`netguard`）、鉴权（可信通道 + 管理员 + CSRF 头）、自更新验签（fail-closed）等断言 |

> 复核环境建议：任意 Linux（x86_64）与 Go 1.27 + Python 3 即可，无需 NAS；所有操作都在临时目录完成，不影响任何运行中的实例。

---

## 方法边界

- 静态分析无法证明「不存在」，只能说明「在源码与包内容可见范围内未发现」；条件触发逻辑、依赖 CVE、运行时网络行为仍属未知面（见「五、未知风险」）。
- 结论仅对 `v0.6.329` 这一版成立；后续版本请重新验签并按本报告步骤复跑。
