#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""从 Gitea 仓库的 releases 生成 moo.json（Moo 应用源协议 v1）。

用途：把「承载 Moo FPK 的仓库」本身变成一个可被 Moo 添加的应用源
（应用 = Moo 自己；多版本走 releases 映射）。

用法：
  python3 tools/gen-repo-moo-json.py \
      --repo https://gitea.example.com/owner/repo \
      --user bluemink --password '***' \
      --app moo --name "Moo 应用商店" \
      --homepage https://gitea.example.com/owner/repo \
      --icon-file fnos/ICON.PNG \
      --out /tmp/moo.json
"""
import argparse
import base64
import json
import re
import sys
import urllib.parse
import urllib.request

SHA_RE = re.compile(r"FPK sha256:\s*`([0-9a-fA-F]{64})`")
SHORT_SHA_RE = re.compile(r"sha256\s+`?([0-9a-fA-F]{8,64})`?")


def api(url, user, password):
    req = urllib.request.Request(url)
    if user:
        tok = base64.b64encode(f"{user}:{password}".encode()).decode()
        req.add_header("Authorization", "Basic " + tok)
    with urllib.request.urlopen(req, timeout=60) as r:
        return json.loads(r.read().decode())


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", required=True, help="仓库地址，如 http://host:3033/owner/repo")
    ap.add_argument("--user", default="")
    ap.add_argument("--password", default="")
    ap.add_argument("--app", default="moo", help="应用 appname（条目 key）")
    ap.add_argument("--name", default="", help="display_name")
    ap.add_argument("--distributor", default="")
    ap.add_argument("--homepage", default="")
    ap.add_argument("--icon-file", default="", help="仓库内图标路径（生成 raw 链接）")
    ap.add_argument("--labels", default="", help="分类标签（如 系统工具）")
    ap.add_argument("--install-type", default="", help="运行方式/安装位置（如 root / 用户空间）")
    ap.add_argument("--preview", default="", help="预览图（仓库内相对路径，逗号分隔）")
    ap.add_argument("--readme-file", default="", help="README 文件（仓库内相对路径）")
    ap.add_argument("--out", default="moo.json")
    ap.add_argument("--limit", type=int, default=500, help="最多拉取多少个 release")
    args = ap.parse_args()

    base = args.repo.rstrip("/")
    parsed = urllib.parse.urlsplit(base)
    host = f"{parsed.scheme}://{parsed.netloc}"
    parts = [p for p in parsed.path.split("/") if p]
    owner, repo = parts[-2], parts[-1]
    api_base = f"{host}/api/v1/repos/{owner}/{repo}"

    releases = []
    page = 1
    while len(releases) < args.limit:
        batch = api(f"{api_base}/releases?limit=50&page={page}", args.user, args.password)
        if not batch:
            break
        releases.extend(batch)
        if len(batch) < 50:
            break
        page += 1
    print(f"拉取 release {len(releases)} 个", file=sys.stderr)

    releases_out = {}
    latest = None
    latest_when = ""
    first_when = ""
    total_downloads = 0
    for rel in releases:
        tag = rel.get("tag_name") or ""
        fpk = next((a for a in rel.get("assets", []) if a.get("name", "").endswith(".fpk")), None)
        if not fpk:
            continue
        ver = tag.lstrip("v")
        body = rel.get("body") or ""
        m = SHA_RE.search(body)
        if not m:
            m = SHORT_SHA_RE.search(body)
        sha = m.group(1) if m else ""
        # 更新日志：正文第一行（模板 = `**亮点**——说明`）
        first = next((ln.strip() for ln in body.splitlines() if ln.strip()), "")
        pkg = {
            # 注意单位：packages.<arch>.size 按 **字节** 约定（与 FnDepot/RROrg 一致）；
            # 人类可读的 MB 另放 size_mb，避免被当成字节读成个位数。
            "download_url": fpk["browser_download_url"],
            "size": fpk["size"],
            "size_mb": round(fpk["size"] / (1024 * 1024), 2),
        }
        if sha:
            pkg["sha256"] = sha
        rel_entry = {"changelog": first, "packages": {"x86": pkg}}
        when = rel.get("published_at") or rel.get("created_at") or ""
        if when:
            rel_entry["updated_at"] = when
            if not first_when or when < first_when:
                first_when = when
        # 真实下载次数：Gitea 资产的 download_count 累加（该版所有附件）
        total_downloads += sum(int(a.get("download_count") or 0) for a in rel.get("assets", []))
        releases_out[ver] = rel_entry
        if latest is None or _ver_gt(ver, latest):
            latest = ver
            latest_when = when

    entry = {
        "display_name": args.name or args.app,
        "app_type": "fpk",
        "platform": ["x86"],
        "desc": f"{args.name or args.app} 自托管的 FPK 发布仓库（releases 作为多版本索引）。",
        "author": args.distributor or owner,
        "distributor": args.distributor or owner,
        "homepage": args.homepage or base,
        "license": "MIT",
        "releases": releases_out,
    }
    if latest and releases_out.get(latest, {}).get("packages", {}).get("x86", {}).get("sha256"):
        entry["sha256"] = releases_out[latest]["packages"]["x86"]["sha256"]
    if args.icon_file:
        entry["icon_url"] = f"{base}/raw/branch/main/{args.icon_file}"
    if args.readme_file:
        rf = args.readme_file
        entry["readme_url"] = rf if rf.startswith("http") else f"{base}/raw/branch/main/{rf}"
    if args.labels:
        entry["labels"] = args.labels
    if args.install_type:
        entry["install_type"] = args.install_type
    if args.preview:
        entry["preview_urls"] = [
            u.strip() if u.strip().startswith("http") else f"{base}/raw/branch/main/{u.strip()}"
            for u in args.preview.split(",") if u.strip()
        ]
    if total_downloads > 0:
        entry["download_count"] = total_downloads
    if first_when:
        entry["first_release_at"] = first_when
    # 顶层 updated_at = 最新那版的发布时间（列表/详情显示「最近更新时间」）
    if latest_when:
        entry["updated_at"] = latest_when

    doc = {
        "schema_version": "moo",
        "source_info": {
            "name": args.name or repo,
            "homepage": args.homepage or base,
            "distributor": args.distributor or owner,
            "generated_by": "gen-repo-moo-json",
        },
        "apps": {args.app: entry},
    }
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=False, indent=2)
        f.write("\n")
    print(f"应用 {args.app}：{len(releases_out)} 个版本（最新 {latest}；累计下载 {total_downloads}；最早 {first_when[:10]}）→ {args.out}", file=sys.stderr)
    if latest:
        print(json.dumps(releases_out[latest], ensure_ascii=False, indent=2)[:400], file=sys.stderr)


def _ver_gt(a: str, b: str) -> bool:
    """版本比较（数值分段；-panel 等后缀按字典序兜底）。"""
    def key(v):
        out = []
        for part in re.split(r"[.\-]", v.lstrip("v")):
            out.append((0, int(part)) if part.isdigit() else (1, part))
        return out
    try:
        return key(a) > key(b)
    except TypeError:
        return a > b


if __name__ == "__main__":
    main()
