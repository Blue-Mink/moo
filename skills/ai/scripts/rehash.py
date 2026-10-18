#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""重算 skills/ai/manifest.json 的文件哈希（并可选校验）。

用法：
  python3 skills/ai/scripts/rehash.py            # 重算并写回（updated_at 同步刷新）
  python3 skills/ai/scripts/rehash.py --check    # 只校验；不一致 exit 1（供 CI 用）
  python3 skills/ai/scripts/rehash.py --add-missing   # 把目录里未登记的业务 .md 也纳入清单

说明：
- 只处理 manifest.files 里已列出的文件；--add-missing 才会新增（排除 SKILL.md/INSTALL.md/README.md）。
- 应用侧同步会逐文件校验 sha256，改完文档必须重算，否则整包被拒。
"""
from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import sys
from datetime import datetime

ROOT = pathlib.Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "manifest.json"
EXCLUDE = {"SKILL.md", "INSTALL.md", "README.md", "manifest.json"}


def sha256(path: pathlib.Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def load() -> dict:
    with MANIFEST.open(encoding="utf-8") as f:
        return json.load(f)


def dump(data: dict) -> None:
    with MANIFEST.open("w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)
        f.write("\n")


def main() -> int:
    ap = argparse.ArgumentParser(description="重算/校验 ai 技能包 manifest 哈希")
    ap.add_argument("--check", action="store_true", help="只校验，不写回")
    ap.add_argument("--add-missing", action="store_true", help="把未登记的 .md 纳入 files")
    args = ap.parse_args()

    if not MANIFEST.exists():
        print(f"!! 缺少 {MANIFEST}", file=sys.stderr)
        return 2

    data = load()
    files = data.setdefault("files", {})
    changed: list[str] = []

    if args.add_missing:
        for p in sorted(ROOT.glob("*.md")):
            if p.name in EXCLUDE or p.name in files:
                continue
            files[p.name] = ""
            print(f"+ 新增 {p.name}")

    missing = [n for n in files if not (ROOT / n).exists()]
    if missing:
        print(f"!! manifest 列出的文件不存在: {', '.join(missing)}", file=sys.stderr)
        return 2

    for name, old in list(files.items()):
        new = sha256(ROOT / name)
        if new != old:
            changed.append(name)
            files[name] = new

    if args.check:
        if changed:
            print("!! 哈希不一致（请运行 rehash.py 后提交）:")
            for n in changed:
                print(f"   - {n}")
            return 1
        print(f"OK: {len(files)} 个文件哈希一致")
        return 0

    if changed:
        data["updated_at"] = datetime.now().astimezone().replace(microsecond=0).isoformat()
        dump(data)
        print(f"已更新 {len(changed)} 个哈希: {', '.join(changed)}")
        print(f"updated_at -> {data['updated_at']}")
    else:
        print(f"无需更新（{len(files)} 个文件哈希一致）")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
