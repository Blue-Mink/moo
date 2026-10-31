#!/usr/bin/env python3
"""校验 Moo release 资产与索引一致（只读：不构建、不上传、不覆盖）。

判定项（任一不符 → FAIL，退出 1）：
  1 TAG 形如 vX.Y.Z
  2 fnos/manifest 的 version == TAG
  3 moo.json apps.moo.version == TAG          （索引跟版纪律）
  4 apps.moo.releases[TAG].packages 存在且非空
  5 每个架构的 FPK / FPK.sha256 / FPK.minisig（>=0.6.329 必需）资产在场
  6 资产 size == packages[arch].size
  7 资产 digest == sha256:packages[arch].sha256（服务端无 digest 时仅 warn）
  8 apps.moo 顶层 download_url / sha256 与 packages 首个架构一致
warn（不 FAIL）：releases 保留超过两版

职责边界（RELEASE_GATE S2）：本脚本只负责**索引 ↔ 资产枚举**。
「客户端同款验收」（.sha256 侧车内容比对 / minisign 验签 / 解包核 manifest 版本、
平台、ELF 架构、前端 bundle 名）的唯一实现在 Go 侧 `internal/release`，
由 `moo-release` 二进制执行：
  --local-dir 模式：索引侧断言通过后调用 `moo-release --local-dir`（不联网）
  远程模式：枚举/自愈后追加 `moo-release --json`，通道表格写入 step summary
二进制路径取环境变量 MOO_RELEASE_BIN，缺省 ./moo-release。

用法：
  GH_TOKEN=... REPO=owner/repo TAG=v0.6.377 python3 .github/scripts/verify_moo_release.py
  python3 .github/scripts/verify_moo_release.py --local-dir .   （发布机本地核）
  加 --skip-api 只做本地校验（自测用）
"""
import hashlib
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request

MINISIG_FROM = (0, 6, 329)


def fail(msg):
    print("FAIL: " + msg)
    sys.exit(1)


def warn(msg):
    print("[warn] " + msg)


def ver_tuple(v):
    try:
        return tuple(int(x) for x in v.lstrip("v").split(".")[:3])
    except Exception:
        return (0, 0, 0)


def api_get(path):
    repo = os.environ.get("REPO") or fail("缺 env REPO")
    tok = os.environ.get("GH_TOKEN", "")
    hdr = {"Accept": "application/vnd.github+json", "User-Agent": "moo-verify"}
    if tok:
        hdr["Authorization"] = "Bearer " + tok
    req = urllib.request.Request("https://api.github.com/repos/" + repo + path, headers=hdr)
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        body = ""
        try:
            body = e.read().decode("utf-8", "replace")[:200]
        except Exception:
            pass
        fail("GitHub API HTTP %d（%s）：%s" % (e.code, path, body))
    except Exception as e:
        fail("GitHub API 请求失败（%s）：%s" % (path, e))



def arg_value(flag):
    args = sys.argv[1:]
    for i, a in enumerate(args):
        if a == flag and i + 1 < len(args):
            return args[i + 1]
        if a.startswith(flag + "="):
            return a.split("=", 1)[1]
    return ""


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def check_local_dir(d, pkgs, ver, need_minisig):
    """索引侧预检（不联网）：本地 FPK 与 moo.json 索引是否一致。

    只做「索引 ↔ 文件」的枚举与对账，**不**在这里实现客户端同款规则
    （侧车内容比对 / minisig 验签 / 解包核 manifest）。那部分是唯一的 Go 实现
    `internal/release.VerifyLocal`，由 moo-release --local-dir 跑（见 run_go_local）。
    """
    bad = []
    d = os.path.expanduser(d)
    if not os.path.isdir(d):
        return ["本地目录不存在：%s" % d]
    for arch, spec in sorted(pkgs.items()):
        spec = spec or {}
        name = spec.get("filename") or ("moo_%s_%s.fpk" % (ver, arch))
        p = os.path.join(d, name)
        if not os.path.isfile(p):
            bad.append("%s: 本地缺 %s" % (arch, name))
            continue
        size = os.path.getsize(p)
        if spec.get("size") and size != spec["size"]:
            bad.append("%s: 本地 size %s != 索引 %s" % (arch, size, spec["size"]))
        got = sha256_file(p)
        if spec.get("sha256") and got != spec["sha256"]:
            bad.append("%s: 本地 sha256 %s… != 索引 %s…" % (arch, got[:12], spec["sha256"][:12]))
        # 侧车「在场」是索引侧断言；其**内容**归 Go 校验
        if not os.path.isfile(p + ".sha256"):
            bad.append("%s: 本地缺侧车 %s.sha256" % (arch, name))
        if need_minisig and not os.path.isfile(p + ".minisig"):
            bad.append("%s: 本地缺签名 %s.minisig" % (arch, name))
    return bad


