#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""update_fnpack.py — FnDepot `fnpack.json`（V2）自动跟版。

从 FPK 的 manifest 读 appname / version / platform，按 **真实 release tag**
拼下载直链（沿用该应用既有直链的仓库前缀），写入 `apps.<key>.releases.<ver>`，
并只保留最近 `--keep` 个版本（默认 2）。

要点：
  * 不新建应用条目：appname 找不到就报错，避免拼错名生成幽灵应用
  * 不动 V2 的应用级字段（fnpack V2 没有顶层 version/download_url）
  * 新写入的架构会增补到应用级 `platform`（默认；`--no-platform-sync` 可关）
  * `size_mb` 仅在该应用历史包已有该字段时才写，保持原风格
  * `--tag` 跟版时必填（FnDepot 的 tag 命名不规律，猜 `v<版本>` 会写出 404 直链）
  * 防降级 / 防同版本换包，需显式开关放行
  * `--check` 同时校验 `platform` 是否覆盖各 `packages` 架构（arm 包不能被 x86-only 隐藏）

用法：
  python3 tools/update_fnpack.py --index fnpack.json --tag v0.8.9 --fpk dist/*.fpk
  python3 tools/update_fnpack.py --index fnpack.json --prune-only --keep 2
  python3 tools/update_fnpack.py --index fnpack.json --check
  python3 tools/update_fnpack.py --index fnpack.json --dry-run --tag ... --fpk ...

退出码：0 成功/无变化；1 校验失败；2 参数或数据错误。
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import update_moo_json as u  # noqa: E402

ALLOWED_ARCH = ("x86", "arm", "all")


def die(msg):
    """参数/数据错误统一退出码 2（沿用 update_moo_json 的文案风格）。"""
    print("错误：" + msg, file=sys.stderr)
    raise SystemExit(2)


def load(path):
    try:
        with open(path, encoding="utf-8") as f:
            raw = f.read()
    except OSError as e:
        die("读取 %s 失败：%s" % (path, e))
    try:
        data = json.loads(raw)
    except ValueError as e:
        die("%s 不是合法 JSON：%s" % (path, e))
    if not isinstance(data, dict):
        die("%s 顶层不是对象" % path)
    return data, raw


def find_key(apps, appname, override):
    want = (override or appname or "").strip()
    if not want:
        die("拿不到 appname（FPK manifest 缺字段？）")
    if want in apps:
        return want
    lower = [k for k in apps if k.lower() == want.lower()]
    if lower:
        return lower[0]
    die("索引里没有应用 %r（不自动新建）；现有：%s" % (want, ", ".join(sorted(apps))))


def url_base(entry, repo):
    """该应用既有直链的仓库前缀（…/releases/download/ 之前）；没有则用 --repo。"""
    for node in (entry.get("releases") or {}).values():
        for pkg in ((node or {}).get("packages") or {}).values():
            url = (pkg or {}).get("download_url") or ""
            i = url.find("/releases/download/")
            if i > 0:
                return url[:i]
    if repo:
        return u.release_base(repo).rstrip("/")
    return ""


def existing_arch_keys(entry):
    keys = []
    for node in (entry.get("releases") or {}).values():
        for arch in ((node or {}).get("packages") or {}):
            if arch not in keys:
                keys.append(arch)
    return keys


def entry_platforms(entry):
    value = entry.get("platform")
    if isinstance(value, list):
        return [str(item).strip() for item in value if str(item).strip()]
    if isinstance(value, str) and value.strip():
        return [value.strip()]
    return []


def merge_platforms(entry, pkgs):
    """把新写入的架构补进应用级 platform；all 已是全集时不改。"""
    current = entry_platforms(entry)
    if "all" in current:
        return []
    added = [arch for arch in pkgs if arch not in current]
    if not current:
        added = list(pkgs)
    if not added:
        return []
    order = {"x86": 0, "arm": 1, "all": 2}
    merged = sorted(dict.fromkeys(current + added), key=lambda item: (order.get(item, 9), item))
    entry["platform"] = merged
    return merged


def arch_from_name(path):
    n = os.path.basename(path).lower()
    if "arm64" in n or "aarch64" in n or "armhf" in n:
        return "arm"
    if "amd64" in n or "x86_64" in n or "x86" in n:
        return "x86"
    return ""


def pick_arch(platform, path, fpks_count, existing_keys):
    """manifest.platform 归一化；缺省时按文件名推断；单包 all 沿用该应用历史约定。"""
    arch = u.normalize_arch(platform)
    if not (platform or "").strip() or arch == "all":
        by_name = arch_from_name(path)
        if by_name:
            arch = by_name
    if arch == "all" and fpks_count == 1:
        if existing_keys == ["all"]:
            return "all"
        if "x86" in existing_keys:
            return "x86"
        if "arm" in existing_keys:
            return "arm"
    if arch not in ALLOWED_ARCH:
        die("不认识的架构 %r（manifest.platform=%r）" % (arch, platform))
    return arch


def prune_entry(entry, keep):
    """按版本号保留最近 keep 个；返回被删版本列表。keep<=0 表示全留。"""
    rels = entry.get("releases")
    if not isinstance(rels, dict) or not rels or keep <= 0:
        return []
    ordered = list(u.sorted_versions(list(rels)))  # 升序
    keep_set = set(ordered[-keep:])
    dropped = [v for v in rels if v not in keep_set]
    if dropped:
        entry["releases"] = {v: rels[v] for v in ordered[::-1] if v in keep_set}
    return dropped


def plan_update(index, paths, args):
    apps = index.get("apps")
    if not isinstance(apps, dict) or not apps:
        die("索引缺 apps 对象")
    infos = []
    for p in paths:
        appname, version, platform, size, sha, _manifest = u.read_fpk(p)
        infos.append((p, appname, version, platform, size, sha))
    names = {i[1] for i in infos}
    if len(names) != 1:
        die("多个 FPK 的 appname 不一致：%s" % ", ".join(sorted(names)))
    versions = {i[2] for i in infos}
    if len(versions) != 1:
        die("多个 FPK 的 version 不一致：%s" % ", ".join(sorted(versions)))
    appname, version = names.pop(), versions.pop()
    key = find_key(apps, appname, args.app)
    entry = apps[key]
    rels = entry.get("releases")
    if not isinstance(rels, dict):
        die("apps.%s.releases 不是对象" % key)

    top = list(u.sorted_versions(list(rels)))[-1] if rels else ""
    if top and version not in rels and u.compare_version(version, top) < 0:
        if not args.allow_downgrade:
            die("拒绝降级：%s 已有更高版本 %s，本次要写 %s（确需补录请加 --allow-downgrade）"
                  % (key, top, version))

    old = rels.get(version) if isinstance(rels.get(version), dict) else {}
    old_pkgs = old.get("packages") or {}
    base = args.base_url.rstrip("/") if args.base_url else url_base(entry, args.repo)
    if not base:
        die("无法推断 %s 的下载基址：既有直链里没有 /releases/download/，请给 --repo 或 --base-url" % key)
    if not args.tag.strip():
        die("跟版必须给 --tag（真实 release tag；FnDepot 的 tag 命名不规律，不能猜）")
    tag = args.tag.strip()
    if any(c.isspace() for c in tag):
        die("--tag 里不能有空白：%r" % tag)

    keys = existing_arch_keys(entry)
    has_size_mb = any("size_mb" in (p or {})
                      for node in rels.values()
                      for p in (((node or {}).get("packages") or {}).values()))
    pkgs = {}
    for path, _an, _ver, platform, size, sha in infos:
        arch = pick_arch(platform, path, len(infos), keys)
        if arch in pkgs:
            die("同一架构出现多个包：%s" % arch)
        prev = old_pkgs.get(arch) or {}
        if prev.get("sha256") and prev["sha256"] != sha and not args.allow_republish:
            die("拒绝同版本换包：%s %s/%s 的 sha256 由 %s 变为 %s（确需覆盖请加 --allow-republish）"
                  % (key, version, arch, prev["sha256"][:16], sha[:16]))
        url = "%s/releases/download/%s/%s" % (base, tag, os.path.basename(path))
        pkg = {"download_url": url, "sha256": sha, "size": size}
        if has_size_mb:
            pkg["size_mb"] = round(size / 1048576, 2)
        pkgs[arch] = pkg
    order = [k for k in keys if k in pkgs] + sorted(k for k in pkgs if k not in keys)
    pkgs = {k: pkgs[k] for k in order}

    changelog = args.changelog
    if changelog is None:
        changelog = old.get("changelog") or ""
    node = {k: v for k, v in old.items() if k not in ("updated_at", "changelog", "packages")}
    node["updated_at"] = args.now
    node["changelog"] = changelog
    node["packages"] = pkgs

    rest = [v for v in u.sorted_versions(list(rels))[::-1] if v != version]
    entry["releases"] = {version: node}
    for v in rest:
        entry["releases"][v] = rels[v]

    merged_platform = []
    if not getattr(args, "no_platform_sync", False):
        merged_platform = merge_platforms(entry, pkgs)

    changes = ["%s %s：%s" % (key, version, ", ".join("%s=%d B" % (a, pkgs[a]["size"]) for a in pkgs))]
    if merged_platform:
        changes.append("%s platform 增补：%s" % (key, ", ".join(merged_platform)))
    dropped = prune_entry(entry, args.keep)
    if dropped:
        changes.append("%s 裁剪旧版本：%s" % (key, ", ".join(dropped)))
    return changes




def sanity_check(index, keep):
    errs = []
    if str(index.get("schema_version")) != "2":
        errs.append('schema_version 不是 "2"（实际 %r）' % index.get("schema_version"))
    apps = index.get("apps")
    if not isinstance(apps, dict) or not apps:
        errs.append("apps 为空或不是对象")
        return errs
    for name, entry in apps.items():
        if not isinstance(entry, dict):
            errs.append("apps.%s 不是对象" % name)
            continue
        platforms = entry_platforms(entry)
        if not platforms:
            errs.append("apps.%s.platform 缺失或为空" % name)
        rels = entry.get("releases")
        if not isinstance(rels, dict) or not rels:
            errs.append("apps.%s.releases 为空或不是对象" % name)
            continue
        if keep > 0 and len(rels) > keep:
            errs.append("apps.%s 有 %d 个版本，超过 --keep %d" % (name, len(rels), keep))
        if platforms and "all" not in platforms:
            for ver, node in rels.items():
                for arch in ((node or {}).get("packages") or {}):
                    if arch not in platforms:
                        errs.append("apps.%s.platform=%r 未包含 packages.%s（%s）"
                                    % (name, platforms, arch, ver))
        for ver, node in rels.items():
            pk = (node or {}).get("packages")
            if not isinstance(pk, dict) or not pk:
                errs.append("apps.%s.releases.%s.packages 为空" % (name, ver))
                continue
            for arch, p in pk.items():
                if arch not in ALLOWED_ARCH:
                    errs.append("apps.%s.%s 架构键非法: %r" % (name, ver, arch))
                url = str((p or {}).get("download_url") or "")
                if not (url.startswith("http://") or url.startswith("https://")):
                    errs.append("apps.%s.%s/%s download_url 非 http(s): %r" % (name, ver, arch, url))
                sha = str((p or {}).get("sha256") or "")
                if len(sha) != 64 or any(c not in "0123456789abcdef" for c in sha):
                    errs.append("apps.%s.%s/%s sha256 非 64 位小写十六进制" % (name, ver, arch))
                size = (p or {}).get("size")
                if not isinstance(size, int) or isinstance(size, bool) or size <= 0:
                    errs.append("apps.%s.%s/%s size 非法: %r" % (name, ver, arch, size))
    return errs


def main(argv=None):
    ap = argparse.ArgumentParser(description="FnDepot fnpack.json（V2）自动跟版")
    ap.add_argument("--index", "--file", dest="index", default="fnpack.json", help="索引文件（默认 fnpack.json）")
    ap.add_argument("--tag", default="", help="真实 release tag（跟版必填），如 v0.8.9 / dog-dev-v1.3.12")
    ap.add_argument("--fpk", nargs="+", action="extend", default=[], metavar="FPK",
                    help="一个或多个 FPK（x86+arm 一次给；支持通配）")
    ap.add_argument("--app", default="", help="应用键覆盖（默认取 FPK manifest 的 appname）")
    ap.add_argument("--repo", default="", help="owner/repo（仅在既有直链无法推断基址时用）")
    ap.add_argument("--base-url", default="", help="直接给仓库基址（…/releases/download 之前）")
    ap.add_argument("--keep", type=int, default=None, help="每个应用保留的版本数（跟版默认 2；--check 默认只查结构）")
    ap.add_argument("--changelog", default=None, help="本版更新日志文本")
    ap.add_argument("--changelog-file", default="", help="从文件读更新日志（release 正文）")
    ap.add_argument("--prune-only", action="store_true", help="只按 --keep 裁剪，不写新版本")
    ap.add_argument("--dry-run", action="store_true", help="只打印改动，不写文件")
    ap.add_argument("--check", action="store_true", help="只做结构校验（不发版）")
    ap.add_argument("--allow-downgrade", action="store_true", help="允许写入比现有最高版本更旧的版本")
    ap.add_argument("--allow-republish", action="store_true", help="允许同版本换包")
    ap.add_argument("--no-platform-sync", action="store_true", help="不把新架构写进应用级 platform（默认会增补）")
    ap.add_argument("--now", default="", help="覆盖 updated_at（测试用）")
    a = ap.parse_args(argv)

    if a.keep is None:
        a.keep = 0 if a.check else 2
    if a.keep < 0:
        die("--keep 不能为负数（0 = 全量保留）")
    if a.changelog_file:
        try:
            with open(a.changelog_file, encoding="utf-8") as f:
                a.changelog = f.read().strip()[:4000]
        except OSError as e:
            die("读 --changelog-file 失败：%s" % e)
    a.now = a.now or __import__("time").strftime("%Y-%m-%dT%H:%M:%S+08:00")

    index, raw = load(a.index)
    if a.check:
        errs = sanity_check(index, a.keep)
        for e in errs:
            sys.stderr.write("FAIL %s\n" % e)
        if errs:
            return 1
        print("OK %s：%d 个应用，%d 个版本节点" % (
            a.index, len(index["apps"]),
            sum(len(v.get("releases") or {}) for v in index["apps"].values())))
        return 0

    changes = []
    if a.prune_only:
        for name, entry in index["apps"].items():
            dropped = prune_entry(entry, a.keep)
            if dropped:
                changes.append("%s 裁剪：%s" % (name, ", ".join(dropped)))
    else:
        if not a.fpk:
            die("缺少 --fpk（可给多个路径 / 通配）；只裁剪请用 --prune-only")
        changes = plan_update(index, a.fpk, a)

    errs = sanity_check(index, 0)
    if errs:
        for e in errs:
            sys.stderr.write("FAIL %s\n" % e)
        die("新索引自检未通过，拒绝写入")

    text = json.dumps(index, ensure_ascii=False, indent=u.detect_indent(raw))
    if raw.endswith("\n"):
        text += "\n"
    if text == raw:
        print("无变化")
        return 0
    for c in changes:
        print("* " + c)
    if a.dry_run:
        print("（--dry-run：未写入）")
        return 0
    u.write_atomic(a.index, text)
    print("已写入 %s（%d 字节）" % (a.index, len(text.encode())))
    return 0


if __name__ == "__main__":
    sys.exit(main())
