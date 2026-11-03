"""Contract + blind-spot checks for the Moo publishing skill.

Complements `test_docs.py` with content coverage (moo.json fields, wizard,
lifecycle, signing) and stale-command regressions. The checker is self-tested
with mutated copies so a broken checker cannot pass silently.
"""

from __future__ import annotations

import re
import shutil
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

REQUIRED = [
    "references/moo-json-fields.md",
    "references/wizard-lifecycle.md",
    "references/self-update-signing.md",
    "references/moo-mate-notes.md",
    "bug_report_url",
    "min_fnos",
    "desc_html",
    "size_bytes",
    "wizard",
    "源级安装向导",
    "SITE_TITLE",
    "releases",
    "packages",
    "source_info",
    "wizard/install",
    "wizard/uninstall",
    "wizard/config",
    "wizard/upgrade",
    "wizard_delete_data",
    "TRIM_OLD_APPVER",
    "config_init",
    "config_callback",
    "190",
    "10500",
    "10150",
    "11000",
    "checkport",
    "gatewaySocket",
    "gatewayPrefix",
    "/cgi/ThirdParty/",
    "--arch arm64",
    "--arch both",
    "GOARCH=arm64",
    "-fnos-arm64.fpk",
    "packages.arm",
    "update_moo_json.py",
    "bundled/tools/update_moo_json.py",
    "tests/ci.sh",
    "--no-platform-sync",
    "api-scope",
    "port-config",
    "appcenter-cli install-fpk",
    "--env /tmp/<app>.env",
    "fnpack build -d",
    "md5sum",
    "--volume",
    "desktop_applaunchname",
    "ICON.PNG",
    "icon_0.png",
    "≥512",
    "minisign",
    ".minisig",
    ".sha256",
    "asset.digest",
    "mate-v",
]

ALLOW = ("没有", "不存在", "已移除", "不要用", "旧命令", "禁止", "不是独立命令")

FORBIDDEN = [
    (r"\bfnpack\s+pack\b", "fnpack 1.2.3/1.2.4 都没有 pack"),
    (r"\bfnpack\s+crc32\b", "fnpack 1.2.3/1.2.4 都没有 crc32"),
    (r"\bCRC32\b", "checksum 是 MD5，不是 CRC32"),
    (r"appcenter-cli\s+install\s+[^\n]*\.fpk", "本地 FPK 用 install-fpk，不是 install"),
    (r"(?<!appcenter-cli )\binstall-fpk\b", "install-fpk 必须带 appcenter-cli 前缀"),
    (r"--arch\s+x86\b", "--arch 只接受 amd64/arm64/all/both，x86 不是合法值"),
    (r"--arch\s+arm\b(?!64)", "--arch 只接受 amd64/arm64/all/both，arm 不是合法值"),
]


def markdown(root: Path):
    return sorted(p for p in root.rglob("*.md") if ".git" not in p.parts)


def check_contract(root: Path) -> list[str]:
    errors: list[str] = []
    try:
        skill = (root / "SKILL.md").read_text(encoding="utf-8")
    except FileNotFoundError:
        return ["缺少 SKILL.md"]
    blob = "\n".join(p.read_text(encoding="utf-8") for p in markdown(root))

    for needle in REQUIRED:
        if needle not in blob:
            errors.append(f"缺少必需内容: {needle}")

    for ref in sorted((root / "references").glob("*.md")):
        if f"references/{ref.name}" not in skill:
            errors.append(f"引用文件未在 SKILL.md 路由: {ref.name}")

    for path in markdown(root):
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if any(token in line for token in ALLOW):
                continue
            for pattern, why in FORBIDDEN:
                if re.search(pattern, line):
                    errors.append(f"{path.name}:{number}: {why}: {line.strip()[:100]}")
    return errors


class ContractTests(unittest.TestCase):
    def test_current_docs_satisfy_contract(self) -> None:
        errors = check_contract(ROOT)
        self.assertEqual([], errors, "\n".join(errors))

    def test_contract_detects_stale_pack_command(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            copy = Path(td) / "skill"
            shutil.copytree(ROOT, copy, ignore=shutil.ignore_patterns(".git", "__pycache__"))
            skill = copy / "SKILL.md"
            skill.write_text(skill.read_text(encoding="utf-8") + "\n\nfnpack pack .\n", encoding="utf-8")
            errors = check_contract(copy)
            self.assertTrue(any("fnpack 1.2.3/1.2.4 都没有 pack" in e for e in errors), errors)

    def test_contract_detects_missing_bug_report_field(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            copy = Path(td) / "skill"
            shutil.copytree(ROOT, copy, ignore=shutil.ignore_patterns(".git", "__pycache__"))
            for path in markdown(copy):
                path.write_text(path.read_text(encoding="utf-8").replace("bug_report_url", ""), encoding="utf-8")
            errors = check_contract(copy)
            self.assertTrue(any("bug_report_url" in e for e in errors), errors)

    def test_contract_detects_missing_arm_build(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            copy = Path(td) / "skill"
            shutil.copytree(ROOT, copy, ignore=shutil.ignore_patterns(".git", "__pycache__"))
            for path in markdown(copy):
                path.write_text(path.read_text(encoding="utf-8").replace("--arch arm64", ""), encoding="utf-8")
            errors = check_contract(copy)
            self.assertTrue(any("--arch arm64" in e for e in errors), errors)

    def test_contract_detects_missing_update_tool(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            copy = Path(td) / "skill"
            shutil.copytree(ROOT, copy, ignore=shutil.ignore_patterns(".git", "__pycache__"))
            for path in markdown(copy):
                path.write_text(path.read_text(encoding="utf-8").replace("update_moo_json.py", ""), encoding="utf-8")
            errors = check_contract(copy)
            self.assertTrue(any("update_moo_json.py" in e for e in errors), errors)

    def test_internal_links_resolve(self) -> None:
        for path in markdown(ROOT):
            for match in re.finditer(r"\]\(([^)\s]+)\)", path.read_text(encoding="utf-8")):
                target = match.group(1)
                if target.startswith(("http://", "https://", "mailto:")):
                    continue
                file_part = target.split("#", 1)[0]
                if not file_part:
                    continue
                resolved = (path.parent / file_part).resolve()
                if resolved.exists():
                    continue
                bundled = (ROOT / "bundled" / file_part.lstrip("./")).resolve()
                self.assertTrue(bundled.exists(), f"{path.name}: 链接失效 {target}")

    def test_contract_detects_wrong_install_command(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            copy = Path(td) / "skill"
            shutil.copytree(ROOT, copy, ignore=shutil.ignore_patterns(".git", "__pycache__"))
            skill = copy / "SKILL.md"
            skill.write_text(skill.read_text(encoding="utf-8") + "\n\nappcenter-cli install app.fpk\n", encoding="utf-8")
            errors = check_contract(copy)
            self.assertTrue(any("本地 FPK 用 install-fpk" in e for e in errors), errors)

    def test_contract_detects_standalone_install_fpk(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            copy = Path(td) / "skill"
            shutil.copytree(ROOT, copy, ignore=shutil.ignore_patterns(".git", "__pycache__"))
            skill = copy / "SKILL.md"
            skill.write_text(skill.read_text(encoding="utf-8") + "\n\ninstall-fpk app.fpk\n", encoding="utf-8")
            errors = check_contract(copy)
            self.assertTrue(any("install-fpk 必须带 appcenter-cli 前缀" in e for e in errors), errors)


if __name__ == "__main__":
    unittest.main()