def release_bin():
    """定位 moo-release 可执行文件（客户端同款验收的唯一实现）。"""
    p = os.environ.get("MOO_RELEASE_BIN", "").strip() or "./moo-release"
    p = os.path.expanduser(p)
    if os.path.isfile(p) and os.access(p, os.X_OK):
        return p
    return ""


def run_go_local(tag, archs, d):
    """调用 Go 二进制做客户端同款验收；返回 (ok, 失败说明列表)。"""
    b = release_bin()
    if not b:
        fail("找不到 moo-release（MOO_RELEASE_BIN=%r）。请先 "
             "`go build -o moo-release ./cmd/moo-release`" % os.environ.get("MOO_RELEASE_BIN", ""))
    bad = []
    for arch in sorted(archs):
        cmd = [b, "--tag", tag.lstrip("v"), "--arch", arch, "--local-dir", d]
        try:
            r = subprocess.run(cmd, capture_output=True, text=True, timeout=600)
        except Exception as e:
            bad.append("%s: 无法执行 moo-release：%s" % (arch, e))
            continue
        out = (r.stdout or "").strip() + (("\n" + r.stderr.strip()) if r.stderr else "")
        print("  [moo-release --arch %s] exit=%d %s" % (arch, r.returncode, out.splitlines()[-1] if out else ""))
        if r.returncode != 0:
            bad.append("%s: 客户端同款验收失败（exit=%d）：%s" % (arch, r.returncode, out))
    return (not bad), bad


def run_go_remote(tag, arch):
    """远程模式追加：调用 Go 做五通道探测 + 客户端同款验收，结果写入 step summary。

    解析失败不影响索引侧结论——本函数只在 Go 侧失败时返回 False 供调用方决定退出码。
    """
    b = release_bin()
    if not b:
        warn("未设置 MOO_RELEASE_BIN，跳过通道探测与客户端同款验收")
        return True
    cmd = [b, "--tag", tag.lstrip("v"), "--arch", arch, "--json", "--timeout", "30"]
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=1800)
    except Exception as e:
        warn("moo-release 执行失败：%s" % e)
        return False
    payload = None
    try:
        payload = json.loads(r.stdout or "{}")
    except Exception as e:
        warn("moo-release JSON 解析失败（%s），原始输出前 400 字：%s" % (e, (r.stdout or "")[:400]))
    if payload:
        append_channel_summary(tag, arch, payload)
    if r.returncode != 0:
        print("FAIL: moo-release --arch %s exit=%d（见 step summary）" % (arch, r.returncode))
        return False
    print("✓ moo-release --arch %s：至少一条通道通过客户端同款验收" % arch)
    return True


def append_channel_summary(tag, arch, payload):
    p = os.environ.get("GITHUB_STEP_SUMMARY")
    if not p:
        return
    lines = ["", "### 通道探测 / 客户端同款验收 `%s`（arch=%s）" % (tag, arch), ""]
    lines.append("| 通道 | 可达 | 验收 | 文件 | 说明 |")
    lines.append("|---|---|---|---|---|")
    for r in payload.get("results") or []:
        if r.get("verified"):
            v = "✅ 通过"
        elif r.get("error"):
            v = "❌ 失败"
        else:
            v = "—"
        art = r.get("artifact") or {}
        f = ("%s (%s B)" % (art.get("path", "").rsplit("/", 1)[-1], art.get("size"))) if art else ""
        msg = (r.get("error") or r.get("note") or "").replace("|", "\\|")[:160]
        lines.append("| %s | %s | %s | %s | %s |"
                     % (r.get("channel", ""), "✅" if r.get("ok") else "❌", v, f, msg))
    try:
        with open(p, "a", encoding="utf-8") as fh:
            fh.write("\n".join(lines) + "\n")
    except OSError:
        pass


