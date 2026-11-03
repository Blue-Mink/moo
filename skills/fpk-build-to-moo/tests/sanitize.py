#!/usr/bin/env python3
"""Sanitization gate for published skill docs.

Scans markdown/JSON/YAML/shell/PS1 files for credentials, internal addresses,
private paths, personal emails and unpublished artifact hashes. RFC5737
addresses (192.0.2.x), example.com and loopback are allowed.

Run: python3 tests/sanitize.py
Exit: 0 clean, 1 hit.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SKIP_DIRS = {".git", "tests", "__pycache__", "bundled"}
SUFFIXES = {".md", ".json", ".yml", ".yaml", ".sh", ".ps1", ".py", ".mjs"}

PATTERNS = [
    (r"\b192\.168\.\d{1,3}\.\d{1,3}\b", "内网 IPv4"),
    (r"\b10\.\d{1,3}\.\d{1,3}\.\d{1,3}\b", "内网 IPv4"),
    (r"\b172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}\b", "内网 IPv4"),
    (r"\bgitea\.linuxst\.cn\b", "内网域名"),
    (r"\bbluemink\b", "真实账号/仓库名"),
    (r"\b\d{6,}@qq\.com\b", "个人邮箱"),
    (r"\bghp_[A-Za-z0-9]{20,}\b", "GitHub token"),
    (r"\bgithub_pat_[A-Za-z0-9_]{20,}\b", "GitHub token"),
    (r"\bgh[os]_[A-Za-z0-9]{20,}\b", "GitHub token"),
    (r"\bsk-[A-Za-z0-9]{20,}\b", "API key"),
    (r"BEGIN [A-Z ]*PRIVATE KEY", "私钥块"),
    (r"(?i)\b(?:password|passwd|pwd)\s*[:=]\s*[^\s<]", "疑似明文口令"),
    (r"(?i)\b(?:token|secret|api[_-]?key)\s*[:=]\s*[^\s<]", "疑似明文凭据"),
    (r"/vol1/@appshare|com\.dustinky", "私有工作区路径"),
    (r"\b[0-9a-f]{64}\b", "未发布构件哈希"),
]

ALLOW = ("192.0.2.", "127.0.0.1", "0.0.0.0", "example.com", "grep -RIn", "命中")


def scan(root: Path) -> list[str]:
    errors: list[str] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file() or any(part in SKIP_DIRS for part in path.parts):
            continue
        if path.suffix.lower() not in SUFFIXES:
            continue
        for number, line in enumerate(path.read_text(encoding="utf-8", errors="ignore").splitlines(), 1):
            if any(token in line for token in ALLOW):
                continue
            for pattern, label in PATTERNS:
                if re.search(pattern, line):
                    errors.append(f"{path.relative_to(root)}:{number}: {label}: {line.strip()[:120]}")
    return errors


def main() -> int:
    errors = scan(ROOT)
    if errors:
        print("SANITIZE FAIL")
        for error in errors:
            print("  " + error)
        return 1
    print(f"SANITIZE PASS ({len(list(ROOT.rglob('*')))} paths scanned)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
