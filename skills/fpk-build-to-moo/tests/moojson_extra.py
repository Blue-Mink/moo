#!/usr/bin/env python3
"""update_moo_json.py 全路径/全参数补测（配合 harness.py）。"""
import json, os, shutil, subprocess, sys, importlib.util, shlex
import moojson_harness as H

ROOT = H.ROOT
SCRIPT = H.SCRIPT
FPK = H.FPK
CASES = os.path.join(H.TMP, "cases2")
os.makedirs(CASES, exist_ok=True)
RES = []

def check(name, cond, detail=""):
    RES.append((name, bool(cond), detail))
    print(("PASS " if cond else "FAIL ") + name + (("  << " + str(detail)) if (detail and not cond) else ""))

def fresh(name, doc=None):
    d = os.path.join(CASES, name)
    shutil.rmtree(d, ignore_errors=True)
    os.makedirs(d)
    doc = doc if doc is not None else H.base_doc()
    with open(os.path.join(d, "moo.json"), "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=False, indent=2)
        f.write("\n")
    return d

def run(args, cwd):
    return subprocess.run([sys.executable, SCRIPT] + args, cwd=cwd,
                          capture_output=True, text=True)

def load(d, name="moo.json"):
    return json.load(open(os.path.join(d, name), encoding="utf-8"))

def pkgj(sha, size, url):
    return {"download_url": url, "sha256": sha, "size": size}

def write_json(d, doc, name="moo.json"):
    with open(os.path.join(d, name), "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=False, indent=2)
        f.write("\n")

S = "f" * 64


def e1():
    r = run(["--help"], ROOT)
    need = ["--keep", "--dry-run", "--check", "--repo", "--allow-appname-mismatch",
            "--allow-version-mismatch", "--changelog-file", "--arch"]
    check("E1 --help 列出全部开关", r.returncode == 0 and all(x in r.stdout for x in need),
          [x for x in need if x not in r.stdout])


def e2():
    d = fresh("e2")
    r = run(["--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo"], d)
    check("E2 省略 --file 时默认 moo.json", r.returncode == 0 and "0.6.383" in load(d)["apps"]["moo"]["releases"], r.stderr[-200:])


def e3():
    d = fresh("e3")
    shutil.copy(os.path.join(d, "moo.json"), os.path.join(d, "idx.json"))
    r = run(["--file", "idx.json", "--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo"], d)
    check("E3 --file 指定自定义索引名", r.returncode == 0 and "0.6.383" in load(d, "idx.json")["apps"]["moo"]["releases"], r.stderr[-200:])


def e4():
    d = fresh("e4")
    r = run(["--file", "nope.json", "--version", "1.0.0", "--fpk", H.X86], d)
    check("E4 索引文件不存在时明确报错", r.returncode != 0 and "找不到" in r.stderr, r.stderr[-200:])


def e5():
    doc = {"schema_version": "moo", "apps": {"moo": {"display_name": "M", "releases": {}}}}
    d = fresh("e5", doc)
    r = run(["--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo"], d)
    check("E5 单应用时自动推断 appname", r.returncode == 0 and "moo" in load(d)["apps"], r.stderr[-300:])


def e6():
    d = fresh("e6")
    r = run(["--version", "0.6.383", "--download-url", "https://x/a.fpk",
             "--sha256", S, "--size", "1"], d)
    check("E6 多应用未给 --appname 时报错", r.returncode != 0 and "appname" in r.stderr, r.stderr[-300:])


def e7():
    d = fresh("e7")
    open(os.path.join(d, "CHANGELOG.md"), "w", encoding="utf-8").write("A" * 5000)
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86,
             "--repo", "Blue-Mink/moo", "--changelog-file", "CHANGELOG.md"], d)
    node = load(d)["apps"]["moo"]["releases"]["0.6.383"]
    check("E7 --changelog-file 截断 4000 字", r.returncode == 0 and len(node["changelog"]) == 4000,
          len(node.get("changelog", "")))


def e8():
    d = fresh("e8")
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86,
             "--repo", "Blue-Mink/moo", "--changelog-file", "missing.md"], d)
    check("E8 changelog 文件缺失明确报错", r.returncode != 0 and "读取" not in r.stderr and "失败" in r.stderr, r.stderr[-200:])


