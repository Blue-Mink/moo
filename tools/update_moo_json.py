#!/usr/bin/env python3
"""update_moo_json.py — 一键维护仓库根目录的 moo.json（零依赖，Python 3.8+）。

你只需要：构建出新的 .fpk，然后

    python3 update_moo_json.py --version 1.2.3 --fpk dist/myapp_1.2.3_x86.fpk

脚本会自动：从 FPK 内 manifest 读 appname/platform → 算 sha256 与字节数 →
按 GitHub Release 约定拼下载直链 → 在 moo.json 的 releases 里加/更新版本节点 →
只保留最近 N 个版本（默认 2）→ 校验 JSON 并打印摘要。

常用参数：
  --dry-run            只打印改动，不写文件
  --download-url URL   直链不走 GitHub Release 约定时手工指定
  --sha256 HEX         不传 --fpk 时手工给校验和
  --size N             不传 --fpk 时手工给字节数
  --arch all|x86|arm   包架构（默认取 manifest.platform，缺失时 all）
  --changelog TEXT     本版更新日志（也可 --changelog-file CHANGELOG.md）
  --keep N             保留最近几个版本节点（默认 2，0=全部保留）
  --repo OWNER/REPO    覆盖仓库推断（默认读 git remote origin；也接受 Gitea/GitLab 完整地址）
  --tag-prefix v       Release tag 前缀（默认 v，即 v1.2.3）
  --tag TAG            完整 tag（tag 命名不规律时用，如 release-1.2.3）
  --allow-republish    允许同版本同架构换包（默认拒绝，防已缓存用户校验失败）
  --allow-downgrade    允许写入比索引里已有版本更旧的版本（默认拒绝，防索引被写回旧版）
  --sync-meta          把包内 manifest 的展示字段（显示名/简介/许可证/安装类型/最低系统）同步进索引
  --no-platform-sync   不把新架构写进应用级 platform（默认会增补）
  --allow-appname-mismatch  --appname 与包内 manifest.appname 不一致时仍写入（默认拒绝）

路径支持 ~ 与通配符（`--fpk 'dist/*.fpk'` 不展开也能用）；索引须为 UTF-8（BOM 自动去除，
CRLF 原样保留，Windows 记事本另存为 UTF-8 即可）。

安全与兜底：镜像/压缩包解析失败会给出明确错误而不是堆栈；写入走「临时文件 + 原子替换」，
中断不会留下半截 JSON；同一输入重复运行不改变字节（内容未变不刷新 updated_at）。
"""
import argparse, difflib, glob, hashlib, json, os, re, subprocess, sys, tarfile, urllib.parse
from datetime import datetime, timezone, timedelta
from functools import cmp_to_key

HEX64 = re.compile(r"^[0-9a-f]{64}$")


def die(msg):
    print(f"错误：{msg}", file=sys.stderr)
    sys.exit(1)


def _no_dup_pairs(pairs):
    """JSON 重复键会被标准库静默覆盖（后者胜），这里直接拒绝，避免悄悄丢数据。"""
    out = {}
    for k, v in pairs:
        if k in out:
            die(f"moo.json 里键 {k!r} 重复出现：JSON 会静默只保留最后一个，请先手工修正")
        out[k] = v
    return out


def _bad_constant(x):
    die(f"moo.json 里出现非法数值 {x}（JSON 标准不允许 NaN/Infinity，Moo 也解析不了）")


def detect_indent(text):
    """沿用原文件的缩进（本仓 moo.json 是 1 空格），避免整文件重排。"""
    for line in text.splitlines()[1:]:
        s = line.lstrip(" ")
        if s:
            return len(line) - len(s) or 2
    return 2


def write_atomic(path, text):
    """同目录临时文件 + fsync + 原子替换：写一半失败也不会毁掉原文件。"""
    tmp = f"{path}.tmp.{os.getpid()}"
    try:
        with open(tmp, "w", encoding="utf-8") as f:
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    except OSError as e:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        die(f"写入 {path} 失败（原文件未改动）：{e}")


