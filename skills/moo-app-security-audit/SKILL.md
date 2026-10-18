---
name: moo-app-security-audit
description: "Audit the security posture of third-party apps in the Moo app source (飞牛 fnOS 第三方应用中心) — malware risk (virus/worm, trojan/backdoor, malicious scripts, reverse shell, mining, data theft), data destruction (deleting/overwriting/encrypting user data, config tampering, ransomware), permission risk (root/privileged), vulnerabilities (hardcoded credentials/known CVE), and attack surface (exposed ports/SSH keys). Requires a running Moo instance: always ask the user to connect theirs first (panel gateway /app/moo/api, loopback + admin headers, or direct port) and when several cards share the same app name, ask the user which one to audit. Scores are SAFETY scores (higher = safer). Three-layer analysis: metadata → source code (maintainer_url/upstream_repo) → FPK unpacking (manifest/cmd callbacks/docker-compose/binary), with bundled scripts/fpk_audit.py. Portable across agents (QwenPaw / Claude Code / Codex / Hermes), see INSTALL.md. Use when the user asks to audit/review Moo app security, third-party FPK risks, app vulnerabilities, attack surface, or wants a security risk assessment of apps in the Moo source. Not for fnOS official apps (fnos-official / 飞牛应用中心); fnos-store is a community source and IS in scope."
---

# Moo 应用安全审核

审核 Moo 应用源（飞牛 fnOS 第三方应用中心）里**非官方第三方应用**的安全性。飞牛官方源（source 标识 `fnos-official`，即「飞牛应用中心」OAuth 源）经平台审核，不在本技能范围。⚠️ **`fnos-store`（conversun/fnos-apps）是社区源、在审计范围内**，勿当官方源。

先定 `SKILL_DIR`（本 SKILL.md 所在目录），工具用绝对路径调用：

```bash
SKILL_DIR=/absolute/path/to/moo-app-security-audit
python3 "$SKILL_DIR/scripts/fpk_audit.py" <fpk或目录>
```

## 〇、开始之前：接入用户自己的 Moo（必做）

本技能**不内置任何应用数据**——所有审核结论都来自用户那台 NAS 上正在运行的 Moo。用户第一次让你做审核时，**先告知需要接入并确认**，不要凭记忆、不要假设、更不要自己编数据。

### 1. 先对用户说清楚

> 「这个审核需要连上你 NAS 上装的 **Moo（第三方应用中心）**，我会只读它的应用目录和详情来判断风险。请确认：① Moo 已经在应用中心运行、页面能打开；② 给我它的访问地址（`http://<你的NAS>:5666`）。」

Moo 未安装 / 未运行 → 先引导安装并启动（构建、安装、热替换见 `moo` 技能），**不要跳过接入直接臆测**。

### 2. 三条通道（按推荐顺序）

| 通道 | Base URL | 适用场景 | 说明 |
|---|---|---|---|
| **面板网关（推荐）** | `http://<NAS_IP>:5666/app/moo/api` | 任意机器；用户已登录 fnOS 面板 | 网关代理注入管理员身份 → 可信通道；手工 curl 需带面板会话 Cookie |
| **本机回环 + 调试代理** | `http://127.0.0.1:13813/app/moo/api` | 命令就跑在 NAS 上 | 必须带两个头：`X-Trim-Isadmin: true`、`X-Moo-Admin: 1` |
| **直连端口** | `http://<NAS_IP>:38100/api` | 只读粗探 | 非可信通道，敏感读/写一律 **403**，仅够确认版本 |

### 3. 接入自检（三条都过才算接通）

```bash
BASE=http://<NAS_IP>:5666/app/moo/api
curl -s "$BASE/about"        # ① 版本 + 运行状态
curl -s "$BASE/apps" | head -c 200   # ② 目录应返回 {"apps":[...]}
curl -s "$BASE/version"      # ③ trusted=true 才可读配置类数据
```

- ① 连不上 → 让用户确认 Moo 是否在运行、地址/端口是否正确
- ② 403「需要管理员身份」→ 改走面板网关通道（让用户先在面板登录）
- ③ `trusted=false` → 只能做有限只读探测，建议换网关通道再继续

### 4. 与 `moo` 技能的关系

`moo` 技能是本技能的**上游参考**：四平面模型、完整 API 契约（鉴权 / 可信通道 / 错误码）、写操作安全规则都在那里。本技能**只读**，不做任何安装/停用/写配置；端点语义不确定时按 `moo` 的 `references/api-full.md` 核对，不要猜。

> 接通后把实际 `BASE` 记在报告开头的「审核环境」一行，便于复现。

