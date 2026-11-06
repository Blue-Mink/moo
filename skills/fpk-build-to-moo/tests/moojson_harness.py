#!/usr/bin/env python3
"""update_moo_json.py 复跑验收：准确性 / 完整性 / 兜底。"""
import atexit, gzip, hashlib, io, json, os, shutil, subprocess, sys, tarfile, tempfile, time

ROOT = os.path.dirname(os.path.abspath(__file__))


def _repo_root():
    """仓库布局（tests/ 向上三级有 moo.json）返回该根目录；独立安装版返回 None。"""
    p = os.path.abspath(os.path.join(ROOT, "..", "..", ".."))
    return p if os.path.exists(os.path.join(p, "moo.json")) else None


_REPO_ROOT = _repo_root()
# tests/ 向上三级 = moo-325/，被测工具在 moo-325/tools/ 下；
# 独立安装版没有仓库树，退回技能自带的 bundled/tools/ 副本。
_SCRIPT_CANDS = [os.environ.get("MOO_SCRIPT") or ""]
if _REPO_ROOT:
    _SCRIPT_CANDS.append(os.path.join(_REPO_ROOT, "tools", "update_moo_json.py"))
_SCRIPT_CANDS.append(os.path.abspath(os.path.join(ROOT, "..", "bundled", "tools",
                                                  "update_moo_json.py")))
SCRIPT = next((p for p in _SCRIPT_CANDS if p and os.path.exists(p)), "")
REPO_INDEX = os.path.join(_REPO_ROOT, "moo.json") if _REPO_ROOT else ""
TMP = tempfile.mkdtemp(prefix="moojson-")
atexit.register(shutil.rmtree, TMP, True)
FPK = os.path.join(TMP, "fpk")
CASES = os.path.join(TMP, "cases")
RES = []

# 合成 FPK（不搬运真包）：read_fpk 只读 manifest 的 appname/version/platform。
# 只保证 appname/version/platform 与真实发布一致（被测工具只读这三项）；
# 字节数由现场生成决定，断言里的 size 期望值一律用 os.path.getsize 动态取，
# 不再为对齐真实包体积而 padding（省 25MB/次的临时 IO）。
FPK_SPECS = [
    ("moo_0.6.383_x86.fpk", "moo", "0.6.383", "x86"),
    ("moo_0.6.383_arm.fpk", "moo", "0.6.383", "arm"),
    ("moo-mate_0.2.36_x86.fpk", "moo-mate", "0.2.36", "x86"),
    ("moo-mate_0.2.36_arm.fpk", "moo-mate", "0.2.36", "arm"),
    ("dog-dev_1.3.12_x86.fpk", "dog-dev", "1.3.12", "x86"),
    ("dog-dev_1.3.12_arm.fpk", "dog-dev", "1.3.12", "arm"),
]
FIXED_TS = "2026-01-01T00:00:00Z"


def _add(tf, name, data):
    ti = tarfile.TarInfo(name)
    ti.size, ti.mtime = len(data), 0
    tf.addfile(ti, io.BytesIO(data))


def _write_gz(path, data):
    """gzip level=0（stored 块）：不做压缩，写入快、字节可预期。"""
    with open(path, "wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw,
                           compresslevel=0, mtime=0) as gz:
            gz.write(data)


def _make_fpk(path, appname, version, platform):
    """造合成 FPK：只需 manifest 三字段（被测工具只读这三项）+ 一个占位成员。"""
    mf = ("appname=%s\nversion=%s\nplatform=%s\n"
          % (appname, version, platform)).encode()
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w") as tf:
        _add(tf, "manifest", mf)
        _add(tf, "payload.bin", os.urandom(4096))
    _write_gz(path, buf.getvalue())


def _pkg(fpk_path, tag_prefix, version):
    with open(fpk_path, "rb") as f:
        raw = f.read()
    return {"download_url": "https://github.com/Blue-Mink/moo/releases/download/%s%s/%s"
                            % (tag_prefix, version, os.path.basename(fpk_path)),
            "sha256": hashlib.sha256(raw).hexdigest(),
            "size": len(raw), "size_mb": round(len(raw) / 1048576, 2)}


_REAL = None


class SkipCase(Exception):
    """独立安装版缺少仓库夹具（moo.json）时跳过真实索引用例。"""


