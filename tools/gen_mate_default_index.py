#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""gen_mate_default_index.py — 生成 Moo Mate 的远程默认应用列表 mate-default-index.json。

背景（0.2.37）：mate 首次安装时不再只吃包内快照，而是先读远程清单
（mate-default-index.json），失败才回落内置 default_index.json.gz。
本脚本把当前 mate 索引导出成远程清单，保证「远程列表 == 当前 mate 应用列表」。

用法：
  python3 tools/gen_mate_default_index.py --src /vol1/@appdata/moo-mate/index.json
  python3 tools/gen_mate_default_index.py --api http://127.0.0.1:38108/api/index
  python3 tools/gen_mate_default_index.py --src ... --write --out mate-default-index.json
  python3 tools/gen_mate_default_index.py --src ... --push        # 推 Gitea 测试清单仓

推送凭据（只走环境变量，不落盘）：
  GITEA_TOKEN / GITEA_USER + GITEA_PASSWORD；GITEA_URL 默认 http://127.0.0.1:3033
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
FORBID = {
    "内网 192.168": r"192\.168\.",
    "内网 10.0.0": r"10\.0\.0\.",
    "本机路径 /vol1/@app": r"/vol1/@app",
    "GitHub PAT": r"ghp_[A-Za-z0-9]{20,}",
}


def die(msg):
    print("错误：" + msg, file=sys.stderr)
    raise SystemExit(2)


def load_doc(a):
    if a.api:
        with urllib.request.urlopen(a.api, timeout=30) as r:
            d = json.load(r)
        apps = {x["appname"]: x for x in (d.get("apps") or []) if x.get("appname")}
        return {"version": 1, "apps": apps}
    try:
        d = json.load(open(a.src, encoding="utf-8"))
    except OSError as e:
        die("读取 %s 失败：%s" % (a.src, e))
    apps = d.get("apps")
    if not isinstance(apps, dict):
        die("索引格式不对：apps 不是对象")
    return {"version": int(d.get("version") or 1), "apps": apps}


def dump_default_index(doc):
    """顶层两行 + 每个应用占一行。

    紧凑单行（680KB / 1 行）在 GitHub 上无法 review；indent=2 会把每个字段、
    连 platform 数组都拆行（16,940 行 / +21%）。折中：一个应用一行，
    体积只多 ~4%，字段与顺序完全不变。
    """
    p = "  "
    items = [
        p * 2 + json.dumps(k, ensure_ascii=False) + ": "
        + json.dumps(doc["apps"][k], ensure_ascii=False, separators=(", ", ": "))
        for k in doc["apps"]
    ]
    return (
        "{\n"
        + p + '"version": ' + json.dumps(doc["version"]) + ",\n"
        + p + '"apps": {\n'
        + ",\n".join(items) + "\n"
        + p + "}\n"
        + "}\n"
    )


def main():
    ap = argparse.ArgumentParser(description="生成/校验/推送 mate 远程默认应用列表")
    ap.add_argument("--src", default="/vol1/@appdata/moo-mate/index.json")
    ap.add_argument("--api", default="", help="从运行中的 mate 读取（/api/index）")
    ap.add_argument("--out", default=os.path.join(REPO_ROOT, "mate-default-index.json"))
    ap.add_argument("--write", action="store_true")
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--push", action="store_true")
    ap.add_argument("--repo", default="bluemink/moo-lists")
    ap.add_argument("--branch", default="main")
    ap.add_argument("--remote-path", default="mate-default-index.json")
    ap.add_argument("--api-base", default=os.environ.get("GITEA_URL", "http://127.0.0.1:3033"))
    ap.add_argument("--message", default="chore(mate): 同步 Moo Mate 默认应用列表")
    a = ap.parse_args()

    doc = load_doc(a)
    if not doc["apps"]:
        die("列表为空，拒绝生成")
    # /api/index 的 apps 是 Go map 序列化，多次请求顺序不同；按 appname 排序保证
    # --check 稳定、远端文件可复现。
    doc["apps"] = {k: doc["apps"][k] for k in sorted(doc["apps"])}
    body = dump_default_index(doc)
    for name, pat in FORBID.items():
        if re.search(pat, body):
            die("脱敏断言失败（%s），拒绝生成" % name)
    print("apps=%d bytes=%d" % (len(doc["apps"]), len(body.encode())))

    if a.check:
        try:
            have = open(a.out, encoding="utf-8").read()
        except OSError as e:
            print("FAIL 读取 %s 失败：%s" % (a.out, e))
            return 1
        if have != body:
            print("FAIL %s 与当前 mate 列表不一致（跑 --write）" % a.out)
            return 1
        print("PASS %s 已是最新" % a.out)
        return 0
    if a.write:
        d = os.path.dirname(a.out)
        if d:
            os.makedirs(d, exist_ok=True)
        open(a.out, "w", encoding="utf-8", newline="\n").write(body)
        print("已写入 %s" % a.out)
    if a.push:
        return push(a, body)
    if not a.write:
        print("--- dry-run（--write 落盘，--check 校验，--push 推送）---")
    return 0


def _auth_header():
    tok = os.environ.get("GITEA_TOKEN", "").strip()
    if tok:
        return {"Authorization": "token " + tok}
    u, p = os.environ.get("GITEA_USER", "").strip(), os.environ.get("GITEA_PASSWORD", "")
    if u and p:
        return {"Authorization": "Basic " + base64.b64encode((u + ":" + p).encode()).decode()}
    return {}


def _req(url, method="GET", body=None, headers=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            t = r.read().decode("utf-8", "replace")
            return r.status, (json.loads(t) if t.strip() else {})
    except urllib.error.HTTPError as e:
        return e.code, {"error": e.read().decode("utf-8", "replace")[:300]}
    except (urllib.error.URLError, OSError) as e:
        return 0, {"error": str(e)[:300]}


def push(a, content):
    base = a.api_base.rstrip("/")
    url = "%s/api/v1/repos/%s/contents/%s" % (base, a.repo, a.remote_path)
    h = _auth_header()
    if not h:
        print("FAIL 缺少凭据：export GITEA_TOKEN 或 GITEA_USER/GITEA_PASSWORD")
        return 1
    st, cur = _req(url + "?ref=" + a.branch, headers=h)
    sha = have = None
    if st == 200:
        sha = cur.get("sha")
        try:
            have = base64.b64decode(cur.get("content") or "").decode("utf-8")
        except Exception:
            have = ""
    elif st not in (404, 409):
        print("FAIL 读取远端失败 HTTP %s %s" % (st, cur.get("error", "")))
        return 1
    if have == content:
        print("跳过：远端 %s 已是最新" % a.remote_path)
        return 0
    body = {"content": base64.b64encode(content.encode()).decode(),
            "message": a.message, "branch": a.branch}
    if sha:
        body["sha"] = sha
    st, resp = _req(url, method="PUT", body=body, headers=h)
    if st in (200, 201):
        print("已推送 %s@%s（%d 字节）" % (a.remote_path, a.branch, len(content.encode())))
        return 0
    print("FAIL 推送失败 HTTP %s %s" % (st, resp.get("error", "")))
    return 1


if __name__ == "__main__":
    sys.exit(main())