## 〇.5 交互约定：需要用户决定的一律给编号

审核中凡是「要用户拍板」的环节（选哪张同名卡、产物保留还是清理、要不要深挖某项），**一律写成带编号的选项**，让用户回 `1` / `2` / `3` 即可，不要用开放式提问（「你要审哪个？」「要不要删？」）。

| 规则 | 说明 |
|---|---|
| 编号 + 一句话代价 | 每个选项写清后果（如「删除不可恢复」） |
| 支持多选 | 用户回「1 3」= 同时选 1 和 3 |
| 必须给默认 | 标注「不回 = 默认 X」；默认只允许是安全的那个（保留文件、继续等待） |
| 不可逆操作无默认 | 删除类必须显式确认，用户不回就**什么都不动** |
| 一次问清 | 同一轮的所有确认点合并成一组编号，不要分多轮追问 |

## 一、审核流程（4 步）

0. **接入与定位**（见「〇」）：确认 Moo 可访问 → 定位目标应用。
   `/api/apps` 中同一应用名常有多张卡（不同源 / 搬运 fork），key 形如 `appname@source`。**命中 >1 条时必须停下来让用户选**，不要自行挑一个；用户不回就不要继续审（审错对象 = 白做且误导）。

   提问按下面这张表展示候选，**用编号选项**（见「〇.5」），用户回序号即可；要审多张就回「1 2」：

   | # | key | 显示名 | 版本 | 来源 | 已装 | 权限 |
   |---|---|---|---|---|---|---|
   | 1 | `foo@Blue-Mink` | Foo | 1.2.0 | 社区源 | 否 | 系统空间 |
   | 2 | `foo@shuangji66` | Foo | 1.1.8 | 搬运源 | 是 | root |

   例外：用户已给出完整 key，或明确要求「全部同名卡都审」，则按其范围执行。
1. **界定范围**：从全目录剔除飞牛官方源（`fnos-official`，406 卡），得非官方应用集合（含 `fnos-store` 等社区源）。
2. **元数据初筛**：按 `install_type`（root/系统空间）、`source_is_fork`、`app_type`、有无 `maintainer_url` 分层，锁定高风险样本。
3. **分层深入**（见下「三层分析」）：有源码看源码，无源码解包 FPK；**每个应用都必须过「三.4 恶意代码与数据破坏专项（M1–M6）」**，命中 M1–M4 直接按硬失败处理。
4. **评级与报告**：按「安全评分」打分，归纳风险模式，输出报告（md 落盘）。
   报告写完后**必须**按「七、收尾」把**本次落盘的产物清单**和**保留/清理编号选项（1/2/3）**一并交给用户；报告附录里也要写这份清单。

## 二、数据访问（admin API）

用「〇」里确认过的 `BASE`；头部按通道加（回环代理需要，面板网关由 Cookie 接管）：

```bash
# 全目录（返回 {apps:[...]}）
curl -s -H "X-Trim-Isadmin: true" -H "X-Moo-Admin: 1" "$BASE/apps"
# 详情（key=appname@source，@ 编码 %40；返回 release_url/maintainer_url/upstream_repo/source_is_fork/install_type/app_type/size_bytes）
curl -s -H "X-Trim-Isadmin: true" -H "X-Moo-Admin: 1" "$BASE/apps/{key}"
```

本地按 `appname` / `display_name` 过滤；**命中多条同名卡时回到流程第 0 步让用户选**，不要自动挑。

## 三、三层分析

### 1. 元数据层（无需下载）

- `install_type = root` / `系统空间` → 回调脚本以 root 执行，提权面大
- `source_is_fork = true` → 搬运源，供应链风险（上游被篡改则全族中招）
- `app_type = docker` → 看 docker-compose 特权/挂载
- 无 `maintainer_url` → 黑盒，只能解包
- `upstream_repo` → 溯源链，判断真实上游可信度

### 2. 源码层（maintainer_url / upstream_repo → GitHub）

用 codeload tarball 或 raw 直拉（无需 token）：
```bash
curl -L "https://codeload.github.com/{owner}/{repo}/tar.gz/refs/heads/main" | tar xz
```
按类别逐项核查，每类在报告里给一行结论：

- **凭据**：硬编码 token / 密码 / 示例密钥
- **执行链**：`curl|sh`、下载即执行、动态 eval
- **驻留**：crontab / systemd / rc.local / `.bashrc`
- **恶意行为**：反弹 shell、挖矿、数据窃取
- **依赖**：已知 CVE 的第三方包与版本
- **外联**：遥测 / 上报地址（对应「数据隐私/外联」维度）