def e9():
    d = fresh("e9")
    open(os.path.join(d, "C.md"), "w").write("fromFile")
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86,
             "--repo", "Blue-Mink/moo", "--changelog", "fromArg", "--changelog-file", "C.md"], d)
    node = load(d)["apps"]["moo"]["releases"]["0.6.383"]
    check("E9 --changelog 优先于文件", r.returncode == 0 and node["changelog"] == "fromArg", node.get("changelog"))


def e10():
    d = fresh("e10")
    doc = load(d)
    doc["apps"]["moo"]["releases"]["0.0.9"] = {"packages": {"x86": pkgj(S, 1, "https://x/009.fpk")}}
    write_json(d, doc)
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86,
             "--repo", "Blue-Mink/moo", "--keep", "0"], d)
    rel = load(d)["apps"]["moo"]["releases"]
    check("E10 --keep 0 全量保留", r.returncode == 0 and set(rel) == {"0.0.9", "0.6.355", "0.6.377", "0.6.383"}, sorted(rel))


def e11():
    d = fresh("e11")
    doc = load(d)
    doc["apps"]["moo"]["releases"]["0.0.9"] = {"packages": {"x86": pkgj(S, 1, "https://x/009.fpk")}}
    write_json(d, doc)
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86,
             "--repo", "Blue-Mink/moo", "--keep", "1"], d)
    rel = load(d)["apps"]["moo"]["releases"]
    check("E11 --keep 1 只留最新一版", r.returncode == 0 and set(rel) == {"0.6.383"}, sorted(rel))


def e12():
    d = fresh("e12")
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86,
             "--repo", "Blue-Mink/moo", "--arch", "arm"], d)
    check("E12 --arch 覆盖时给出警告", "警告" in r.stdout and "arm" in r.stdout, r.stdout[-200:])
    pk = load(d)["apps"]["moo"]["releases"]["0.6.383"]["packages"]
    check("E12 --arch 覆盖生效且只写 arm", list(pk) == ["arm"], sorted(pk))


def e13():
    d = fresh("e13")
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86,
             "--repo", "Blue-Mink/moo", "--tag-prefix", ""], d)
    url = load(d)["apps"]["moo"]["releases"]["0.6.383"]["packages"]["x86"]["download_url"]
    check("E13 --tag-prefix 为空时 tag=版本号", r.returncode == 0 and
          url == "https://github.com/Blue-Mink/moo/releases/download/0.6.383/moo_0.6.383_x86.fpk", url)


def e14():
    cases = {
        "git@github.com:o/r.git": "https://github.com/o/r",
        "ssh://git@git.example.com/o/r.git": "https://git.example.com/o/r",
        "https://gitlab.com/o/r.git": "https://gitlab.com/o/r",
        "git.example.com/o/r": "https://git.example.com/o/r",
        "o/r": "https://github.com/o/r",
    }
    ok = True; det = []
    for repo, want in cases.items():
        d = fresh("e14_" + str(abs(hash(repo)) % 100000))
        r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86, "--repo", repo], d)
        url = load(d)["apps"]["moo"]["releases"]["0.6.383"]["packages"]["x86"]["download_url"]
        if r.returncode != 0 or not url.startswith(want + "/releases/download/v0.6.383/"):
            ok = False; det.append((repo, url))
    check("E14 五种仓库地址形态均正确", ok, det)


def e15():
    d = fresh("e15")
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86], d)
    check("E15 非 git 目录且无 --repo 时明确报错", r.returncode != 0 and "--repo" in r.stderr, r.stderr[-200:])


def e16():
    d = fresh("e16")
    open(os.path.join(d, "moo.json"), "w").write("{not json")
    r = run(["--version", "1.0.0", "--fpk", H.X86], d)
    check("E16 非法 JSON 明确报错", r.returncode != 0 and "不是合法 JSON" in r.stderr, r.stderr[-200:])


def e17():
    d = fresh("e17", {"schema_version": "moo", "apps": []})
    r = run(["--version", "1.0.0", "--fpk", H.X86], d)
    check("E17 apps 非对象时报错", r.returncode != 0 and "没有 apps" in r.stderr, r.stderr[-200:])