def upload_asset(upload_url, name, data):
    base = upload_url.split("{")[0]
    url = "%s?name=%s" % (base, urllib.parse.quote(name))
    hdr = {"Content-Type": "application/octet-stream", "User-Agent": "moo-verify",
           "Authorization": "Bearer " + os.environ.get("GH_TOKEN", "")}
    req = urllib.request.Request(url, data=data, headers=hdr, method="POST")
    with urllib.request.urlopen(req, timeout=180) as r:
        return json.load(r)


def fetch_bytes(url):
    req = urllib.request.Request(url, headers={"User-Agent": "moo-verify"})
    with urllib.request.urlopen(req, timeout=180) as r:
        return r.read()


def write_summary(tag, bad, fixed):
    p = os.environ.get("GITHUB_STEP_SUMMARY")
    if not p:
        return
    lines = ["## Moo release 资产校验 `%s`" % tag, ""]
    lines.append("- 侧车自动补传：**%d**" % len(fixed))
    for f in fixed:
        lines.append("  - ✅ " + f)
    if bad:
        lines.append("- 不可自愈的问题：**%d**" % len(bad))
        for b in bad:
            lines.append("  - ⚠️ " + b)
        lines.append("")
        lines.append("> 以上问题需在发布机修复后重跑（本 job 为 advisory，不阻塞 tag 流水线）")
    else:
        lines.append("- ✅ 全部资产与 moo.json 索引一致")
    try:
        with open(p, "a", encoding="utf-8") as f:
            f.write("\n".join(lines) + "\n")
    except OSError:
        pass

def check_remote(rel, pkgs, ver, need_minisig, do_fix):
    """核对 release 资产；do_fix 时自动补 .sha256 侧车（可自愈项）。"""
    assets = {a["name"]: a for a in (rel.get("assets") or [])}
    bad, fixed = [], []
    for arch, spec in sorted(pkgs.items()):
        spec = spec or {}
        name = spec.get("filename") or ("moo_%s_%s.fpk" % (ver, arch))
        a = assets.get(name)
        if not a:
            bad.append("%s: 缺 FPK %s" % (arch, name))
            continue
        if spec.get("size") and a.get("size") != spec["size"]:
            bad.append("%s: size %s != 索引 %s" % (arch, a.get("size"), spec["size"]))
        dg = a.get("digest") or ""
        if dg.startswith("sha256:") and spec.get("sha256") and dg[7:] != spec["sha256"]:
            bad.append("%s: digest %s… != 索引 %s…" % (arch, dg[7:19], spec["sha256"][:12]))
        if not dg:
            warn("%s: 服务端未返回 digest，跳过 sha256 比对（仅比 size）" % arch)
        side = name + ".sha256"
        if side not in assets:
            if not do_fix:
                bad.append("%s: 缺侧车 %s（自更新 fail-closed 必需）" % (arch, side))
            else:
                try:
                    if dg.startswith("sha256:"):
                        sha = dg[7:]
                    else:
                        sha = hashlib.sha256(fetch_bytes(a["browser_download_url"])).hexdigest()
                    upload_asset(rel["upload_url"], side, ("%s  %s\n" % (sha, name)).encode())
                    fixed.append("%s → %s" % (arch, side))
                except Exception as e:
                    bad.append("%s: 补传 %s 失败：%s" % (arch, side, e))
        if need_minisig and (name + ".minisig") not in assets:
            bad.append("%s: 缺签名 %s.minisig（CI 无私钥，无法自愈）" % (arch, name))
    return bad, fixed


