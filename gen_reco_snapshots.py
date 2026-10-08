#!/usr/bin/env python3
# gen_reco_snapshots.py — 0.6.314 C：发现页固定前三推荐「完整元数据快照」生成器（构建期）
#
# 背景：发现页推荐前三固定位（fnos-apps-store / fndepot / fn-knock）在对应
# 应用源被删除或抓取失败时回落内嵌快照——快照=完整展示字段 + README 全文 +
# 完整 releases 明细 + 图标二进制 base64（不依赖上游 URL 存活）。
#
# 数据源（raw.githubusercontent 主 + jsDelivr 备，与 Moo 候选竞速同思路）：
#   1. fnos-apps-store ← conversun/fnos-apps/main/apps.json
#      （conversun 索引无 readme/多版本字段：readme 取 homepage 仓库
#       conversun/fnos-store/main/README.md；releases=当前版本单条，
#       包 URL 按 Go 适配器 conversun.go 同规则构造）
#   2. fndepot ← Blue-Mink/FnDepot/main/fnpack.json（V2 releases 多版本）
#   3. fn-knock ← kci-lnk/fn-knock-turborepo/main/moo.json（releases 多架构）
#
# 用法：python3 gen_reco_snapshots.py [--out internal/reco/snapshots.json]
# 产物头部带 generated_at（抓取时间戳，JSON 无注释，时间戳即首字段）。
# 生成失败（网络/字段缺失/自检不过）→ 退出码 1 且不覆盖已有产物。
# 顺序=用户定稿（上→下）：fnos-apps-store → fndepot → fn-knock（不随机）。
import base64
import datetime
import json
import os
import sys
import urllib.request

TIMEOUT = 30
UA = {"User-Agent": "moo-reco-snapshot/0.6.314"}


def http_get(url):
    req = urllib.request.Request(url, headers=UA)
    with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
        return r.read()


def fetch_with_fallback(urls):
    """依次尝试候选 URL，返回 (bytes, 胜出url)；全败抛异常。"""
    errs = []
    for u in urls:
        try:
            return http_get(u), u
        except Exception as e:  # noqa: BLE001
            errs.append(f"{u} → {e}")
    raise RuntimeError("全部候选失败:\n  " + "\n  ".join(errs))


def fetch_json(urls):
    """依次尝试候选 URL，返回 (解析后的JSON, 胜出url)；全败抛异常。"""
    data, winner = fetch_with_fallback(urls)
    return json.loads(data.decode("utf-8")), winner


def raw(owner, repo, ref, path):
    return f"https://raw.githubusercontent.com/{owner}/{repo}/{ref}/{path}"


def jsdelivr(owner, repo, path):
    return f"https://cdn.jsdelivr.net/gh/{owner}/{repo}/{path}"


def pick(d, *keys):
    """取第一个非空标量字段（string/number），对齐 Go pickStr/anyInt 语义。"""
    for k in keys:
        v = d.get(k)
        if v is None:
            continue
        if isinstance(v, str) and v.strip():
            return v.strip()
        if isinstance(v, (int, float)) and not isinstance(v, bool):
            return v
    return ""


def pick_str_list(d, *keys):
    """字符串或字符串数组 → 逗号分隔（对齐 Go pickStrList 的 ", " 拼接）。"""
    for k in keys:
        v = d.get(k)
        if isinstance(v, str) and v.strip():
            return v.strip()
        if isinstance(v, list):
            parts = [str(x).strip() for x in v if isinstance(x, str) and x.strip()]
            if parts:
                return ", ".join(parts)
    return ""


def app_type_of(entry):
    """→ AppInfo 层 app_type（native/docker），对齐 Go toAppInfo。"""
    for k in ("isdocker", "is_docker"):
        v = entry.get(k)
        if v is True or (isinstance(v, str) and v.strip().lower() in ("true", "1")):
            return "docker"
    if pick(entry, "app_type").lower() == "docker":
        return "docker"
    return "native"


def sort_versions_desc(versions):
    def key(v):
        nums = []
        for part in v.lstrip("v").split("."):
            i = 0
            while i < len(part) and part[i].isdigit():
                i += 1
            nums.append(int(part[:i]) if i else 0)
        return nums
    return sorted(versions, key=key, reverse=True)