def read_fpk(path):
    """从 FPK（tar.gz）里取 manifest，返回 (appname, version, platform, size, sha256)。"""
    if not os.path.exists(path):
        die(f"读 FPK 失败（{path}）：文件不存在（路径写错？或通配符没展开）")
    if not os.path.isfile(path):
        die(f"读 FPK 失败（{path}）：不是普通文件（是目录？）")
    try:
        size = os.path.getsize(path)
    except OSError as e:
        die(f"读 FPK 失败（{path}）：{e}（没有读权限？）")
    h = hashlib.sha256()
    try:
        with open(path, "rb") as f:
            for chunk in iter(lambda: f.read(1 << 20), b""):
                h.update(chunk)
    except OSError as e:
        die(f"读 FPK 失败（{path}）：{e}（没有读权限？）")
    info = {}
    try:
        with tarfile.open(path, "r:gz") as tf:
            name = next((n for n in tf.getnames() if n.strip("./").lower() == "manifest"), "")
            if not name:
                die(f"{path} 里没有 manifest（不是标准 FPK？）")
            m = tf.extractfile(name)
            for line in (m.read().decode("utf-8", "replace").splitlines() if m else []):
                if "=" in line and not line.strip().startswith("#"):
                    k, v = line.split("=", 1)
                    info[k.strip()] = v.strip()
    except SystemExit:
        raise
    except Exception as e:  # noqa: BLE001
        die(f"读 FPK 失败（{path}）：{e}")
    return (info.get("appname", ""), info.get("version", ""),
            info.get("platform", "all"), size, h.hexdigest(), info)


def normalize_repo(s):
    """接受 OWNER/REPO、https://github.com/OWNER/REPO(.git)、git@github.com:OWNER/REPO.git。"""
    s = (s or "").strip()
    if not s:
        return ""
    m = re.search(r"github\.com[:/]+([^/]+)/([^/#?]+?)(?:\.git)?/?$", s)
    if m:
        return f"{m.group(1)}/{m.group(2)}"
    return s.rstrip("/")


def git_repo():
    """读 origin。GitHub 返回 owner/repo（短式），其它托管返回完整 URL 基址。"""
    try:
        url = subprocess.run(["git", "remote", "get-url", "origin"],
                             capture_output=True, text=True, timeout=10).stdout.strip()
    except Exception:  # noqa: BLE001
        return ""
    if not url:
        return ""
    m = re.search(r"github\.com[:/]([^/]+)/([^/#?]+?)(?:\.git)?/?$", url)
    if m:
        return f"{m.group(1)}/{m.group(2)}"
    return release_base(url)


def release_base(repo):
    """仓库标识 → Release 下载基址（支持 GitHub / Gitea / GitLab / 通用 git 地址）。

    - Owner/Repo            → https://github.com/Owner/Repo
    - https://host/a/b(.git)→ https://host/a/b
    - git@host:a/b.git      → https://host/a/b
    - host/a/b（带点域名）   → https://host/a/b
    """
    s = (repo or "").strip().rstrip("/")
    if not s:
        return ""
    if s.startswith(("http://", "https://")):
        return re.sub(r"\.git$", "", s)
    if "://" in s:  # ssh://git@host/owner/repo
        s = s.split("://", 1)[1].split("@", 1)[-1]
        return "https://" + re.sub(r"\.git$", "", s)
    m = re.match(r"^[^@/]+@([^:/]+):(.+)$", s)  # git@host:owner/repo
    if m:
        return f"https://{m.group(1)}/" + re.sub(r"\.git$", "", m.group(2))
    if "/" in s and "." in s.split("/")[0]:
        return "https://" + re.sub(r"\.git$", "", s)
    return "https://github.com/" + re.sub(r"\.git$", "", s)


