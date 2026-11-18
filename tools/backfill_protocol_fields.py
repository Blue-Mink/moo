#!/usr/bin/env python3
"""回填 mate 索引的协议字段（readme_url / releases / changelog）。

背景：mate 的 index.json 与默认清单里，部分应用只有顶层 version/download_url/sha256，
缺 readme_url / releases / changelog → Moo 详情页没有 README 与更新日志。
本脚本复用 sync_upstream_defaults.probe_repo 的探测逻辑，**只补这三个字段**，
不动 version / sha256 / size 等核心字段（最小改动）。

用法：
  python3 tools/backfill_protocol_fields.py --target /path/index.json            # dry-run
  python3 tools/backfill_protocol_fields.py --target /path/index.json --apply     # 写盘（自动备份）
  可选：--keys dpanel,istoreos   --no-backup
"""
import argparse
import json
import os
import re
import shutil
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sync_upstream_defaults as S  # noqa: E402

FIELDS = ("readme_url", "releases", "changelog")

_IMG = re.compile(r"^!\[")  # 上游行常被截断，闭合括号可能缺失
_BARE_URL = re.compile(r"^https?://\S+$")


def post_clean(text, ver=""):
    """二次清洗：剔除 markdown 图片行与裸链接行，最多保留 6 行。

    上游 notes 常夹带资产徽章 / 裸链接（istoreos 全是 shields.io 图片），
    直接进 changelog 会让详情页一团糟。清洗后为空则退化为「版本更新」一句话。
    """
    kept = []
    for line in (text or "").splitlines():
        s = line.strip()
        if not s or _IMG.match(s) or _BARE_URL.match(s):
            continue
        kept.append(line)
    if not kept:
        return ("%s 版本更新" % ver) if ver else ""
    return "\n".join(kept[:6])


def scrub(obj):
    """脱敏自检：补丁里不得出现内网地址 / 本机路径 / PAT。"""
    blob = json.dumps(obj, ensure_ascii=False)
    hits = [name for name, pat in S.FORBID.items() if re.search(pat, blob)]
    return hits


def build_patch(doc, keys, token):
    apps = doc.get("apps") or {}
    patch, problems = {}, {}
    for key in keys:
        cur = apps.get(key)
        if not isinstance(cur, dict):
            problems[key] = "目标清单里没有这个应用"
            continue
        repo = S.UPSTREAM.get(key)
        if not repo:
            problems[key] = "不在上游白名单"
            continue
        try:
            target, drift = S.probe_repo(key, repo, cur, token)
        except Exception as e:  # 网络/资产异常不应中断其它应用
            problems[key] = str(e)[:160]
            continue
        add = {k: target[k] for k in FIELDS if target.get(k)}
        ver = target.get("version") or ""
        if add.get("changelog"):
            add["changelog"] = post_clean(add["changelog"], ver)
        for node in (add.get("releases") or {}).values():
            if isinstance(node, dict) and node.get("changelog"):
                node["changelog"] = post_clean(node["changelog"], ver)
        if not add:
            problems[key] = "上游未提供任何可补字段"
            continue
        patch[key] = {"add": add, "drift": drift}
    return patch, problems


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--target", required=True, help="要回填的索引 JSON 路径")
    ap.add_argument("--keys", default="", help="逗号分隔的应用 key，默认全部白名单")
    ap.add_argument("--apply", action="store_true", help="真正写盘（默认只预览）")
    ap.add_argument("--no-backup", action="store_true")
    args = ap.parse_args()

    doc = S.load_index(args.target)
    keys = [k for k in (args.keys.split(",") if args.keys else S.UPSTREAM.keys()) if k]
    token = S.get_token()
    patch, problems = build_patch(doc, keys, token)

    print("== 补丁预览 (%s) ==" % os.path.basename(args.target))
    for key, item in patch.items():
        add = item["add"]
        rels = sorted((add.get("releases") or {}).keys(), key=S.vkey, reverse=True)
        cl = add.get("changelog") or ""
        print(" %-24s readme=%s releases=%s changelog=%d行/%d字" % (
            key, "✓" if add.get("readme_url") else "×", rels,
            len(cl.splitlines()), len(cl)))
        for d in item["drift"]:
            print("    漂移: %s %s -> %s" % d)
    for key, why in problems.items():
        print(" ! %-22s 跳过：%s" % (key, why))
    hits = scrub(patch)
    print(" 脱敏自检:", "PASS" if not hits else "FAIL %s" % hits)
    if hits:
        sys.exit(2)
    if not args.apply:
        print("\n(dry-run) 加 --apply 才会写盘。")
        return
    if not patch:
        print("没有可写的补丁。")
        return

    backup = ""
    if not args.no_backup:
        backup = "%s.bak-%s" % (args.target, time.strftime("%Y%m%d-%H%M%S"))
        shutil.copy2(args.target, backup)
        print("备份:", backup)

    before = json.loads(json.dumps(doc))
    for key, item in patch.items():
        doc["apps"][key].update(item["add"])

    # 自证：除三个字段外，其它字段一字未动
    diff_other = []
    for key, item in patch.items():
        for f in set(before["apps"][key]) | set(doc["apps"][key]):
            if f in FIELDS:
                continue
            if before["apps"][key].get(f) != doc["apps"][key].get(f):
                diff_other.append("%s.%s" % (key, f))
    if diff_other:
        print("!! 非目标字段被改动，放弃写盘:", diff_other)
        sys.exit(3)

    tmp = args.target + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=False, indent=1)
        f.write("\n")
    os.replace(tmp, args.target)
    after = S.load_index(args.target)
    ok = all((after["apps"][k].get(f) == v) for k, item in patch.items() for f, v in item["add"].items())
    print("写盘完成:", args.target, "| 回读校验:", "PASS" if ok else "FAIL")
    if not ok:
        sys.exit(4)


if __name__ == "__main__":
    main()