### 3. 包层（无源码 → 下载 release_url 的 FPK → `fpk_audit.py`）

```bash
python3 "$SKILL_DIR/scripts/fpk_audit.py" <下载的.fpk>
```
脚本按类别输出（对应八维中的后五维）：

- **权限**：manifest `run-as`、`cmd/*` 回调脚本的危险写法
- **暴露面**：docker-compose 特权 / host 网络 / 敏感挂载
- **凭据与网络**：二进制里的凭据、内网 IP、矿池地址、外联域名
- **混淆/反分析**：UPX / 加壳特征
- **数据隐私/外联**：上报端点与遥测行为

### 4. 恶意代码与数据破坏专项（M1–M6，必查）

三层分析之外，**每个被审应用都要单独过这六项**，结论写进报告（命中/未命中/无法判断）。

| 编号 | 类别 | 查什么（源码 / 脚本 / 二进制 / 运行时） |
|---|---|---|
| **M1** | 病毒 / 蠕虫 | 自我复制、批量改写其他应用文件、遍历 `@appdata/*` 感染、定时传播 |
| **M2** | 木马 / 后门 | 反弹 shell、C2 回连、无鉴权远控端口、隐藏账号、写入 `authorized_keys`、免密 SSH、隐蔽隧道（frp/ngrok） |
| **M3** | 恶意脚本 | `curl\|sh` 下载即执行、`base64 -d \| sh`、混淆/加壳、反调试、白利用（借合法工具执行） |
| **M4** | 数据破坏 | 删除/覆盖/加密用户数据：`rm -rf` 越界、`find / -delete`、`dd if=/dev/zero`、`truncate -s 0`、`mkfs`/`wipefs`、`DROP TABLE`/`DELETE FROM`、批量改名加密（勒索） |
| **M5** | 数据窃取 | 读取 `/etc/shadow`、`~/.ssh`、浏览器 Cookie、云凭据、`@appdata` 其它应用配置，并外传 |
| **M6** | 隐蔽 / 反取证 | `history -c`、删日志、`journalctl --vacuum`、隐藏进程/文件、时间戳篡改 |

**判定规则**

- **M1–M4 任一被证实 → 直接判定「不要安装 / 立即卸载」**：安全性维度记 **0**，综合分不再按加权计算（见 `references/scoring-model.md` 硬失败规则）。
- **M5/M6 被证实** → 安全性 ≤ 10，结论同 🔴，并在报告里列出「读了什么、发到哪」。
- **无法判断**（加壳/闭源/无源码）→ 不算命中，但必须写进「未知风险」，并在结论里明说「该项未能验证」。
- **区分正常运维删除**：卸载自己数据目录、清理自己的缓存/日志属正常；**越界删除他人目录、删用户文件、删备份、删其他应用数据**才是 M4。

**快速取证命令**（只读；在 FPK 解包目录或源码树执行）

```bash
# M2/M4/M6：脚本与源码危险模式（含命中片段，便于核验误报）
grep -RnE 'authorized_keys|socat.*EXEC|rm[[:space:]]+-rf|find[[:space:]]+/[[:space:]]|dd[[:space:]]+if=/dev/(zero|urandom)|mkfs|wipefs|shred|history[[:space:]]+-c|/var/log' .
# M3：下载执行 / 混淆
grep -RnE 'curl[^|]*\|[[:space:]]*(sh|bash)|base64[[:space:]]+-d|eval[[:space:]]' .
# M5：敏感路径
grep -RnE '/etc/shadow|\.ssh/|id_rsa|Cookies|\.aws/|@appdata/' .
```

> ⚠️ **误报纪律**：WAF / 杀毒 / 扫描器类产品会内嵌恶意关键词库（`RANSOMWARE`/`xmrig`/`meterpreter` 等），命中时必须看上下文；`SECRET="${FOO:-}"` 这类环境变量取值不是硬编码凭据；`eval "var=\${...}"` 这种变量间接取值要与「拼接外部输入执行」区分。判恶意必须给出**具体文件 + 片段 + 行为链**。

配套参考：

- [references/audit-checklist.md](references/audit-checklist.md) —— 完整检查清单
- [references/default-creds.md](references/default-creds.md) —— 弱默认口令库
- [references/report-template.md](references/report-template.md) —— 八维报告模板

**供应链完整性对比**（搬运源 FPK vs 上游官方 FPK 的二进制 sha256）：
```bash
python3 "$SKILL_DIR/scripts/supply_chain_compare.py" <搬运fpk> <上游官方fpk或release_url>
```