def main():
    tag = os.environ.get("TAG", "").strip()
    m = re.fullmatch(r"v(\d+\.\d+\.\d+)", tag)
    if not m:
        fail("tag 必须形如 vX.Y.Z，实际：%r" % tag)
    ver = m.group(1)

    man = open("fnos/manifest", encoding="utf-8").read()
    mm = re.search(r"^version\s*=\s*(\S+)", man, re.M)
    if not mm:
        fail("fnos/manifest 缺 version")
    if mm.group(1) != ver:
        fail("fnos/manifest version=%s != tag %s" % (mm.group(1), ver))
    print("✓ manifest version = " + ver)

    idx = json.load(open("moo.json", encoding="utf-8"))
    app = (idx.get("apps") or {}).get("moo")
    if not app:
        fail("moo.json 缺 apps.moo")
    if app.get("version") != ver:
        fail("moo.json apps.moo.version=%r != %s（索引跟版纪律）" % (app.get("version"), ver))
    print("✓ moo.json 顶层 version = " + ver)

    rels = app.get("releases") or {}
    pkgs = (rels.get(ver) or {}).get("packages") or {}
    if not pkgs:
        fail("moo.json 缺 apps.moo.releases.%s.packages" % ver)
    print("✓ releases[%s].packages = %s" % (ver, sorted(pkgs)))

    # 顶层指针必须指向 packages 里的某个真实架构（不假设"首个"——map 顺序不稳定）
    fn = (app.get("download_url") or "").rsplit("/", 1)[-1]
    fm = re.fullmatch(r"moo_%s_(.+)\.fpk" % re.escape(ver), fn)
    if not fm:
        fail("apps.moo.download_url 文件名 %s 不形如 moo_%s_<arch>.fpk" % (fn, ver))
    arch = fm.group(1)
    if arch not in pkgs:
        fail("apps.moo.download_url 指向架构 %s，但 packages 里只有 %s" % (arch, sorted(pkgs)))
    top_sha = app.get("sha256") or ""
    pk_sha = (pkgs[arch] or {}).get("sha256") or ""
    if top_sha and pk_sha and top_sha != pk_sha:
        fail("apps.moo 顶层 sha256 与 packages.%s.sha256 不一致" % arch)
    pk_size = (pkgs[arch] or {}).get("size")
    if app.get("size") and pk_size and app["size"] != pk_size:
        fail("apps.moo 顶层 size %s != packages.%s.size %s" % (app["size"], arch, pk_size))
    print("✓ 顶层 download_url / sha256 / size 跟到最新架构 " + arch)

    keys = sorted(rels, key=ver_tuple, reverse=True)
    if keys and keys[0] != ver:
        fail("releases 首版 %s != %s" % (keys[0], ver))
    if len(keys) > 2:
        warn("releases 保留 %d 版（纪律：只留两版）：%s" % (len(keys), keys))

    need_minisig = ver_tuple(ver) >= MINISIG_FROM

    if "--skip-api" in sys.argv[1:]:
        print("OK（--skip-api：仅本地校验）")
        return

    # 模式 1：发布机预检（不联网）——推 tag 之前跑，失败就别推
    local_dir = arg_value("--local-dir")
    if local_dir:
        bad = check_local_dir(local_dir, pkgs, ver, need_minisig)
        write_summary(tag, bad, [])
        if bad:
            for b in bad:
                print("FAIL: " + b)
            sys.exit(1)
        print("OK(索引侧): 本地 %s 的 %d 个架构文件与索引一致（size/sha256/侧车在场）"
              % (local_dir, len(pkgs)))
        # 客户端同款验收：唯一实现是 Go（侧车内容 / minisig 验签 / 解包核 manifest）
        ok, gobad = run_go_local(ver, pkgs.keys(), local_dir)
        if not ok:
            for b in gobad:
                print("FAIL: " + b)
            sys.exit(1)
        print("OK: 本地 %s 的 %d 个架构通过客户端同款验收（含 .sha256%s）"
              % (local_dir, len(pkgs), "/.minisig" if need_minisig else ""))
        return

    # 模式 2：CI 事后核对（advisory）——缺 .sha256 可自动补，其余只报告
    rel = api_get("/releases/tags/" + tag)
    do_fix = "--fix-sidecars" in sys.argv[1:]
    bad, fixed = check_remote(rel, pkgs, ver, need_minisig, do_fix)
    for f in fixed:
        print("FIX: " + f)
    if bad:
        for b in bad:
            print("FAIL: " + b)
        write_summary(tag, bad, fixed)
        sys.exit(1)
    print("OK(索引侧): %s 的 %d 个架构资产与 moo.json 逐字一致（含 .sha256%s）"
          % (tag, len(pkgs), "/.minisig" if need_minisig else ""))
    write_summary(tag, bad, fixed)

    # 追加：五通道探测 + 客户端同款验收（Go 唯一实现）；非 0 退出码 → 本脚本退出 1
    # （advisory job 自身 continue-on-error，不会把流水线刷红）
    if not run_go_remote(tag, arch):
        sys.exit(1)


if __name__ == "__main__":
    main()
