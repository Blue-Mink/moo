#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""sync_about_changelog.py — 定时把「已发布索引」的最近两版日志同步到 Gitea changelog.md。

发版后 GitHub 上的 moo.json 就是权威索引；本脚本拉它、取最近两版 changelog、
与 Gitea 清单仓 changelog.md 比对，不同则推送。由 crontab 每 15 分钟跑一次，
发版后无需再手工执行 update_about_changelog.py（药丸弹窗只读这份文件的第一段，
所以一处更新两处生效）。

凭据（按优先级）：
  GITEA_TOKEN  >  GITEA_USER + GITEA_PASSWORD  >  ~/.moo-gitea-cred（两行：用户名/口令，0600）
"""
import argparse
import base64
import datetime
import json
import os
import re
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import update_about_changelog as gen  # noqa: E402

DEFAULT_SOURCES = [
    "https://raw.githubusercontent.com/Blue-Mink/moo/main/moo.json",
    "https://cdn.jsdelivr.net/gh/Blue-Mink/moo@main/moo.json",
    "https://gh-proxy.com/https://raw.githubusercontent.com/Blue-Mink/moo/main/moo.json",
    "https://raw.gitmirror.com/Blue-Mink/moo/main/moo.json",
]


def fetch(url, timeout=12):
    import urllib.request
    req = urllib.request.Request(url, headers={"User-Agent": "moo-about-sync/1"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.read().decode("utf-8", "replace")


def load_creds():
    if os.environ.get("GITEA_TOKEN", "").strip():
        return
    if os.environ.get("GITEA_USER", "").strip() and os.environ.get("GITEA_PASSWORD", ""):
        return
    path = os.path.expanduser("~/.moo-gitea-cred")
    if os.path.isfile(path):
        lines = [l.strip() for l in open(path, encoding="utf-8").read().splitlines() if l.strip()]
        if len(lines) >= 2:
            os.environ["GITEA_USER"], os.environ["GITEA_PASSWORD"] = lines[0], lines[1]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep", type=int, default=2)
    ap.add_argument("--source", action="append", default=[])
    ap.add_argument("--repo", default="bluemink/moo-lists")
    ap.add_argument("--branch", default="main")
    ap.add_argument("--api-base", default=os.environ.get("GITEA_URL", "http://127.0.0.1:3033"))
    ap.add_argument("--dry", action="store_true")
    ap.add_argument("--quiet", action="store_true")
    a = ap.parse_args()

    load_creds()
    if not (os.environ.get("GITEA_TOKEN") or (os.environ.get("GITEA_USER") and os.environ.get("GITEA_PASSWORD"))):
        print("FAIL 缺 Gitea 凭据（GITEA_TOKEN / GITEA_USER+GITEA_PASSWORD / ~/.moo-gitea-cred）")
        return 2

    sources = a.source or DEFAULT_SOURCES
    body, src = None, ""
    for u in sources:
        try:
            cand = fetch(u)
        except Exception as e:
            if not a.quiet:
                print("WARN %s: %s" % (u, e))
            continue
        if not cand.strip():
            if not a.quiet:
                print("WARN %s: 响应为空，换下一个源" % u)
            continue
        try:
            parsed = json.loads(cand)
        except ValueError as e:
            if not a.quiet:
                print("WARN %s: 内容不是 JSON（%s），换下一个源" % (u, e))
            continue
        if not isinstance(parsed, dict) or not isinstance(parsed.get("apps"), dict):
            if not a.quiet:
                print("WARN %s: 不是 moo.json 结构（缺 apps），换下一个源" % u)
            continue
        body, src = cand, u
        break
    if not body:
        print("FAIL 拉取远程 moo.json 失败（%d 个候选均不可用：网络失败/空响应/非 JSON）" % len(sources))
        return 1

    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as f:
        f.write(body)
        tmp = f.name
    try:
        want, used = gen.build_changelog(tmp, a.keep)
    finally:
        os.unlink(tmp)
    if not want:
        print("FAIL 远程 moo.json 没有可用 changelog")
        return 1

    # 防回退：远程索引比清单现有最新版还旧时跳过（发版前索引尚未跟上，
    # 否则 cron 会把刚发布的 changelog.md 覆盖回上一版）。
    headers = gen._auth_header()
    url = "%s/api/v1/repos/%s/contents/changelog.md" % (a.api_base.rstrip("/"), a.repo)
    status, cur = gen._req(url + "?ref=" + a.branch, headers=headers)
    if status not in (200, 404):
        print("FAIL 读取远端 changelog.md 失败 HTTP %s %s" % (status, cur.get("error", "")))
        return 1
    have = ""
    if status == 200:
        try:
            have = base64.b64decode(cur.get("content") or "").decode("utf-8")
        except Exception:
            have = ""
    m = re.search(r"(?m)^#{1,4}\s*[vV]?(\d+\.\d+(?:\.\d+)?)", have)
    if m and gen.ver_key(m.group(1)) > gen.ver_key(used[0]):
        if not a.quiet:
            print("跳过：清单现有最新版 %s 高于远程索引 %s（不回退）" % (m.group(1), used[0]))
        return 0

    args = argparse.Namespace(repo=a.repo, branch=a.branch, remote_path="changelog.md",
                              api_base=a.api_base, message="chore(changelog): 同步最近两个版本的更新日志")
    if a.dry:
        print("--- dry run：%s / 版本 %s ---" % (src, " / ".join(used)))
        print(want)
        return 0

    rc = gen.push(args, want, quiet=a.quiet)
    if rc == 0 and not a.quiet:
        print("来源 %s；版本 %s" % (src, " / ".join(used)))
    elif rc != 0:
        print("FAIL 同步失败（来源 %s，版本 %s）" % (src, " / ".join(used)))
    return rc


if __name__ == "__main__":
    # 心跳行：无论有无变化都写一行（含时间戳），用于 cron 健康检查。
    _rc = main()
    print("[%s] DONE rc=%d" % (datetime.datetime.now().strftime("%Y-%m-%d %H:%M:%S"), _rc), flush=True)
    sys.exit(_rc)
