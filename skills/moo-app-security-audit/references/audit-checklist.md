# Moo 应用安全审核 — 检查清单

> 配套脚本 `scripts/fpk_audit.py` 自动扫描；脚本报警可能含误报（见 §3.4 误报排除），命中后须人工逐条核验再定性。

## 一、元数据层（无需下载，从详情端点 /api/apps/{key}）

| 检查项 | 风险信号 | 判定 |
|---|---|---|
| install_type | `root` / `系统空间` | 回调脚本以 root 执行，提权面大 |
| source_is_fork | true | 搬运源，供应链风险（上游被篡改则全族中招） |
| app_type | docker | 看 docker-compose 特权/挂载 |
| maintainer_url 缺失 | 无源码线索 | 黑盒，只能解包分析 |
| upstream_repo | 指向第三方仓库 | 溯源链，判断真实上游可信度 |
| service_port / web_port | 暴露端口 | 暴露面 |

## 二、源码层（maintainer_url / upstream_repo → GitHub）

| 检查项 | 危险模式 |
|---|---|
| 硬编码凭据 | token / password / api_key / sk- / ghp_ / ssh private key |
| 网络行为 | 下载执行 curl\|sh、外联矿池、反弹 shell /dev/tcp |
| 数据窃取 | 上传 /etc/shadow、环境变量、敏感目录 |
| 数据收集 | 设备信息（型号/MAC/序列号/系统版本）+ 用户数据（配置/日志/账号）+ 上传到哪、是否明示 |
| 外联地址 | 遥测/上传/版本检查的 URL（对应「数据隐私/外联」维度） |
| 依赖漏洞 | 已知 CVE 依赖（go.mod / package.json 版本） |
| 持久化 | crontab、systemctl、rc.local 注入 |

## 三、包层（无源码 → 下载 release_url 的 FPK 解包）

### 3.1 manifest

- `run-as`（config/privilege）：`root` = 高风险
- install_type / service_port / checkport

### 3.2 cmd/* 回调脚本（root 执行）

危险模式：
- `curl|sh` / `wget|sh` / `curl -s ... | bash`（下载执行）
- `base64 -d | bash` / `eval`（编码混淆执行）
- `/dev/tcp/` / `nc -e`（反弹 shell）
- 矿池特征：`stratum+tcp` / `xmrig` / `pool.`
- `cat /etc/shadow` / 上传敏感文件（数据窃取）
- `crontab` / `systemctl enable` / `/etc/rc.local`（持久化后门）
- `chmod 777` / `chmod +s` / `sudo`（权限扩大）
- 硬编码凭据（token/password/内网 IP）

### 3.3 docker-compose.yaml（docker 类型）

- `privileged: true`
- 挂载 `/` / `/var/run/docker.sock` / `/etc`
- `network_mode: host`
- `cap_add: [ALL]` / `SYS_ADMIN`
- 端口暴露 `0.0.0.0:port`

### 3.4 二进制（strings / grep / file）

- 危险字符串：矿池、反弹 shell、硬编码 token、内网 IP
- Go 二进制：`strings` 提取 ASCII（注意中文需 `grep -ac` 直扫）
- **UPX/加壳检测**（「混淆/反分析」维度）：`file` 报 `no section header` + `statically linked`，或 strings 见 `upx.sf.net`，或头部高熵 → 加壳，静态分析受限
- **外联地址**（「数据隐私/外联」维度）：`grep -aoE 'https?://[a-z0-9./_-]+'` 提取全部 URL，区分官方域名/第三方/未知服务器

#### 误报排除（脚本报警 ≠ 真风险，须人工核验）

- `-----BEGIN ... PRIVATE KEY-----` 等 PEM 头常量 → 密钥格式识别串，非真实私钥
- 内嵌 Swagger UI / 前端 JS bundle 的 `license/source/password/token` → 库元数据与函数名
- `eval "x=\${var}"` 变量间接引用 → 非危险 eval
- RFC1918 私网三段（192.168/16、10/8、172.16/12）→ 私网网段定义（内网 IP 分类逻辑）
- 正则粘连（如 `10.200046.1.1`）→ 字符串误匹配

### 3.5 恶意代码与数据破坏（M1–M6，必查）

- [ ] **M1 病毒/蠕虫**：遍历 `@appdata/*`、`/vol*` 批量复制/改写；感染其他应用可执行文件；定时传播
- [ ] **M2 木马/后门**：`authorized_keys`、`socat ... EXEC`、`nc -l`、隐藏账号、免密 SSH、未在 UI 说明的隧道（frp/ngrok）
- [ ] **M3 恶意脚本**：`curl|sh`、`base64 -d | sh`、混淆执行外部输入、反调试
- [ ] **M4 数据破坏**：越界 `rm -rf`、`find / -delete`、`dd if=/dev/zero`、`truncate -s 0`、`mkfs`/`wipefs`、`DROP TABLE`/`DELETE FROM`、批量改名加密（勒索）
- [ ] **M5 数据窃取**：读 `/etc/shadow`、`~/.ssh`、Cookie、云凭据、其他应用配置，并外传
- [ ] **M6 反取证**：`history -c`、删 `/var/log`、`journalctl --vacuum`、隐藏进程/文件、时间戳篡改
- [ ] **误报核验**：WAF/杀毒类产品的恶意关键词库（`RANSOMWARE`/`xmrig`/`meterpreter`）要看上下文；`SECRET="${FOO:-}"` 不是硬编码凭据；`eval "x=\${var}"` 不是危险 eval
- [ ] **正常 vs 恶意删除**：卸载自己数据、清自己缓存/日志 = 正常；越界删他人目录/用户文件/备份 = M4
- [ ] 结论写明：命中 / 未命中 / **无法判断（加壳闭源）**，三种状态不能含糊

## 四、风险评级

> ✅ 评级方向：**安全分越高越安全**（≥70 🟢 安全 / 40-70 🟡 中 / <40 🔴 危险）；分最低的那一维就是报告里要重点解释的风险点。

| 等级 | 定义 |
|---|---|
| 高 | root 提权 + 下载执行/反弹 shell/挖矿/数据窃取/特权 docker |
| 中 | root 回调但无恶意行为、危险挂载、硬编码凭据、无源码黑盒 |
| 低 | 规范打包、有源码可审计、无危险行为 |

## 五、Moo 生态特有风险

- 搬运源供应链（fork 全族共享同一上游二进制）
- 无源码黑盒（大量第三方 FPK 无 maintainer_url）
- 回调脚本 root 执行面
- docker 应用特权滥用

## 六、收尾（出完 md 报告后必做）

- [ ] 报告附录列出本次落盘的 FPK / 源码树 / 解包目录 / 审计输出（绝对路径 + 大小）
- [ ] 只列自己产生的文件；用户既有文件、Moo 数据目录、应用安装目录不列不碰
- [ ] 用编号选项问用户：`1` 全部保留 / `2` 只留源码+报告 / `3` 全部清理
- [ ] 标注「不回 = 默认 1（保留）」，删除类无默认动作
- [ ] 用户回 3 时：先回显绝对路径 + 字节数 → 删 → 逐项验证不存在 → 报释放空间
- [ ] 删除禁止通配符扫盘；会话中断后接手先读报告附录，别凭记忆删
