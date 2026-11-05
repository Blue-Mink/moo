#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Moo 技能 CI 自检（无第三方依赖）。

检查项：
1. 每个 skills/<name>/SKILL.md 有 name/description frontmatter
2. SKILL.md / INSTALL.md 里的相对 .md 链接可解析（技能目录 → 仓库根 → bundled/）
3. skills/ai 的 manifest 哈希与文件一致
4. 每个技能的 scripts/install.sh 可执行、`--list` 正常、dry-run 成功
5. 有 tests/ 的技能跑 unittest
6. 敏感模式扫描（凭据/内网地址）

用法：python3 skills/ci_check.py [--quiet]
"""
from __future__ import annotations

import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent
SKILLS = ROOT / "skills"
LINK_RE = re.compile(r"\]\((\.{0,2}/?[^)\s]+\.md)(#[^)]+)?\)")
# 反引号里点名的仓库内脚本路径必须真实存在（2026-11-05 事故：
# release-gates.md 让人执行 `tools/wait_ci_green.py`，但该文件当时未入库，
# 干净 clone 里没有它 → 文档指向空，照做的人必失败）。
# 只校验 tools/ 与 .github/scripts/ 两类真实脚本目录，避免把示例/产物路径误判。
REF_PATH_RE = re.compile(r"`[^`\n]*?((?:tools|\.github/scripts)/[\w./-]+\.[A-Za-z0-9]+)")
# 敏感模式与发布门同源：.github/sanitize-patterns.txt（单一事实来源）。
# 本文件是公开仓库，绝不写入真实凭据的字面或前缀——此前这里硬编码过
# 一条真实口令的前缀模式，等于把凭据结构写进公开仓，已移除。找不到主清单时才用 fallback。
SECRET_FALLBACK = [
    (r"ghp_[A-Za-z0-9]{20,}", "GitHub token"),
    (r"192\.168\.\d+\.\d+", "内网地址"),
    (r"10\.0\.0\.\d+", "内网地址"),
]


def load_secret_patterns() -> list[tuple[str, str]]:
    f = ROOT / ".github" / "sanitize-patterns.txt"
    out: list[tuple[str, str]] = []
    if f.is_file():
        for line in f.read_text(encoding="utf-8", errors="ignore").splitlines():
            line = line.lstrip("\ufeff").strip()
            if not line or line.startswith("#"):
                continue
            out.append((line, "敏感模式"))
    return out or SECRET_FALLBACK
failures: list[str] = []


def fail(msg: str) -> None:
    failures.append(msg)
    print(f"FAIL  {msg}")


def ok(msg: str) -> None:
    print(f"ok    {msg}")


def frontmatter(path: pathlib.Path) -> dict:
    text = path.read_text(encoding="utf-8")
    m = re.match(r"^---\n(.*?)\n---\n", text, re.S)
    if not m:
        return {}
    data = {}
    for line in m.group(1).splitlines():
        if ":" in line and not line.startswith((" ", "\t")):
            k, v = line.split(":", 1)
            data[k.strip()] = v.strip().strip('"')
    return data


def check_skill(skill: pathlib.Path) -> None:
    name = skill.name
    sk = skill / "SKILL.md"
    if not sk.exists():
        fail(f"{name}: 缺少 SKILL.md")
        return
    fm = frontmatter(sk)
    if not fm.get("name"):
        fail(f"{name}: SKILL.md 缺 name")
    if not fm.get("description"):
        fail(f"{name}: SKILL.md 缺 description")

    for md in ("SKILL.md", "INSTALL.md"):
        p = skill / md
        if not p.exists():
            continue
        for m in LINK_RE.finditer(p.read_text(encoding="utf-8")):
            target = m.group(1)
            if target.startswith("http"):
                continue
            cands = [skill / target, ROOT / target, skill / "bundled" / target.lstrip("./")]
            if not any(c.exists() for c in cands):
                fail(f"{name}/{md}: 链接失效 {target}")
                break

    inst = skill / "scripts" / "install.sh"
    if not inst.exists():
        fail(f"{name}: 缺少 scripts/install.sh")
        return
    if not (skill / "scripts" / "install.ps1").exists():
        fail(f"{name}: 缺少 scripts/install.ps1（Windows）")
    r = subprocess.run(["sh", "-n", str(inst)], capture_output=True, text=True)
    if r.returncode != 0:
        fail(f"{name}: install.sh 语法错误 {r.stderr.strip()[:120]}")
        return
    r = subprocess.run(["sh", str(inst), "--list"], capture_output=True, text=True)
    if r.returncode != 0 or "qwenpaw" not in r.stdout:
        fail(f"{name}: install.sh --list 异常")
    with tempfile.TemporaryDirectory() as td:
        r = subprocess.run(
            ["sh", str(inst), "--target", "skills-dir", "--dest", td],
            capture_output=True, text=True,
        )
        if r.returncode != 0 or "复制" not in r.stdout:
            fail(f"{name}: install.sh dry-run 异常 {r.stderr.strip()[:120]}")

    tests = skill / "tests"
    if tests.is_dir():
        ci = tests / "ci.sh"
        if ci.is_file():
            r = subprocess.run(
                ["sh", str(ci)], cwd=skill, capture_output=True, text=True, timeout=300,
            )
            if r.returncode != 0:
                fail(f"{name}: CI 门禁失败\n{(r.stdout + r.stderr)[-500:]}")
            else:
                ok(f"{name}: CI 门禁通过（sanitize + tests + E2E）")
        else:
            r = subprocess.run(
                [sys.executable, "-m", "unittest", "discover", "-s", "tests"],
                cwd=skill, capture_output=True, text=True,
            )
            if r.returncode != 0:
                fail(f"{name}: 测试失败\n{r.stderr[-400:]}")
            else:
                ok(f"{name}: tests 通过")
            e2e = tests / "e2e_local.py"
            if e2e.is_file():
                r = subprocess.run(
                    [sys.executable, str(e2e)],
                    cwd=skill, capture_output=True, text=True, timeout=180,
                )
                if r.returncode != 0:
                    fail(f"{name}: E2E 失败\n{r.stderr[-400:]}")
                else:
                    ok(f"{name}: E2E 通过")


def check_ai_manifest() -> None:
    ai = SKILLS / "ai"
    man = ai / "manifest.json"
    if not man.exists():
        return
    data = json.loads(man.read_text(encoding="utf-8"))
    for fname, want in data.get("files", {}).items():
        p = ai / fname
        if not p.exists():
            fail(f"ai/manifest: 文件缺失 {fname}")
            continue
        got = hashlib.sha256(p.read_bytes()).hexdigest()
        if got != want:
            fail(f"ai/manifest: 哈希不一致 {fname}（跑 scripts/rehash.py）")


def check_secrets() -> None:
    for p in SKILLS.rglob("*"):
        if not p.is_file() or p.stat().st_size > 2_000_000:
            continue
        if p.suffix.lower() not in {".md", ".sh", ".py", ".json", ".mjs", ".toml", ".ps1"}:
            continue
        try:
            text = p.read_text(encoding="utf-8", errors="ignore")
        except OSError:
            continue
        for pat, label in load_secret_patterns():
            m = re.search(pat, text)
            if m:
                fail(f"{p.relative_to(ROOT)}: 命中{label} → {m.group(0)[:40]}")


def check_referenced_paths() -> None:
    """skills/**/*.md 里反引号点名的 tools/、.github/scripts/ 路径必须存在。

    在干净 clone（CI）里，未入库的文件不存在 → 直接判红，堵住「文档指向空」。
    """
    total, missing = 0, []
    for md in sorted(SKILLS.rglob("*.md")):
        for m in REF_PATH_RE.finditer(md.read_text(encoding="utf-8", errors="replace")):
            rel = m.group(1)
            if "*" in rel or "<" in rel or ">" in rel:  # 通配/占位符跳过
                continue
            total += 1
            if not (ROOT / rel).is_file():
                missing.append(f"{md.relative_to(ROOT)} -> {rel}")
    if missing:
        fail("引用了仓库里不存在的脚本路径：" + "; ".join(missing))
    else:
        ok(f"文档引用的脚本路径都存在（{total} 条）")


def main() -> int:
    if not SKILLS.is_dir():
        print("!! 找不到 skills/ 目录", file=sys.stderr)
        return 2
    skills = [d for d in sorted(SKILLS.iterdir()) if d.is_dir() and (d / "SKILL.md").exists()]
    if not skills:
        print("!! 没有找到任何技能", file=sys.stderr)
        return 2
    for s in skills:
        check_skill(s)
    check_ai_manifest()
    check_secrets()
    check_referenced_paths()
    print()
    if failures:
        print(f"结果：FAIL（{len(failures)} 项）")
        return 1
    print(f"结果：PASS（{len(skills)} 个技能）")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