def icon_b64(url):
    data = http_get(url)
    if data[:4] != b"\x89PNG":
        raise RuntimeError(f"图标不是 PNG（{url}，前 4 字节 {data[:4]!r}）")
    if len(data) > 512 * 1024:
        raise RuntimeError(f"图标过大（{len(data)}B）: {url}")
    return base64.b64encode(data).decode("ascii"), len(data)


def readme_text(url, label):
    try:
        data = http_get(url)
    except Exception as e:  # noqa: BLE001
        raise RuntimeError(f"README 抓取失败（{label}）: {url} → {e}")
    if not data.strip():
        raise RuntimeError(f"README 为空（{label}）: {url}")
    return data.decode("utf-8", "replace").strip(), url


def build_conversun():
    """fnos-apps-store ← conversun/fnos-apps apps.json（单版本、无 readme 字段）。"""
    doc, idx_url = fetch_json([
        raw("conversun", "fnos-apps", "main", "apps.json"),
        raw("conversun", "fnos-apps", "master", "apps.json"),
        jsdelivr("conversun", "fnos-apps", "apps.json"),
    ])
    entry = None
    for a in doc.get("apps", []):
        if a.get("appname") == "fnos-apps-store" or a.get("slug") == "fnos-apps-store":
            entry = a
            break
    if entry is None:
        raise RuntimeError("conversun apps.json 未找到 fnos-apps-store 条目")
    ver = str(entry.get("version") or "").strip()
    if not ver:
        raise RuntimeError("fnos-apps-store 缺 version")
    # 包 URL 构造与 internal/source/conversun.go translateConversun 同规则
    base = "https://github.com/conversun/fnos-apps/releases/download"
    tag = entry.get("release_tag") or f"fnos-apps-store/v{ver}"
    prefix = entry.get("file_prefix") or "fnos-apps-store"
    pkgs = []
    for arch in (entry.get("platforms") or []):
        if not arch:
            continue
        # conversun 索引无体积/校验和字段 → 置空（与实时条目一致：列表不显示）
        pkgs.append({
            "arch": arch,
            "download_url": f"{base}/{tag}/{prefix}_{ver}_{arch}.fpk",
            "sha256": "",
            "size": 0,
        })
    icon, icon_size = icon_b64(entry["icon_url"])
    # conversun 索引无 readme_url：取 homepage 仓库 README（上游即商店本体文档）
    readme, readme_src = readme_text(
        raw("conversun", "fnos-store", "main", "README.md"), "conversun README")
    return {
        "appname": "fnos-apps-store",
        "source": "fnos-store",
        "source_url": "https://github.com/conversun/fnos-apps",
        "key": "fnos-apps-store",
        "display_name": entry.get("display_name") or "fnOS Apps",
        "desc": entry.get("description") or "",
        "author": "conversun",
        "author_url": "https://github.com/conversun",
        "distributor": "conversun",
        "distributor_url": "",
        "homepage": entry.get("homepage_url") or "https://github.com/conversun/fnos-store",
        "bug_report_url": "",
        "license": "",
        "min_fnos": "",
        "labels": entry.get("category") or "",
        "platforms": [p for p in (entry.get("platforms") or []) if p],
        "app_type": app_type_of(entry),
        "install_type": entry.get("app_type") or "",
        "service_port": int(entry.get("service_port") or 0),
        "updated_at": entry.get("updated_at") or "",
        "first_release_at": "",
        "download_count": int(entry.get("download_count") or 0),
        "icon_url": entry.get("icon_url") or "",
        "icon_b64": icon,
        "icon_bytes": icon_size,
        "readme": readme,
        "readme_url": readme_src,
        "preview_urls": [],
        "releases": [{
            "version": ver,
            "changelog": "",
            "updated_at": entry.get("updated_at") or "",
            "packages": pkgs,
        }],
        "latest_version": ver,
        "index_url": idx_url,
    }


