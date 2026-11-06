"""变异测试：把被测工具改坏一处，对应测试必须变红（不修改仓库里的源文件）。"""
import os, shutil, subprocess, sys, tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
# 仓库布局（tests/ 向上三级有 moo.json）用 tools/ 源文件；独立安装版退回 bundled/tools/ 副本
# 必须与 harness 选中同一个文件，否则「改了 A 却跑 B」会得到假的 REV-OK/REV-FAIL。
_REPO_ROOT = os.path.abspath(os.path.join(HERE, "..", "..", ".."))
_HAS_REPO = os.path.exists(os.path.join(_REPO_ROOT, "moo.json"))
_CANDS = [os.environ.get("MOO_SCRIPT") or "",
          os.path.join(_REPO_ROOT, "tools", "update_moo_json.py") if _HAS_REPO else "",
          os.path.abspath(os.path.join(HERE, "..", "bundled", "tools", "update_moo_json.py"))]
TOOL = next((p for p in _CANDS if p and os.path.exists(p)), "")
if not TOOL:
    sys.exit("FATAL: 找不到 update_moo_json.py（仓库 tools/ 与 bundled/tools/ 均缺失）")
SRC = open(TOOL, encoding="utf-8").read()
MUT_DIR = tempfile.mkdtemp(prefix="moojson-mut-")
MUT_PATH = os.path.join(MUT_DIR, "update_moo_json.py")
# 注：t2 复用 t1 产出的 cases/t1，单跑 t2 会因缺文件而假红，故 R1 用 t1+t2。
MUT = {
 "R1-幂等": ("if not node.get(\"updated_at\") or json.dumps(node, sort_keys=True, ensure_ascii=False) != prev_sig:", "if True:", ["t1", "t2"]),
 "R5-读权限报错": ("""    try:
        with open(path, "rb") as f:
            for chunk in iter(lambda: f.read(1 << 20), b""):
                h.update(chunk)
    except OSError as e:
        die(f"读 FPK 失败（{path}）：{e}（没有读权限？）")""", """    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)""", ["t16"]),
 "R2-原子写": ("    write_atomic(os.path.realpath(a.file), out)", "    open(a.file, \"w\", encoding=\"utf-8\").write(out)", ["t15"]),
 "R3-非GitHub": ("        base = release_base(repo)", "        base = \"https://github.com/\" + repo", ["t13"]),
 "R4-appname": ("        elif not a.allow_appname_mismatch:", "        elif False:", ["t8"]),
 "R6-缩进": ("indent=detect_indent(text_in)", "indent=2", ["t19"]),
 "R7-size口径": ("entry[\"size\"] = lp[\"size\"] if prev_size >= 1024 else round(lp[\"size\"] / 1e6, 3)", "entry[\"size\"] = round(lp[\"size\"] / 1e6, 3)", ["t1"]),
 "R8-末尾换行": ("""    if raw.endswith(b"\\n"):  # 保持原文件的行尾习惯（有的索引末尾没有换行）
        out += "\\r\\n" if b"\\r\\n" in raw else "\\n\"""", "    out += \"\\n\"", ["t23"]),
 "R9-packages类型": ("        die(f\"apps.{appname}.releases.{version}.packages 不是对象\")", "        pass", ["e20"]),
 "R10-包字段保留": ("        node_pkgs[p[\"arch\"]] = pk", "        node_pkgs[p[\"arch\"]] = {\"download_url\": p[\"url\"], \"sha256\": p[\"sha256\"], \"size\": p[\"size\"], \"size_mb\": round(p[\"size\"] / 1048576, 2)}", ["e26"]),
 "R11-未知架构": ("        if arch not in (\"x86\", \"arm\", \"all\"):", "        if False:", ["e38"]),
}
bad = 0
for name, (a, b, tests) in MUT.items():
    if a not in SRC:
        print("SKIP", name, "锚点未找到"); bad += 1; continue
    with open(MUT_PATH, "w", encoding="utf-8") as f:
        f.write(SRC.replace(a, b))
    env = dict(os.environ, MOO_SCRIPT=MUT_PATH)
    runner = os.path.join(HERE, "moojson_extra.py" if tests[0][0] in "ew" else "moojson_harness.py")
    r = subprocess.run([sys.executable, runner] + tests, capture_output=True,
                       text=True, env=env, cwd=HERE)
    skipped = [t for t in tests if ("SKIP " + t) in (r.stdout or "")]
    if skipped:
        print("SKIP-REV " + name + " → 目标用例在独立安装版被跳过（" + ",".join(skipped) + "）")
        continue
    ok = r.returncode != 0
    print(("REV-OK  " if ok else "REV-FAIL") + " " + name + " → 去掉修复后 " + ",".join(tests) + (" 变红" if ok else " 仍绿"))
    if not ok:
        print((r.stdout or "")[-400:]); bad += 1
shutil.rmtree(MUT_DIR, ignore_errors=True)
sys.exit(1 if bad else 0)
