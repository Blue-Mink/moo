# 加壳 / 改头二进制：动态 dump 解包法

> 适用场景：FPK 里的原生二进制被 UPX 或**改头 UPX**（变了 version/format 字节）加壳，
> `upx -d` 报 `NotPacked` / `header corrupted`，`strings` 只能看到壳 stub 与极少量常量，
> 无法判断是否夹带后门、外联、弱口令逻辑。本流程把「看不见」变成「可以看」。
>
> 纪律：**只在隔离环境（一次性容器 / 沙箱 / 虚拟机）里做**，全程只读原始包；
> 解出来的二进制**不要在生产机执行**。本方法不改变「闭源 = 控制流仍不可读」的性质。

---

## 〇、开工前先记三件事

| 项 | 记什么 | 为什么 |
|---|---|---|
| 样本指纹 | 原始二进制 **sha256 + 字节数** | 解壳产物要能回溯到具体样本 |
| 环境 | 发行版 / gdb 版本 / 可用内存 | dump 长度常是内存段大小，内存不足会静默截断 |
| 判定口径 | 「解壳成功」= 四重验证全过（见 §5） | 只 dump 出一个能跑的 ELF 不算成功，可能是半截 |

---

## 一、先判型（30 秒，决定走哪条路）

```bash
file   <bin>          # 常见输出：ELF 64-bit ... statically linked, stripped（且无 "not stripped"）
readelf -l <bin>      # 看 LOAD 段布局与 Entry
readelf -h <bin>      # 看 e_shoff / e_shnum
xxd -s 0xec -l 16 <bin>   # PackHeader
```

**PackHeader（文件偏移 `0xec`）**：前 4 字节是 `55 50 58 21`（`UPX!`），后 4 字节是
`version` + `format`。官方标准形态常见 `0x38` / `0x14`；出现**其它取值**（例如 `0x34`）
就是**改过头的变种**——这正是官方 `upx -d` 与常见脱壳器一律拒绝的原因。

**典型加壳布局**（readelf -l 实测形态）：

```
LOAD#1  0x400000   FileSiz 0x1000     MemSiz 0x6b32e50   RW    <- 解压目标（MemSiz 很大，FileSiz 极小）
LOAD#2  0x6f33000  FileSiz 0x15e8879  MemSiz 0x15e8879   R E   <- 壳 stub + 压缩数据
GNU_STACK
（无 section header）
```

判据：**低地址段 FileSiz≈0x1000 而 MemSiz 数十 MB** = 解压目标；
**Entry 落在高地址可执行段** = 壳 stub。两条同时成立即确认加壳，可继续。

> 如果两个 LOAD 都很规整、Entry 落在 `.text` 起始、`strings` 能看到大量业务常量，
> 那多半只是普通 strip，别白跑一遍动态 dump。

---

## 二、怎么找壳的跳转点（不靠猜）

### 2.1 反汇编 stub 尾部

```bash
objdump -d --start-address=0x<stub_start> --stop-address=0x<stub_end> <bin> | tail -80
```

改版 UPX 的 stub 尾部有一条稳定的特征链（本方法实测复现）：

```
push 0x587075            ; "upX\0"
mov eax,0x13f; syscall   ; 319 = memfd_create("upX")   ← 注意不是 uname(63)
mov rax,0x9;   syscall   ; mmap(fd, PROT_READ|PROT_EXEC, MAP_SHARED)
mov rax,0x3;   syscall   ; close(fd)
pop rax                  ; rax = mmap 基址
add rax,0x10
jmp rax                  ; ★ 阶段 1 跳转点
```

判定要点：

- **`0x13f`（319）= `memfd_create`**，不是 `uname`；`0x27`（39）= `getpid`，不是 `mkdir`。
  网上流传的解包笔记在这两处常写错，照抄会误判。
- 若 stub 里出现 `mmap` + `mprotect` + `jmp r??` 而无 `memfd_create`，说明是别的壳
  （或 UPX 的另一种变体），**不要硬套本流程**，改用 §6 的通用观察路径。

### 2.2 ★ 地址换算（最常见的假失败）

反汇编/`xxd` 给的常常是**文件偏移**，CPU 用的是**运行地址**：

```
运行地址 = 文件偏移 - 该段的 p_offset + 该段的 p_vaddr
```

在本类布局里可简化为 `运行地址 = 文件偏移 + 段2.VirtAddr`（段2 的 `p_offset` 恰为 0）。

