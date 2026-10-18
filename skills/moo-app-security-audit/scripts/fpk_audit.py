#!/usr/bin/env python3
"""Moo 应用 FPK 安全审计脚本（解包分析）。
用法：python3 fpk_audit.py <fpk文件或目录>
输出：manifest 权限 + cmd 回调脚本危险模式 + docker-compose 危险配置 + 二进制危险字符串。
只读分析，不运行任何东西。"""
import os, re, sys, tarfile, io, gzip, subprocess, tempfile, shutil, struct

# ── 危险模式正则 ──────────────────────────────────────────────
SHELL_DANGER = [
    # 恶意执行 / 下载即执行
    ("下载执行",      r"curl[^\n|]*\|\s*(sh|bash)|wget[^\n|]*\|\s*(sh|bash)"),
    ("反弹shell",     r"/dev/tcp/|nc\s+-[e]|\bbash\s+-i\b"),
    ("挖矿",          r"stratum\+tcp|xmrig|minerd|cpuminer"),
    ("编码混淆执行",  r"base64[^\n|]*\|\s*(sh|bash|base64\s+-d)"),
    ("eval(需核验)",  r"eval\s+"),
    # 木马 / 后门 / 隐蔽通道
    ("木马后门",      r"authorized_keys|socat[^\n]*EXEC|nc\s+-l|ncat\s+-l|useradd\s+-|/etc/sudoers"),
    ("隧道工具(需核验用途)", r"\bfrpc\b|cloudflared[^\n]*tunnel|ngrok"),
    # 数据破坏（M4）：删除/覆盖/加密用户数据
    ("数据破坏",      r"rm\s+-rf\s+/(?!dev/null)|find\s+/\s+.*-delete|shred\s+|truncate\s+-s\s*0"
                     r"|dd\s+if=/dev/zero|mkfs\.|wipefs|cryptsetup|DROP\s+TABLE|DELETE\s+FROM"),
    ("未加引号删除",  r"rm\s+-rf\s+[^\n\"']*\$"),
    # 数据窃取 / 反取证
    ("数据窃取",      r"/etc/shadow|/root/\.ssh|/etc/passwd|\.aws/credentials|Cookies"),
    ("反取证",        r"history\s+-c|unset\s+HISTFILE|rm\s+-f\s+/var/log|journalctl\s+--vacuum|timestomp"),
    ("持久化",        r"crontab|systemctl\s+enable|rc\.local"),
    ("权限扩大",      r"chmod\s+777|chmod\s+\+s|setuid"),
]

# 二进制字符串里的恶意行为特征（M1/M2/M4）
BIN_MALWARE = [
    ("挖矿",     r"stratum\+tcp|xmrig|minerd|cpuminer|cryptonight"),
    ("勒索",     r"ransomware|your files have been encrypted|decrypt.{0,40}(bitcoin|btc|monero)"
                 r"|\.(locked|encrypted)\b.{0,20}(bitcoin|pay)"),
    ("远控",     r"meterpreter|cobalt\s?strike|beacon\.dll|\bsliver\s+(agent|implant)|powershell\s+-enc"),
    ("数据销毁", r"shred\s+-[a-z]*\s+/|wipefs\s+/|dd\s+if=/dev/(zero|urandom)\s+of=/"),
]

CRED_RE = re.compile(
    r"(ghp_[A-Za-z0-9]{20,}|sk-[A-Za-z0-9]{20,}|-----BEGIN (RSA|OPENSSH|EC) PRIVATE KEY-----"
    r"|api[_-]?key\s*[:=]\s*['\"][A-Za-z0-9]{16,}|(password|passwd|token|secret)\s*[:=]\s*['\"][^\s'\"]{8,})",
    re.I)
IP_RE = re.compile(r"(?:192\.168\.[0-9]+\.[0-9]+|10\.\d+\.\d+\.\d+|172\.(?:1[6-9]|2\d|3[01])\.\d+\.\d+)")

DOCKER_DANGER = [
    ("privileged",     r"privileged:\s*true"),
    ("挂载根/敏感",    r"(-|/)\s*/\s*:/|docker\.sock|/etc[/:]|/var/run"),
    ("host网络",       r"network_mode:\s*host"),
    ("全cap",          r"cap_add:\s*\[?.*(ALL|SYS_ADMIN)"),
]