def e18():
    d = fresh("e18", {"schema_version": "moo", "apps": {"moo": {"releases": []}}})
    r = run(["--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo"], d)
    check("E18 releases 非对象时报错", r.returncode != 0 and "不是对象" in r.stderr, r.stderr[-200:])


def e19():
    d = fresh("e19", {"schema_version": "moo", "apps": {"moo": {"releases": {"0.6.383": []}}}})
    r = run(["--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo"], d)
    check("E19 版本节点非对象时报错", r.returncode != 0 and "不是对象" in r.stderr, r.stderr[-200:])


def e20():
    d = fresh("e20", {"schema_version": "moo", "apps": {"moo": {"releases": {"0.6.383": {"packages": []}}}}})
    r = run(["--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo"], d)
    check("E20 packages 非对象时给出明确错误而非堆栈",
          r.returncode != 0 and "Traceback" not in r.stderr, r.stderr[-300:])


def e21():
    doc = H.base_doc(); doc["apps"]["bad"] = {"display_name": "Bad"}
    d = fresh("e21", doc)
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo"], d)
    check("E21 自检：无下载地址且无 releases 的应用被拦", r.returncode != 0 and "既没有 download_url" in r.stderr, r.stderr[-200:])


def e22():
    doc = H.base_doc(); doc["apps"]["bad"] = {"download_url": "https://x/a.fpk", "sha256": "zz"}
    d = fresh("e22", doc)
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo"], d)
    check("E22 自检：非法 sha256 被拦", r.returncode != 0 and "不是 64 位" in r.stderr, r.stderr[-200:])


def e23():
    doc = H.base_doc(); doc["apps"]["bad"] = {"releases": {"1.0.0": {}}}
    d = fresh("e23", doc)
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo"], d)
    check("E23 自检：版本节点缺 packages 被拦", r.returncode != 0 and "没有 packages" in r.stderr, r.stderr[-200:])


def e24():
    doc = H.base_doc(); doc["apps"]["bad"] = {"releases": {"1.0.0": {"packages": {"x86": {"sha256": S}}}}}
    d = fresh("e24", doc)
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo"], d)
    check("E24 自检：包缺 download_url 被拦", r.returncode != 0 and "缺 download_url" in r.stderr, r.stderr[-200:])


def e25():
    doc = H.base_doc(); doc["apps"]["bad"] = {"releases": {"1.0.0": {"packages": {"x86": {"download_url": "https://x/a", "sha256": "xy"}}}}}
    d = fresh("e25", doc)
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo"], d)
    check("E25 自检：包 sha256 非法被拦", r.returncode != 0 and "不是 64 位" in r.stderr, r.stderr[-200:])


def e26():
    d = fresh("e26")
    doc = load(d)
    doc["apps"]["moo"]["releases"]["0.6.383"] = {"packages": {"x86": dict(pkgj(S, 1, "https://old"), custom="keep-me")},
                                                  "notes": "版本备注"}
    write_json(d, doc)
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86, "--repo", "Blue-Mink/moo",
             "--allow-republish"], d)
    node = load(d)["apps"]["moo"]["releases"]["0.6.383"]
    check("E26 包内自定义字段保留", r.returncode == 0 and node["packages"]["x86"].get("custom") == "keep-me",
          node["packages"]["x86"])
    check("E26 版本节点自定义字段保留", node.get("notes") == "版本备注", node.get("notes"))


def e27():
    spec = importlib.util.spec_from_file_location("upd", SCRIPT)
    m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
    cases = [("0.10.0", "0.9.0", 1), ("1.13.0-1", "1.13.0", 1), ("1.0.0-10", "1.0.0-9", 1),
             ("1.0.0-rc.2", "1.0.0", -1), ("1.0.0-rc.38", "1.0.0-rc.37", 1),
             ("1.0.0+build5", "1.0.0", 0), ("1.0.0-x86", "1.0.0", 0),
             ("1.0", "1.0.0", 0), ("v1.2.3", "1.2.3", 0), ("1.8.0_492", "1.8.0.492", 0)]
    bad = [(a, b, m.compare_version(a, b), w) for a, b, w in cases if m.compare_version(a, b) != w]
    check("E27 版本比较与协议 §5.3 十条一致", not bad, bad)


