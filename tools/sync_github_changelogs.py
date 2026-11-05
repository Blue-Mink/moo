#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""sync_github_changelogs.py — 把最近两版更新日志同步到 GitHub 仓根。

Moo 生产 `about_lists_url` 未配置时读 GitHub 仓根（Blue-Mink/moo@main）：
  changelog.md      <- apps.moo.releases
  mate-changelog.md <- apps.moo-mate.releases

流程：拉 moo.json（raw / jsDelivr / gh-proxy / raw.gitmirror）→ 生成两份 →
与 GitHub Contents API 上的现有文件比对 → 不同才 PUT。
内容有变化时同时写本地仓库同名文件（保持工作树与远端一致，避免下次
发版把旧内容又提交回去）。

凭据：GITHUB_TOKEN / GH_TOKEN  >  ~/.moo-gh-token（0600）

用法：
  python3 tools/sync_github_changelogs.py --dry
  python3 tools/sync_github_changelogs.py --quiet     # cron 用
"""
import argparse
import atexit
import base64
import datetime
import json
import os
import re
import shutil
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)                      # moo-325/
sys.path.insert(0, HERE)

import update_about_changelog as gen_moo          # noqa: E402
import update_mate_changelog as gen_mate          # noqa: E402

DEFAULT_SOURCES = [
    "https://raw.githubusercontent.com/Blue-Mink/moo/main/moo.json",
    "https://cdn.jsdelivr.net/gh/Blue-Mink/moo@main/moo.json",
    "https://gh-proxy.com/https://raw.githubusercontent.com/Blue-Mink/moo/main/moo.json",
    "https://raw.gitmirror.com/Blue-Mink/moo/main/moo.json",
]

TARGETS = [
    # (仓内路径, 本地文件名, 生成器类型)
    ("changelog.md", "changelog.md", "moo"),
    ("mate-changelog.md", "mate-changelog.md", "mate"),
]


def load_token():
    for k in ("GITHUB_TOKEN", "GH_TOKEN"):
        v = (os.environ.get(k) or "").strip()
        if v:
            return v
    p = os.path.join(os.path.expanduser("~"), ".moo-gh-token")
    try:
        with open(p, encoding="utf-8") as f:
            return f.read().strip()
    except OSError:
        return ""


def fetch(url, timeout=15):
    import urllib.request
    req = urllib.request.Request(url, headers={"User-Agent": "moo-changelog-sync/1"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.read().decode("utf-8", "replace")


def ver_key(v):
    """版本排序键（与 update_mate_changelog 同口径，数字段补 4 位）。"""
    return gen_mate.ver_key(v)


def first_version(text):
    """取 markdown 里第一个 `## <ver>` 的版本号。"""
    for line in (text or "").splitlines():
        m = re.match(r"^##\s+(\S+)\s*$", line.strip())
        if m:
            return m.group(1)
    return ""


def app_version(data, name):
    """应用的当前版本：顶层 version 优先，缺失则取 releases 里最大的键。"""
    app = (data.get("apps") or {}).get(name) or {}
    if not isinstance(app, dict):
        return ""
    v = (app.get("version") or "").strip()
    rels = app.get("releases")
    if isinstance(rels, dict) and rels:
        latest = max(rels.keys(), key=ver_key)
        if not v or ver_key(latest) > ver_key(v):
            v = latest
    return v


def pick_source(cands, kind):
    """在全部可达源里挑该 target 版本最高的那份，避免 CDN 旧缓存把内容回退。"""
    best = None
    for path, moo_ver, mate_ver, url in cands:
        ver = moo_ver if kind == "moo" else mate_ver
        if not ver:
            continue
        if best is None or ver_key(ver) > ver_key(best[1]):
            best = (path, ver, url)
    return best


def gh(method, path, token, body=None, api_base="https://api.github.com",
       repo="Blue-Mink/moo", timeout=25):
    """调 GitHub REST；返回 (status, json_or_text)。"""
    import urllib.error
    import urllib.request
    url = "%s/repos/%s/contents/%s" % (api_base.rstrip("/"), repo, path)
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers={
        "Authorization": "Bearer " + token,
        "Accept": "application/vnd.github+json",
        "Content-Type": "application/json",
        "User-Agent": "moo-changelog-sync/1",
    })
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read()
            try:
                return r.status, json.loads(raw)
            except ValueError:
                return r.status, raw.decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, raw.decode("utf-8", "replace")
    except Exception as e:                                    # noqa: BLE001
        return 0, {"error": "%s: %s" % (type(e).__name__, e)}


def build(kind, moo_json, keep):
    if kind == "moo":
        return gen_moo.build_changelog(moo_json, keep)
    return gen_mate.build_changelog(moo_json, keep)


def main():
    ap = argparse.ArgumentParser(description="同步 changelog.md / mate-changelog.md 到 GitHub 仓根")
    ap.add_argument("--repo", default="Blue-Mink/moo")
    ap.add_argument("--branch", default="main")
    ap.add_argument("--keep", type=int, default=2)
    ap.add_argument("--api-base", default="https://api.github.com")
    ap.add_argument("--source", default="", help="指定 moo.json URL（默认多源自动选择）")
    ap.add_argument("--no-local", action="store_true", help="不写本地仓库文件")
    ap.add_argument("--allow-downgrade", action="store_true",
                    help="允许把远端日志降到更旧版本（默认拒绝，防 CDN 旧缓存回退）")
    ap.add_argument("--dry", action="store_true")
    ap.add_argument("--quiet", action="store_true")
    a = ap.parse_args()

    token = load_token()
    if not token:
        print("FAIL 缺 GitHub token（GITHUB_TOKEN / GH_TOKEN / ~/.moo-gh-token）")
        return 1

    sources = [a.source] if a.source else DEFAULT_SOURCES
    tmpdir = tempfile.mkdtemp(prefix="moo-chlog-")
    atexit.register(shutil.rmtree, tmpdir, True)  # cron 每 15 分钟一次，不清理会持续泄漏
    cands = []              # [(tmp_json_path, moo_ver, mate_ver, url)]
    for i, u in enumerate(sources):
        try:
            text = fetch(u)
            data = json.loads(text)
            if not isinstance(data.get("apps"), dict):
                raise ValueError("缺 apps")
            p = os.path.join(tmpdir, "moo-%d.json" % i)
            with open(p, "w", encoding="utf-8") as f:
                f.write(text)
            cands.append((p, app_version(data, "moo"), app_version(data, "moo-mate"), u))
        except Exception as e:                                # noqa: BLE001
            if not a.quiet:
                print("WARN %s: %s" % (u, e))
    if not cands:
        print("FAIL 拉取远程 moo.json 失败（%d 个候选均不可用）" % len(sources))
        return 1

    rc = 0
    for path, local_name, kind in TARGETS:
        best = pick_source(cands, kind)
        if not best:
            print("FAIL %s：所有源都缺 %s 版本信息" % (path, kind))
            rc = 1
            continue
        moo_json, src_ver, used_src = best
        try:
            body, used = build(kind, moo_json, a.keep)
        except SystemExit as e:
            print("FAIL 生成 %s 失败（exit %s）" % (path, e.code))
            rc = 1
            continue
        if not body:
            print("FAIL 生成 %s 为空" % path)
            rc = 1
            continue
        status, cur = gh("GET", "%s?ref=%s" % (path, a.branch), token, api_base=a.api_base, repo=a.repo)
        sha, have = None, ""
        if status == 200 and isinstance(cur, dict):
            sha = cur.get("sha")
            try:
                have = base64.b64decode(cur.get("content") or "").decode("utf-8")
            except Exception:                                 # noqa: BLE001
                have = ""
        elif status not in (404,):
            print("FAIL 读取 %s 失败 HTTP %s %s" % (path, status, str(cur)[:120]))
            rc = 1
            continue
        if have == body:
            if not a.quiet:
                print("跳过 %s（已最新：%s）" % (path, " / ".join(used)))
            continue
        cur_first, new_first = first_version(have), first_version(body)
        if (cur_first and new_first
                and ver_key(new_first) < ver_key(cur_first)
                and not a.allow_downgrade):
            print("WARN 拒绝回退 %s：远端 %s > 生成 %s（来源 %s；确认要降级加 --allow-downgrade）"
                  % (path, cur_first, new_first, used_src))
            continue
        if a.dry:
            print("--- dry run：将更新 %s -> %s（%d 字节） ---" % (path, " / ".join(used), len(body.encode())))
            print(body)
            continue
        payload = {
            "message": "docs(changelog): %s 跟版 %s" % (path, " / ".join(used)),
            "content": base64.b64encode(body.encode()).decode(),
            "branch": a.branch,
        }
        if sha:
            payload["sha"] = sha
        st, resp = gh("PUT", path, token, body=payload, api_base=a.api_base, repo=a.repo)
        if st not in (200, 201):
            print("FAIL 推送 %s 失败 HTTP %s %s" % (path, st, str(resp)[:160]))
            rc = 1
            continue
        print("已更新 %s -> %s（%d 字节，%s）" % (path, " / ".join(used), len(body.encode()), used_src))
        if not a.no_local:
            lp = os.path.join(REPO, local_name)
            try:
                with open(lp, "w", encoding="utf-8") as f:
                    f.write(body)
            except OSError as e:
                print("WARN 写本地 %s 失败：%s" % (lp, e))
    return rc


if __name__ == "__main__":
    # 心跳行：无论有无变化都写一行（含时间戳），用于 cron 健康检查。
    # 缺行 = 进程没跑/异常退出；日志 mtime 停滞超过阈值即视为故障。
    _rc = main()
    print("[%s] DONE rc=%d" % (datetime.datetime.now().strftime("%Y-%m-%d %H:%M:%S"), _rc), flush=True)
    sys.exit(_rc)