# 版本比较：严格移植 Moo 的 internal/source/version.go（ParseVersion/CompareVersion），
# 保证「裁剪保留哪几版」「顶层跟到哪一版」与客户端判定完全同源。
PRE_TAGS = {"rc", "beta", "alpha", "dev", "pre", "preview", "snapshot", "next"}
LABEL_PREFIXES = ("latest-", "release-", "stable-", "nightly-", "master-", "head-", "dev-")


def parse_version(raw):
    """返回 (core[], pre[], rev, ok)。ok=False 表示不可比（标签前缀/畸形）。"""
    s = (raw or "").strip().lower()
    if not s:
        return [], [], 0, False
    if s.startswith("v"):
        s = s[1:]
    for lp in LABEL_PREFIXES:
        if s.startswith(lp):
            return [], [], 0, False
    i, buf = 0, []
    while i < len(s):
        c = s[i]
        if c.isdigit() or c == ".":
            buf.append(c)
            i += 1
            continue
        if c == "_" and 0 < i < len(s) - 1 and s[i - 1].isdigit() and s[i + 1].isdigit():
            buf.append(".")
            i += 1
            continue
        break
    if not buf:
        return [], [], 0, False
    core = []
    for part in "".join(buf).split("."):
        if part == "" or not part.isdigit():
            return [], [], 0, False
        core.append(int(part))
    rest = s[i:]
    rest = rest[1:] if rest.startswith("-") else rest
    rest = rest[1:] if rest.startswith("_") else rest
    if not rest:
        return core, [], 0, True
    j = 0
    while j < len(rest) and rest[j].isascii() and rest[j].isalpha():
        j += 1
    if j > 0 and rest[:j] in PRE_TAGS:
        pre = [rest[:j]]
        for seg in re.split(r"[.\-_]", rest[j:].lstrip(".-_")):
            if seg:
                pre.append(seg)
        return core, pre, 0, True
    if rest.isdigit():
        return core, [], int(rest), True
    return core, [], 0, True


def compare_pre(a, b):
    for x, y in zip(a, b):
        xn, yn = (int(x) if x.isdigit() else None), (int(y) if y.isdigit() else None)
        if xn is not None and yn is not None:
            if xn != yn:
                return -1 if xn < yn else 1
        elif xn is not None:
            return -1
        elif yn is not None:
            return 1
        elif x != y:
            return -1 if x < y else 1
    if len(a) == len(b):
        return 0
    return -1 if len(a) < len(b) else 1


def compare_version(a, b):
    """与 Moo CompareVersion 同口径；任一侧不可解析返回 0（不可比）。"""
    ac, ap, ar, aok = parse_version(a)
    bc, bp, br, bok = parse_version(b)
    if not aok or not bok:
        return 0
    for i in range(max(len(ac), len(bc))):
        x = ac[i] if i < len(ac) else 0
        y = bc[i] if i < len(bc) else 0
        if x != y:
            return -1 if x < y else 1
    if not ap and bp:
        return 1
    if ap and not bp:
        return -1
    if ap:
        c = compare_pre(ap, bp)
        if c:
            return c
    if ar != br:
        return -1 if ar < br else 1
    return 0


def sorted_versions(versions):
    """可解析版本按 Moo 口径排序；不可解析的（latest-… 之类）一律视为最旧。"""
    good = [v for v in versions if parse_version(v)[3]]
    bad = [v for v in versions if not parse_version(v)[3]]
    return sorted(bad) + sorted(good, key=cmp_to_key(compare_version))


def normalize_arch(p):
    """manifest.platform / --arch → Moo 认识的三档（x86 / arm / all）。"""
    s = (p or "").strip().lower()
    if s in ("", "all", "any", "universal"):
        return "all"
    if s in ("x86", "x86_64", "amd64", "x64", "i386", "i686"):
        return "x86"
    if s in ("arm", "arm64", "aarch64", "armv7", "armv7l", "armhf"):
        return "arm"
    return s