def e28():
    spec = importlib.util.spec_from_file_location("upd2", SCRIPT)
    m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
    ok = m.parse_version("latest-2026.10.17")[3] is False and m.compare_version("latest-2026.10.17", "1.0.0") == 0
    check("E28 latest-* 判定为不可比", ok)


def e29():
    spec = importlib.util.spec_from_file_location("upd3", SCRIPT)
    m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
    got = m.sorted_versions(["1.0.0", "latest-x", "0.9.0", "1.0.0-rc.1"])
    check("E29 排序：不可比最旧、预发布低于正式版", got == ["latest-x", "0.9.0", "1.0.0-rc.1", "1.0.0"], got)


def e30():
    d = fresh("e30")
    open(os.path.join(d, "fake.fpk"), "w").write("not a tar")
    r = run(["--version", "1.0.0", "--fpk", "fake.fpk"], d)
    check("E30 非 FPK 文件明确报错", r.returncode != 0 and "读 FPK 失败" in r.stderr, r.stderr[-200:])
    import tarfile
    with tarfile.open(os.path.join(d, "empty.fpk"), "w:gz") as tf:
        info = tarfile.TarInfo("readme.txt"); data = b"x"
        import io; info.size = len(data); tf.addfile(info, io.BytesIO(data))
    r2 = run(["--version", "1.0.0", "--fpk", "empty.fpk"], d)
    check("E30b 无 manifest 的包明确报错", r2.returncode != 0 and "没有 manifest" in r2.stderr, r2.stderr[-200:])


def _mkfpk(path, appname, version=None, platform="x86_64"):
    import tarfile, io
    lines = [f"appname={appname}"]
    if version is not None:
        lines.append(f"version={version}")
    if platform is not None:
        lines.append(f"platform={platform}")
    data = ("\n".join(lines) + "\n").encode()
    with tarfile.open(path, "w:gz") as tf:
        info = tarfile.TarInfo("manifest"); info.size = len(data)
        tf.addfile(info, io.BytesIO(data))


def e31():
    d = fresh("e31")
    _mkfpk(os.path.join(d, "a_x86.fpk"), "alpha", "1.0.0", "x86_64")
    _mkfpk(os.path.join(d, "b_arm.fpk"), "beta", "1.0.0", "aarch64")
    r = run(["--fpk", "a_x86.fpk", "b_arm.fpk", "--repo", "o/r"], d)
    check("E31 一次传两个不同应用被拒", r.returncode != 0 and "appname 不一致" in r.stderr, r.stderr[-250:])


def e32():
    d = fresh("e32")
    _mkfpk(os.path.join(d, "a1.fpk"), "alpha", "1.0.0", "x86_64")
    _mkfpk(os.path.join(d, "a2.fpk"), "alpha", "2.0.0", "aarch64")
    r = run(["--fpk", "a1.fpk", "a2.fpk", "--repo", "o/r"], d)
    check("E32 同应用两个版本被拒", r.returncode != 0 and "manifest.version 不一致" in r.stderr, r.stderr[-250:])


def e33():
    d = fresh("e33")
    _mkfpk(os.path.join(d, "a.fpk"), "moo", "0.6.383", "x86_64")
    _mkfpk(os.path.join(d, "b.fpk"), "moo", "0.6.383", "aarch64")
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", "a.fpk", "b.fpk",
             "--download-url", "https://x/a.fpk"], d)
    check("E33 --download-url 与多包互斥", r.returncode != 0 and "只能用于单个包" in r.stderr, r.stderr[-250:])


def e34():
    d = fresh("e34")
    r = run(["--appname", "moo", "--version", "2.0.0", "--download-url", "https://x/a.fpk",
             "--sha256", S], d)
    check("E34 download-url 模式缺 size 报错", r.returncode != 0 and "--size" in r.stderr, r.stderr[-200:])


def e35():
    d = fresh("e35")
    r = run(["--appname", "moo", "--version", "2.0.0", "--download-url", "https://x/a.fpk",
             "--sha256", "abc", "--size", "10"], d)
    check("E35 非法 sha256 被拒", r.returncode != 0 and "sha256" in r.stderr, r.stderr[-200:])


