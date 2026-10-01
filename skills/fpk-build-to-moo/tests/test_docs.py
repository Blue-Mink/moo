#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""fpk-build-to-moo 技能文档自检：结构完整 + 路由可达 + 无敏感信息泄漏。

运行：python3 -m unittest discover -s tests   （在技能根目录执行）
"""
import pathlib
import re
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
SKILL = ROOT / "SKILL.md"

# 不得出现在技能文档里的敏感模式
FORBIDDEN = [
    (r"192\.168\.\d+\.\d+", "真实内网地址（示例请用 RFC5737 网段 192.0.2.x）"),
    (r"\b10\.\d+\.\d+\.\d+", "真实内网地址"),
    (r"ghp_[A-Za-z0-9]{20,}", "GitHub token"),
    (r"Abc\d{6}", "疑似真实口令"),
    (r"bluemink", "真实账号/仓库名（示例请用 owner/repo）"),
    (r"389fca67|81e9137b|e edd1462", "未公开构件的 sha256"),
]


class TestSkillDocs(unittest.TestCase):
    def test_skill_md_exists_with_frontmatter(self):
        self.assertTrue(SKILL.exists(), "缺少 SKILL.md")
        text = SKILL.read_text(encoding="utf-8")
        self.assertTrue(text.startswith("---"), "SKILL.md 缺少 frontmatter")
        self.assertIn("name: fpk-build-to-moo", text)
        self.assertIn("description:", text)

    def test_router_links_resolve(self):
        text = SKILL.read_text(encoding="utf-8")
        for m in re.finditer(r"\]\(([^)]+\.md)(#[^)]+)?\)", text):
            target = m.group(1)
            if target.startswith("http"):
                continue
            self.assertTrue((ROOT / target).exists(), f"路由链接失效: {target}")

    def test_protocol_reference_present(self):
        # 仓库版：协议正文就是仓库里的 docs/MOO-PROTOCOL.md（不另存副本，避免漂移）
        ref = ROOT.parents[1] / "docs" / "MOO-PROTOCOL.md"
        self.assertTrue(ref.exists(), "缺少 docs/MOO-PROTOCOL.md")
        body = ref.read_text(encoding="utf-8")
        self.assertIn('"schema_version": "moo"', body)
        self.assertIn("分类取值", body, "协议文档应含分类章节")
        self.assertIn("固定清单", body, "分类应为固定清单")

    def test_no_sensitive_leaks(self):
        for path in ROOT.rglob("*"):
            if not path.is_file() or path.suffix not in (".md", ".py", ".json", ".sh"):
                continue
            if "/tests/" in str(path):
                continue
            text = path.read_text(encoding="utf-8", errors="ignore")
            for pattern, why in FORBIDDEN:
                hits = re.findall(pattern, text)
                if hits:
                    self.fail(f"{path.relative_to(ROOT)} 命中敏感模式「{why}」: {hits[:3]}")


if __name__ == "__main__":
    unittest.main()