def read_text(data):
    for enc in ("utf-8", "latin-1"):
        try:
            return data.decode(enc)
        except Exception:
            continue
    return data.decode("utf-8", errors="replace")


def scan_shell(name, text):
    """返回 [(标签, 命中片段)]；片段只取首个匹配，便于人工核验误报。"""
    hits = []
    for label, pat in SHELL_DANGER:
        m = re.search(pat, text, re.I | re.M)
        if m:
            snippet = " ".join(m.group(0).split())[:80]
            hits.append((label, snippet))
    # 凭据检测 + PEM 头误报排除（多个 BEGIN 连排 = 密钥格式识别串，非真实私钥）
    cred_m = CRED_RE.search(text)
    if cred_m:
        g = cred_m.group(0)
        left = g.split("=")[0].lower()
        if g.startswith("-----BEGIN") and text.count("-----BEGIN") > 1:
            hits.append(("PEM头常量(误报)", g[:40]))
        elif "${" in g or re.search(r"[:=]\s*['\"]?\$", g):
            # SECRET="${FOO:-}" 这类环境变量取值不是硬编码凭据
            hits.append(("变量取值(非硬编码)", g[:60]))
        elif "webhook" in left or "secret" in left or "token" in left or "password" in left:
            hits.append(("凭据字段(需核验是否真实值)", g[:60]))
        else:
            hits.append(("硬编码凭据", g[:60]))
    # 内网 IP 检测 + 私网网段定义误报排除（.0 结尾 = RFC1918 网段，非真实主机）
    if IP_RE.search(text):
        hosts = [ip for ip in IP_RE.findall(text)
                 if not ip.endswith(".0") and not ip.endswith(".0.0")]
        hits.append(("内网IP", ",".join(hosts[:3])) if hosts else ("私网网段定义(误报)", ""))
    return hits


def scan_malware_blob(text):
    """二进制/大文本里的恶意行为特征（返回 [(标签, 片段)]）。

    WAF / 杀毒 / 扫描器类产品会内嵌恶意关键词库（xss/sqli/ransomware…），
    命中「勒索 / 远控」词时若同段还有大量安全关键词，标注为疑似关键词库，
    必须在报告里人工核验，不能直接判恶意。
    """
    lower = text.lower()
    sec_kw = ("xss", "sqli", "csrf", "waf", "injection", "webshell", "ransomware", "botnet")
    waf_like = sum(1 for k in sec_kw if k in lower) >= 4
    out = []
    for label, pat in BIN_MALWARE:
        m = re.search(pat, text, re.I)
        if m:
            if waf_like and label in ("勒索", "远控"):
                label += "(疑似安全关键词库)"
            out.append((label, " ".join(m.group(0).split())[:60]))
    return out




def detect_packing(data):
    """检测二进制加壳/混淆（对应「混淆/反分析」维度）。返回描述或 None。"""
    if len(data) < 0x40 or data[:4] != b"\x7fELF":
        return None
    try:
        is64 = data[4] == 2  # ELFCLASS64
        if is64:
            shoff = struct.unpack("<Q", data[0x28:0x30])[0]
            shnum = struct.unpack("<H", data[0x3C:0x3E])[0]
        else:
            shoff = struct.unpack("<I", data[0x20:0x24])[0]
            shnum = struct.unpack("<H", data[0x30:0x32])[0]
    except Exception:
        return None
    if b"UPX!" in data or b"upx.sf.net" in data:
        return "UPX 加壳"
    if shoff == 0 and shnum == 0:
        return "加壳/无符号(no section header)"
    return None


# 已知无害 URL 前缀（回环 / 协议 namespace / UPX 标识），外联检测时过滤
KNOWN_BENIGN_URL = (
    "http://127.0.0.1", "http://localhost", "http://0.0.0.0",
    "http://www.w3.org", "https://www.w3.org",
    "http://schemas.xmlsoap.org", "https://schemas.xmlsoap.org",
    "http://upx.sf.net",
)


def detect_urls(data):
    """提取二进制/脚本里的 URL（对应「数据隐私/外联」维度），过滤已知无害前缀。"""
    text = read_text(data)
    urls = set(re.findall(r"https?://[a-zA-Z0-9./_%?=&-]+", text))
    return sorted(u for u in urls if not u.startswith(KNOWN_BENIGN_URL))