def real_index():
    """真实形态索引：仓库 moo.json 为底，moo / moo-mate 的版本节点按合成 FPK
    独立重算（sha256 走 hashlib，不经过被测工具），其余条目原样保留。"""
    global _REAL
    if not REPO_INDEX:
        raise SkipCase("独立安装版无仓库 moo.json")
    if _REAL is None:
        doc = json.load(open(REPO_INDEX, encoding="utf-8"))
        for app, ver, prefix in (("moo", "0.6.383", "v"), ("moo-mate", "0.2.36", "mate-v")):
            pkgs = {a: _pkg(os.path.join(FPK, "%s_%s_%s.fpk" % (app, ver, a)), prefix, ver)
                    for a in ("x86", "arm")}
            e = doc["apps"][app]
            e["releases"] = {ver: {"packages": pkgs, "changelog": "本版更新",
                                   "updated_at": FIXED_TS}}
            e.update({"version": ver, "platform": ["x86", "arm"], "updated_at": FIXED_TS,
                      "download_url": pkgs["x86"]["download_url"],
                      "sha256": pkgs["x86"]["sha256"],
                      "size": pkgs["x86"]["size"], "size_mb": pkgs["x86"]["size_mb"]})
        _REAL = doc
    return json.loads(json.dumps(_REAL))


def real_index_file():
    """落盘的真实形态索引（1 空格缩进、末尾无换行，与仓库现状一致）。"""
    p = os.path.join(TMP, "real_index.json")
    if not os.path.exists(p):
        with open(p, "w", encoding="utf-8") as f:
            f.write(json.dumps(real_index(), ensure_ascii=False, indent=1))
    return p


os.makedirs(CASES, exist_ok=True)
os.makedirs(FPK, exist_ok=True)
for _n, _a, _v, _p in FPK_SPECS:
    _make_fpk(os.path.join(FPK, _n), _a, _v, _p)

def check(name, cond, detail=""):
    RES.append((name, bool(cond), detail))
    print(("PASS " if cond else "FAIL ") + name + (("  << " + str(detail)) if (detail and not cond) else ""))

def sha_cmd(p):
    return subprocess.run(["sha256sum", p], capture_output=True, text=True).stdout.split()[0]

def run(args, cwd):
    return subprocess.run([sys.executable, SCRIPT] + args, cwd=cwd,
                          capture_output=True, text=True)

