#!/usr/bin/env python3
"""verify_release_contract.py — 版本契约交叉断言（RELEASE_GATE P2）

治的是「同一版本号存在多处，各自漂移」这一类事故：
  · 0.6.369：moo.json 顶层 download_url/sha256 还指 0.6.355（卡片写新版、下的是旧包）
  · 0.6.370：moo.json 顶层 changelog 残留旧文案，盖掉 releases.<ver>.changelog（关于页显示旧日志）
  · 0.6.370~376：七版没打 tag，索引却已前进

设计要点（对齐 fn-knock `release-preflight.sh` 的 assert_equal 思路）：
  * 一次性收集**全部**违规再退出，不是撞到第一个就停（否则修一个冒一个）
  * 分阶段语义：--phase code 断言「索引还没前进」，--phase index 断言「索引已对齐」
  * 任何一条违规 → exit 1；全部通过 → exit 0

用法：
  python3 .github/scripts/verify_release_contract.py --tag 0.6.378 --phase code
  python3 .github/scripts/verify_release_contract.py --tag 0.6.378 --phase index \
      --fndepot ../../../FnDepot_repo
"""
import argparse
import json
import os
import re
import sys

FAILS = []
WARNS = []


def fail(msg):
    FAILS.append(msg)


def warn(msg):
    WARNS.append(msg)


