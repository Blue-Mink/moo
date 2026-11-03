#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""update_about_changelog.py — 从 moo.json 生成关于页远程清单 changelog.md（最近 N 版）。

发版 SOP 的一环：moo.json 的 apps.moo.releases 就是唯一事实源，
本脚本按版本号倒序取最近 --keep 个版本（默认 2），生成
「## <版本>」+「- 一条日志一行」的 changelog.md，供 Moo 关于页
/ 更新提醒弹窗读取（不用重建 FPK 就能改日志）。

⚠️ moo.json 的 apps 下有 **两个**应用：`moo` 与 `moo-mate`（各有独立版本序列）。
本脚本只取 `apps.moo`：changelog.md 是 Moo 自己的版本段，Mate 的日志不混入
（服务端 latestChangelogSection / 版本正则只认 `## <数字版本>`，混排会串段）。

用法：
  python3 tools/update_about_changelog.py                  # dry-run，打印生成内容
  python3 tools/update_about_changelog.py --check          # 与 --out 比对，不一致退出 1
  python3 tools/update_about_changelog.py --write          # 写本地 --out
  python3 tools/update_about_changelog.py --write --push   # 写并推送 Gitea 清单仓

推送凭据（只走环境变量，不落盘）：
  GITEA_TOKEN            优先（API token）
  GITEA_USER / GITEA_PASSWORD   次选（Basic）
  GITEA_URL              默认 http://127.0.0.1:3033
