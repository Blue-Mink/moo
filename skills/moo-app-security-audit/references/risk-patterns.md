# Moo 应用生态 · 风险模式归纳（基于 2026-10-17 抽样审核）

> 抽样 18 应用 + 1 索引仓（native 13 / docker 5），覆盖 shuangji66 搬运家族、自打包（有源码）、无源码黑盒、系统空间/root 高风险面。结果：高 2 · 中 6 · 低 10 · **恶意代码 0**。

## 生态级数据（2026-10-17）

- 全目录 2164 卡；非官方（排除 fnos-official 飞牛官方源 406 卡）1758 卡（含 fnos-store 社区源 153 卡）
- 非官方 distinct appname **432**（234 个 appname 被 ≥2 个源打包，fntermx 高达 54 源）
- 类型：native 1497 / docker 107
- 安装类型：存储空间 957 / 系统空间 **208** / root **40** / 未标 378
- shuangji66 家族（10+ 个 fork 源）合计 **762 卡（47%）**
- 有源码线索 623 / 无源码黑盒 982

## 十六大风险模式

### 1. 搬运/供应链放大（最高系统性风险）

同一 appname 被大量 fork 源重复打包分发，任何一环被投毒即全链放大。溯源难、来源分散。
**识别**：`source_is_fork=true` / `upstream_repo` 指向同一上游 / 多源同 appname。

### 2. root / 系统空间安装面

`run-as: root` 集中在服务器面板/媒体服务器/网盘挂载三类（1panel/embyserver/CloudDrive2/deepseek.harness），均需系统级权限但安装前无显著提示。
**识别**：`install_type=root` 或 `系统空间`；FPK `config/privilege` `run-as: root`。

### 3. Docker 特权容器（容器逃逸高发形态）

「Docker 管理型」应用（MoviePilot 类）要求 `docker.sock` 挂载 + `network_mode: host` + `PUID/PGID=0`。
**识别**：docker-compose `privileged: true`、`/var/run/docker.sock`、`network_mode: host`、`cap_add: [ALL/SYS_ADMIN]`。

### 4. 回调脚本 root 系统注入

回调脚本拥有系统级写权限，可复制二进制进 `/usr/local/bin`、装 systemd 服务、把明文密码写进系统脚本。
**识别**：`cmd/*` 里 `cp ... /usr/local/bin`、`systemctl enable`、`sed ... > /usr/local/...`、`usr-local-linker`。

### 5. 弱默认凭据 / 硬编码口令

装完即有已知默认口令，未改密即被接管。
**识别**：`config.go` / `install_callback` 里 `默认口令 666`、`123456`、`admin_password`、`admin/admin_password`。

### 6. 持有第三方登录态 / 会话中继

应用长期持有第三方账号会话（DeepSeek token、Loomy/讯飞 HttpOnly Cookie），泄露即账号失陷。
**识别**：描述/文档提及「登录态迁移」「会话 cookie」；源码持有 token/cookie 持久化。

### 7. 无源码黑盒（闭源二进制）

982 张卡无 maintainer_url，只能 FPK 解包，二进制内部行为无法静态审计。
**识别**：详情 `maintainer_url` 缺失；FPK 内是闭源二进制（emby-server/clouddrive/server.js）。

### 8. 任意代码执行面

应用支持用户上传脚本/接口文件被服务端执行。
**识别**：描述提及「上传 .js/.json 接口文件」；服务端直接 require/exec 上传内容。

### 9. 镜像源混杂（供应链镜像不可见）

docker 镜像来自 docker.io / ghcr.io / 南大镜像 ghcr.nju.edu.cn / 自建 GHCR，来源与更新不可见。
**识别**：docker-compose `image:` 指向非官方 registry。

### 10. UPX 加壳 / 混淆（反静态分析，对应「混淆/反分析」维度）

