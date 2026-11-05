#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""wait_ci_green.py — 打 tag 前等待 GitHub 上 main 的 Gate 绿灯（发布前置硬门）。

背景（2026-11-05 事故）：commit 推上 GitHub 后立刻打 tag，GitHub 的 Gate 还没跑完；
等它跑完发现红（测试断言依赖宿主机 /vol1），tag 已经落在红 commit 上。
本脚本把「Gate 绿」变成打 tag 的前置条件。

用法：
  python3 tools/wait_ci_green.py                       # 等当前 main HEAD
  python3 tools/wait_ci_green.py --sha <sha>           # 等指定 commit
  python3 tools/wait_ci_green.py --expect-sha <sha>    # 额外要求 main HEAD == 该 sha（打 tag 目标）
  python3 tools/wait_ci_green.py --timeout 900 --quiet

退出码：
  0 = Gate success（可以打 tag）
  1 = Gate failure / 被取消（禁止打 tag）
  2 = 超时或异常（禁止打 tag）
  3 = main HEAD 与 --expect-sha 不一致

凭据：GITHUB_TOKEN / GH_TOKEN / ~/.moo-gh-token（只读 actions/runs 需要 repo 读权限）
"""
import argparse
import json
import os
import sys
import time

REPO_DEFAULT = "Blue-Mink/moo"
WORKFLOW_DEFAULT = "Release"
JOB_KEYWORD = "gate"


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


def api(path, token, api_base="https://api.github.com"):
    import urllib.error
    import urllib.request
    req = urllib.request.Request(api_base.rstrip("/") + path, headers={
        "Authorization": "Bearer " + token,
        "Accept": "application/vnd.github+json",
        "User-Agent": "moo-wait-ci-green/1",
    })
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, json.loads(r.read())
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read())
        except ValueError:
            return e.code, {}
    except Exception as e:                                   # noqa: BLE001
        return 0, {"error": "%s: %s" % (type(e).__name__, e)}


def find_run(runs, workflow, sha):
    """同名 workflow 里挑该 sha 最新的一条 run。"""
    cands = [r for r in runs
             if r.get("name") == workflow and (r.get("head_sha") or "").startswith(sha[:12])]
    if not cands:
        return None
    return max(cands, key=lambda r: r.get("run_number") or 0)


def gate_state(run_id, token, api_base):
    st, d = api("/repos/Blue-Mink/moo/actions/runs/%d/jobs?per_page=30" % run_id, token, api_base)
    if st != 200:
        return "unknown", "unknown", str(d)[:100]
    for j in d.get("jobs", []):
        if JOB_KEYWORD in (j.get("name") or "").lower():
            return j.get("status") or "?", j.get("conclusion") or "?", ""
    return "missing", "missing", "该 run 没有 Gate job"


def main():
    ap = argparse.ArgumentParser(description="打 tag 前等待 GitHub main 的 Gate 绿灯")
    ap.add_argument("--repo", default=REPO_DEFAULT)
    ap.add_argument("--workflow", default=WORKFLOW_DEFAULT)
    ap.add_argument("--sha", default="", help="要等待的 commit（默认取 main HEAD）")
    ap.add_argument("--expect-sha", default="",
                    help="要求 main HEAD == 该 sha（发版时传「即将打 tag 的 sha」，防止 tag 落在非 main 提交上）")
    ap.add_argument("--repo-dir", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    ap.add_argument("--api-base", default="https://api.github.com")
    ap.add_argument("--timeout", type=int, default=1200, help="总等待秒数（默认 1200）")
    ap.add_argument("--interval", type=int, default=15)
    ap.add_argument("--quiet", action="store_true")
    a = ap.parse_args()

    token = load_token()
    if not token:
        print("FAIL 缺 GitHub token（GITHUB_TOKEN / GH_TOKEN / ~/.moo-gh-token）")
        return 2

    sha = a.sha.strip()
    if not sha:
        st, d = api("/repos/%s/commits/main" % a.repo, token, a.api_base)
        if st != 200:
            print("FAIL 读取 main HEAD 失败 HTTP %s %s" % (st, str(d)[:120]))
            return 2
        sha = d["sha"]
    if a.expect_sha:
        exp = a.expect_sha.strip()
        if not (sha.startswith(exp[:12]) or exp.startswith(sha[:12])):
            print("FAIL main(%s) != 预期 tag 目标(%s)：tag 不能打在非 main 的提交上"
                  % (sha[:12], exp[:12]))
            return 3
        if not a.quiet:
            print("main == 预期 tag 目标（%s）✓" % exp[:12])
    if not a.quiet:
        print("等待 %s 的 %s / Gate …（sha %s，超时 %ds）" % (a.repo, a.workflow, sha[:12], a.timeout))

    deadline = time.time() + a.timeout
    last = ""
    while True:
        st, d = api("/repos/%s/actions/runs?head_sha=%s&per_page=30" % (a.repo, sha), token, a.api_base)
        if st != 200:
            print("WARN 查询 runs 失败 HTTP %s %s" % (st, str(d)[:120]))
        else:
            run = find_run(d.get("workflow_runs") or [], a.workflow, sha)
            if run is None:
                state = "no-run"
            else:
                js, jc, note = gate_state(run["id"], token, a.api_base)
                state = "%s/%s" % (js, jc)
                if js == "completed":
                    if jc == "success":
                        if not a.quiet:
                            print("Gate 绿 ✓（run %s）" % run["id"])
                        return 0
                    if not a.quiet:
                        print("Gate 未通过：%s%s（run %s）" % (jc, ("，" + note) if note else "", run["id"]))
                    return 1
            if state != last:
                if not a.quiet:
                    print("  %s  %s" % (time.strftime("%H:%M:%S"), state))
                last = state
        if time.time() >= deadline:
            print("FAIL 超时：%ds 内未拿到 Gate 结论（最后状态 %s）" % (a.timeout, last or "no-run"))
            return 2
        time.sleep(a.interval)


if __name__ == "__main__":
    sys.exit(main())
