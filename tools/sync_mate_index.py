#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""sync_mate_index.py — Moo Mate 发版索引一键跟版。

单一事实源 = 构建产物 FPK 本身（字节数 + sha256），不再手抄数字。

一次更新三处：
  1) moo-325/moo.json            apps.moo-mate（顶层字段 + releases 两版制）
  2) FnDepot_repo/fnpack.json    apps.moo-mate.releases（V2 无顶层版本字段）
  3) mate-changelog.md           两份（_about_lists/ 与 moo-325/）

用法：
  # 预览（默认 dry-run）
  python3 tools/sync_mate_index.py --version 0.2.50 --prev 0.2.36 --changelog-file ../_mate_changelog.txt
  # 写入
  python3 tools/sync_mate_index.py --version 0.2.50 --prev 0.2.36 --changelog-file ../_mate_changelog.txt --write
  # 校验（发版门禁）：索引与磁盘 FPK / 侧车不一致则退出 1
  python3 tools/sync_mate_index.py --version 0.2.50 --check
"""
import argparse
import hashlib
import json
import os
import subprocess
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WS = os.path.dirname(REPO_ROOT)
BASE_URL = "https://github.com/Blue-Mink/moo/releases/download/mate-v%s"
ARCHS = ("x86", "arm")


def die(msg):
    print("错误：" + msg, file=sys.stderr)
    raise SystemExit(2)


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def mb(n):
    return round(n / (1024.0 * 1024.0), 2)


def read_json(path):
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except OSError as e:
        die("读取 %s 失败：%s" % (path, e))
    except ValueError as e:
        die("%s 不是合法 JSON：%s" % (path, e))


def write_json(path, doc, indent):
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=False, indent=indent)
        f.write("\n")
    os.replace(tmp, path)
    return os.path.getsize(path)


def ver_key(v):
    """版本排序键：0.2.10 > 0.2.9。"""
    import re
    nums = [int(x) for x in re.findall(r"\d+", v)[:4]]
    nums += [0] * (4 - len(nums))
    return tuple(nums)


def fpk_facts(path):
    """读取 FPK 事实：字节数 + sha256，并与同名 .sha256 侧车交叉校验。"""
    if not os.path.isfile(path):
        die("FPK 不存在：%s" % path)
    size = os.path.getsize(path)
    sha = sha256_file(path)
    side = path + ".sha256"
    if os.path.isfile(side):
        with open(side, encoding="utf-8") as f:
            first = (f.readline() or "").split()
        if first and first[0].lower() != sha:
            die("侧车 sha256 与 FPK 不一致：%s\n  侧车=%s\n  实测=%s" % (side, first[0], sha))
    return {"size": size, "sha256": sha, "size_mb": mb(size)}


def pkg(base, ver, arch, facts):
    return {
        "download_url": "%s/moo-mate_%s_%s.fpk" % (base, ver, arch),
        "sha256": facts["sha256"],
        "size": facts["size"],
        "size_mb": facts["size_mb"],
    }


def real_release_versions(app):
    """索引里可保留的历史版本：同时具备 x86/arm packages 的版本。"""
    out = []
    for v, rel in (app.get("releases") or {}).items():
        pk = (rel or {}).get("packages") or {}
        if all(a in pk for a in ARCHS):
            out.append(v)
    return sorted(out, key=ver_key)


def pick_prev(app, ver, explicit):
    """选第二版：显式 --prev 优先；否则取 < ver 的最高「完整」历史版本。"""
    have = [v for v in real_release_versions(app) if ver_key(v) < ver_key(ver)]
    if explicit:
        if explicit not in have:
            die("--prev %s 不在索引的完整历史版本里：%s" % (explicit, have))
        return explicit
    return have[-1] if have else None


def build_releases(app, ver, prev, base, pkgs, changelog_lines, updated_at):
    """两版制 releases（新版本在前，随后是上一已发布版）——完整保留旧节点原值。"""
    old = app.get("releases") or {}
    new = {}
    new[ver] = {
        "updated_at": updated_at,
        "changelog": "\n".join(changelog_lines),
        "packages": {a: pkg(base, ver, a, pkgs[a]) for a in ARCHS},
    }
    if prev:
        if prev not in old:
            die("索引里缺少上一版节点 %s，无法保留" % prev)
        new[prev] = old[prev]
    return new


def plan_moo_json(doc, ver, prev, pkgs, changelog_lines, updated_at):
    app = doc["apps"]["moo-mate"]
    base = BASE_URL % ver
    before = json.dumps(app, ensure_ascii=False, sort_keys=True)
    app["version"] = ver
    app["download_url"] = "%s/moo-mate_%s_x86.fpk" % (base, ver)
    app["size"] = pkgs["x86"]["size"]
    app["size_mb"] = pkgs["x86"]["size_mb"]
    app["sha256"] = pkgs["x86"]["sha256"]
    app["updated_at"] = updated_at
    app["platform"] = list(ARCHS)
    app["changelog"] = ""
    app["releases"] = build_releases(app, ver, prev, base, pkgs, changelog_lines, updated_at)
    after = json.dumps(app, ensure_ascii=False, sort_keys=True)
    return before != after


def plan_fnpack(doc, ver, prev, pkgs, changelog_lines, updated_at):
    app = doc["apps"]["moo-mate"]
    base = BASE_URL % ver
    before = json.dumps(app.get("releases") or {}, ensure_ascii=False, sort_keys=True)
    app["releases"] = build_releases(app, ver, prev, base, pkgs, changelog_lines, updated_at)
    after = json.dumps(app["releases"], ensure_ascii=False, sort_keys=True)
    return before != after


def sync_changelog_md(repo_root, ws, do_write):
    """调用 tools/update_mate_changelog.py 生成两份 mate-changelog.md。"""
    tool = os.path.join(repo_root, "tools", "update_mate_changelog.py")
    if not os.path.isfile(tool):
        return ["跳过 mate-changelog：未找到 %s" % tool]
    outs = [
        os.path.join(ws, "_about_lists", "mate-changelog.md"),
        os.path.join(repo_root, "mate-changelog.md"),
    ]
    notes = []
    for out in outs:
        cmd = [sys.executable, tool, "--out", out] + (["--write"] if do_write else [])
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode != 0:
            die("生成 %s 失败：%s" % (out, (r.stderr or r.stdout).strip()[:400]))
        notes.append("%s %s" % ("写入" if do_write else "预览", out))
    return notes


def check_all(moo_doc, fen_doc, ver, pkgs, keep):
    """校验索引与磁盘 FPK 是否一致；返回失败项列表。"""
    fails = []
    base = BASE_URL % ver
    app = (moo_doc.get("apps") or {}).get("moo-mate") or {}
    if app.get("version") != ver:
        fails.append("moo.json version=%s，期望 %s" % (app.get("version"), ver))
    if app.get("download_url") != "%s/moo-mate_%s_x86.fpk" % (base, ver):
        fails.append("moo.json download_url=%s" % app.get("download_url"))
    exp_size = pkgs["x86"]["size"]
    exp_sha = pkgs["x86"]["sha256"]
    if app.get("size") != exp_size:
        fails.append("moo.json size=%s，期望 %s" % (app.get("size"), exp_size))
    if app.get("size_mb") != pkgs["x86"]["size_mb"]:
        fails.append("moo.json size_mb=%s，期望 %s" % (app.get("size_mb"), pkgs["x86"]["size_mb"]))
    if (app.get("sha256") or "").lower() != exp_sha:
        fails.append("moo.json sha256=%s，期望 %s" % ((app.get("sha256") or "")[:16], exp_sha[:16]))
    for label, doc in (("moo.json", moo_doc), ("fnpack.json", fen_doc)):
        rel = (((doc.get("apps") or {}).get("moo-mate") or {}).get("releases") or {})
        node = rel.get(ver) or {}
        pks = node.get("packages") or {}
        for a in ARCHS:
            got = pks.get(a) or {}
            want = pkgs[a]
            if got.get("sha256") != want["sha256"] or got.get("size") != want["size"]:
                fails.append("%s releases.%s.packages.%s 与 FPK 不一致" % (label, ver, a))
            if got.get("download_url") != "%s/moo-mate_%s_%s.fpk" % (base, ver, a):
                fails.append("%s releases.%s.packages.%s download_url 异常" % (label, ver, a))
        if len(rel) > keep:
            fails.append("%s releases 有 %d 个版本，超过 keep=%d" % (label, len(rel), keep))
        if not node.get("changelog"):
            fails.append("%s releases.%s 缺 changelog" % (label, ver))
    return fails


def main():
    ap = argparse.ArgumentParser(description="Moo Mate 发版索引一键跟版（moo.json + fnpack.json + mate-changelog）")
    ap.add_argument("--version", default="", help="新版本号；缺省取 fnos-mate/manifest（单一事实源）")
    ap.add_argument("--prev", default="", help="保留的上一已发布版本；缺省取索引里 < 新版 的最高完整版本")
    ap.add_argument("--x86", default="", help="x86 FPK 路径（默认 <repo>/moo-mate_<ver>_x86.fpk）")
    ap.add_argument("--arm", default="", help="arm FPK 路径（默认 <repo>/moo-mate_<ver>_arm.fpk）")
    ap.add_argument("--moo-json", default=os.path.join(REPO_ROOT, "moo.json"))
    ap.add_argument("--fnpack-json", default=os.path.join(WS, "FnDepot_repo", "fnpack.json"))
    ap.add_argument("--changelog", action="append", default=[], help="更新日志一行（可重复）")
    ap.add_argument("--changelog-file", default="", help="从文件读更新日志（一行一条）")
    ap.add_argument("--updated-at", default="", help="ISO 时间；缺省=当前本地时间")
    ap.add_argument("--keep", type=int, default=2, help="releases 保留版本数（默认 2）")
    ap.add_argument("--check", action="store_true", help="只校验索引与磁盘 FPK 是否一致")
    ap.add_argument("--write", action="store_true", help="真正写入（缺省 dry-run）")
    args = ap.parse_args()

    ver = args.version.strip()
    if not ver:
        man = os.path.join(REPO_ROOT, "fnos-mate", "manifest")
        try:
            with open(man, encoding="utf-8") as f:
                for line in f:
                    if line.strip().startswith("version"):
                        ver = line.split("=", 1)[1].strip()
                        break
        except OSError as e:
            die("读取 manifest 失败：%s" % e)
        if not ver:
            die("manifest 里没有 version，也未提供 --version")
        print("版本取自 manifest：%s" % ver)
    fpk = {
        "x86": args.x86 or os.path.join(REPO_ROOT, "moo-mate_%s_x86.fpk" % ver),
        "arm": args.arm or os.path.join(REPO_ROOT, "moo-mate_%s_arm.fpk" % ver),
    }
    pkgs = {a: fpk_facts(p) for a, p in fpk.items()}
    for a in ARCHS:
        print("%s: %s  %d B  %s" % (a, os.path.basename(fpk[a]), pkgs[a]["size"], pkgs[a]["sha256"][:16]))

    moo_doc = read_json(args.moo_json)
    fen_doc = read_json(args.fnpack_json)

    if args.check:
        fails = check_all(moo_doc, fen_doc, ver, pkgs, args.keep)
        if fails:
            print("\n".join("FAIL " + f for f in fails))
            print("索引校验：%d 项不一致" % len(fails))
            raise SystemExit(1)
        print("索引校验：PASS（moo.json / fnpack.json 与磁盘 FPK 一致）")
        return

    lines = [x.strip() for x in args.changelog if x.strip()]
    if args.changelog_file:
        try:
            with open(args.changelog_file, encoding="utf-8") as f:
                lines += [x.strip() for x in f if x.strip()]
        except OSError as e:
            die("读取 --changelog-file 失败：%s" % e)
    if not lines:
        die("缺少更新日志：用 --changelog 或 --changelog-file 提供")

    updated_at = args.updated_at
    if not updated_at:
        from datetime import datetime
        updated_at = datetime.now().astimezone().replace(microsecond=0).isoformat()

    prev = pick_prev(moo_doc["apps"]["moo-mate"], ver, args.prev)
    print("版本：%s（上一版保留：%s）" % (ver, prev or "无"))

    changed_moo = plan_moo_json(moo_doc, ver, prev, pkgs, lines, updated_at)
    changed_fen = plan_fnpack(fen_doc, ver, prev, pkgs, lines, updated_at)
    print("moo.json  %s" % ("有变更" if changed_moo else "无变更"))
    print("fnpack.json  %s" % ("有变更" if changed_fen else "无变更"))

    if not args.write:
        print("dry-run：未写入（加 --write 生效）")
        for n in sync_changelog_md(REPO_ROOT, WS, False):
            print("  " + n)
        return

    n1 = write_json(args.moo_json, moo_doc, 1)
    n2 = write_json(args.fnpack_json, fen_doc, 2)
    print("已写入 %s（%d B）" % (args.moo_json, n1))
    print("已写入 %s（%d B）" % (args.fnpack_json, n2))
    for n in sync_changelog_md(REPO_ROOT, WS, True):
        print("  " + n)


if __name__ == "__main__":
    main()
