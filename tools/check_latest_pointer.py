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

发布门（2026-11-27 F12 / 审计 N9「索引先于 release」）：
**改 moo.json 的版本项之前**先跑这条；latest 不是本次版本就不许动索引。

  python3 tools/check_latest_pointer.py --require v0.6.578      # 硬门：不一致即 exit 1
  python3 tools/check_latest_pointer.py --require v0.6.578 --dry-run   # 只看要断言什么
  python3 tools/check_latest_pointer.py --require v0.6.578 --latest-tag v0.6.578
      # --latest-tag：不联网，直接用给定值（离线自测/已确知指针时用）

为什么要有这道门：GitHub release 未发布/未接管 latest 时就把 moo.json 写到新版本，
客户端的自更新探测会拿到 0.6.578 的 FPK URL 但资产 404 ——「资产齐全」不等于
「用户拿得到」，索引必须永远晚于 release（AGENTS.md 同名铁律）。

退出码：0 = 通过；1 = 异常（含发布门不通过）；2 = --dry-run（未校验，未知）。
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


def normalize_tag(v):
    """`0.6.578` / ` v0.6.578 ` → `v0.6.578`（--require 两种写法都接受）。"""
    v = (v or "").strip()
    if v and not v.startswith("v"):
        v = "v" + v
    return v


def main():
    ap = argparse.ArgumentParser(description="GitHub releases/latest 指针守卫")
    ap.add_argument("--expect", default="", help="期望的 latest tag，如 v0.6.539")
    ap.add_argument("--list", action="store_true", help="列出最近 8 个 release")
    ap.add_argument("--require", default="",
                    help="发布门：latest 必须等于该 tag（如 v0.6.578）才允许改 moo.json")
    ap.add_argument("--latest-tag", default="",
                    help="直接用给定的 latest tag（不联网；离线自测/已确知指针时用）")
    ap.add_argument("--dry-run", action="store_true",
                    help="只打印将要执行的断言、不联网不校验（退出码 2）")
    a = ap.parse_args()

    require = normalize_tag(a.require)
    expect = normalize_tag(a.expect)

    idx_ver = ""
    try:
        idx = json.load(open(os.path.join(ROOT, "moo.json"), encoding="utf-8"))
        idx_ver = str(idx["apps"]["moo"]["version"])
    except Exception as e:  # noqa: BLE001
        print(f"[warn] 读 moo.json 取期望版本失败（{e}）")
    if not expect and idx_ver:
        expect = "v" + idx_ver
    want = require or expect

    if a.dry_run:
        print("[dry-run] 未联网、未做任何校验（退出码 2 = 未知，不要当通过）")
        print(f"[dry-run] 将要断言：releases/latest == {want or '<未知>'}")
        print(f"[dry-run] moo.json apps.moo.version = {idx_ver or '<读取失败>'}")
        print("[dry-run] 正式用法：python3 tools/check_latest_pointer.py --require v<本次版本>")
        return 2

    if a.latest_tag:
        # 注意：--latest-tag 是「远端事实」，不能再做 v 前缀归一（否则
        # mate-v0.2.55 会被补成 vmate-v0.2.55，形态校验失真）。
        tag = a.latest_tag.strip()
        print(f"[offline] latest tag 取自 --latest-tag：{tag}")
    else:
        tok = token()
        latest = get("/releases/latest", tok)
        tag = (latest.get("tag_name") or "").strip()

    bad = []
    if tag.startswith("mate-"):
        bad.append(f"latest 被 Mate 抢走（{tag}）→ Moo 客户端检测不到更新")
    elif not re.match(r"^v\d+(\.\d+)*$", tag):
        bad.append(f"latest tag 形态异常（{tag!r}）→ self_update 会静默判无更新")
    if require and tag != require:
        bad.append(f"[发布门] latest={tag} != 本次 {require}：release 还没发布或没接管 "
                   "latest，此时改 moo.json 会让客户端拿到 404 的下载链接"
                   "（索引永远晚于 release）")
    elif not require and expect and tag != expect:
        bad.append(f"latest={tag} 与期望 {expect} 不一致")

    if a.list and not a.latest_tag:
        for r in get("/releases?per_page=8", token()):
            print(f"  {r['tag_name']:<22} prerelease={r.get('prerelease')} "
                  f"draft={r.get('draft')} at={r.get('created_at')}")

    if bad:
        for b in bad:
            print("FAIL: " + b)
        print("修复：先把本次 release 发布/转正并 PATCH {\"make_latest\":\"true\"}，"
              "确认 `releases/latest == v<本次>` 之后**再**改 moo.json 的版本项；"
              "mate 的 release 一律 make_latest=false。")
        return 1
    if require:
        print(f"OK(发布门)：releases/latest = {tag} == 本次 {require}，可以改 moo.json")
    else:
        print(f"OK: releases/latest = {tag}（Moo 指针正常）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