## 四、安全评分模型

**八维安全分**（每维 0-100，**分数越高越安全**）：

| 维度 | 主要看什么 |
|---|---|
| 安全性（恶意风险） | 病毒/木马/后门/恶意脚本/数据破坏/数据窃取（见「三.4 M1–M6」） |
| 权限 | run-as、systemd 自启、系统路径注入 |
| 漏洞 | 硬编码凭据、弱默认口令、依赖 CVE |
| 暴露面 | 对外端口、docker.sock、host 网络、危险挂载 |
| 供应链 | 搬运深度、多源放大、上游可信度 |
| 未知风险 | 闭源二进制、镜像漂移、动态/延迟行为 |
| 混淆/反分析 | 加壳、混淆器、反调试、无符号 |
| 数据隐私/外联 | 收集什么、上传到哪、是否明示 |

- **综合安全分 = min(八维) × 0.6 + avg(八维) × 0.4**（短板主导：任何一维高危都会明显压低综合分）
- 等级：**≥ 70 🟢 安全** / **40-70 🟡 中** / **< 40 🔴 危险**

> ✅ **评分口径（必须照此输出）**：所有分数都是 **安全分**。
> **分数越高 = 越安全，分数越低 = 越危险。**
> - **100 分** = 该维度无风险 / 防护到位
> - **0 分** = 该维度极高风险（后门、root + 系统注入、明文凭据、数据窃取…）
> - 「权限 90」= 权限收敛得体面；「权限 10」= root + 自启 + 系统路径注入。
> - **禁止**再把分数叫「风险分」，也不要输出「越高越危险」的口径说明。

完整定义与打分规则见 [references/scoring-model.md](references/scoring-model.md)。

### 未知风险（Uncertainty）—— 审计看不到的部分，必须单独标注

- **闭源二进制**：strings 只看常量，看不到控制流/条件触发逻辑
- **动态/延迟行为**：静态分析看不到运行时外联，恶意逻辑可能条件触发
- **镜像漂移**：`image:latest` 内容随时变，审计版本 ≠ 用户实际拉取版本
- **供应链投毒窗口**：审计后上游被投毒，结论有时效性
- **依赖 CVE 未扫**：go.mod/package.json 依赖漏洞默认不深扫

> 注：加壳/混淆/反调试已独立为「混淆/反分析」维度（可检测），不计入「未知风险」。

### 补充审核维度（除八维外，按需纳入）

- **供应链信誉**：维护者可信度、fork 深度、上游活跃度（最近提交时间）
- **更新机制安全**：自更新是否校验 sha256/签名
- **数据隐私**：收集什么数据、上传到哪里、有无遥测
- **卸载干净度**：卸载是否留残留（crontab/systemd/rc.local）
- **历史漏洞记录**：该 app 是否有 GitHub advisories / 公开 CVE 记录
- **合规性**：license、隐私政策、是否闭源收费

## 五、Moo 生态特有风险模式

完整归纳见 [references/risk-patterns.md](references/risk-patterns.md)。核心十条（按严重度排序）：

1. **搬运/供应链放大**（最高）：432 个 appname 被 156 个 fork 源放大到 1605 卡（fntermx 高达 54 源），shuangji66 家族占 47%，任何一环投毒即全链放大。
2. **root/系统空间安装面**：208 系统空间 + 40 root 卡，`run-as: root` 集中于服务器面板/媒体服务器/网盘挂载。
3. **Docker 特权容器**：docker.sock + host 网络 + root，容器逃逸高发。
4. **回调脚本 root 系统注入**：`cp → /usr/local/bin`、systemd 自启、明文密码写系统脚本。
5. **弱默认凭据**：Lucky `666`、lxserver `123456`、lunatv `admin_password`。
6. **持有第三方登录态**：deepseek.harness（会话 token）、loomyproxy（Loomy Cookie）。
7. **无源码黑盒**：982 卡无 maintainer_url，闭源二进制无法静态审计。
8. **任意代码执行面**：duanju 上传 .js 接口文件即执行。
9. **镜像源混杂**：docker.io / ghcr.io / 南大镜像 / 自建 GHCR 并存。
10. **当前基线（正面）**：抽样 0 恶意代码（无 curl|sh 注入/挖矿/反弹 shell），风险集中在供应链、权限面、凭据管理。

安装前按四类给出提示：

1. **Docker 管理型**：挂了 docker.sock 或 host 网络，等于把宿主机交给容器
2. **系统空间 / root**：回调脚本以 root 执行，可改系统文件
3. **持有第三方登录态**：需要你的账号 Cookie / token 才能运行
4. **弱默认口令**：首次登录必须立即改密码