def extract_fpk(fpk):
    """解包 FPK（tar.gz），返回 {路径: 内容}。"""
    out = {}
    with tarfile.open(fpk, "r:gz") as tf:
        for m in tf.getmembers():
            if not m.isfile():
                continue
            f = tf.extractfile(m)
            if f is None:
                continue
            out[m.name] = f.read()
    return out


def audit_fpk(fpk):
    print(f"\n========== {os.path.basename(fpk)} ==========")
    files = extract_fpk(fpk)

    # manifest 权限（config/privilege）
    priv = files.get("config/privilege") or files.get("config/privilege.json")
    if priv:
        t = read_text(priv)
        run_as = re.search(r'run-as["\s:]+([^"},\s]+)', t)
        print(f"  [manifest] run-as = {run_as.group(1) if run_as else '?'}  {'⚠️ root' if run_as and run_as.group(1)=='root' else ''}")

    # cmd 回调脚本
    cmds = {k: v for k, v in files.items() if k.startswith("cmd/")}
    for name, data in sorted(cmds.items()):
        t = read_text(data)
        hits = scan_shell(name, t)
        if hits:
            detail = "; ".join(f"{lbl}={snip}" if snip else lbl for lbl, snip in hits)
            print(f"  [cmd/{name.split('/')[1]}] ⚠️ {detail}")
        else:
            print(f"  [cmd/{name.split('/')[1]}]   ✅")


    # docker-compose
    dc = None
    for k, v in files.items():
        if "docker-compose" in k and k.endswith((".yaml", ".yml")):
            dc = (k, read_text(v))
            break
    if dc:
        k, t = dc
        hits = [lbl for lbl, pat in DOCKER_DANGER if re.search(pat, t, re.I)]
        print(f"  [docker-compose] {'⚠️ ' + ', '.join(hits) if hits else '✅ 无明显危险配置'}")

    # 二进制（app.tgz 内的 server 或大文件）
    app_tgz = files.get("app.tgz")
    if app_tgz:
        try:
            with tarfile.open(fileobj=io.BytesIO(app_tgz), mode="r:gz") as af:
                for m in af.getmembers():
                    if not m.isfile():
                        continue
                    data = af.extractfile(m).read()
                    if len(data) < 100_000:
                        continue
                    # 只扫可执行大文件
                    if m.name.endswith((".json", ".png", ".jpg", ".map")):
                        continue
                    text = read_text(data)
                    cred = CRED_RE.findall(text)
                    # 内网 IP 排除私网网段定义
                    ips = set(ip for ip in IP_RE.findall(text)
                              if not ip.endswith(".0") and not ip.endswith(".0.0"))
                    malware = scan_malware_blob(text)
                    packed = detect_packing(data)
                    urls = detect_urls(data)
                    flags = []
                    for lbl, snip in malware:
                        flags.append(f"恶意特征[{lbl}:{snip}]")
                    if cred:
                        flags.append(f"凭据{len(cred)}")
                    if ips:
                        flags.append(f"内网IP{len(ips)}")
                    if packed:
                        flags.append(packed)
                    if urls:
                        shown = ",".join(urls[:5]) + ("…" if len(urls) > 5 else "")
                        flags.append(f"外联{len(urls)}:{shown}")
                    if flags:
                        print(f"  [binary/{m.name}] ⚠️ " + " ".join(flags))
        except Exception as e:
            print(f"  [binary] 解包失败: {e}")

    # manifest 字段
    mf = files.get("manifest")
    if mf:
        t = read_text(mf)
        for field in ("appname", "version", "install_type", "service_port"):
            m = re.search(rf"{field}\s*=\s*(.+)", t)
            if m:
                print(f"  [manifest] {field} = {m.group(1).strip()}")


if __name__ == "__main__":
    targets = sys.argv[1:]
    if not targets:
        print("用法: python3 fpk_audit.py <fpk文件或目录>")
        sys.exit(1)
    for tgt in targets:
        if os.path.isdir(tgt):
            for f in sorted(os.listdir(tgt)):
                if f.endswith(".fpk"):
                    audit_fpk(os.path.join(tgt, f))
        elif os.path.isfile(tgt):
            audit_fpk(tgt)
        else:
            print(f"!! 不存在: {tgt}")