def entry_platforms(entry):
    value = entry.get("platform")
    if isinstance(value, list):
        return [str(item).strip() for item in value if str(item).strip()]
    if isinstance(value, str) and value.strip():
        return [value.strip()]
    return []


def merge_platforms(entry, arches):
    """把新写入的架构补进应用级 platform；all 已是全集时不改。"""
    current = entry_platforms(entry)
    if "all" in current:
        return []
    added = [arch for arch in arches if arch not in current]
    if not current:
        added = list(arches)
    if not added:
        return []
    order = {"x86": 0, "arm": 1, "all": 2}
    merged = sorted(dict.fromkeys(current + added), key=lambda item: (order.get(item, 9), item))
    entry["platform"] = merged
    return merged


def main():
    p = argparse.ArgumentParser(description="一键更新 moo.json（Moo 应用源协议）")
    p.add_argument("--file", default="moo.json")
    p.add_argument("--appname", default="")
    p.add_argument("--version", default="")
    p.add_argument("--fpk", nargs="+", default=[], help="一个或多个 FPK（x86+arm 可一次传）")
    p.add_argument("--download-url", default="")
    p.add_argument("--sha256", default="")
    p.add_argument("--size", type=int, default=0)
    p.add_argument("--arch", default="")
    p.add_argument("--changelog", default="")
    p.add_argument("--changelog-file", default="")
    p.add_argument("--keep", type=int, default=2)
    p.add_argument("--repo", default="")
    p.add_argument("--tag-prefix", default="v")
    p.add_argument("--tag", default="",
                   help="完整 Release tag（默认 = --tag-prefix + 版本号；tag 命名不规律时用）")
    p.add_argument("--allow-republish", action="store_true",
                   help="允许同版本同架构覆盖不同 sha256 的包（默认拒绝，防已缓存用户校验失败）")
    p.add_argument("--allow-version-mismatch", action="store_true",
                   help="允许 --version 与包内 manifest.version 不一致（默认拒绝）")
    p.add_argument("--allow-appname-mismatch", action="store_true",
                   help="允许 --appname 与包内 manifest.appname 不一致（默认拒绝）")
    p.add_argument("--dry-run", action="store_true", help="只打印 diff，不写文件")
    p.add_argument("--check", action="store_true", help="只校验：文件是否已与预期一致（CI 用）")
    p.add_argument("--allow-downgrade", action="store_true",
                   help="允许写入比索引里已有版本更旧的版本（默认拒绝，防把索引写回旧版）")
    p.add_argument("--sync-meta", action="store_true",
                   help="把包内 manifest 的展示字段（display_name / desc / license / "
                        "install_type / min_fnos）同步进索引（默认不动这些字段）")
    p.add_argument("--no-platform-sync", action="store_true",
                   help="不把新架构写进应用级 platform（默认会增补）")
    a = p.parse_args()
    a.appname = (a.appname or "").strip()
    a.version = (a.version or "").strip()
    a.tag = (a.tag or "").strip()
    if a.keep < 0:
        die("--keep 不能为负数（0 = 全量保留）")

    a.file = os.path.expanduser(a.file)
    if not os.path.exists(a.file):
        die(f"找不到 {a.file}（请在仓库根目录运行，或用 --file 指定）")
    try:
        raw = open(a.file, "rb").read()
    except OSError as e:
        die(f"读不了 {a.file}：{e}（权限不足？或它是个目录？）")
    if raw.startswith(b"\xef\xbb\xbf"):
        raw = raw[3:]
        print("提示：moo.json 带 UTF-8 BOM，已自动去掉")
    try:
        text_in = raw.decode("utf-8")
    except UnicodeDecodeError as e:
        die(f"{a.file} 不是 UTF-8 编码（{e}）。Windows 记事本请「另存为 → UTF-8」后重试")
    try:
        doc = json.loads(text_in, object_pairs_hook=_no_dup_pairs, parse_constant=_bad_constant)
    except SystemExit:
        raise
    except Exception as e:  # noqa: BLE001
        die(f"{a.file} 不是合法 JSON：{e}")

    apps = doc.get("apps") or {}
    if not isinstance(apps, dict) or not apps:
        die("moo.json 里没有 apps（应为 {appname: {...}}）")

    fpk_paths = []
    for pat in (a.fpk or []):
        pth = os.path.expanduser(pat)
        if not os.path.exists(pth) and any(ch in pth for ch in "*?["):
            hits = sorted(glob.glob(pth))
            if not hits:
                die(f"--fpk 通配符没有匹配到任何文件：{pat}")
            fpk_paths.extend(hits)
        else:
            fpk_paths.append(pth)
    appname, mver = a.appname, ""
    pkgs = []  # [{arch, filename, size, sha256, url}]
    f_apps = set()
    meta = {}  # 包内 manifest 的展示类字段（供 --sync-meta 用）
    for path in fpk_paths:
        f_app, f_ver, f_plat, f_size, f_sha, f_info = read_fpk(path)
        if f_app:
            f_apps.add(f_app)
        appname = appname or f_app
        if mver and f_ver and f_ver != mver:
            die(f"多个 FPK 的 manifest.version 不一致：{mver} vs {f_ver}（{path}）")
        mver = mver or f_ver
        arch = normalize_arch(a.arch or f_plat)
        if arch not in ("x86", "arm", "all"):
            die(f"无法识别的架构 {a.arch or f_plat}（只支持 x86 / arm / all；"
                f"飞牛包请填 x86_64 / aarch64），否则会写出客户端不认识的 packages 键")
        if a.arch and normalize_arch(f_plat) != arch:
            print(f"警告：--arch={a.arch} 覆盖了包内 manifest.platform={f_plat}；"
                  f"确认这确实是 {arch} 包再用，否则用户会选错架包")
        if any(p["arch"] == arch for p in pkgs):
            die(f"多个 FPK 解析出同一架构 {arch}（{path}）——同架构只能有一个包。\n"
                f"常见原因：release 里同时有 full / lite 两个包，或同一版本重复上传。\n"
                f"请只传主包（CI 里把 FPK_GLOB 收窄，例如 '*-full.fpk'）")
        pkgs.append({"arch": arch, "filename": os.path.basename(path), "size": f_size, "sha256": f_sha})
        if not meta:
            meta = f_info  # 多包时以第一个包的 manifest 为准（appname/version 已在上方校验一致）
        print(f"FPK：{os.path.basename(path)} | appname={f_app} version={f_ver} "
              f"platform={f_plat} → {arch} | size={f_size} sha256={f_sha[:16]}…")
    if len(f_apps) > 1:
        die(f"多个 FPK 的 manifest.appname 不一致：{', '.join(sorted(f_apps))}——"
            f"一次只能更新一个应用，请分开调用")
    if a.appname and f_apps:
        by_lower = {x.lower(): x for x in f_apps}
        if a.appname in f_apps:
            appname = a.appname
        elif a.appname.lower() in by_lower:
            canon = by_lower[a.appname.lower()]
            print(f"提示：--appname {a.appname!r} 与包内 {canon!r} 仅大小写不同，按包内写法 {canon!r}")
            appname = canon
        elif not a.allow_appname_mismatch:
            die(f"--appname（{a.appname}）与包内 manifest.appname（{next(iter(f_apps))}）不一致。\n"
                f"索引键必须等于包内 appname，否则会把包写到错的应用条目下；\n"
                f"确认要这样写请加 --allow-appname-mismatch")
        else:
            print(f"警告：--appname（{a.appname}）与包内 manifest.appname（{next(iter(f_apps))}）"
                  f"不一致，已按 --allow-appname-mismatch 写入")
    if not appname:
        if len(apps) == 1:
            appname = next(iter(apps))
        else:
            die("apps 里有多条应用，请用 --appname 指定要更新哪一条")

    version = a.version or mver
    if not version:
        die("缺少版本号：用 --version，或 --fpk 指向含 manifest.version 的包")
    if a.version and mver and a.version != mver and not a.allow_version_mismatch:
        die(f"--version（{a.version}）与包内 manifest.version（{mver}）不一致。\n"
            f"索引版本必须与包内版本一致，否则用户更新判断会错乱；\n"
            f"确认要这样写请加 --allow-version-mismatch")

    if a.download_url:
        if len(pkgs) > 1:
            die("--download-url 只能用于单个包；多架构请交给脚本按 Release 约定推导")
        sha_in = (a.sha256 or (pkgs[0]["sha256"] if pkgs else "")).strip().lower()
        size_in = a.size or (pkgs[0]["size"] if pkgs else 0)
        if not HEX64.match(sha_in or ""):
            die("--download-url 模式下请同时给 --sha256（或加 --fpk 让脚本自己算）")
        if not size_in:
            die("--download-url 模式下请同时给 --size（或加 --fpk 让脚本自己算）")
        arch = normalize_arch(a.arch or (pkgs[0]["arch"] if pkgs else "all"))
        pkgs = [{"arch": arch, "filename": "", "size": size_in, "sha256": sha_in, "url": a.download_url}]
    else:
        if not pkgs:
            die("推导下载直链需要 --fpk 以取得文件名（或直接给 --download-url）")
        repo = normalize_repo(a.repo) if a.repo else git_repo()
        base = release_base(repo)
        if not base:
            die("无法推断下载直链：请传 --download-url，或 --repo OWNER/REPO，或在 git 仓库里运行")
        tag = a.tag
        if not tag:
            if a.tag_prefix and version.lower().startswith(a.tag_prefix.lower()):
                tag = version  # 版本号自带 v 前缀时别拼成 vv1.2.3
            else:
                tag = f"{a.tag_prefix}{version}" if a.tag_prefix else version
        if re.search(r"\s", tag):
            die(f"--tag 里不能有空白字符：{tag!r}")
        for p in pkgs:
            p["url"] = (f"{base}/releases/download/{tag}/"
                        f"{urllib.parse.quote(p['filename'])}")

    for p in pkgs:
        if urllib.parse.urlparse(p["url"]).scheme not in ("http", "https"):
            die(f"下载直链必须是 http(s)：{p['url']}（Moo 只认 http/https）")

    changelog = a.changelog
    if not changelog and a.changelog_file:
        try:
            txt = open(os.path.expanduser(a.changelog_file), encoding="utf-8").read()
            changelog = txt.strip()[:4000]
        except OSError as e:
            die(f"读 --changelog-file 失败：{e}")

    # 大小写不敏感地沿用已有应用键：避免「Moo」与「moo」被写成两条重复应用
    cands = [k for k in apps if isinstance(k, str) and k.lower() == appname.lower()]
    if len(cands) > 1:
        die(f"apps 里有多个仅大小写不同的键 {cands}，请先合并后再更新")
    if appname not in apps and len(cands) == 1:
        print(f"提示：索引里已有键 {cands[0]!r}，与包内 appname {appname!r} 仅大小写不同，沿用已有键")
        appname = cands[0]
    entry = apps.setdefault(appname, {})
    releases = entry.setdefault("releases", {})
    if not isinstance(releases, dict):
        die("apps.%s.releases 不是对象" % appname)

    # 防回溯：索引里已有更高版本时，默认拒绝把索引写回旧版本。
    # 触发场景：手动 workflow_dispatch 传了旧 tag、给旧分支补发版、脚本被重放。
    # 后果：旧节点被重新加回 releases，顶层跟版虽取最大值不降级，但索引会「长回去」。
    # 需要时用 --allow-downgrade（例如确实要给老版本线补一次包）。
    if releases and not a.allow_downgrade and parse_version(version)[3]:
        comparable = [v for v in releases if isinstance(v, str) and parse_version(v)[3]]
        if comparable:
            cur_max = max(comparable, key=cmp_to_key(compare_version))
            if compare_version(version, cur_max) < 0:
                die(f"索引里已有更高版本 {cur_max}，本次要写的是 {version}（更旧）。\n"
                    f"把索引写回旧版本会让已升级的用户看不到更新；\n"
                    f"确实要给旧分支 / 旧 tag 补发版，请加 --allow-downgrade。")

    node = releases.setdefault(version, {})
    if not isinstance(node, dict):
        die(f"apps.{appname}.releases.{version} 不是对象")
    node_pkgs = node.setdefault("packages", {})
    if not isinstance(node_pkgs, dict):
        die(f"apps.{appname}.releases.{version}.packages 不是对象")
    prev_sig = json.dumps(node, sort_keys=True, ensure_ascii=False)
    for p in pkgs:
        old = node_pkgs.get(p["arch"])
        if (isinstance(old, dict) and old.get("sha256")
                and str(old["sha256"]).lower() != p["sha256"] and not a.allow_republish):
            die(f"版本 {version} 的 {p['arch']} 包已存在且 sha256 不同：\n"
                f"  旧 {str(old['sha256'])[:16]}…  新 {p['sha256'][:16]}…\n"
                f"同版本静默换包会让已缓存校验和的用户安装失败；请递增版本号，或加 --allow-republish")
        pk = node_pkgs.get(p["arch"])
        if not isinstance(pk, dict):
            pk = {}
        pk.update({"download_url": p["url"], "sha256": p["sha256"],
                   "size": p["size"], "size_mb": round(p["size"] / 1048576, 2)})
        node_pkgs[p["arch"]] = pk
    if not a.no_platform_sync:
        merged_platform = merge_platforms(entry, [p["arch"] for p in pkgs])
        if merged_platform:
            print(f"应用 platform 增补：{', '.join(merged_platform)}")
    # --sync-meta（可选）：把包内 manifest 的展示字段同步进索引。
    # 默认不动这些字段——它们常被作者手工润色过；只有显式加参数才覆盖。
    if a.sync_meta:
        if not meta:
            print("提示：--sync-meta 需要 --fpk（从包内 manifest 取值），本次跳过")
        else:
            for src_key, dst_key in (("display_name", "display_name"), ("desc", "desc"),
                                     ("license", "license"), ("install_type", "install_type"),
                                     ("os_min_version", "min_fnos")):
                val = (meta.get(src_key) or "").strip()
                if not val:
                    continue
                if src_key == "desc" and '"""' in val:
                    print("提示：manifest.desc 是多行块（含三引号），脚本只能读到首行片段，"
                          "为避免写坏简介已跳过 desc 同步")
                    continue
                old_val = entry.get(dst_key)
                if old_val == val:
                    continue
                entry[dst_key] = val
                print(f"同步字段 {dst_key}：{str(old_val)[:36]!r} → {val[:36]!r}")

    node["changelog"] = changelog or node.get("changelog", "")
    # 幂等：内容没变就不刷新 updated_at（重复运行不产生无意义 diff / 假更新）
    if not node.get("updated_at") or json.dumps(node, sort_keys=True, ensure_ascii=False) != prev_sig:
        node["updated_at"] = datetime.now(timezone(timedelta(hours=8))).isoformat(timespec="seconds")

    # 裁剪：只保留最近 N 版，但**永不裁掉本次刚写入的版本**
    if a.keep > 0 and len(releases) > a.keep:
        keep = set(sorted_versions(releases)[-a.keep:]) | {version}
        for v in list(releases):
            if v not in keep:
                releases.pop(v, None)
                print(f"裁剪旧版本节点：{v}")

    # 顶层便捷字段跟版（协议允许省略；存在则必须与最新版一致，否则卡片显示旧版本）
    latest = sorted_versions(releases)[-1] if releases else version
    lnode = releases.get(latest) or {}
    lp = None
    for arch in ("x86", "all", "arm"):
        lp = (lnode.get("packages") or {}).get(arch)
        if lp:
            break
    if lp is None and (lnode.get("packages") or {}):
        lp = next(iter((lnode.get("packages") or {}).values()))
    if lp and ("version" in entry or "download_url" in entry):
        entry["version"] = latest
        entry["download_url"] = lp["download_url"]
        entry["sha256"] = lp["sha256"]
        if "size_bytes" in entry:
            entry["size_bytes"] = lp["size"]
        if "size_mb" in entry:
            entry["size_mb"] = round(lp["size"] / 1048576, 2)
        if "size" in entry:
            # 跟随现有写法：>=1024 视为字节（真实索引即如此），否则按协议 MB
            try:
                prev_size = float(entry["size"])
            except (TypeError, ValueError):
                prev_size = 0.0
            entry["size"] = lp["size"] if prev_size >= 1024 else round(lp["size"] / 1e6, 3)

    # 自检：每条应用至少要有一个可解析的下载地址 + 合法 sha256
    for name, e in apps.items():
        dl = e.get("download_url") or ""
        rel = e.get("releases") or {}
        if not dl and not rel:
            die(f"应用 {name} 既没有 download_url 也没有 releases")
        if e.get("sha256") and not HEX64.match(e["sha256"]):
            die(f"应用 {name} 的 sha256 不是 64 位十六进制")
        for ver, nd in (rel or {}).items():
            if not isinstance(nd, dict) or not (nd.get("packages") or {}):
                die(f"应用 {name} 的 releases.{ver} 没有 packages（每个版本至少要有一个架构包）")
            for ar, pk in nd["packages"].items():
                if not pk.get("download_url"):
                    die(f"应用 {name} 的 releases.{ver}.packages.{ar} 缺 download_url")
                if pk.get("sha256") and not HEX64.match(pk["sha256"]):
                    die(f"应用 {name} 的 releases.{ver}.packages.{ar} 的 sha256 不是 64 位十六进制")

    out = json.dumps(doc, ensure_ascii=False, indent=detect_indent(text_in))
    if b"\r\n" in raw:  # Windows 换行：保持 CRLF，避免整文件 diff
        out = out.replace("\n", "\r\n")
    if raw.endswith(b"\n"):  # 保持原文件的行尾习惯（有的索引末尾没有换行）
        out += "\r\n" if b"\r\n" in raw else "\n"
    print(f"\n应用：{appname}  版本：{version}  新写入架构：{', '.join(p['arch'] for p in pkgs)}")
    for p in pkgs:
        print(f"  [{p['arch']}] {p['url']}")
        print(f"         {p['size']} 字节（{p['size']/1e6:.3f} MB）  sha256={p['sha256']}")
    print(f"当前版本节点：{', '.join(sorted_versions(releases))}（顶层跟版：{latest}）")

    old_text = text_in
    diff = list(difflib.unified_diff(old_text.splitlines(True), out.splitlines(True),
                                     fromfile=a.file + "（当前）", tofile=a.file + "（更新后）"))
    changed = bool(diff)

    if a.check:
        sys.stdout.writelines(diff)
        print("CHECK：内容一致 OK" if not changed else "CHECK：与预期不一致（需重新生成）")
        sys.exit(0 if not changed else 1)

    if a.dry_run or not changed:
        sys.stdout.writelines(diff)
        print("（--dry-run：未写入）" if a.dry_run else "无变化，无需写入")
        return

    sys.stdout.writelines(diff)
    write_atomic(os.path.realpath(a.file), out)

    # 回读校验：确保写进去的就是算出来的（防半截写/编码问题）
    back = json.loads(open(a.file, encoding="utf-8").read())
    for p in pkgs:
        got = back["apps"][appname]["releases"][version]["packages"][p["arch"]]
        if (got["sha256"], got["size"], got["download_url"]) != (p["sha256"], p["size"], p["url"]):
            die(f"回读校验失败：{p['arch']} 包写入后与预期不一致")
    print(f"已写入 {a.file}（回读校验通过）")


if __name__ == "__main__":
    main()
