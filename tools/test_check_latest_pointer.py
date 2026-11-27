#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""test_check_latest_pointer.py — 发布门（--require）的自测用例（纯 stdlib）。

2026-11-27（F12 / 审计 N9「索引先于 release」）：`check_latest_pointer.py
--require` 是「改 moo.json 之前的硬门」。本文件用 `--latest-tag` 注入远端值
（不联网），逐条断言门的判定：

  1 远端 latest 落后于本次版本（= 索引先于 release 的典型形态）→ exit 1
  2 latest == 本次版本 → exit 0
  3 `--require` 接受不带 v 的写法（0.6.578 == v0.6.578）→ exit 0
  4 latest 被 mate 抢走 → exit 1（形态异常 + 发布门）
  5 `--dry-run` 不联网、不校验 → exit 2（明确不是「通过」）

用法：python3 tools/test_check_latest_pointer.py   （退出码 0 = 全部用例通过）
"""
import os
import re
import shutil
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "tools", "check_latest_pointer.py")

# (用例名, 参数, 期望退出码, 输出里必须出现/不得出现的片段)
CASES = [
    ("gate-blocks-lagging-latest", ["--require", "v9.9.9", "--latest-tag", "v9.9.8"], 1,
     ["[发布门]", "索引永远晚于 release"]),
    ("gate-passes-when-latest-matches", ["--require", "v9.9.9", "--latest-tag", "v9.9.9"], 0,
     ["OK(发布门)"]),
    ("gate-accepts-bare-version", ["--require", "9.9.9", "--latest-tag", "v9.9.9"], 0,
     ["OK(发布门)"]),
    ("gate-rejects-mate-pointer", ["--require", "v9.9.9", "--latest-tag", "mate-v9.9.9"], 1,
     ["latest 被 Mate 抢走"]),
    ("dry-run-exits-2", ["--require", "v9.9.9", "--dry-run"], 2,
     ["[dry-run]", "未做任何校验"]),
]


def run(args):
    env = dict(os.environ)
    env.pop("GH_TOKEN", None)   # 任何用例都不许真的打 GitHub
    env.pop("GITHUB_TOKEN", None)
    py = sys.executable or shutil.which("python3") or "python3"
    return subprocess.run([py, SCRIPT] + args,
                          capture_output=True, text=True, timeout=60, env=env)


def main():
    fails = 0
    for name, args, want_code, needles in CASES:
        r = run(args)
        out = (r.stdout or "") + (r.stderr or "")
        ok = r.returncode == want_code
        missing = [n for n in needles if n not in out]
        # dry-run 用例必须显式声明「未校验」，且不得出现通过字样
        if ok and not missing:
            print("PASS %-32s exit=%d" % (name, r.returncode))
            continue
        fails += 1
        print("FAIL %-32s exit=%d want=%d missing=%s" % (name, r.returncode, want_code, missing))
        print("     output: " + re.sub(r"\s+", " ", out).strip()[:300])
    if fails:
        print("发布门自测失败：%d/%d" % (fails, len(CASES)))
        return 1
    print("发布门自测通过：%d/%d" % (len(CASES), len(CASES)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
