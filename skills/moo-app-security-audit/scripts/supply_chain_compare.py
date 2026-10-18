#!/usr/bin/env python3
"""供应链完整性对比脚本。

用途：对比「搬运源 FPK」与「上游官方 FPK」内的二进制，验证搬运者是否篡改。
用法：
    python3 supply_chain_compare.py <本地搬运fpk> <上游官方fpk或release_url>

输出：两侧 app.tgz 内大文件（≥100KB）的名称/大小/sha256，逐文件对比一致/不一致/仅一侧存在。
说明：sha256 不一致可能是编译参数/strip 差异，不必然等于被篡改，需结合大小与文件清单判断。
"""
import os, re, sys, tarfile, io, hashlib, subprocess

MIN_SIZE = 100_000
SKIP_EXT = (".json", ".png", ".jpg", ".jpeg", ".map", ".css", ".html", ".svg", ".woff", ".woff2", ".ttf", ".br")


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def binaries_in_fpk(fpk_path):
    """解包 FPK，提取 app.tgz 内 ≥100KB 的非资源文件 → {name: (size, sha256)}。"""
    out = {}
    with tarfile.open(fpk_path, "r:gz") as tf:
        # 找 app.tgz
        app_tgz = None
        for m in tf.getmembers():
            if m.name.endswith("app.tgz") and m.isfile():
                app_tgz = tf.extractfile(m).read()
                break
        if app_tgz is None:
            return out
    with tarfile.open(fileobj=io.BytesIO(app_tgz), mode="r:gz") as af:
        for m in af.getmembers():
            if not m.isfile():
                continue
            if m.name.endswith(SKIP_EXT):
                continue
            data = af.extractfile(m).read()
            if len(data) < MIN_SIZE:
                continue
            out[m.name] = (len(data), sha256(data))
    return out


def download(url, dest):
    """下载上游 FPK 到本地临时文件（仅 https；目的地址=用户显式传入的 URL，便于审计）。"""
    if not re.match(r"^https://", url):
        raise SystemExit(f"!! 只允许 https 上游地址: {url}")
    rc = subprocess.call([
        "curl", "-fsSL", "--max-time", "600",
        "-A", "moo-app-security-audit", "-o", dest, url,
    ])
    if rc != 0:
        raise SystemExit(f"!! 下载失败（curl exit {rc}）: {url}")


def main():
    if len(sys.argv) != 3:
        print("用法: python3 supply_chain_compare.py <本地搬运fpk> <上游官方fpk或release_url>")
        sys.exit(1)
    local = sys.argv[1]
    remote = sys.argv[2]
    if not os.path.isfile(local):
        print(f"!! 本地 FPK 不存在: {local}")
        sys.exit(1)

    # 上游：本地文件直接用，否则当 URL 下载
    if os.path.isfile(remote):
        upstream = remote
    elif remote.startswith("http"):
        upstream = os.path.join(os.path.dirname(local) or ".", "_upstream_compare.fpk")
        print(f"下载上游: {remote}")
        download(remote, upstream)
    else:
        print(f"!! 上游不是本地文件也不是 URL: {remote}")
        sys.exit(1)

    left = binaries_in_fpk(local)
    right = binaries_in_fpk(upstream)
    all_names = sorted(set(left) | set(right))
    if not all_names:
        print("两侧均未提取到 ≥100KB 的二进制文件。")
        return

    print(f"\n{'文件':<40} {'本地大小':>10} {'上游大小':>10}  结论")
    print("-" * 80)
    for name in all_names:
        ls, lh = left.get(name, (0, ""))
        rs, rh = right.get(name, (0, ""))
        if name not in left:
            verdict = "仅上游有"
        elif name not in right:
            verdict = "仅本地有"
        elif lh == rh:
            verdict = "sha256 一致 ✅"
        else:
            verdict = "sha256 不一致 ⚠️"
        print(f"{name:<40} {ls:>10} {rs:>10}  {verdict}")

    print("\n说明: sha256 不一致可能是编译参数/strip 差异，需结合大小与文件清单判断是否被篡改。")


if __name__ == "__main__":
    main()