def e36():
    d = fresh("e36")
    _mkfpk(os.path.join(d, "nov.fpk"), "alpha", None, "x86_64")
    r = run(["--appname", "alpha", "--fpk", "nov.fpk", "--repo", "o/r"], d)
    check("E36 包内无版本且未给 --version 时报错", r.returncode != 0 and "缺少版本号" in r.stderr, r.stderr[-250:])


def e37():
    d = fresh("e37")
    r = run(["--appname", "moo", "--version", "2.0.0"], d)
    check("E37 无 --fpk 且无 --download-url 时报错", r.returncode != 0 and "--fpk" in r.stderr, r.stderr[-250:])


def e38():
    d = fresh("e38")
    _mkfpk(os.path.join(d, "m.fpk"), "alpha", "1.0.0", "mips")
    r = run(["--fpk", "m.fpk", "--repo", "o/r"], d)
    check("E38 未知架构被拒（不写坏 packages 键）", r.returncode != 0 and "架构" in r.stderr, r.stderr[-250:])


def e39():
    d = fresh("e39")
    before = open(os.path.join(d, "moo.json"), "rb").read()
    r = run(["--appname", "moo", "--version", "0.6.383", "--fpk", H.X86,
             "--repo", "Blue-Mink/moo", "--check", "--dry-run"], d)
    after = open(os.path.join(d, "moo.json"), "rb").read()
    check("E39 --check+--dry-run 只校验不落盘", before == after and r.returncode == 1, r.returncode)


def e40():
    d = fresh("e40")
    # apps.other 在夹具里已有更高版本 1.0.0（补录场景），需 --allow-downgrade
    r = run(["--appname", "other", "--version", "0.6.383", "--fpk", H.X86,
             "--repo", "Blue-Mink/moo", "--allow-appname-mismatch", "--allow-downgrade"], d)
    node = load(d)["apps"]["other"]["releases"].get("0.6.383", {}).get("packages", {}).get("x86", {})
    check("E40 允许时不匹配也写入但给出警告",
          r.returncode == 0 and "警告" in r.stdout
          and node.get("size") == os.path.getsize(
              os.path.join(H.FPK, "moo_0.6.383_x86.fpk")), node)


def _wf():
    import yaml
    return yaml.safe_load(open(os.path.join(ROOT, "..", "..", "..", "templates", "moo-autoupdate.yml"), encoding="utf-8"))


def w1():
    d = _wf()
    env = d["jobs"]["update"]["env"]
    check("W1 workflow 可解析且默认值齐全",
          env["FPK_GLOB"] == "*.fpk" and env["MOO_JSON"] == "moo.json" and env["SCRIPT_REF"] == "main"
          and env["BRANCH"] == "main" and d["jobs"]["update"]["runs-on"] == "ubuntu-latest", env)


def w2():
    d = fresh("w2")
    step = [s for s in _wf()["jobs"]["update"]["steps"] if s.get("name") == "Resolve tag"][0]["run"]
    results = []
    for tag in ["v1.2.3", "mate-v0.2.36", "1.0.0"]:
        code = step.replace("${{ github.event.release.tag_name || inputs.tag }}", tag)
        out = os.path.join(d, "out.txt")
        env = dict(os.environ, GITHUB_OUTPUT=out)
        r = subprocess.run(["bash", "-e", "-c", code], cwd=d, env=env, capture_output=True, text=True)
        got = ""
        if os.path.exists(out):
            lines = [l for l in open(out).read().splitlines() if l.startswith("tag=")]
            got = lines[0].split("=", 1)[1] if lines else ""
            os.remove(out)
        results.append((tag, r.returncode, got, tag))
    check("W2 tag 原样透传（版本取包内 manifest，不再解析）",
          all(rc == 0 and got == want for _, rc, got, want in results), results)
    code = step.replace("${{ github.event.release.tag_name || inputs.tag }}", "")
    r = subprocess.run(["bash", "-e", "-c", code], cwd=d,
                       env=dict(os.environ, GITHUB_OUTPUT=os.path.join(d, "o.txt")),
                       capture_output=True, text=True)
    check("W2b 空 tag 明确失败", r.returncode != 0, r.stderr[-120:])