def build_fnpack_app(owner, repo, appname, source_name, source_url,
                     index_candidates, readme_override=None):
    """fnpack.json V2 / moo.json（releases 多版本多架构）通用构建器。"""
    doc, idx_url = fetch_json(index_candidates)
    entries = doc.get("apps", {})
    entry = entries.get(appname)
    if not isinstance(entry, dict):
        raise RuntimeError(f"{repo} 索引未找到 {appname} 条目")
    rel_raw = entry.get("releases")
    releases = []
    if isinstance(rel_raw, dict) and rel_raw:
        for ver in sort_versions_desc(list(rel_raw.keys())):
            rv = rel_raw.get(ver) or {}
            if not isinstance(rv, dict):
                continue
            pkgs = []
            praw = rv.get("packages") or {}
            if isinstance(praw, dict):
                for arch in ("x86", "arm", "all",
                             *[k for k in praw if k not in ("x86", "arm", "all")]):
                    p = praw.get(arch)
                    if not isinstance(p, dict):
                        continue
                    url = str(p.get("download_url") or "").strip()
                    if not url:
                        continue
                    size = p.get("size_bytes") or p.get("size") or 0
                    try:
                        size = int(size)
                    except (TypeError, ValueError):
                        size = 0
                    pkgs.append({
                        "arch": arch,
                        "download_url": url,
                        "sha256": str(p.get("sha256") or "").strip(),
                        "size": size,
                    })
            if pkgs:
                releases.append({
                    "version": str(ver),
                    "changelog": str(rv.get("changelog") or "").strip(),
                    "updated_at": str(rv.get("updated_at") or "").strip(),
                    "packages": pkgs,
                })
    if not releases:
        raise RuntimeError(f"{appname} releases 为空（快照要求 ≥1 版本）")
    platforms = entry.get("platform") or entry.get("platforms") or []
    if isinstance(platforms, str):
        platforms = [platforms]
    if not isinstance(platforms, list):
        platforms = []
    platforms = [str(p).strip() for p in platforms if str(p).strip()]
    readme_url = readme_override or str(entry.get("readme_url") or "").strip()
    if readme_url and not readme_url.startswith("http"):
        # 相对路径：按索引所在仓库 raw main 补全（与 Go resolveRel 语义一致）
        readme_url = raw(owner, repo, "main", readme_url.lstrip("./"))
    readme, readme_src = (readme_text(readme_url, appname) if readme_url
                          else ("", ""))
    icon = icon_b64(str(entry["icon_url"]).strip()) if entry.get("icon_url") else ("", 0)
    icon_b64v, icon_size = icon
    version = str(entry.get("version") or releases[0]["version"]).strip()
    return {
        "appname": appname,
        "source": source_name,
        "source_url": source_url,
        "key": appname,
        "display_name": str(entry.get("display_name") or appname).strip(),
        "desc": str(entry.get("desc") or "").strip(),
        "desc_html": str(entry.get("desc_html") or "").strip(),
        "author": pick(entry, "author", "maintainer"),
        "author_url": pick(entry, "author_url", "maintainer_url"),
        "distributor": str(entry.get("distributor") or "").strip(),
        "distributor_url": str(entry.get("distributor_url") or "").strip(),
        "homepage": str(entry.get("homepage") or "").strip(),
        "bug_report_url": str(entry.get("bug_report_url") or "").strip(),
        "license": str(entry.get("license") or "").strip(),
        "min_fnos": str(entry.get("min_fnos") or "").strip(),
        "labels": pick_str_list(entry, "labels", "categories", "tags"),
        "platforms": platforms,
        "app_type": app_type_of(entry),
        "install_type": str(entry.get("install_type") or "").strip(),
        "service_port": int(str(entry.get("service_port") or "0") or 0),
        "updated_at": str(entry.get("updated_at") or "").strip()
        or (releases[0].get("updated_at") or ""),
        "first_release_at": str(entry.get("first_release_at") or "").strip(),
        "download_count": int(str(entry.get("download_count") or "0") or 0),
        "icon_url": str(entry.get("icon_url") or "").strip(),
        "icon_b64": icon_b64v,
        "icon_bytes": icon_size,
        "readme": readme,
        "readme_url": readme_src,
        "preview_urls": [str(u) for u in (entry.get("preview_urls") or [])
                         if isinstance(u, str) and u.strip()],
        "releases": releases,
        "latest_version": version,
        "index_url": idx_url,
    }