## 六、边界

- 只读分析，绝不安装/运行任何被审应用，不触碰主 NAS 已安装应用
- FPK / 源码下载到临时目录，**默认保留**；清理必须由用户在「七、收尾」显式选编号，不得自动删除
- 发现敏感信息只描述类型，不贴原文；报告脱敏（凭据/内网 IP/内网域名/个人邮箱）

## 七、收尾：产物清单与清理确认（必须做）

报告写完后**不要自行删任何东西**（下载的 FPK、解出的源码树、临时解包目录、审计日志），
按下面两步收尾。

### 1. 先把产物列清楚

在 md 报告末尾的「附录：审计产物与清理确认」里逐条列出本次落盘的东西：

| # | 产物 | 路径 | 大小 | 用途 |
|---|---|---|---|---|
| 1 | 安装包 | `{绝对路径}` | `{大小}` | 解包审计 / 复现 |
| 2 | 源码树 | `{绝对路径}` | `{大小}` | 源码层审计 |
| 3 | 解包目录 | `{绝对路径}` | `{大小}` | 包层审计 |
| 4 | 审计输出 | `{绝对路径}` | `{大小}` | 证据留档 |

**只列本次审计自己产生的文件**：用户的既有文件、Moo 数据目录、应用安装目录一律不列、不碰。

### 2. 用编号选项问用户（1/2/3）

固定给三选一，必须写清不可逆与默认值：

```text
审核完成。本次落盘产物：{n} 项，共 {大小}（明细见报告附录）。
请回序号（可多选，如「1 3」）：
1. 全部保留（推荐：便于复现与复核；不删任何东西）
2. 只保留源码树 + md 报告，删掉 FPK 与解包目录（省空间，仍可复看代码）
3. 全部清理（删 FPK、源码树、解包目录；md 报告保留）
不回 = 默认 1（保留），不会删除任何文件。
```

执行规则：

- **编号就是答案**：用户回 `1`/`2`/`3`（或组合）即执行，无需再复述路径。
- 执行删除前先回显**绝对路径 + 字节数**；删完逐项验证不存在，并报释放空间。
- 删除范围严格限定在报告附录列出的路径，**禁止通配符扫盘**（`rm -rf /tmp/*`、`find -delete` 之类）。
- 用户选了清理而报告里还引用这些路径（复现命令 / 证据）→ 顺带提示「已清理，复现需重新下载」。
- 会话被隔离或中断：产物路径已写进报告附录，接手时先读附录再决定，**不要凭记忆删文件**。

## 八、跨 Agent 安装（QwenPaw / Claude Code / Codex / Hermes）

本技能是**纯 Markdown + 标准库 Python**，不依赖任何平台专有 API，可移植到其它 Agent。安装方式见 `INSTALL.md`，一键脚本：

```bash
sh "$SKILL_DIR/scripts/install.sh" --list          # 查看全部目标
sh "$SKILL_DIR/scripts/install.sh" --target cursor --apply
sh "$SKILL_DIR/scripts/install.sh" --target all-project --apply
sh "$SKILL_DIR/scripts/install.sh" --target hermes --dest <skills目录> --apply
```

| 形态 | 目标 | 说明 |
|---|---|---|
| 技能目录 | `qwenpaw` `claude-code` `qwen-code` `agents-skills` `skills-dir` | 复制整个技能（SKILL.md + scripts + references） |
| 提示词/命令 | `codex` `gemini-cli` `hermes` `generic` | Codex prompts / Gemini 自定义命令 / 通用提示词 |
| 项目规则 | `agents-md` `cursor` `windsurf` `cline` `roo` `kilo` `continue` `augment` `kiro` `copilot` `openhands` `trae` `junie` `aider` `warp` | 写入各 Agent 的规则路径；单文件类用标记段落追加 |
| 组合 | `all-project` | AGENTS.md + 常见 IDE 规则一次到位 |

> `AGENTS.md` 是跨工具事实标准（Codex / Cursor / Claude Code 回退 / Continue / Aider / OpenHands / Copilot agent / Kiro 等），
> 不确定对方支持什么时优先装 `agents-md`。完整矩阵、手动步骤、更新/卸载见 `INSTALL.md`。

**平台无关约束**：只读审计；脚本用 `python3` 标准库；临时产物落临时目录并走「七、收尾」的清理确认；不写入被审应用；不依赖 Moo 之外的网络（除拉源码/FPK）。
