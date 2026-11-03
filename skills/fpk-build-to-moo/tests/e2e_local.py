#!/usr/bin/env python3
"""Local E2E for the publishing chain.

Build (fnpack if available, otherwise a synthetic FPK) -> validate manifest
checksum/app.tgz -> write moo.json + fnpack.json -> serve over HTTP -> fetch
index/FPK/icon and verify sha256/size. No device, no network, no writes outside
a temporary directory.

Run: python3 tests/e2e_local.py
Exit: 0 PASS/SKIP, 1 failure.
"""
from __future__ import annotations

import hashlib
import io
import json
import shutil
import struct
import subprocess
import sys
import tarfile
import tempfile
import threading
import urllib.request
import zlib
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

APP = "e2e-demo"
VER = "1.0.0"
PORT = 18080


def png512() -> bytes:
    w = h = 512
    raw = b"".join(b"\x00" + bytes((30, 120, 200)) * w for _ in range(h))

    def chunk(tag: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + tag + data + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)

    return (b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b""))


def write_source(root: Path, platform: str = "x86") -> None:
    (root / "manifest").write_text(
        f"""appname={APP}
version={VER}
display_name=E2E Demo
desc=E2E demo app
source=thirdparty
maintainer=Example
distributor=Example
platform={platform}
service_port={PORT}
checkport=false
ctl_stop=true
desktop_uidir=ui
desktop_applaunchname={APP}.main
install_type=
changelog=E2E test
""", encoding="utf-8")
    for d in ("cmd", "config", "wizard", "app/ui/images"):
        (root / d).mkdir(parents=True, exist_ok=True)
    for name in ("install_init", "install_callback", "uninstall_init", "uninstall_callback",
                 "upgrade_init", "upgrade_callback", "config_init", "config_callback", "common"):
        f = root / "cmd" / name
        f.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        f.chmod(0o755)
    main = root / "cmd/main"
    main.write_text('#!/bin/sh\ncase "$1" in start|stop|status) exit 0;; *) exit 1;; esac\n', encoding="utf-8")
    main.chmod(0o755)
    (root / "config/privilege").write_text('{"defaults":{"run-as":"package"}}', encoding="utf-8")
    (root / "config/resource").write_text('{"data-share":{"shares":[]}}', encoding="utf-8")
    (root / "wizard/install").write_text(json.dumps([{"stepTitle": "基本配置", "items": [
        {"type": "text", "field": "wizard_access_port", "label": "端口", "initValue": str(PORT)}
    ]}], ensure_ascii=False), encoding="utf-8")
    (root / "app/ui/config").write_text(json.dumps({".url": {f"{APP}.main": {
        "title": "E2E Demo", "icon": "images/ICON.PNG", "type": "url",
        "protocol": "http", "port": str(PORT), "url": "/", "allUsers": True}}}, ensure_ascii=False), encoding="utf-8")
    icon = png512()
    for name in ("ICON.PNG", "ICON_256.PNG"):
        (root / name).write_bytes(icon)
    for name in ("ICON.PNG", "ICON_64.PNG", "ICON_256.PNG"):
        (root / "app/ui/images" / name).write_bytes(icon)
    (root / "app/index.html").write_text("<h1>ok</h1>", encoding="utf-8")


def synthetic_fpk(src: Path, out_dir: Path, name: str) -> Path:
    app_tgz = out_dir / "app.tgz"
    with tarfile.open(app_tgz, "w:gz") as tf:
        for rel in ("index.html", "ui/config", "ui/images/ICON.PNG"):
            tf.add(src / "app" / rel, arcname=rel)
    manifest = (src / "manifest").read_text(encoding="utf-8").rstrip() + \
        f"\nchecksum={hashlib.md5(app_tgz.read_bytes()).hexdigest()}\n"
    fpk = out_dir / name
    with tarfile.open(fpk, "w:gz") as tf:
        for name, data in (("manifest", manifest.encode()), ("ICON.PNG", (src / "ICON.PNG").read_bytes()),
                           ("ICON_256.PNG", (src / "ICON_256.PNG").read_bytes()), ("app.tgz", app_tgz.read_bytes())):
            info = tarfile.TarInfo(name)
            info.size = len(data)
            tf.addfile(info, io.BytesIO(data))
        for rel in ("cmd/main", "config/privilege", "config/resource", "wizard/install"):
            tf.add(src / rel, arcname=rel)
    return fpk