def build_fndepot():
    return build_fnpack_app(
        "Blue-Mink", "FnDepot", "fndepot", "Blue-Mink",
        "https://github.com/Blue-Mink/FnDepot",
        [raw("Blue-Mink", "FnDepot", "main", "fnpack.json"),
         raw("Blue-Mink", "FnDepot", "master", "fnpack.json"),
         jsdelivr("Blue-Mink", "FnDepot", "fnpack.json")],
    )


def build_fn_knock():
    return build_fnpack_app(
        "kci-lnk", "fn-knock-turborepo", "fn-knock", "kci-lnk",
        "https://github.com/kci-lnk/fn-knock-turborepo",
        # 已知：jsDelivr 未收录该仓库 moo.json（返回空）——raw 成功即可，
        # jsDelivr 候选仅作形式兜底（Moo 运行时候选竞速同机制，不受影响）。
        [raw("kci-lnk", "fn-knock-turborepo", "main", "moo.json"),
         jsdelivr("kci-lnk", "fn-knock-turborepo", "moo.json")],
    )


def self_check(apps):
    order = ["fnos-apps-store", "fndepot", "fn-knock"]
    if [a["appname"] for a in apps] != order:
        raise RuntimeError(f"三 key 或顺序错误: {[a['appname'] for a in apps]}")
    for a in apps:
        if not a["display_name"]:
            raise RuntimeError(f"{a['appname']} display_name 为空")
        if not a["readme"]:
            raise RuntimeError(f"{a['appname']} readme 为空")
        if not a["releases"]:
            raise RuntimeError(f"{a['appname']} releases 为空")
        for r in a["releases"]:
            if not r["packages"]:
                raise RuntimeError(f"{a['appname']} {r['version']} 无包")
            for p in r["packages"]:
                if not p["download_url"].startswith("http"):
                    raise RuntimeError(f"{a['appname']} 包下载链接异常: {p['download_url']}")
        if not a["icon_b64"]:
            raise RuntimeError(f"{a['appname']} 图标缺失")
        raw_bytes = base64.b64decode(a["icon_b64"])
        if raw_bytes[:4] != b"\x89PNG":
            raise RuntimeError(f"{a['appname']} 图标 base64 解出非 PNG")


def main():
    out = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                       "internal/reco/snapshots.json")
    if "--out" in sys.argv:
        out = sys.argv[sys.argv.index("--out") + 1]
    # 顺序=用户定稿（上→下）：fnos-apps-store → fndepot → fn-knock
    apps = [build_conversun(), build_fndepot(), build_fn_knock()]
    self_check(apps)
    doc = {
        "generated_at": datetime.datetime.now(datetime.timezone.utc)
        .strftime("%Y-%m-%dT%H:%M:%SZ"),
        "generator": "gen_reco_snapshots.py (0.6.314 C)",
        "note": "发现页固定前三推荐完整元数据快照；源删除/抓取失败时回落，"
                "源恢复后自动切回实时（按当前目录每请求判定，非粘性）。",
        "apps": apps,
    }
    data = json.dumps(doc, ensure_ascii=False, indent=1).encode("utf-8")
    if len(data) > 400 * 1024:
        print(f"[warn] 快照 {len(data)//1024}KB 超预期（100-200KB），仍继续",
              file=sys.stderr)
    tmp = out + ".tmp"
    with open(tmp, "wb") as f:
        f.write(data)
    os.replace(tmp, out)
    for a in apps:
        print(f"  {a['appname']}: {a['display_name']} v{a['releases'][0]['version']} "
              f"readme={len(a['readme'])}B icon={a['icon_bytes']}B "
              f"releases={len(a['releases'])} pkgs="
              f"{sum(len(r['packages']) for r in a['releases'])}")
    print(f"OK {out} ({len(data)//1024}KB)")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as e:  # noqa: BLE001
        print(f"FAIL: {e}", file=sys.stderr)
        sys.exit(1)