二进制经 UPX 或自定义壳压缩/混淆，strings/grep 只能看到壳 stub 与极少量常量，默认口令逻辑、更新下载地址、外联地址等关键字符串全部不可见，搬运者是否夹带无法静态确认。
**识别**：`file` 报 `no section header` + `statically linked`；strings 见 `upx.sf.net`；`no section header` 是强信号。lucky 2.27.2 即典型（官方原版也 UPX，但搬运源叠加后供应链更不可信）。

### 11. 数据外联 / 遥测（对应「数据隐私/外联」维度）

应用运行时外联（遥测上传、版本检查、日志上送、下载更新），需区分官方域名/第三方/未知服务器。静态只能提取 URL 常量，看不到运行时外联目标。
**识别**：`grep -aoE 'https?://[a-z0-9./_-]+'` 提取全部 URL；外联非官方域名或未知服务器即提权风险。

### 12. 正面观察（当前生态基线）

抽样样本打包脚本/源码**未发现** `curl|sh` 管道注入、base64 混淆、反弹 shell、挖矿矿池等明显恶意模式。当前生态整体恶意率低，风险集中在供应链、权限面与凭据管理，而非显式恶意代码。mi-paiai 的 compose 加固（read_only/cap_drop ALL/no-new-privileges/tmpfs）是正面范例。

### 13. 木马 / 后门（M2，硬失败）

特征：反弹 shell、C2 定时回连、无鉴权远控端口、隐藏账号、写 `~/.ssh/authorized_keys`、
免密 SSH、隐蔽隧道（frp/ngrok/cloudflared 未在 UI 说明用途）。
**识别**：`grep -RnE 'authorized_keys|socat.*EXEC|nc -l|/etc/sudoers'`；二进制查 `meterpreter|cobalt strike|beacon`；
运行时核对监听端口是否与文档一致。
**处置**：证实即安全性 0，🔴 立即卸载。

### 14. 病毒 / 蠕虫（M1，硬失败）

特征：遍历 `@appdata/*` 或 `/vol*` 批量改写/复制自身、感染其他应用的可执行文件、
定时二次传播、修改其他应用的启动脚本。
**识别**：`find` + `sed -i`/`cp` 组合；运行期文件 mtime 异常批量变化；启动脚本被非本应用改写。
**处置**：证实即安全性 0，🔴 立即卸载并检查同机其他应用是否被感染。

### 15. 数据破坏 / 勒索（M4，硬失败）

特征：越界 `rm -rf`（删别人的 `@appdata/<app>`、用户目录、备份）、`find / -delete`、
`dd if=/dev/zero`、`truncate -s 0`、`mkfs`/`wipefs`、数据库 `DROP TABLE`/`DELETE FROM`、
批量改名加密并索要赎金。
**识别**：脚本里未加引号的 `rm -rf ${VAR}`、路径含 `/vol` 或通配符；SQL 破坏语句；
二进制里 `.locked`/`encrypted` 字样 + 支付提示。
**处置**：证实即安全性 0，🔴 立即卸载；提示用户先做快照/备份再动。

### 16. 数据窃取与反取证（M5 / M6）

特征：读取 `/etc/shadow`、`~/.ssh`、浏览器 Cookie、云凭据、其他应用配置后外传；
`history -c`、删 `/var/log`、`journalctl --vacuum`、隐藏进程/文件、时间戳篡改。
**识别**：敏感路径读取 + 同进程外联；日志被异常截断；二进制含 `timestomp` 等字样。
**处置**：M5 证实 → 安全性 ≤10、🔴 不要安装；M6 证实 → 安全性 ≤30 并压低未知风险。

## 建议的安装前提示（四类）

1. Docker 管理型（docker.sock + host 网络 + root）—— 提示容器逃逸面
2. 系统空间 / root 安装 —— 提示系统级写权限
3. 持有第三方登录态 —— 提示账号会话风险
4. 弱默认口令 —— 提示「装完立即改密」
