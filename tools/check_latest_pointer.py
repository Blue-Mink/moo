#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""check_latest_pointer.py — GitHub `releases/latest` 指针守卫（发版前后各跑一次）。

背景：Moo 与 Moo Mate 的 release 同放 `Blue-Mink/moo` 一个仓，而
`/releases/latest` 全仓只有一个指针。mate 建 release 若未显式
`make_latest=false`，会把指针抢走 → Moo 客户端 self_update 主通道
（只认 latest）把 `mate-v0.2.55` 解析成 [0,2,55]，判「无更新」并短路
整条兜底链，HTTP 200 且无 last_error，从外部看一切正常（2026-11-08
实测潜伏 4 天）。

判定：
  - latest tag 必须以 `v` 开头且形如 `v<数字>(.<数字>)*`；
  - 不得是 `mate-v*`；
  - `--expect` 给出时还必须与之一致（默认取 moo.json 的 apps.moo.version）。

用法：
  python3 tools/check_latest_pointer.py                 # 期望值取自 moo.json
  python3 tools/check_latest_pointer.py --expect v0.6.539
  python3 tools/check_latest_pointer.py --list          # 顺带列出最近 8 个 release
退出码：0 = 指针正常；1 = 异常。
"""
import argparse
import json
import os
import re
import sys
import urllib.request

API = "https://api.github.com/repos/Blue-Mink/moo"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def token():
    t = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if t:
        return t.strip()
    for p in (os.path.expanduser("~/.moo-gh-token"),
              os.path.join(os.path.dirname(ROOT), ".gh-token")):
        if os.path.isfile(p):
            return open(p, encoding="utf-8").read().strip()
    return ""


def get(path, tok):
    req = urllib.request.Request(API + path)
    req.add_header("Accept", "application/vnd.github+json")
    req.add_header("X-GitHub-Api-Version", "2022-11-28")
    if tok:
        req.add_header("Authorization", "Bearer " + tok)
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.load(r)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--expect", default="", help="期望的 latest tag，如 v0.6.539")
    ap.add_argument("--list", action="store_true", help="列出最近 8 个 release")
    a = ap.parse_args()

    expect = a.expect
    if not expect:
        try:
            idx = json.load(open(os.path.join(ROOT, "moo.json"), encoding="utf-8"))
            expect = "v" + str(idx["apps"]["moo"]["version"])
        except Exception as e:  # noqa: BLE001
            print(f"[warn] 读 moo.json 取期望版本失败（{e}），仅做形态校验")

    tok = token()
    latest = get("/releases/latest", tok)
    tag = (latest.get("tag_name") or "").strip()
    bad = []
    if tag.startswith("mate-"):
        bad.append(f"latest 被 Mate 抢走（{tag}）→ Moo 客户端检测不到更新")
    elif not re.match(r"^v\d+(\.\d+)*$", tag):
        bad.append(f"latest tag 形态异常（{tag!r}）→ self_update 会静默判无更新")
    if expect and tag != expect:
        bad.append(f"latest={tag} 与期望 {expect} 不一致")

    if a.list:
        for r in get("/releases?per_page=8", tok):
            print(f"  {r['tag_name']:<22} prerelease={r.get('prerelease')} "
                  f"draft={r.get('draft')} at={r.get('created_at')}")

    if bad:
        for b in bad:
            print("FAIL: " + b)
        print("修复：PATCH /repos/Blue-Mink/moo/releases/<id> {\"make_latest\":\"false\"}（mate），"
              "或把 Moo 最新版 PATCH 成 true")
        return 1
    print(f"OK: releases/latest = {tag}（Moo 指针正常）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