def fresh(name, doc=None):
    d = os.path.join(CASES, name)
    shutil.rmtree(d, ignore_errors=True)
    os.makedirs(d)
    doc = doc if doc is not None else base_doc()
    with open(os.path.join(d, "moo.json"), "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=False, indent=2)
        f.write("\n")
    return d

def load(d):
    return json.load(open(os.path.join(d, "moo.json"), encoding="utf-8"))

def pkg(sha, size, url):
    return {"download_url": url, "sha256": sha, "size": size}

def base_doc():
    s = "a" * 64
    return {
        "schema_version": "moo",
        "source_info": {"name": "验收源", "author": "tester"},
        "apps": {
            "moo": {
                "display_name": "Moo", "version": "0.6.377",
                "download_url": "https://github.com/Blue-Mink/moo/releases/download/v0.6.377/moo_0.6.377_x86.fpk",
                "sha256": s, "size": 5176019, "size_mb": 4.94,
                "categories": ["系统工具"],
                "releases": {"0.6.377": {"packages": {"x86": pkg(s, 5176019, "https://x/377.fpk")}},
                             "0.6.355": {"packages": {"x86": pkg(s, 5164516, "https://x/355.fpk")}}},
            },
            "other": {
                "display_name": "Other", "version": "1.0.0",
                "download_url": "https://example.com/other-1.0.0.fpk", "sha256": "b" * 64,
                "releases": {"1.0.0": {"packages": {"all": pkg("b" * 64, 123, "https://example.com/other-1.0.0.fpk")}}},
            },
        },
    }


X86 = "../../fpk/moo_0.6.383_x86.fpk"
ARM = "../../fpk/moo_0.6.383_arm.fpk"


def t1():
    d = fresh("t1")
    before = load(d)
    r = run(["--file", "moo.json", "--version", "0.6.383", "--fpk", X86, ARM,
             "--repo", "Blue-Mink/moo", "--changelog", "本版更新"], d)
    check("T1 退出码 0", r.returncode == 0, r.stderr[-400:])
    doc = load(d)
    node = doc["apps"]["moo"]["releases"]["0.6.383"]
    for arch, fn in (("x86", "moo_0.6.383_x86.fpk"), ("arm", "moo_0.6.383_arm.fpk")):
        f = os.path.join(FPK, fn)
        pk = node["packages"][arch]
        check(f"T1 {arch} sha256 与 sha256sum 一致", pk["sha256"] == sha_cmd(f), pk["sha256"])
        check(f"T1 {arch} size 与 stat 一致", pk["size"] == os.path.getsize(f), f"{pk['size']} vs {os.path.getsize(f)}")
        want = f"https://github.com/Blue-Mink/moo/releases/download/v0.6.383/{fn}"
        check(f"T1 {arch} 直链符合 Release 约定", pk["download_url"] == want, pk["download_url"])
    check("T1 changelog 写入", node.get("changelog") == "本版更新", node.get("changelog"))
    check("T1 updated_at 带 +08:00", str(node.get("updated_at", "")).endswith("+08:00"), node.get("updated_at"))
    e = doc["apps"]["moo"]
    check("T1 顶层 version 跟版", e["version"] == "0.6.383", e.get("version"))
    check("T1 顶层 sha256 跟 x86", e["sha256"] == node["packages"]["x86"]["sha256"], e.get("sha256"))
    check("T1 顶层 size 保持字节口径", e["size"] == node["packages"]["x86"]["size"], e.get("size"))
    check("T1 顶层 size_mb 与 MiB 一致", abs(e["size_mb"] - round(node["packages"]["x86"]["size"] / 1048576, 2)) < 1e-9, e.get("size_mb"))
    check("T1 keep=2 裁剪", sorted(doc["apps"]["moo"]["releases"]) == ["0.6.377", "0.6.383"], sorted(doc["apps"]["moo"]["releases"]))
    check("T1 其他应用逐字节未变", doc["apps"]["other"] == before["apps"]["other"])
    check("T1 display_name/categories 保留", e.get("display_name") == "Moo" and e.get("categories") == ["系统工具"])
    raw = open(os.path.join(d, "moo.json"), "rb").read()
    check("T1 输出无 BOM", not raw.startswith(b"\xef\xbb\xbf"))


def t2():
    d = os.path.join(CASES, "t1")
    b1 = open(os.path.join(d, "moo.json"), "rb").read()
    time.sleep(1.05)  # updated_at 是秒级：跨秒才能暴露「无谓刷新时间戳」的幂等回归
    r = run(["--file", "moo.json", "--version", "0.6.383", "--fpk", X86, ARM,
             "--repo", "Blue-Mink/moo", "--changelog", "本版更新"], d)
    b2 = open(os.path.join(d, "moo.json"), "rb").read()
    check("T2 幂等：同输入重复运行无字节变化", b1 == b2, f"rc={r.returncode} diff={len(b1)}->{len(b2)}")
    return b1


def t3():
    d = fresh("t3")
    b1 = open(os.path.join(d, "moo.json"), "rb").read()
    r = run(["--file", "moo.json", "--version", "0.6.383", "--fpk", X86, "--repo", "Blue-Mink/moo", "--dry-run"], d)
    b2 = open(os.path.join(d, "moo.json"), "rb").read()
    check("T3 --dry-run 不落盘", b1 == b2 and r.returncode == 0, f"rc={r.returncode}")


def t4():
    d = os.path.join(CASES, "t1")
    r0 = run(["--file", "moo.json", "--version", "0.6.383", "--fpk", X86, ARM,
              "--repo", "Blue-Mink/moo", "--changelog", "本版更新", "--check"], d)
    check("T4 --check 一致时退出 0", r0.returncode == 0, r0.stdout[-200:])
    r1 = run(["--file", "moo.json", "--version", "0.6.384", "--fpk", X86,
              "--repo", "Blue-Mink/moo", "--check"], d)
    check("T4 --check 需要更新时退出 1", r1.returncode == 1, f"rc={r1.returncode}")


def t5():
    d = fresh("t5")
    run(["--file", "moo.json", "--version", "0.6.383", "--fpk", X86, "--repo", "Blue-Mink/moo"], d)
    x = load(d)["apps"]["moo"]["releases"]["0.6.383"]["packages"]["x86"].copy()
    run(["--file", "moo.json", "--version", "0.6.383", "--fpk", ARM, "--repo", "Blue-Mink/moo"], d)
    node = load(d)["apps"]["moo"]["releases"]["0.6.383"]["packages"]
    check("T5 分两次写 x86+arm 共存", set(node) == {"x86", "arm"}, sorted(node))
    check("T5 先写的 x86 未被覆盖", node["x86"] == x)


def t6():
    d = fresh("t6")
    s = "c" * 64
    r = run(["--file", "moo.json", "--appname", "moo", "--version", "2.0.0",
             "--download-url", "https://cdn.example.com/a.fpk", "--sha256", s,
             "--size", "12345", "--changelog", "手填"], d)
    check("T6 --download-url 手填模式成功", r.returncode == 0, r.stderr[-300:])
    pk = load(d)["apps"]["moo"]["releases"]["2.0.0"]["packages"]
    check("T6 手填 url/sha/size 落盘", list(pk) == ["all"] and pk["all"] ==
          {"download_url": "https://cdn.example.com/a.fpk", "sha256": s, "size": 12345,
           "size_mb": 0.01}, pk)
    r2 = run(["--file", "moo.json", "--appname", "moo", "--version", "2.0.1",
              "--download-url", "https://cdn.example.com/b.fpk", "--size", "1"], d)
    check("T6 缺 sha256 明确报错", r2.returncode != 0 and "sha256" in r2.stderr, r2.stderr[-200:])


def t7():
    d = fresh("t7")
    r = run(["--file", "moo.json", "--version", "9.9.9", "--fpk", X86, "--repo", "Blue-Mink/moo"], d)
    check("T7 版本与包内不一致被拒", r.returncode != 0 and "manifest.version" in r.stderr, r.stderr[-200:])
    r2 = run(["--file", "moo.json", "--version", "9.9.9", "--fpk", X86, "--repo", "Blue-Mink/moo",
              "--allow-version-mismatch"], d)
    check("T7 --allow-version-mismatch 放行", r2.returncode == 0 and
          "9.9.9" in load(d)["apps"]["moo"]["releases"], r2.stderr[-200:])


def t8():
    d = fresh("t8")
    dup = os.path.join(d, "dup_moo_0.6.383_x86.fpk")
    shutil.copy(os.path.join(FPK, "moo_0.6.383_x86.fpk"), dup)
    r = run(["--file", "moo.json", "--appname", "moo", "--version", "0.6.383",
             "--fpk", X86, os.path.basename(dup), "--repo", "Blue-Mink/moo"], d)
    check("T8 同架构双包被拒", r.returncode != 0 and "同一架构" in r.stderr, r.stderr[-300:])
    d2 = fresh("t8b")
    before = load(d2)["apps"]["other"]
    # 加 --allow-downgrade：否则 apps.other 已有的更高版本会先被防回溯拦下，
    # 掩盖「appname 不匹配是否串写」这条断言（R4 反向验证会假绿）
    r2 = run(["--file", "moo.json", "--appname", "other", "--version", "0.6.383",
              "--fpk", X86, "--repo", "Blue-Mink/moo", "--allow-downgrade"], d2)
    doc = load(d2)
    bad = doc["apps"]["other"].get("releases", {}).get("0.6.383", {}).get("packages", {}).get("x86", {})
    check("T8b FPK 的 appname 与 --appname 不符时不得串写",
          not (bad.get("sha256") == sha_cmd(os.path.join(FPK, "moo_0.6.383_x86.fpk"))),
          f"rc={r2.returncode} 已写入 other: {bool(bad)}")


def t9():
    d = fresh("t9", {"schema_version": "moo", "apps": {}})
    r = run(["--file", "moo.json", "--version", "1.0.0", "--fpk", X86], d)
    check("T9 空 apps 明确报错", r.returncode != 0 and "没有 apps" in r.stderr, r.stderr[-200:])


def t10():
    d = fresh("t10")
    p = os.path.join(d, "moo.json")
    body = open(p, "rb").read()
    open(p, "wb").write(b"\xef\xbb\xbf" + body)
    r = run(["--file", "moo.json", "--version", "0.6.383", "--fpk", X86, "--repo", "Blue-Mink/moo"], d)
    out = open(p, "rb").read()
    ok = r.returncode == 0 and not out.startswith(b"\xef\xbb\xbf")
    try:
        json.loads(out.decode("utf-8"))
    except Exception:
        ok = False
    check("T10 BOM 输入被去除且结果合法", ok, r.stderr[-200:])


def t11():
    s = "d" * 64
    doc = {"schema_version": "moo", "apps": {"moo": {
        "display_name": "Moo", "version": "1.0.0",
        "download_url": "https://x/1.0.0.fpk", "sha256": s, "size_bytes": 10,
        "releases": {
            "0.6.355": {"packages": {"x86": pkg(s, 1, "https://x/355.fpk")}},
            "0.6.377": {"packages": {"x86": pkg(s, 2, "https://x/377.fpk")}},
            "1.0.0-rc.2": {"packages": {"x86": pkg(s, 3, "https://x/rc2.fpk")}},
            "1.0.0": {"packages": {"x86": pkg(s, 4, "https://x/100.fpk")}},
            "latest-2026.10.17": {"packages": {"x86": pkg(s, 5, "https://x/latest.fpk")}},
        }}}}
    d = fresh("t11", doc)
    # 该夹具刻意让索引里已有更高版本 1.0.0（补录历史版本场景），
    # 需显式 --allow-downgrade，否则会被「防回溯」拦下（见 UPDATER_HARDEN）。
    r = run(["--file", "moo.json", "--version", "0.6.383", "--fpk", X86,
             "--repo", "Blue-Mink/moo", "--keep", "2", "--allow-downgrade"], d)
    doc2 = load(d)
    kept = sorted(doc2["apps"]["moo"]["releases"])
    check("T11 裁剪保留最新 2 版 + 本次版本", kept == ["0.6.383", "1.0.0", "1.0.0-rc.2"], kept)
    check("T11 不可比标签 latest-* 被裁掉", "latest-2026.10.17" not in kept, kept)
    check("T11 顶层跟到真正最新 1.0.0", doc2["apps"]["moo"]["version"] == "1.0.0",
          doc2["apps"]["moo"]["version"])
    check("T11 裁剪后顶层指向存在节点", "1.0.0" in doc2["apps"]["moo"]["releases"])


def t12():
    d = fresh("t12")
    before = load(d)
    run(["--file", "moo.json", "--version", "0.6.383", "--fpk", ARM, "--repo", "Blue-Mink/moo", "--keep", "0"], d)
    after = load(d)
    check("T12 未指定应用的 releases 逐字节不变", after["apps"]["other"] == before["apps"]["other"])
    check("T12 同应用旧版本节点字段保留", after["apps"]["moo"]["releases"]["0.6.355"] ==
          before["apps"]["moo"]["releases"]["0.6.355"])


def t13():
    d = fresh("t13")
    r = run(["--file", "moo.json", "--version", "0.6.383", "--fpk", X86,
             "--repo", "https://gitea.example.com/owner/repo"], d)
    url = load(d)["apps"]["moo"]["releases"]["0.6.383"]["packages"]["x86"]["download_url"]
    want = "https://gitea.example.com/owner/repo/releases/download/v0.6.383/moo_0.6.383_x86.fpk"
    check("T13 非 GitHub 仓库直链推导正确", r.returncode == 0 and url == want, url)


def t14():
    d = fresh("t14")
    src = os.path.join(FPK, "moo_0.6.383_x86.fpk")
    dst = os.path.join(d, "我的 应用_0.6.383_x86.fpk")
    shutil.copy(src, dst)
    r = run(["--file", "moo.json", "--version", "0.6.383", "--fpk", os.path.basename(dst),
             "--repo", "Blue-Mink/moo"], d)
    url = load(d)["apps"]["moo"]["releases"]["0.6.383"]["packages"]["x86"]["download_url"]
    check("T14 中文/空格文件名 URL 编码", r.returncode == 0 and
          url.endswith("/%E6%88%91%E7%9A%84%20%E5%BA%94%E7%94%A8_0.6.383_x86.fpk"), url)


def t15():
    d = fresh("t15")
    # 放大文件，令受限写入必然中断，检验是否原子替换
    doc = load(d)
    doc["source_info"]["description"] = "x" * 300000
    with open(os.path.join(d, "moo.json"), "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=False, indent=2)
    orig = open(os.path.join(d, "moo.json"), "rb").read()
    cmd = ("ulimit -f 100; cd %s && %s %s --file moo.json --version 0.6.383 "
           "--fpk %s --repo Blue-Mink/moo >/dev/null 2>&1" % (d, sys.executable, SCRIPT, X86))
    subprocess.run(["bash", "-c", cmd])
    now = open(os.path.join(d, "moo.json"), "rb").read()
    intact = True
    try:
        json.loads(now.decode("utf-8"))
    except Exception:
        intact = False
    check("T15 写盘中断后原文件仍完整（原子替换）", intact and now == orig,
          f"orig={len(orig)}B now={len(now)}B valid={intact}")


def t16():
    d = fresh("t16")
    r = run(["--file", "moo.json", "--version", "0.6.383", "--fpk", "../../fpk/nope.fpk"], d)
    check("T16 不存在的 FPK 给出明确错误而非堆栈",
          r.returncode != 0 and "Traceback" not in r.stderr and "读 FPK 失败" in r.stderr,
          r.stderr[-200:])
    p = os.path.join(d, "noperm.fpk")
    shutil.copy(os.path.join(FPK, "moo_0.6.383_x86.fpk"), p)
    os.chmod(p, 0)
    r2 = run(["--version", "0.6.383", "--fpk", "noperm.fpk", "--repo", "Blue-Mink/moo"], d)
    os.chmod(p, 0o644)
    check("T16b 无读权限 FPK 明确报错",
          r2.returncode != 0 and "Traceback" not in r2.stderr and "读 FPK 失败" in r2.stderr,
          r2.stderr[-200:])


def t17():
    s = "e" * 64
    doc = {"schema_version": "moo", "apps": {"dog-dev": {
        "display_name": "Dog", "releases": {}}}}
    d = fresh("t17", doc)
    f = "../../fpk/dog-dev_1.3.12_x86.fpk"
    r = run(["--file", "moo.json", "--version", "1.3.12", "--fpk", f,
             "--repo", "Blue-Mink/dog-dev"], d)
    e = load(d)["apps"]["dog-dev"]
    check("T17 新应用条目写 releases", r.returncode == 0 and "1.3.12" in e.get("releases", {}), r.stderr[-200:])
    x86 = e.get("releases", {}).get("1.3.12", {}).get("packages", {}).get("x86", {})
    ok = bool(x86.get("download_url")) and len(x86.get("sha256", "")) == 64 and x86.get("size", 0) > 0
    check("T17 新应用 releases 含包级直链/校验（Moo 可从 releases 补齐）", ok, e.get("releases"))


def t18():
    real = real_index()
    rn = real["apps"]["moo"]["releases"].get("0.6.383", {})
    d = os.path.join(CASES, "t1")
    mine = load(d)["apps"]["moo"]["releases"]["0.6.383"]
    check("T18 版本节点键集合与真实发布一致", set(mine) == set(rn), (sorted(mine), sorted(rn)))
    need = {"download_url", "sha256", "size"}
    for arch in ("x86", "arm"):
        if arch in rn.get("packages", {}):
            check(f"T18 {arch} 必备字段齐全", need <= set(mine["packages"][arch]), sorted(mine["packages"][arch]))
            if "size_mb" in rn["packages"][arch]:
                check(f"T18 {arch} size_mb 与真实发布一致",
                      mine["packages"][arch].get("size_mb") == rn["packages"][arch]["size_mb"],
                      (mine["packages"][arch].get("size_mb"), rn["packages"][arch]["size_mb"]))
            check(f"T18 {arch} sha 与真实发布一致",
                  mine["packages"][arch]["sha256"] == rn["packages"][arch]["sha256"],
                  mine["packages"][arch]["sha256"])


def t19():
    d = fresh("t19")
    real = real_index()
    with open(os.path.join(d, "moo.json"), "w", encoding="utf-8") as f:
        json.dump(real, f, ensure_ascii=False, indent=1)
        f.write("\n")
    for fn in ("moo_0.6.383_x86.fpk", "moo_0.6.383_arm.fpk"):
        shutil.copy(os.path.join(FPK, fn), d)
    r = run(["--file", "moo.json", "--version", "0.6.383", "--fpk",
             "moo_0.6.383_x86.fpk", "moo_0.6.383_arm.fpk", "--repo", "Blue-Mink/moo", "--check"], d)
    check("T19 真实索引重算零差异（--check 退出 0）", r.returncode == 0, r.stdout[-800:])


def t20():
    d = fresh("t20")
    r = run(["--file", "moo.json", "--appname", "moo", "--version", "2.1.0",
             "--download-url", "https://cdn.example.com/a.fpk", "--sha256", "C" * 64,
             "--size", "1000"], d)
    pk = load(d)["apps"]["moo"]["releases"].get("2.1.0", {}).get("packages", {}).get("all", {})
    check("T20 大写 sha256 归一为小写", r.returncode == 0 and pk.get("sha256") == "c" * 64, pk)
    d2 = fresh("t21")
    r2 = run(["--file", "moo.json", "--version", "0.6.383", "--fpk", X86, "--repo", "Blue-Mink/moo"], d2)
    p21 = os.path.join(d2, "moo.json")
    doc21 = json.load(open(p21, encoding="utf-8"))
    with open(p21, "w", encoding="utf-8") as f:
        json.dump(doc21, f, ensure_ascii=False, indent=1)
        f.write("\n")
    run(["--file", "moo.json", "--version", "0.6.384", "--fpk", X86,
         "--repo", "Blue-Mink/moo", "--allow-version-mismatch"], d2)
    second = open(p21, encoding="utf-8").read().splitlines()[1]
    check("T21 保留原文件缩进（1 空格）", second.startswith(' "') and not second.startswith('  "'), repr(second[:12]))


def t22():
    d = fresh("t22")
    real = real_index()
    with open(os.path.join(d, "moo.json"), "w", encoding="utf-8") as f:
        json.dump(real, f, ensure_ascii=False, indent=1)
        f.write("\n")
    for fn in ("moo_0.6.383_x86.fpk", "moo_0.6.383_arm.fpk",
               "moo-mate_0.2.36_x86.fpk", "moo-mate_0.2.36_arm.fpk"):
        shutil.copy(os.path.join(FPK, fn), d)
    r1 = run(["--file", "moo.json", "--version", "0.6.383", "--fpk",
              "moo_0.6.383_x86.fpk", "moo_0.6.383_arm.fpk", "--repo", "Blue-Mink/moo", "--check"], d)
    r2 = run(["--file", "moo.json", "--version", "0.2.36", "--fpk",
              "moo-mate_0.2.36_x86.fpk", "moo-mate_0.2.36_arm.fpk", "--repo", "Blue-Mink/moo", "--tag-prefix", "mate-v", "--check"], d)
    check("T22 真实索引 moo 重算零差异", r1.returncode == 0, r1.stdout[-400:])
    check("T22 真实索引 moo-mate 重算零差异", r2.returncode == 0, r2.stdout[-400:])


def t23():
    d = fresh("t23")
    shutil.copy(real_index_file(), os.path.join(d, "moo.json"))
    for fn in ("moo_0.6.383_x86.fpk", "moo_0.6.383_arm.fpk",
               "moo-mate_0.2.36_x86.fpk", "moo-mate_0.2.36_arm.fpk"):
        shutil.copy(os.path.join(FPK, fn), d)
    before = hashlib.sha256(open(os.path.join(d, "moo.json"), "rb").read()).hexdigest()
    run(["--file", "moo.json", "--version", "0.6.383", "--fpk",
         "moo_0.6.383_x86.fpk", "moo_0.6.383_arm.fpk", "--repo", "Blue-Mink/moo"], d)
    run(["--file", "moo.json", "--version", "0.2.36", "--fpk",
         "moo-mate_0.2.36_x86.fpk", "moo-mate_0.2.36_arm.fpk", "--repo", "Blue-Mink/moo",
         "--tag-prefix", "mate-v"], d)
    after = hashlib.sha256(open(os.path.join(d, "moo.json"), "rb").read()).hexdigest()
    check("T23 真实索引写回后 sha256 逐字节不变", before == after, f"{before[:16]} vs {after[:16]}")


if __name__ == "__main__":
    if not SCRIPT:
        print("FATAL: 找不到 update_moo_json.py（仓库 tools/ 与 bundled/tools/ 均缺失）")
        sys.exit(2)
    only = sys.argv[1:] or None
    fns = [t1, t2, t3, t4, t5, t6, t7, t8, t9, t10, t11, t12, t13, t14, t15, t16, t17, t18, t19, t20, t22, t23]
    skipped = 0
    for fn in fns:
        if only and fn.__name__ not in only:
            continue
        try:
            fn()
        except SkipCase as e:
            skipped += 1
            print("SKIP %s  << %s" % (fn.__name__, e))
        except Exception as e:
            check(fn.__name__ + " 崩溃", False, repr(e))
    bad = [r for r in RES if not r[1]]
    print("\n=== %d 项，%d PASS，%d FAIL，%d SKIP ===" % (len(RES), len(RES) - len(bad), len(bad), skipped))
    for n, _, d in bad:
        print("FAIL:", n, "|", d)
    sys.exit(1 if bad else 0)
