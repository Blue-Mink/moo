#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""update_fnpack.py 回归用例（2026-11-28）。

覆盖三件容易回归的事：
  1. release 只给一个架构时，既有另一个架构的包**不能被删**（真丢包）；
  2. 同版本同包同日志重复跑 = 字节不变（否则每次 dispatch 都空提交）；
  3. 防同版本换包 / platform 覆盖校验 / --prune-only 保留 N 版。
"""
import hashlib
import io
import json
import os
import sys
import tarfile
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import update_fnpack as F  # noqa: E402

BASE = "https://github.com/Blue-Mink/moo/releases/download/v0.6.579/"


def make_fpk(dirpath, appname="moo", version="0.6.579", platform="x86", name=None):
    """造一个最小 FPK（tar.gz + manifest），返回路径 / size / sha256。"""
    path = os.path.join(dirpath, name or ("%s_%s_%s.fpk" % (appname, version, platform)))
    manifest = "appname=%s\nversion=%s\nplatform=%s\n" % (appname, version, platform)
    with tarfile.open(path, "w:gz") as tf:
        data = manifest.encode()
        ti = tarfile.TarInfo("manifest")
        ti.size = len(data)
        tf.addfile(ti, io.BytesIO(data))
    raw = open(path, "rb").read()
    return path, len(raw), hashlib.sha256(raw).hexdigest()


def pkg(url, sha, size):
    return {"download_url": url, "sha256": sha, "size": size}


def index_with(releases, platform=("x86",)):
    if isinstance(platform, str):
        platform = (platform,)
    return {
        "schema_version": 2,
        "apps": {"moo": {"desc": "d", "platform": list(platform), "releases": releases}},
    }


class UpdateFnpackTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp(prefix="fnpack-test-")
        self.idx = os.path.join(self.dir, "fnpack.json")

    def write(self, data):
        with open(self.idx, "w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, indent=2)
            f.write("\n")
        return open(self.idx, "rb").read()

    def read(self):
        return json.load(open(self.idx, encoding="utf-8"))

    def test_single_arch_keeps_existing_other_arch(self):
        fpk, size, sha = make_fpk(self.dir, platform="x86")
        old = {"0.6.579": {"updated_at": "2026-11-28T02:30:00+08:00", "changelog": "c",
                           "packages": {"x86": pkg(BASE + "moo_0.6.579_x86.fpk", sha, size),
                                        "arm": pkg(BASE + "moo_0.6.579_arm.fpk", "a" * 64, 123)}}}
        before = self.write(index_with(old, ("x86", "arm")))
        # platform 只有 x86，但既有 arm 包：本次不给 arm，必须保留
        rc = F.main(["--index", self.idx, "--tag", "v0.6.579", "--fpk", fpk])
        self.assertEqual(rc, 0)
        self.assertEqual(open(self.idx, "rb").read(), before, "同版本同包重复跑必须字节不变")
        self.assertIn("arm", self.read()["apps"]["moo"]["releases"]["0.6.579"]["packages"])

    def test_new_version_written_and_pruned(self):
        fpk, size, sha = make_fpk(self.dir, version="0.6.580", platform="x86")
        rels = {
            "0.6.579": {"updated_at": "x", "changelog": "c",
                        "packages": {"x86": pkg(BASE + "a.fpk", "b" * 64, 1)}},
            "0.6.500": {"updated_at": "y", "changelog": "old",
                        "packages": {"x86": pkg(BASE + "b.fpk", "c" * 64, 1)}},
        }
        self.write(index_with(rels))
        rc = F.main(["--index", self.idx, "--tag", "v0.6.580", "--fpk", fpk,
                     "--changelog", "新版本", "--now", "2026-11-28T09:00:00+08:00"])
        self.assertEqual(rc, 0)
        got = self.read()["apps"]["moo"]["releases"]
        self.assertEqual(sorted(got, reverse=True)[:2], ["0.6.580", "0.6.579"])
        self.assertEqual(len(got), 2, "keep=2 必须裁掉更旧的 0.6.500")
        self.assertEqual(got["0.6.580"]["packages"]["x86"]["sha256"], sha)
        self.assertIn("/releases/download/v0.6.580/", got["0.6.580"]["packages"]["x86"]["download_url"])

    def test_same_version_new_sha_refused(self):
        fpk, size, sha = make_fpk(self.dir, platform="x86")
        rels = {"0.6.579": {"updated_at": "x", "changelog": "c",
                            "packages": {"x86": pkg(BASE + "moo_0.6.579_x86.fpk", "0" * 64, size)}}}
        self.write(index_with(rels))
        with self.assertRaises(SystemExit) as ctx:
            F.main(["--index", self.idx, "--tag", "v0.6.579", "--fpk", fpk])
        self.assertEqual(ctx.exception.code, 2)

    def test_check_flags_platform_gap(self):
        rels = {"0.6.579": {"updated_at": "x", "changelog": "c",
                            "packages": {"x86": pkg(BASE + "a.fpk", "b" * 64, 1),
                                         "arm": pkg(BASE + "b.fpk", "c" * 64, 1)}}}
        self.write(index_with(rels, ("x86",)))
        self.assertEqual(F.main(["--index", self.idx, "--check"]), 1)

    def test_prune_only_keeps_two(self):
        rels = {}
        for v in ("0.6.580", "0.6.579", "0.6.500"):
            rels[v] = {"updated_at": "x", "changelog": "", "packages": {"x86": pkg(BASE + v, "d" * 64, 1)}}
        self.write(index_with(rels))
        self.assertEqual(F.main(["--index", self.idx, "--prune-only", "--keep", "2"]), 0)
        self.assertEqual(sorted(self.read()["apps"]["moo"]["releases"]), ["0.6.579", "0.6.580"])


if __name__ == "__main__":
    unittest.main()
