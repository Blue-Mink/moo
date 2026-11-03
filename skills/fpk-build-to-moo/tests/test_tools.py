"""Tool-level checks for the bundled Moo index maintainer.

Locates `update_moo_json.py` either in the installed `bundled/tools/` copy or
in the repository's `tools/` directory, builds synthetic x86 + arm FPKs, runs
the tool, and asserts both architectures land in `releases.<ver>.packages`.
"""
from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "tests"))
import e2e_local as fixture  # noqa: E402

TOOL_CANDIDATES = [
    ROOT / "bundled" / "tools" / "update_moo_json.py",
    ROOT.parents[1] / "tools" / "update_moo_json.py",
]


def tool_path() -> Path | None:
    for candidate in TOOL_CANDIDATES:
        if candidate.is_file():
            return candidate
    return None


class UpdateMooJsonToolTests(unittest.TestCase):
    def test_dual_arch_update(self) -> None:
        tool = tool_path()
        if tool is None:
            self.skipTest("update_moo_json.py not found in bundled/ or repo tools/")
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for platform, suffix in (("x86", "x86"), ("arm", "arm")):
                src = root / f"src-{suffix}"
                src.mkdir(parents=True)
                fixture.write_source(src, platform)
                fixture.synthetic_fpk(src, root, f"e2e-demo_{suffix}.fpk")

            index = {
                "schema_version": "moo",
                "source_info": {"name": "tool test"},
                "apps": {"e2e-demo": {
                    "display_name": "E2E Demo",
                    "version": "0.9.0",
                    "platform": ["x86"],
                    "download_url": "https://example.com/repo/releases/download/v0.9.0/e2e-demo_x86.fpk",
                    "sha256": "a" * 64,
                    "size": 0.1,
                    "categories": ["实用效率"],
                    "desc": "tool test",
                }},
            }
            index_path = root / "moo.json"
            index_path.write_text(json.dumps(index, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

            run = subprocess.run(
                [sys.executable, str(tool), "--file", str(index_path), "--version", "1.0.0",
                 "--fpk", str(root / "e2e-demo_x86.fpk"), str(root / "e2e-demo_arm.fpk"),
                 "--repo", "Owner/Repo"],
                capture_output=True, text=True, timeout=60,
            )
            self.assertEqual(0, run.returncode, run.stdout + run.stderr)

            data = json.loads(index_path.read_text(encoding="utf-8"))
            entry = data["apps"]["e2e-demo"]
            self.assertEqual("1.0.0", entry["version"])
            self.assertEqual(["x86", "arm"], entry["platform"])
            packages = entry["releases"]["1.0.0"]["packages"]
            self.assertEqual({"x86", "arm"}, set(packages))
            for arch in ("x86", "arm"):
                self.assertGreater(packages[arch]["size"], 0)
                self.assertEqual(64, len(packages[arch]["sha256"]))
                self.assertTrue(packages[arch]["download_url"].endswith(f"e2e-demo_{arch}.fpk"))


if __name__ == "__main__":
    unittest.main()