> 踩坑实录：用文件偏移下断点，gdb **不报错、不命中**，程序直接跑完 —— 容易被误判成
> 「样本不带壳 / 断点没触发」。下断点后必须确认 gdb 打印了 `Breakpoint N at 0x...`，
> 且该地址落在 `info proc mappings` 的可执行区间内。

---
## 三、gdb 两阶段 dump

改版 UPX 是**两段解压**：stub 先把一个 8 KB 量级的二段解压器放进 memfd 并跳过去，
二段器再解压真正的主体。所以断点要下两次。

### 3.1 阶段 1：抓二段解压器

```
# gdb 脚本（要点，非完整脚本）
set pagination off
set disable-randomization on           # 关掉 ASLR → 映射地址固定、dump 可复现（代价：地址与真实运行不同，按 mappings 取基址）
break *0x<阶段1运行地址>
run
# 命中后：
info registers rax                    # rax = memfd 映射基址（二段器入口）
dump binary memory memfd.bin <rax> <rax+0x2000>
```

- `memfd.bin` 大小通常 8 KB；**大小异常（几十 KB 或 0）说明抓错地址**。
- 记下 `rax` 值，阶段 2 的断点要相对它算：`rax + 0x15a` 是最常见的二段入口偏移
  （多版本实测通用，但**必须在本样本上实测命中**，不能直接信）。

### 3.2 阶段 2：抓主体 + OEP

```
break *<阶段2运行地址>                 # 通常是 memfd 基址 + 0x15a
continue
# 命中后：
info registers r14                     # r14 常为 OEP（原始入口点），记下来
dump binary memory unpacked.raw <解压目标段基址> <基址 + MemSiz>
```

- dump 长度 = 解压目标 LOAD 的 `MemSiz`（数十 MB 很正常）。
- 命中阶段 2 后 `r14` 若为 0 或明显越界，说明抓早了/抓晚了一次，重跑。
- **不确定性**：偏移 `0x15a`、用 `r14` 存 OEP 都是「本类样本的常见形态」，
  换样本要重新确认；写报告时按「实测值 + 实测依据」写，不要写成通用定律。

### 3.3 确定性

同一份样本连跑两次 dump，产出的 sha256 应当一致。若不一致，多半是
ASLR 没固定（`disable-randomization` 处于 off / 被 wrapper 覆盖）或 dump 起点算错，
先修流程再谈结论。

---

## 四、截断定稿判据（改版壳最集中的坑）

`dump binary memory` 出来的镜像是**整段内存**，尾部往往是零填充；直接当 ELF 用会
`file` 报 `too large section header offset`、`readelf` 报
`Reading N bytes extends past end of file for string table`。必须截断，且判据要写死：

```python
# 定稿判据（伪代码）
cut = max( [p_offset + p_filesz for ph in PT_LOAD],          # 所有 PT_LOAD 的文件尾
           [sh_offset + sh_size for sh in sections
            if sh_type not in (SHT_NULL, SHT_NOBITS)] )       # 所有**占文件空间**的节尾
```

三个必须注意的点（每一条都实测踩过）：

1. **不能只按 `PT_LOAD` 截**：改版壳把节数据放在文件尾部，按段截会切掉 `shstrtab` 等节，
   得到「文件更小但 readelf 报错」的半成品。
2. **不能用 `e_shoff + e_shnum*e_shentsize` 当上界**：加壳样本的 `e_shoff` 常只有几百字节
   （节头表被挪到**文件头部**），而这个乘积会算出一个小得离谱的 cut。
3. **必须排除 `SHT_NOBITS`（`.bss` 等）**：它们有 `sh_offset/sh_size` 却不占文件空间，
   计入会把 cut 撑到整个 dump（等于没截）。

截断后自检：

```bash
file <out>        # 期望：ELF 64-bit LSB executable, x86-64, statically linked, stripped
readelf -h <out>  # 期望：0 错误；Entry 值应等于 OEP
```

> 多次实测：满足上述判据时，cut 值在不同运行间稳定，产物 sha256 可复现。

---

## 五、四重验证（缺一不算解壳成功）

| # | 检查 | 通过判据 | 失败含义 |
|---|---|---|---|
| 1 | Entry 变化 | 新 Entry ≠ 壳 stub 入口，且 == 阶段 2 记录到的 OEP | 抓错阶段 / dump 起点错 |
| 2 | 非零率 | 解压后 > 60%；压缩态通常 < 1% | dump 了未解压的压缩区 |
| 3 | 编译器指纹 | 能读出 Go/Rust 版本串或 `.comment`，且与上游官方发布件一致 | 截断过早，只拿到头部 |
| 4 | 可执行性 | 隔离环境内 `./out -h`/`--version` 能打印版本或帮助（不连网、不写系统目录） | ELF 头/段表不一致 |