"""
import argparse
import base64
import json
import os
import re
import sys
import urllib.error
import urllib.request

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WS = os.path.dirname(REPO_ROOT)


def ver_key(v):
    """版本号排序键：数字段补到 4 段，后缀按字符串比较（0.6.10 > 0.6.9）。"""
    nums = [int(x) for x in re.findall(r"\d+", v)[:4]]
    nums += [0] * (4 - len(nums))
    suffix = re.sub(r"^[\d.\-_]+", "", v)
    return (tuple(nums), suffix)


def die(msg):
    print("错误：" + msg, file=sys.stderr)
    raise SystemExit(2)


def build_changelog(moo_json, keep=2):
    try:
        with open(moo_json, encoding="utf-8") as f:
            data = json.load(f)
    except OSError as e:
        die("读取 %s 失败：%s" % (moo_json, e))
    except ValueError as e:
        die("%s 不是合法 JSON：%s" % (moo_json, e))
    try:
        rels = data["apps"]["moo"]["releases"]
    except (KeyError, TypeError):
        die("moo.json 缺少 apps.moo.releases")
    ordered = sorted(rels.keys(), key=ver_key, reverse=True)

    def entry_of(v):
        """取某版本的 changelog 文本；畸形节点给明确报错，不抛 AttributeError 堆栈。"""
        e = rels[v]
        if not isinstance(e, dict):
            die("版本 %s 的节点不是对象" % v)
        ch = e.get("changelog")
        if ch is None:
            return ""
        if not isinstance(ch, str):
            die("版本 %s 的 changelog 不是字符串（%s）" % (v, type(ch).__name__))
        return ch

    # 只有写了 changelog 的版本才占配额（空节点不挤掉真正的上一个版本）
    usable = [v for v in ordered if entry_of(v).strip()]
    blocks, used = [], []
    for v in usable[:keep]:
        text = entry_of(v).strip()
        if not text:
            continue
        lines = []
        for raw in text.splitlines():
            line = raw.strip()
            if not line:
                continue
            if not re.match(r"^[-*+]\s", line):
                line = "- " + line
            lines.append(line)
        blocks.append("## %s\n%s" % (v, "\n".join(lines)))
        used.append(v)
    body = "\n\n".join(blocks)
    return (body + "\n") if body else "", used


def main():
    ap = argparse.ArgumentParser(description="生成/校验/推送关于页 changelog.md")
    ap.add_argument("--moo-json", default=os.path.join(REPO_ROOT, "moo.json"))
    ap.add_argument("--out", default=os.path.join(WS, "_about_lists", "changelog.md"))
    ap.add_argument("--keep", type=int, default=2)
    ap.add_argument("--check", action="store_true", help="与 --out 比对，不一致退出 1")
    ap.add_argument("--write", action="store_true", help="写入 --out")
    ap.add_argument("--push", action="store_true", help="推送到 Gitea 清单仓")
    ap.add_argument("--repo", default="bluemink/moo-lists")
    ap.add_argument("--branch", default="main")
    ap.add_argument("--remote-path", default="changelog.md")
    ap.add_argument("--api-base", default=os.environ.get("GITEA_URL", "http://127.0.0.1:3033"))
    ap.add_argument("--message", default="chore(changelog): 同步最近两个版本的更新日志")
    args = ap.parse_args()

    if args.keep < 1:
        die("--keep 必须 >= 1")
    want, used = build_changelog(args.moo_json, args.keep)
    if not want:
        die("生成结果为空：releases 里没有可用 changelog")
    print("版本：%s（共 %d 段，%d 字节）" % (" / ".join(used), len(used), len(want.encode())))

    if args.check:
        try:
            with open(args.out, encoding="utf-8") as f:
                have = f.read()
        except OSError as e:
            print("FAIL 读取 %s 失败：%s" % (args.out, e))
            return 1
        if have != want:
            print("FAIL %s 与 moo.json 最近 %d 版不一致（跑 --write 更新）" % (args.out, args.keep))
            return 1
        print("PASS %s 已是最新" % args.out)
        return 0

    if args.write:
        d = os.path.dirname(args.out)
        try:
            if d:  # --out 只给裸文件名（如 CI 里写仓库根 changelog.md）时 dirname 为空，不能 makedirs("")
                os.makedirs(d, exist_ok=True)
            with open(args.out, "w", encoding="utf-8", newline="\n") as f:
                f.write(want)
        except OSError as e:
            die("写入 %s 失败：%s" % (args.out, e))
        print("已写入 %s" % args.out)
    else:
        print("--- dry-run（--write 落盘，--check 校验）---")
        print(want)

    if args.push:
        return push(args, want)
    return 0


def _auth_header():
    token = os.environ.get("GITEA_TOKEN", "").strip()
    if token:
        return {"Authorization": "token " + token}
    user = os.environ.get("GITEA_USER", "").strip()
    pw = os.environ.get("GITEA_PASSWORD", "")
    if user and pw:
        raw = base64.b64encode((user + ":" + pw).encode()).decode()
        return {"Authorization": "Basic " + raw}
    return {}


def _req(url, method="GET", body=None, headers=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            txt = resp.read().decode("utf-8", "replace")
            return resp.status, (json.loads(txt) if txt.strip() else {})
    except urllib.error.HTTPError as e:
        return e.code, {"error": e.read().decode("utf-8", "replace")[:300]}
    except (urllib.error.URLError, OSError) as e:
        # 网络不可达/超时/连接被拒：返回 0 让调用方走「读取失败」分支，
        # 而不是抛 traceback（cron 日志里难看且不好判定）。
        return 0, {"error": str(e)[:300]}


def push(args, content, quiet=False):
    base = args.api_base.rstrip("/")
    url = "%s/api/v1/repos/%s/contents/%s" % (base, args.repo, args.remote_path)
    headers = _auth_header()
    if not headers:
        print("FAIL 缺少凭据：export GITEA_TOKEN 或 GITEA_USER/GITEA_PASSWORD")
        return 1
    status, cur = _req(url + "?ref=" + args.branch, headers=headers)
    sha, have = None, ""
    if status == 200:
        sha = cur.get("sha")
        try:
            have = base64.b64decode(cur.get("content") or "").decode("utf-8")
        except Exception:
            have = ""
    elif status not in (404, 409):
        print("FAIL 读取远端失败 HTTP %s %s" % (status, cur.get("error", "")))
        return 1
    if have == content:
        if not quiet:
            print("跳过：远端 %s 已是最新" % args.remote_path)
        return 0
    body = {"content": base64.b64encode(content.encode()).decode(),
            "message": args.message, "branch": args.branch}
    if sha:
        body["sha"] = sha
    status, resp = _req(url, method="PUT", body=body, headers=headers)
    if status in (200, 201):
        if not quiet:
            print("已推送 %s@%s（%d 字节）" % (args.remote_path, args.branch, len(content.encode())))
        return 0
    print("FAIL 推送失败 HTTP %s %s" % (status, resp.get("error", "")))
    return 1


if __name__ == "__main__":
    sys.exit(main())