def w3():
    d = _wf()
    txt = json.dumps(d, ensure_ascii=False)
    check("W3 有「资产为空」护栏", "release 里没有匹配" in txt and "exit 1" in txt, "缺少空资产护栏")
    check("W3b 脚本获取有 vendor/raw/jsdelivr/gh-proxy 四层",
          all(x in txt for x in ("tools/update_moo_json.py", "raw.githubusercontent.com", "jsdelivr", "gh-proxy.com")), "")


def _git(cmd, cwd):
    return subprocess.run(cmd, cwd=cwd, shell=True, capture_output=True, text=True)


def _repo(path):
    shutil.rmtree(path, ignore_errors=True)
    os.makedirs(path)
    _git("git init -q --bare origin.git", path)
    w = os.path.join(path, "work"); os.makedirs(w)
    _git("git init -q -b main", w)
    _git("git config user.email t@e.com && git config user.name t", w)
    _git("git remote add origin ../origin.git", w)
    doc = {"schema_version": "moo", "apps": {"dog-dev": {"display_name": "D", "version": "1.3.11",
           "download_url": "https://x/1.3.11.fpk", "sha256": S, "size": 1,
           "releases": {"1.3.11": {"packages": {"x86": pkgj(S, 1, "https://x/1.3.11.fpk")}}}}}}
    write_json(w, doc)
    _git("git add -A && git commit -qm init && git push -q origin HEAD:main", w)
    return w


def _commit_block(version):
    d = _wf()
    step = [s for s in d["jobs"]["update"]["steps"] if s.get("name") == "Commit if changed"][0]["run"]
    return step.replace("${{ steps.t.outputs.version }}", version).replace(
        "${{ steps.t.outputs.tag }}", version)


def w4():
    w = _repo(os.path.join(CASES, "w4"))
    r = _git("MOO_JSON=moo.json BRANCH=main bash -e -c %s" % shlex.quote(_commit_block("1.3.11")), w)
    log = _git("git log --oneline", w).stdout.strip().splitlines()
    check("W4 无变化时跳过提交", r.returncode == 0 and "无变化" in r.stdout and len(log) == 1, (r.stdout[-200:], log))


def w5():
    w = _repo(os.path.join(CASES, "w5"))
    hook = os.path.join(CASES, "w5", "origin.git", "hooks", "pre-receive")
    os.makedirs(os.path.dirname(hook), exist_ok=True)
    state = os.path.join(CASES, "w5", "once")
    open(hook, "w").write('#!/bin/sh\nif [ ! -f %s ]; then touch %s; echo reject-first >&2; exit 1; fi\nexit 0\n' % (state, state))
    os.chmod(hook, 0o755)
    doc = json.load(open(os.path.join(w, "moo.json"), encoding="utf-8"))
    doc["apps"]["dog-dev"]["version"] = "1.3.12"
    write_json(w, doc)
    r = _git("MOO_JSON=moo.json BRANCH=main bash -e -c %s" % shlex.quote(_commit_block("1.3.12")), w)
    log = _git("git --git-dir=../origin.git log --oneline main", w).stdout.strip().splitlines()
    check("W5 push 失败后 rebase 重试成功", r.returncode == 0 and len(log) == 2, (r.stdout[-300:], r.stderr[-300:], log))


if __name__ == "__main__":
    only = sys.argv[1:] or None
    fns = [e1, e2, e3, e4, e5, e6, e7, e8, e9, e10, e11, e12, e13, e14, e15, e16, e17, e18, e19, e20,
           e21, e22, e23, e24, e25, e26, e27, e28, e29, e30, e31, e32, e33, e34, e35, e36, e37, e38, e39, e40, w1, w2, w3, w4, w5]
    for fn in fns:
        if only and fn.__name__ not in only:
            continue
        try:
            fn()
        except Exception as ex:
            check(fn.__name__ + " 崩溃", False, repr(ex))
    bad = [r for r in RES if not r[1]]
    print("\n=== extra: %d 项，%d PASS，%d FAIL ===" % (len(RES), len(RES) - len(bad), len(bad)))
    for n, _, det in bad:
        print("FAIL:", n, "|", det)
    sys.exit(1 if bad else 0)