建议加第 5 条：**可复现**（两次 dump + 截断的 sha256 一致）。不可复现的产物不能作为
「这份二进制就是官方原版」的证据。

---

## 六、解壳之后做什么（把「能跑」变成「能判」）

1. **字符串全量提取**：不要用 GNU `strings` 判中文/中文文案——它只提 ASCII，会漏检。
   用 Python 自扫 UTF-8 游走，或 `grep -a`。**先统计命中总数，再看样例**：
   `grep ... | head -20` 只看到前几条就断言「无命中」，是本类审核最常见的假阴性。
2. **分类归因**：域名 / IP / `exec` / `eval` / 凭据模式分组，逐个说明用途（DNS 服务商、
   ACME CA、OAuth、镜像仓库、时间同步、公共 IP 查询……）。归不了因的才叫「未知外联」。
3. **运行时观测**（隔离环境）：`strace -f -e trace=network` 抓系统调用；
   要抓 DNS 名就把 53 端口重定向到本地记录器，**先自检仪表**（解析一个已知域名，确认
   被记录），用完立刻删除规则并复核无残留。
4. **前端单独抓**：`curl /` 取首页 → 抽出全部 `.js` → 搜域名。服务端「0 条 HTTP」
   **不等于**不联网：浏览器侧会直连第三方（IP 查询、推送、CDN）。
5. **结论写边界**：明确写出未覆盖项（长周期定时任务、条件触发分支、需要账号的实操路径、
   只测了哪些版本）。「启动 100 秒内无第一方遥测」≠「无遥测」。

---

## 七、纪律与红线

- **只在隔离环境执行**；原始样本与解壳产物都不放进生产目录。
- 解壳**不改变**「闭源」这一事实：得到的是汇编 / 伪代码，不是源码；控制流仍未逐条读懂。
  评分时「未知风险」维度可按「已解壳、可进一步分析」适度参考，但**不能因为解壳成功就上调等级**。
- 解壳只影响「混淆/反分析」维度的判断；恶意与否仍走 M1–M6 判定。
- 大文件（数十 MB）不必回传主仓库：留 **sha256 + 复现脚本 + 环境说明**即可，
  脚本能在数分钟内重建同一产物。

---

## 八、报告里要写的复现清单

```
样本            <文件名>  <字节数>  sha256:<...>
环境            <发行版> / gdb <版本> / RAM <...>
阶段 1 运行地址 0x... （文件偏移 0x... + 段2 VirtAddr 0x...）
阶段 2 运行地址 0x... （memfd 基址 + 0x...，本样本实测命中）
解压目标        MemSiz 0x...  → dump 0x... 字节
cut             0x...（判据：max(PT_LOAD 尾, 非 NOBITS 节尾)）
产物            <elf> sha256:<...> / memfd.bin sha256:<...> / strings.txt sha256:<...>
复现命令        sh unpack_tools/run.sh   （约 N 分钟）
```

以上字段齐全，别人才能在**不看你的机器**的情况下复核结论。

---

## 九、工具分工（谁干什么，别硬套）

| 工具 | 适合 | 不适合 / 注意 |
|---|---|---|
| `file` / `readelf` / `objdump` / `xxd` | 判型、看段表、反汇编 stub、读 PackHeader | 改头壳的 `upx -d` 一定失败，别反复试 |
| `gdb` | 两阶段动态 dump、取 OEP | 断点要用运行地址；关掉地址随机化固定 |
| 自写截断脚本 | 按定稿判据切 ELF | 必须排除 `SHT_NOBITS` |
| **REA**（`morluto/rea`） | 解壳后的下一步：反汇编/伪代码、符号与调用引用、Go 二进制模块表、JS/Electron 静态分析；由 Agent 通过 MCP 驱动 | 需 Node 22+，原生分析要挂 Hopper/Ghidra/IDA/Kuna 之一；见 [rea-toolkit.md](rea-toolkit.md) |
| `strings`（GNU） | 纯 ASCII 常量 | **漏 UTF-8 中文**，不能据此断言「没有某文案」 |
| `rizin` / `radare2` | 交互式看控制流 | 学习成本高，先有明确问题再上 |

---

*本方法来自真实样本的实测复现（含三次失败与两处文档勘误），所有主机名、IP、应用名、
样本哈希在写入本技能时已剔除；复现时请以你自己样本的实测值为准。*