def load_json(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def read_manifest_version(root):
    p = os.path.join(root, "fnos", "manifest")
    try:
        with open(p, encoding="utf-8") as fh:
            for line in fh:
                m = re.match(r"^version\s*=\s*(\S+)", line.strip())
                if m:
                    return m.group(1)
    except OSError as e:
        fail("读不到 fnos/manifest：%s" % e)
    return ""


def pkg_filename(spec, ver, arch):
    return (spec or {}).get("filename") or ("moo_%s_%s.fpk" % (ver, arch))


def check_tag(tag):
    if not re.fullmatch(r"\d+\.\d+\.\d+", tag):
        fail("tag 必须是 X.Y.Z（不带 v），实际 %r" % tag)


def check_manifest(root, ver):
    got = read_manifest_version(root)
    if got != ver:
        fail("fnos/manifest version=%r != tag %r" % (got, ver))


def check_index_phase(app, ver, rels):
    if app.get("version") != ver:
        fail("moo.json apps.moo.version=%r != %r（索引跟版纪律）" % (app.get("version"), ver))
    rel = rels.get(ver) or {}
    if not rel:
        fail("moo.json 缺 releases.%s 节点" % ver)
        return {}
    if not (rel.get("changelog") or "").strip():
        fail("releases.%s.changelog 为空（详情页/关于页会没有本版说明）" % ver)
    if (app.get("changelog") or "").strip():
        warn("moo.json 顶层 changelog 非空（0.6.326 起约定为空，残留会盖掉多版本说明）")
    return rel


def check_packages(rel, ver):
    pkgs = (rel.get("packages") or {})
    if not pkgs:
        fail("releases.%s.packages 为空" % ver)
        return {}
    for arch, spec in sorted(pkgs.items()):
        spec = spec or {}
        name = pkg_filename(spec, ver, arch)
        if not name.startswith("moo_%s_" % ver):
            fail("packages.%s 文件名 %r 不含版本 %s" % (arch, name, ver))
        if not re.fullmatch(r"[0-9a-f]{64}", spec.get("sha256") or ""):
            fail("packages.%s.sha256 非法：%r" % (arch, spec.get("sha256")))
        if not isinstance(spec.get("size"), int) or spec.get("size", 0) <= 0:
            fail("packages.%s.size 非法：%r" % (arch, spec.get("size")))
    return pkgs


def check_top_pointer(app, ver, pkgs):
    url = app.get("download_url") or ""
    base = url.rsplit("/", 1)[-1]
    hit = [a for a, s in pkgs.items() if pkg_filename(s, ver, a) == base]
    if not hit:
        fail("顶层 download_url 指向 %r，不在 packages 的文件名里（0.6.369 事故形态）" % base)
        return
    arch = hit[0]
    spec = pkgs[arch] or {}
    if app.get("sha256") != spec.get("sha256"):
        fail("顶层 sha256=%s… != packages.%s.sha256=%s…"
             % ((app.get("sha256") or "")[:12], arch, (spec.get("sha256") or "")[:12]))
    if app.get("size") != spec.get("size"):
        fail("顶层 size=%r != packages.%s.size=%r" % (app.get("size"), arch, spec.get("size")))


def check_retention(rels, ver):
    keys = sorted(rels.keys())
    if len(keys) > 2:
        fail("releases 保留 %d 个版本 %s（约定只留最新两版）" % (len(keys), keys))
    if ver not in keys:
        fail("releases 里没有本次版本 %s" % ver)


def check_changelog_file(root, ver):
    p = os.path.join(root, "internal", "api", "changelog_current.json")
    try:
        d = load_json(p)
    except (OSError, ValueError) as e:
        fail("读不到 internal/api/changelog_current.json：%s（先跑一次 build.sh）" % e)
        return
    if d.get("version") != ver:
        fail("changelog_current.json version=%r != %r（构建时未重新生成）" % (d.get("version"), ver))
    if not (d.get("changelog") or "").strip():
        fail("changelog_current.json changelog 为空（关于页无本版说明）")


def check_readme(root, ver, pkgs):
    p = os.path.join(root, "README.md")
    try:
        text = open(p, encoding="utf-8").read()
    except OSError as e:
        fail("读不到 README.md：%s" % e)
        return
    if ver not in text:
        fail("README.md 未出现版本号 %s（安装表/徽章没跟版）" % ver)
    for arch, spec in sorted(pkgs.items()):
        sha = spec.get("sha256")
        if sha and sha not in text:
            warn("README.md 未出现 packages.%s 的完整 sha256（安装表可能没跟版）" % arch)


def check_dist(dist, ver, pkgs):
    for arch, spec in sorted(pkgs.items()):
        name = pkg_filename(spec, ver, arch)
        for suf in ("", ".sha256", ".minisig"):
            p = os.path.join(dist, name + suf)
            if not os.path.isfile(p):
                fail("产物缺失：%s" % p)
            elif os.path.getsize(p) == 0:
                fail("产物为空：%s" % p)


def check_fndepot(path, ver, pkgs):
    p = os.path.join(path, "fnpack.json")
    try:
        d = load_json(p)
    except (OSError, ValueError) as e:
        fail("读不到 %s：%s" % (p, e))
        return
    app = (d.get("apps") or {}).get("moo") or {}
    rel = (app.get("releases") or {}).get(ver) or {}
    if not rel:
        fail("FnDepot fnpack.json 缺 apps.moo.releases.%s" % ver)
        return
    fp = rel.get("packages") or {}
    for arch, spec in sorted(pkgs.items()):
        other = fp.get(arch) or {}
        if other.get("sha256") != spec.get("sha256"):
            fail("FnDepot packages.%s.sha256 与 moo.json 不一致" % arch)
        if other.get("size") != spec.get("size"):
            fail("FnDepot packages.%s.size 与 moo.json 不一致" % arch)


def main():
    ap = argparse.ArgumentParser(description="Moo 版本契约交叉断言")
    ap.add_argument("--tag", required=True)
    ap.add_argument("--phase", required=True, choices=["code", "index"])
    ap.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.dirname(
        os.path.abspath(__file__)))))
    ap.add_argument("--dist", default="")
    ap.add_argument("--fndepot", default="")
    a = ap.parse_args()
    ver = a.tag[1:] if a.tag.startswith("v") else a.tag
    root = os.path.abspath(a.root)

    check_tag(ver)
    check_manifest(root, ver)
    try:
        idx = load_json(os.path.join(root, "moo.json"))
    except (OSError, ValueError) as e:
        print("FAIL: 读不到 moo.json：%s" % e)
        return 1
    app = (idx.get("apps") or {}).get("moo") or {}
    rels = app.get("releases") or {}

    if a.phase == "code":
        # 代码阶段：索引必须还没前进（否则就是「索引先于 release」的旧缺陷）
        if app.get("version") == ver:
            fail("phase=code 但 moo.json 顶层已是 %s —— 索引不该在代码阶段前进" % ver)
        if ver in rels:
            warn("phase=code 但 releases 已有 %s 节点（构建期会用到，属正常；推代码时不要带 moo.json）" % ver)
    else:
        rel = check_index_phase(app, ver, rels)
        pkgs = check_packages(rel, ver) if rel else {}
        if pkgs:
            check_top_pointer(app, ver, pkgs)
            check_retention(rels, ver)
            check_changelog_file(root, ver)
            check_readme(root, ver, pkgs)
            if a.dist:
                check_dist(a.dist, ver, pkgs)
            if a.fndepot:
                check_fndepot(a.fndepot, ver, pkgs)

    for w in WARNS:
        print("[warn] " + w)
    for f in FAILS:
        print("FAIL: " + f)
    if FAILS:
        print("契约检查失败：%d 条" % len(FAILS))
        return 1
    print("契约检查通过（phase=%s tag=%s）" % (a.phase, ver))
    return 0


if __name__ == "__main__":
    sys.exit(main())