def build_fpk(src: Path, out_dir: Path, suffix: str) -> tuple[Path, str]:
    if shutil.which("fnpack"):
        r = subprocess.run(["fnpack", "build", "-d", str(src)], cwd=out_dir,
                           text=True, capture_output=True, timeout=120)
        if r.returncode != 0:
            raise RuntimeError(f"fnpack build failed ({suffix}): {r.stdout[-400:]} {r.stderr[-400:]}")
        fpk = next(out_dir.glob("*.fpk"))
        target = out_dir / f"{APP}_{suffix}.fpk"
        if fpk != target:
            fpk.rename(target)
        return target, "fnpack"
    return synthetic_fpk(src, out_dir, f"{APP}_{suffix}.fpk"), "synthetic"


def manifest_of(fpk: Path) -> dict[str, str]:
    with tarfile.open(fpk, "r:*") as tf:
        raw = tf.extractfile("manifest").read().decode("utf-8")
    out = {}
    for line in raw.splitlines():
        if "=" in line and not line.lstrip().startswith("#"):
            k, v = line.split("=", 1)
            out[k.strip()] = v.strip()
    return out


def validate_fpk(fpk: Path, platform: str) -> bytes:
    with tarfile.open(fpk, "r:*") as tf:
        names = tf.getnames()
        app_tgz = tf.extractfile("app.tgz").read()
        icon = tf.extractfile("ICON.PNG").read()
        assert "manifest" in names and "app.tgz" in names, names
    manifest = manifest_of(fpk)
    assert manifest.get("checksum") == hashlib.md5(app_tgz).hexdigest(), "manifest.checksum != md5(app.tgz)"
    assert manifest.get("platform") == platform
    with tarfile.open(fileobj=io.BytesIO(app_tgz), mode="r:*") as at:
        members = at.getnames()
        assert "ui/config" in members, members
        assert not any(m.startswith("app/") for m in members), "app.tgz has outer app/ prefix"
    w, h = struct.unpack(">II", icon[16:24])
    assert w >= 512 and h >= 512, (w, h)
    return app_tgz


