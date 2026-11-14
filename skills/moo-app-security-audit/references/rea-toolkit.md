# REA（反汇编 / 逆向工具链）在本技能里的用法

> 上游：<https://github.com/morluto/rea>（MIT，网站 <https://rea.tools/>，npm 包 `rea-agents`）
> 定位：**一个给 Agent 用的逆向 MCP**——把「反汇编、伪代码、符号与引用、Go 模块表、
> JS/Electron 结构、抓包还原」这些原本靠人肉点工具的动作，变成 Agent 可以按需调用、
> 且**结论带证据与边界**的查询。
>
> 在本技能里它是**解壳之后的下一步**，不是第一步：先用
> [packed-binary-unpack.md](packed-binary-unpack.md) 把壳去掉，再用它读逻辑。

---

## 一、什么时候值得引入

| 场景 | 用 REA 能回答 | 不引入也能凑合 |
|---|---|---|
| 解壳后的 ELF，需要「某条能力怎么实现」 | 反汇编 / 伪代码 / 交叉引用 / 调用链 | `objdump -d` + 人工读 |
| Go 二进制，想知道真实依赖与构建来源 | 内嵌编译器版本、模块列表、replace、构建设置（内置静态解析，**不需要装 Go**） | `go version -m <bin>` |
| 判断二进制加固水平 | PIE / NX / RELRO / CFG 等头部标志 | `readelf -lW`、`checksec` 脚本 |
| 前端 bundle 有可疑外联 | 模块 / 路由 / IPC 边界 / release 暴露面（`audit-release-exposure`） | `grep -oE 'https?://...'` |
| 只有 HAR / pcap | 端点、DNS、TLS SNI、协议分布、HTTP 请求（需 tshark） | `tshark -r` 手撸 |
| 需要「证据链」写进报告 | 每条结论附文件/地址/片段与局限说明 | 截图 + 手抄 |

**不需要引入的情况**：只是查 `strings` 里的域名、只看 manifest / 回调脚本、样本已开源。
别为了一个 `grep` 就装一套工具链。

---

## 二、安装（三种，按环境选）

```bash
# ① 让 Agent 自己注册（推荐）：写入 MCP 配置 + 配套工作流说明，改前会备份
npx rea-agents setup

# ② 装成全局命令，长期用
npm install --global rea-agents
rea --help

# ③ 一次性调用，不落地
npx -y rea-agents@latest <子命令> ...
```

- 要求：**Node.js 22.x（≥22.23.4）/ 24.x（≥24.22.1）/ 26.12.1+**，以及 npm。
- Linux 上 Ghidra / JADX / 固件类 / JS 静态分析还需要一个 **procps 兼容的 `ps`**
  （用于进程归属检查）——精简镜像里常缺，缺了会报错。
- 原生二进制分析需要一个**反汇编后端**：Hopper / Ghidra / IDA / Kuna 任选其一，
  已有装好的就复用；setup 可选装 Hopper（会请求确认）。**没有后端就只能做内置静态检查**
  （Go 模块、ELF notes、加固标志、PE imports 等）。
- 更新：`rea update`（全局安装）；`npx` 用法改跑 `npx rea-agents@latest setup` 刷新注册。

---

## 三、与本技能流程的对接点

1. **三.3 包层**：`fpk_audit.py` 报「加壳 / 无 section header」→ 先按
   `packed-binary-unpack.md` 解壳；解出 ELF 后再用 REA 读逻辑。
2. **三.4 M1–M6**：M2/M3/M5 的「行为链」证据（谁调用了 `exec`、数据从哪读到哪发）
   用交叉引用追，比 grep 靠谱；但**结论仍要落到具体地址 + 片段**。
3. **数据隐私/外联**：字符串归因之后，用 HAR/pcap 分析补「实际连了什么」，
   再用前端 bundle 分析补「浏览器侧连了什么」。
4. **报告**：凡是 REA 给出的结论，把「工具 + 版本 + 输入文件 sha256 + 局限」一并写下，
   否则无法复核。

---

## 四、安全与纪律（第三方工具，按第三方对待）

- **只从官方渠道获取**：`morluto/rea`、`rea.tools`、npm `rea-agents`。同名仓库 / 抄来的
  README 不构成「官方」。
- **在目标项目之外的受信目录里安装/运行**（官方 README 明确要求），避免把工具产物写进被审样本树。
- **锁定版本**：`rea-agents@<version>` 固定，不要在生产审核里用 `@latest` 的浮动结果；
  记录版本号进报告。
- 分析默认在本地跑，但**仍是第三方代码**：不要在装着生产凭据的机器上跑；
  沙箱/一次性容器优先。
- 反汇编后端（Ghidra/IDA/Hopper）自身也可能联网/上传样本，**断网环境下先确认**。
- 工具能给的是「证据 + 局限」，不是「安全结论」：**判定权在 M1–M6 与评分模型**，
  不要因为「REA 没发现问题」就上调安全分。

---

## 五、不可用时的替代路径（必须能降级）

| 需求 | 降级方案 |
|---|---|
| 反汇编 / 伪代码 | `objdump -d`；`rizin -A -qc 'pdf @ main'` |
| 交叉引用 | `objdump -d` + `grep` 调用目标地址；或 `rizin` 的 `axt` |
| Go 依赖 | `go version -m <bin>`；无 Go 工具链时读 `.go.buildinfo` 段 |
| 加固标志 | `readelf -lW`（GNU_STACK/GNU_RELRO）、`readelf -d`（BIND_NOW）、`file`（PIE 看 `Type: DYN`） |
| 前端外联 | `curl /` → 抽 `.js` → `grep -oE 'https?://[^"'\''\\ ]+'` 去重排序 |
| 抓包 | `tshark -r x.pcap -Y dns -T fields -e dns.qry.name \| sort -u` |

> 降级不是「凑合」：只要**证据字段齐全**（文件、地址/行号、片段、样本 sha256），
> 结论同样成立。工具只影响效率，不影响结论强度。

---

## 六、写进报告的固定句式

```
工具         REA <version>（provider: <Ghidra/IDA/Hopper/Kuna/内置>）
输入         <文件> sha256:<...>
方法         <反汇编交叉引用 / Go 模块表 / JS 静态分析 / pcap 分析>
结论         <一句话 + 证据位置>
局限         <未覆盖的控制流 / 条件触发 / 版本范围>
```