def main() -> int:
    from functools import partial
    with tempfile.TemporaryDirectory(prefix="fpk-e2e-") as td:
        root = Path(td)
        src, out, pub = root / "src", root / "out", root / "pub"
        for d in (src, out, pub / "packages", pub / "assets"):
            d.mkdir(parents=True, exist_ok=True)
        results: dict[str, dict[str, object]] = {}
        modes: set[str] = set()
        for platform, suffix in (("x86", "x86"), ("arm", "arm")):
            src_i = root / f"src-{suffix}"
            out_i = root / f"out-{suffix}"
            src_i.mkdir(parents=True, exist_ok=True)
            out_i.mkdir(parents=True, exist_ok=True)
            write_source(src_i, platform)
            fpk, mode = build_fpk(src_i, out_i, suffix)
            validate_fpk(fpk, platform)
            sha = hashlib.sha256(fpk.read_bytes()).hexdigest()
            size = fpk.stat().st_size
            results[suffix] = {"sha256": sha, "size": size}
            modes.add(mode)
            shutil.copy2(fpk, pub / "packages" / f"{APP}_{suffix}.fpk")
            if suffix == "x86":
                shutil.copy2(src_i / "ICON.PNG", pub / "assets/ICON.PNG")
        mode = "+".join(sorted(modes))
        x86, arm = results["x86"], results["arm"]
        moo = {
            "schema_version": "moo",
            "source_info": {"name": "E2E source"},
            "apps": {APP: {
                "display_name": "E2E Demo", "version": VER,
                "download_url": f"packages/{APP}_x86.fpk", "sha256": x86["sha256"],
                "size": round(x86["size"] / 1048576, 3), "size_bytes": x86["size"],
                "categories": ["实用效率"], "desc": "E2E demo",
                "bug_report_url": "https://example.com/issues",
                "wizard": {"fields": [{"key": "SITE_TITLE", "label": "站点标题", "default": "demo", "required": True}]},
                "releases": {VER: {"packages": {
                    "x86": {"download_url": f"packages/{APP}_x86.fpk", "sha256": x86["sha256"], "size": x86["size"]},
                    "arm": {"download_url": f"packages/{APP}_arm.fpk", "sha256": arm["sha256"], "size": arm["size"]},
                }}},
            }},
        }
        fnpack = {
            "schema_version": "2",
            "source_info": {"name": "E2E source", "author": "Example"},
            "apps": {APP: {
                "display_name": "E2E Demo", "desc": "E2E demo", "platform": ["x86", "arm"],
                "categories": ["系统工具"], "icon_url": "assets/ICON.PNG",
                "run_as": "package", "install_type": "", "is_docker": False,
                "releases": {VER: {"packages": {
                    "x86": {"download_url": f"packages/{APP}_x86.fpk", "sha256": x86["sha256"], "size": x86["size"]},
                    "arm": {"download_url": f"packages/{APP}_arm.fpk", "sha256": arm["sha256"], "size": arm["size"]},
                }}},
            }},
        }
        (pub / "moo.json").write_text(json.dumps(moo, ensure_ascii=False), encoding="utf-8")
        (pub / "fnpack.json").write_text(json.dumps(fnpack, ensure_ascii=False), encoding="utf-8")

        handler = partial(SimpleHTTPRequestHandler, directory=str(pub))
        httpd = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        thread = threading.Thread(target=httpd.serve_forever, daemon=True)
        thread.start()
        base = f"http://127.0.0.1:{httpd.server_address[1]}"
        try:
            for name in ("moo.json", "fnpack.json"):
                with urllib.request.urlopen(f"{base}/{name}", timeout=10) as r:
                    assert r.status == 200
            for suffix in ("x86", "arm"):
                with urllib.request.urlopen(f"{base}/packages/{APP}_{suffix}.fpk", timeout=10) as r:
                    body = r.read()
                    expected = results[suffix]
                    assert r.status == 200 and len(body) == expected["size"]
                    assert hashlib.sha256(body).hexdigest() == expected["sha256"]
            with urllib.request.urlopen(f"{base}/assets/ICON.PNG", timeout=10) as r:
                icon = r.read()
                assert struct.unpack(">II", icon[16:24]) == (512, 512)
            with urllib.request.urlopen(f"{base}/moo.json", timeout=10) as r:
                got = json.load(r)
            assert got["apps"][APP]["sha256"] == x86["sha256"]
            assert got["apps"][APP]["releases"][VER]["packages"]["arm"]["sha256"] == arm["sha256"]
            assert got["apps"][APP]["wizard"]["fields"][0]["key"] == "SITE_TITLE"
            with urllib.request.urlopen(f"{base}/fnpack.json", timeout=10) as r:
                got = json.load(r)
            assert got["apps"][APP]["platform"] == ["x86", "arm"]
            assert got["apps"][APP]["releases"][VER]["packages"]["x86"]["size"] == x86["size"]
            assert got["apps"][APP]["releases"][VER]["packages"]["arm"]["size"] == arm["size"]
        finally:
            httpd.shutdown()
            thread.join(timeout=5)
        print(f"E2E PASS  mode={mode}  x86={x86['size']}B  arm={arm['size']}B  "
              f"sha256={x86['sha256'][:12]}…/{arm['sha256'][:12]}…  index=200  fpk=200(x2)  icon=512")
        return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"E2E FAIL: {exc}", file=sys.stderr)
        raise SystemExit(1)
