#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""sync_upstream_defaults.py — Moo Mate 默认清单的上游跟版与探针。

背景（0.2.72）：mate-default-index.json 是 mate 首次安装的远程默认列表，
此前只能靠人工发现上游发版再手工改。本脚本把「查上游 → 比对 → 改清单」
自动化，并提供一个只读探针给 cron 定时跑。

用法：
  python3 tools/sync_upstream_defaults.py --check            # 只读；有漂移退出码 1
  python3 tools/sync_upstream_defaults.py --write --emit-gz  # 人工执行，落盘 + 重打内嵌 gz
  python3 tools/sync_upstream_defaults.py --probe            # 探针：每日一轮（ETag 条件请求）→ 写 HEARTBEAT
  python3 tools/sync_upstream_defaults.py --status           # 只读：探针/清单/配额状态
  python3 tools/sync_upstream_defaults.py --verify           # 清单自检 8 条断言

探针的调度与配额策略（2026-11-18 改，取代此前「每 6 小时一轮」）：
  * 日期配额：同一自然日只跑一轮，且只在 03:00 之后（cron 仍可每 15 分钟触发，
    到点后第一轮跑完当天就不再联网，其余轮次只做本地判定即退出）。
  * 每个仓库只取 /releases/latest（一次请求，不再翻 releases 列表）并带 ETag：
    304 = 上游未变，零配额消耗、零解析。
  * 剩余配额 < 200 直接放弃本轮（不推进日期，1 小时后重试），不把配额吃干。
  * 单实例 flock、state 原子写（tmp + rename）、日志 256KB 轮转、连续失败 3 次写 HEARTBEAT。

边界（刻意不做的事）：
  * 不做 --push 一条龙：默认清单是新装首屏，必须人工看一眼再推。
  * 不按 release tag 写版本号：istoreos 的 tag 是 20260317，包内 manifest 才是 1.0.0。
  * 不改 desc / display_name / author 等元数据：那是人工维护的口径。

凭据：需要 GitHub token（本机 github.com 直连不通，只能走 api.github.com；匿名配额
60/h 被其他任务吃光，实测 remaining=0）。来源顺序：$GH_TOKEN → $MOO_GH_TOKEN_FILE →
~/.moo-gh-token（0600，家目录，不在任何 git 仓库内）。token 只进请求头，不打印、
不写日志、不写 state、不复制。
"""
import argparse
import gzip
import io
import json
import os
import re
import time
import shutil
import sys
import tarfile
import urllib.error
import urllib.request

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WS = os.path.dirname(REPO_ROOT)
DEFAULT_INDEX = os.path.join(REPO_ROOT, "mate-default-index.json")
EMBED_GZ = os.path.join(REPO_ROOT, "cmd", "moo-mate", "default_index.json.gz")
STATE_PATH = os.path.join(REPO_ROOT, "_prep_mate_index", "upstream_watch.state.json")
WATCH_LOG = os.path.join(REPO_ROOT, "_prep_mate_index", "upstream_watch.log")
HEARTBEAT = os.path.join(WS, "HEARTBEAT.md")
LOCK_PATH = os.path.join(REPO_ROOT, "_prep_mate_index", "upstream_watch.lock")
PROBE_AFTER_MINUTES = 3 * 60   # 每天 03:00 之后才跑（日期配额，避免零点刚过就抢配额）
RATE_FLOOR = 200               # 剩余配额低于此值本轮直接放弃（留给本机其他任务）
MAX_CONSEC_FAIL = 3            # 连续失败达到该次数写一次 HEARTBEAT
RETRY_BACKOFF = 3600           # 配额不足/中止后 1 小时内不再重试
LOG_MAX_BYTES = 256 * 1024     # 探针日志轮转阈值

# 白名单：只认这 5 个上游仓库（与 mate-default-index.json 的 source 一一对应）。
# 新增条目必须同时改这里和清单，避免脚本去追不认识的仓库。
UPSTREAM = {
    "com.dustinky.qwenpaw": "naspk-com/QwenPaw",
    "dpanel": "donknap/dpanel",
    "fan-control": "AriesOxO/fnos-fan-control",
    "istoreos": "wukongdaily/istoreos-docker-builder",
    "motrix": "RockerHX/motrix-fnos",
}
UA = "moo-mate-upstream-sync/1.0"
# 需要「以包内 manifest 为准」的应用：tag 是日期或与包内版本脱钩的仓库。
# istoreos 的 tag 是 20260317，包内 manifest 才是 1.0.0（历史坑，不能按 tag 猜）。
NEEDS_MANIFEST = {"istoreos"}
SEMVER_RE = re.compile(r"^v?\d+(\.\d+){1,3}([-+][0-9A-Za-z.\-]+)?$")
MANIFEST_MAX_BYTES = 12 * 1024 * 1024
FORBID = {
    "内网 192.168": r"192\.168\.",
    "内网 10.0.0": r"10\.0\.0\.",
    "本机应用路径": "/vol1/" + "@app",
    "GitHub PAT": r"ghp_[A-Za-z0-9]{20,}",
}


def get_token():
    """GitHub token：环境变量 → $MOO_GH_TOKEN_FILE → ~/.moo-gh-token（0600）。

    为什么需要：本机 github.com 直连不通（releases.atom 实测超时），只能走
    api.github.com；匿名配额 60/h 会被本机其他任务吃光（实测 remaining=0），
    认证配额 5000/h 才稳定。5 个仓库 / 轮、每天一轮 = 最多 6 次/天。

    密钥边界：token 只进请求头。脚本不打印、不写日志、不写 state、不复制到任何
    其他文件；~/.moo-gh-token 位于用户家目录，不在任何 git 仓库内。
    """
    tok = (os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN") or "").strip()
    if tok:
        return tok
    path = (os.environ.get("MOO_GH_TOKEN_FILE") or "").strip() or os.path.expanduser("~/.moo-gh-token")
    try:
        with open(path, encoding="utf-8") as f:
            return f.read().strip()
    except OSError:
        return ""


def gh_json(url, token, timeout=40):
    req = urllib.request.Request(url, headers={
        "Accept": "application/vnd.github+json", "User-Agent": UA})
    if token:
        req.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.load(r)


class QuotaLow(RuntimeError):
    """配额不足：放弃本轮，不推进「今日已完成」标记。"""


def _remaining(headers):
    try:
        return int(headers.get("X-RateLimit-Remaining"))
    except (TypeError, ValueError):
        return None


def gh_latest(repo, token, etag="", timeout=40):
    """取 /repos/{repo}/releases/latest，带 If-None-Match 条件请求。

    返回 (status, release_or_None, etag, remaining)：
      * 200 → release dict（assset digest 等都在里面）
      * 304 → None（上游与上次逐字节一致，零配额消耗、零解析）
    404（仓库没有正式 release）回退列表接口取第一条正式版，不带 ETag。
    """
    url = "https://api.github.com/repos/%s/releases/latest" % repo
    req = urllib.request.Request(url, headers={
        "Accept": "application/vnd.github+json", "User-Agent": UA})
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if etag:
        req.add_header("If-None-Match", etag)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return 200, json.load(r), (r.headers.get("ETag") or etag), _remaining(r.headers)
    except urllib.error.HTTPError as e:
        rem = _remaining(e.headers)
        if e.code == 304:
            return 304, None, (e.headers.get("ETag") or etag), rem
        if e.code == 403 and rem == 0:
            raise QuotaLow("GitHub 配额耗尽（remaining=0）")
        if e.code == 404:
            rels = gh_json("https://api.github.com/repos/%s/releases?per_page=5" % repo, token, timeout) or []
            rels = [x for x in rels if not x.get("draft") and not x.get("prerelease")]
            if not rels:
                raise RuntimeError("上游没有正式 release")
            return 200, rels[0], "", rem
        raise


def gh_stream(url, token, timeout=120):
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    if token:
        req.add_header("Authorization", "Bearer " + token)
    return urllib.request.urlopen(req, timeout=timeout)


class _Limited:
    """给 tarfile 的只读包装：超过 limit 字节即抛错，避免为取 manifest 拖下整包。

    QwenPaw 这类应用的 FPK 可达数百 MB；manifest 通常在归档前部，读到即停。
    真读不到就回退 tag，不让整批卡在下载上。
    """

    def __init__(self, f, limit):
        self.f, self.limit, self.n = f, limit, 0

    def read(self, n=-1):
        if n is None or n < 0:
            n = 65536
        b = self.f.read(min(n, max(0, self.limit - self.n)))
        self.n += len(b)
        if self.n >= self.limit:
            raise IOError("manifest 超出读取上限 %d 字节" % self.limit)
        return b

    def readinto(self, buf):
        b = self.read(len(buf))
        buf[:len(b)] = b
        return len(b)

    def close(self):
        self.f.close()


def resolve_version(key, tag, asset, token, cur_sha="", cur_ver=""):
    """版本号：默认用 tag；tag 非语义版本或应用在白名单里时以包内 manifest 为准。

    cur_sha/cur_ver：清单里已有的 sha 与版本。若本次资产 digest 与之一致，
    说明包字节完全相同 ⇒ 包内 manifest 也相同，直接复用版本号，不必下载整包。
    """
    t = normalize_ver(tag)
    if cur_ver and cur_sha and asset_sha(asset).lower() == str(cur_sha).lower():
        return normalize_ver(cur_ver)
    need = key in NEEDS_MANIFEST or not SEMVER_RE.match(str(tag or ""))
    if not need:
        return t
    mv = manifest_version(asset.get("browser_download_url") or asset.get("url"), token)
    if mv:
        return mv
    # 绝不回退 tag：istoreos 的 tag 是 20260317，写进去就成了假版本号。
    # 读不到就返回空，让调用方跳过该应用（下次探针再试）。
    print("  ! %s: 包内 manifest 读取失败，本轮跳过（不回退 tag %s）" % (key, t))
    return ""


def normalize_ver(v):
    v = str(v or "").strip()
    if v[:1] in ("v", "V"):
        v = v[1:]
    return v


def manifest_version(url, token):
    """流式读 FPK 里的 manifest 取版本号；读不到返回空串。

    istoreos 这类仓库的 release tag 是日期（20260317），只有包内 manifest 才是真版本，
    所以版本一律以包为准，绝不按 tag 猜。
    """
    for attempt in range(2):  # GitHub 偶发 Remote end closed，重试一次
        try:
            return _read_manifest(url, token)
        except Exception as e:
            if attempt == 0:
                continue
            print("  ! manifest 读取失败 %s: %s" % (url.split("/")[-1], str(e)[:120]))
    return ""


def _read_manifest(url, token):
    with gh_stream(url, token) as raw:
        with tarfile.open(fileobj=_Limited(raw, MANIFEST_MAX_BYTES), mode="r|gz") as tf:
            for m in tf:
                if not m.isfile() or os.path.basename(m.name) != "manifest":
                    continue
                f = tf.extractfile(m)
                if f is None:
                    return ""
                txt = f.read().decode("utf-8", "replace")
                try:  # 主流 FPK：manifest 是 JSON
                    return normalize_ver(json.loads(txt).get("version"))
                except Exception:  # istoreos 这类：manifest 是 INI（key = value）
                    m2 = re.search(r"^\s*version\s*=\s*(\S+)", txt, re.M)
                    return normalize_ver(m2.group(1)) if m2 else ""
    return ""


def pick_assets(rel, platform):
    """按原条目的 platform 口径挑 FPK 资产。"""
    found = {}
    for a in rel.get("assets") or []:
        low = (a.get("name") or "").lower()
        if not low.endswith(".fpk"):
            continue
        arch = "arm" if ("arm" in low or "aarch64" in low) else "x86"
        found.setdefault(arch, a)
    if not found:
        return {}
    plats = [p for p in (platform or []) if p]
    if not plats or plats == ["all"] or plats == ["any"]:
        a = found.get("x86") or found.get("arm")
        return {"all": a} if a else {}
    out = {}
    for arch in plats:
        if arch in found:
            out[arch] = found[arch]
    return out


def asset_sha(a):
    d = str(a.get("digest") or "")
    if d.startswith("sha256:"):
        return d[7:].strip().lower()
    return ""


def clean_notes(body, limit=8):
    """把上游 release notes 洗成可直接进 mate 弹窗的几行。

    过滤：资产表 / 裸链接 / Full Changelog 段 / Markdown 表格 / HTML 标签 / 空行。
    """
    out = []
    for ln in (body or "").splitlines():
        s = ln.strip()
        if not s:
            continue
        low = s.lower()
        if "full changelog" in low or "compare view" in low:
            continue
        if re.match(r"^[*\-+]\s*https?://", s):
            continue
        if re.search(r"\.(fpk|sha256|minisig)\b", low):
            continue
        if s.startswith("|") or s.startswith("```"):
            continue
        if re.match(r"^<[a-z/]", low):
            continue
        # 纯图片/徽章行（![alt](url)）不是可读日志；去掉图片与链接后若几乎没字也丢掉
        if re.match(r"^!?\[[^\]]*\]\([^)]*\)\s*$", s):
            continue
        bare = re.sub(r"!?\[[^\]]*\]\([^)]*\)", "", s)
        bare = re.sub(r"https?://\S+", "", bare)
        if len(re.sub(r"[^0-9A-Za-z\u4e00-\u9fff]", "", bare)) < 2:
            continue
        s = re.sub(r"^#+\s*", "", s)
        s = re.sub(r"\*\*", "", s)
        s = re.sub(r"^\s*[*\-+]\s*", "- ", s)
        out.append(s[:160])
        if len(out) >= limit:
            break
    return "\n".join(out)


def to_unix(iso):
    """ISO8601（GitHub published_at）→ Unix 秒；解析失败返回 0。"""
    from datetime import datetime, timezone
    s = str(iso or "")
    try:
        dt = datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)
        return int(dt.timestamp())
    except Exception:
        return 0


def vkey(v):
    parts = re.findall(r"\d+", str(v or ""))
    return tuple(int(x) for x in parts[:4]) if parts else (0,)


def probe_repo(key, repo, cur, token, etag=""):
    """查一个上游仓库的最新正式 release，返回 (target, drift, new_etag)。

    target 为 None 表示 304：上游与上次逐字节一致，调用方按「已最新」处理。
    日常探测每个仓库只花 0~1 次请求（此前要翻 releases 列表）。
    """
    status, latest, new_etag, remaining = gh_latest(repo, token, etag)
    if remaining is not None and remaining < RATE_FLOOR:
        raise QuotaLow("GitHub 配额不足（remaining=%d）" % remaining)
    if status == 304 or latest is None:
        return None, [], new_etag
    platform = cur.get("platform") or []
    assets = pick_assets(latest, platform)
    if not assets:
        raise RuntimeError("最新 release 没有 .fpk 资产")

    main_arch = "all" if "all" in assets else ("x86" if "x86" in assets else "arm")
    packages = {}
    for arch, a in assets.items():
        packages[arch] = {
            "download_url": a.get("browser_download_url") or "",
            "sha256": asset_sha(a),
            "size": int(a.get("size") or 0),
        }
    if not packages[main_arch]["sha256"]:
        raise RuntimeError("资产缺 digest，无法核对 sha256")

    # 先算 digest 再定版本：包字节与清单现有 sha 完全一致 ⇒ manifest 必然一致，
    # 直接复用现有版本号，省掉整包下载（istoreos 这类「tag 不是版本号」的应用
    # 否则每次探测都要拉几十 MB 的 FPK）。
    ver = resolve_version(key, latest.get("tag_name"), assets[main_arch], token,
                          cur_sha=(cur.get("sha256") or ""),
                          cur_ver=cur.get("version"))
    if not ver:
        raise RuntimeError("无法确定版本号（tag 与包内 manifest 都不可用）")

    oldver = normalize_ver(cur.get("version"))
    pub_date = str(latest.get("published_at") or "")[:10]
    body_notes = clean_notes(latest.get("body")) or ("%s（%s）" % (ver, pub_date))
    # 同一版本可能以 "v1.11.2" / "1.11.2" 两种写法同时存在（历史遗留），
    # 先按归一化版本去重，避免同一版本出现两个节点。
    rel_map = {k: v for k, v in (cur.get("releases") or {}).items() if normalize_ver(k) != ver}
    rel_map[ver] = {
        "changelog": body_notes,
        "updated_at": str(latest.get("published_at") or "")[:10],
        "packages": packages,
    }
    if oldver and oldver != ver and oldver not in rel_map:
        # 旧版节点：用清单现有顶层字段兜一个（含各自 packages），保证「上一版」可见
        # releases.<ver>.updated_at 在协议里是 ISO 日期字符串（与顶层 int64 不同），
        # 旧版节点没有就留空（omitempty 不输出），绝不写数字。
        prev_node = (cur.get("releases") or {}).get(oldver) or {}
        rel_map[oldver] = {
            "updated_at": str(prev_node.get("updated_at") or ""),
            "packages": cur.get("packages") or prev_node.get("packages") or {},
        }
    keep = sorted(rel_map.keys(), key=vkey, reverse=True)[:2]
    rel_map = {k: rel_map[k] for k in keep}

    size_bytes = packages[main_arch]["size"]
    # 顶层 updated_at 在 mate 的 indexDoc 里是 int64（Unix 秒），不是字符串；
    # 写成 ISO 日期会让 mate 启动时反序列化失败（0.2.72 首跑实测踩到）。
    pub_ts = to_unix(latest.get("published_at"))
    # 上游把发布说明写成纯徽章/图片时，clean_notes 会返回空 → 保留清单里原有文案，
    # 不能把可读的更新日志冲成一句占位或一张图片。
    notes = clean_notes(latest.get("body")) or (cur.get("changelog") or "").strip() or ("%s（%s）" % (ver, pub_date))
    target = {
        "version": ver,
        "download_url": packages[main_arch]["download_url"],
        "sha256": packages[main_arch]["sha256"],
        # size 沿用清单既有口径：MB（两位小数）；size_bytes 是字节，两者必须自洽
        "size": round(size_bytes / 1048576.0, 2),
        "size_bytes": size_bytes,
        "updated_at": pub_ts,
        "releases": rel_map,
        "changelog": notes,
    }
    if "all" not in assets:
        target["packages"] = packages
    elif cur.get("packages"):
        target["packages"] = packages

    drift = []
    if oldver != ver:
        drift.append(("version", oldver, ver))
    else:
        old_sha = (cur.get("sha256") or "").lower()
        new_sha = packages[main_arch]["sha256"]
        if old_sha and old_sha != new_sha:
            drift.append(("sha256", old_sha[:12], new_sha[:12]))
    if not (cur.get("readme_url") or ""):
        target["readme_url"] = "https://raw.githubusercontent.com/%s/HEAD/README.md" % repo
    return target, drift, new_etag


def load_index(path=DEFAULT_INDEX):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def save_index(doc, path=DEFAULT_INDEX):
    """按仓库既有风格写回：一应用一行，key 顺序沿用原文件（与 gen 脚本一致）。

    刻意不用 sort_keys：否则整文件行内顺序全变，diff 无法审阅。
    """
    sep = (", ", ": ")
    p2 = "  "
    lines = ["{"]
    head = [(k, v) for k, v in doc.items() if k != "apps"]
    for i, (k, v) in enumerate(head):
        lines.append(p2 + json.dumps(k, ensure_ascii=False) + ": "
                     + json.dumps(v, ensure_ascii=False, separators=sep) + ",")
    if "apps" in doc:
        lines.append(p2 + '"apps": {')
        items = list(doc["apps"].items())
        for i, (k, v) in enumerate(items):
            comma = "," if i < len(items) - 1 else ""
            lines.append(p2 * 2 + json.dumps(k, ensure_ascii=False) + ": "
                         + json.dumps(v, ensure_ascii=False, separators=sep) + comma)
        lines.append(p2 + "}")
    lines.append("}")
    txt = "\n".join(lines) + "\n"
    json.loads(txt)  # 自检：写坏就抛
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(txt)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)  # 原子替换：中断也不会留下半份清单
    return len(txt.encode("utf-8"))


def emit_gz(doc, path=EMBED_GZ):
    """重打内嵌兜底清单（gzip -9n 等价：mtime=0，可复现）。"""
    raw = json.dumps(doc, ensure_ascii=False, sort_keys=True).encode("utf-8")
    buf = io.BytesIO()
    with gzip.GzipFile(fileobj=buf, mode="wb", compresslevel=9, mtime=0) as gz:
        gz.write(raw)
    data = buf.getvalue()
    tmp = path + ".tmp"
    with open(tmp, "wb") as f:
        f.write(data)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)
    return len(data)


def cmd_fill(doc, token, emit=False):
    """补齐元数据：版本没变也把 updated_at / packages / size_bytes / readme_url / changelog 同步。

    背景：cmd_write 只写"版本或 sha 变了"的应用，导致未漂移应用的缺字段永远补不上
    （实测 dpanel/fan-control/istoreos 缺顶层 updated_at、fan-control 缺顶层 packages）。
    本命令强制全量重取（不带 ETag），只在整体自检通过后原子写回。
    """
    apps = doc.setdefault("apps", {})
    changed, fails = [], []
    for key, repo in UPSTREAM.items():
        cur = apps.get(key) or {}
        try:
            tgt, notes, _ = probe_repo(key, repo, cur, token, "")
        except QuotaLow as e:
            print("  ! 配额不足，停止补齐：%s" % e)
            break
        except Exception as e:
            fails.append(key)
            print("  ! %s: %s" % (key, str(e)[:120]))
            continue
        if tgt is None:
            continue
        # 关键：target 只含协议字段，必须合并进原条目，
        # 直接替换会把 appname/desc/author/icon 等元数据整片抹掉（实测过）。
        merged = dict(cur)
        merged.update(tgt)
        if merged != cur:
            apps[key] = merged
            changed.append(key)
            print("  补齐 %s: %s" % (key, "; ".join(notes) if notes else "元数据同步"))
    probs = cmd_verify(doc)
    if probs:
        print("VERIFY FAIL（%d 项）→ 不写盘" % len(probs))
        for p in probs:
            print("  - " + p)
        return 1
    if changed:
        save_index(doc)
        if emit:
            emit_gz(doc)
        print("已补齐 %d 个应用并写回%s" % (len(changed), "（含内嵌 gz）" if emit else ""))
    else:
        print("无需补齐：清单字段已完整")
    return 1 if fails else 0


def cmd_check(doc, token, verbose=True):
    """只读比对；返回 (漂移列表, 失败列表)。"""
    drifts, fails = [], []
    for key, repo in UPSTREAM.items():
        cur = doc["apps"].get(key)
        if cur is None:
            fails.append((key, "清单里没有这个应用"))
            continue
        try:
            target, drift, _ = probe_repo(key, repo, cur, token)
        except QuotaLow as e:
            fails.append((key, str(e)[:160]))
            break
        except Exception as e:
            fails.append((key, str(e)[:160]))
            continue
        if target is None:  # 304：上游与上次逐字节一致
            if verbose:
                print("  %-24s %-10s    已最新（304）" % (
                    key, normalize_ver(cur.get("version")) or "-"))
            continue
        if drift:
            drifts.append((key, repo, drift, target))
        if verbose:
            old = normalize_ver(cur.get("version"))
            print("  %-24s %-10s -> %-10s %s" % (
                key, old or "-", target["version"],
                "有更新" if drift else "已最新"))
    return drifts, fails


def cmd_write(doc, token, emit):
    drifts, fails = cmd_check(doc, token)
    for key, repo, drift, target in drifts:
        app = doc["apps"][key]
        for k, v in target.items():
            app[k] = v
        print("  写入 %s -> %s" % (key, target["version"]))
    n = save_index(doc)
    print("已写 %s（%d 字节）" % (os.path.relpath(DEFAULT_INDEX, REPO_ROOT), n))
    if emit:
        gz = emit_gz(doc)
        print("已重打 %s（%d 字节）" % (os.path.relpath(EMBED_GZ, REPO_ROOT), gz))
    if fails:
        print("注意：%d 个应用探测失败" % len(fails))
        for k, e in fails:
            print("  ! %s: %s" % (k, e))
    return 0 if not fails else 1


def cmd_verify(doc):
    """8 条断言；返回问题列表（空 = 通过）。"""
    probs = []
    apps = doc.get("apps") or {}
    if set(apps) != set(UPSTREAM):
        probs.append("apps 键集合与白名单不一致: %s" % sorted(set(apps) ^ set(UPSTREAM)))
    for key in UPSTREAM:
        a = apps.get(key)
        if not a:
            continue
        ver = normalize_ver(a.get("version"))
        rels = a.get("releases") or {}
        if rels:
            top = sorted(rels, key=vkey, reverse=True)[0]
            if normalize_ver(top) != ver:
                probs.append("%s: version=%s 与 releases 首键=%s 不一致" % (key, ver, top))
            if len(rels) > 2:
                probs.append("%s: releases 超过 2 版（%d）" % (key, len(rels)))
        sha = (a.get("sha256") or "").lower()
        if sha and not re.fullmatch(r"[0-9a-f]{64}", sha):
            probs.append("%s: sha256 非 64 位 hex" % key)
        if a.get("size") and a.get("size_bytes"):
            if abs(float(a["size"]) - a["size_bytes"] / 1048576.0) > 0.02:
                probs.append("%s: size(MB)=%s 与 size_bytes=%s 不自洽" % (
                    key, a["size"], a["size_bytes"]))
        # mate 的 appEntry 用 int64 接 updated_at：写成 ISO 字符串会让整份清单解析失败
        if rels:
            if not isinstance(a.get("updated_at"), int):
                probs.append("%s: updated_at 必须是 unix 整数（mate int64 字段）" % key)
            if not isinstance(a.get("size_bytes"), int) or a.get("size_bytes") <= 0:
                probs.append("%s: size_bytes 缺失或非正整数" % key)
        for arch, p in (a.get("packages") or {}).items():
            s = (p.get("sha256") or "").lower()
            if not re.fullmatch(r"[0-9a-f]{64}", s):
                probs.append("%s/packages/%s: sha256 非 64 位 hex" % (key, arch))
            if not str(p.get("download_url") or "").startswith("https://"):
                probs.append("%s/packages/%s: download_url 非 https" % (key, arch))
        # 声明了多架构就必须每个架构都有包，否则前端会显示缺包
        for arch in (a.get("platform") or []):
            if arch in ("all", "any"):
                continue
            if a.get("packages") and arch not in (a.get("packages") or {}):
                probs.append("%s: platform 声明 %s 但 packages 缺该架构" % (key, arch))
        for pv, node in (a.get("releases") or {}).items():
            ua = node.get("updated_at")
            if not isinstance(ua, str):
                probs.append("%s/%s: releases.updated_at 必须是字符串（mate 字段类型）" % (key, pv))
            elif normalize_ver(pv) == ver and not ua.strip():
                probs.append("%s/%s: 最新版 releases.updated_at 不能为空" % (key, pv))
            if normalize_ver(pv) == ver and not (node.get("packages") or {}):
                probs.append("%s/%s: 最新版 releases 缺 packages" % (key, pv))
            for arch, p in (node.get("packages") or {}).items():
                s = (p.get("sha256") or "").lower()
                if s and not re.fullmatch(r"[0-9a-f]{64}", s):
                    probs.append("%s/%s/%s: sha256 非 64 位 hex" % (key, pv, arch))
        ru = a.get("readme_url") or ""
        if ru and not ru.startswith("https://"):
            probs.append("%s: readme_url 非 https" % key)
        cl = a.get("changelog") or ""
        if not cl:
            # 只有跟过版（有 releases）的应用才要求 changelog；未跟版的保持原样
            if rels:
                probs.append("%s: 有 releases 但 changelog 为空" % key)
        elif re.search(r"\.(fpk|sha256|minisig)\b", cl.lower()) or re.search(r"^\s*[*\-+]\s*https?://", cl, re.M):
            probs.append("%s: changelog 含资产表/裸链接" % key)
    blob = json.dumps(doc, ensure_ascii=False)
    for name, pat in FORBID.items():
        if re.search(pat, blob):
            probs.append("脱敏命中 %s" % name)
    return probs


def load_state():
    try:
        with open(STATE_PATH, encoding="utf-8") as f:
            st = json.load(f)
        return st if isinstance(st, dict) else {}
    except Exception:
        return {}


def save_state(state):
    """原子写：tmp + rename，避免 cron 与手工并发读到半个 JSON。"""
    os.makedirs(os.path.dirname(STATE_PATH), exist_ok=True)
    tmp = STATE_PATH + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(state, f, ensure_ascii=False, indent=2, sort_keys=True)
        f.write("\n")
    os.replace(tmp, STATE_PATH)


def append_watch_log(line):
    """探针日志超 256KB 轮转到 .1，避免再出现无轮转的大日志。"""
    try:
        if os.path.getsize(WATCH_LOG) > LOG_MAX_BYTES:
            os.replace(WATCH_LOG, WATCH_LOG + ".1")
    except OSError:
        pass
    os.makedirs(os.path.dirname(WATCH_LOG), exist_ok=True)
    with open(WATCH_LOG, "a", encoding="utf-8") as f:
        f.write(line + "\n")


def probe_due(state, now=None):
    """日期配额：同一自然日只跑一轮，且只在 03:00 之后。"""
    from datetime import datetime
    now = now or datetime.now()
    if float(state.get("retry_after_ts") or 0) > time.time():
        return False
    if (state.get("last_ok_date") or "") == now.strftime("%Y-%m-%d"):
        return False
    return (now.hour * 60 + now.minute) >= PROBE_AFTER_MINUTES


def _acquire_lock():
    try:
        import fcntl
    except ImportError:
        return True
    os.makedirs(os.path.dirname(LOCK_PATH), exist_ok=True)
    fh = open(LOCK_PATH, "w")
    try:
        fcntl.flock(fh.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        fh.close()
        return None
    return fh


def _release_lock(fh):
    if fh is None or fh is True:
        return
    try:
        import fcntl
        fcntl.flock(fh.fileno(), fcntl.LOCK_UN)
    except OSError:
        pass
    fh.close()


def cmd_status(doc):
    """只读状态：跑没跑、配额、清单值/上游缓存值/连续失败数。"""
    state = load_state()
    cache = state.get("cache") or {}
    fails = state.get("fails") or {}
    etags = state.get("etags") or {}
    print("mate 默认清单探针状态")
    print("  上次运行   %s" % (state.get("updated_at") or "-"))
    print("  上次成功   %s（%s）" % (
        state.get("last_ok_date") or "-",
        "今日待跑" if probe_due(state) else "今日已完成"))
    rem = state.get("quota_remaining")
    print("  配额剩余   %s" % ("-" if rem is None else rem))
    seen = int(state.get("last_check_ts") or 0)
    print("  最近检查   %s" % (time.strftime("%Y-%m-%d %H:%M", time.localtime(seen)) if seen else "-"))
    print("  %-26s %-9s %-9s %-5s %s" % ("应用", "清单", "上游", "ETag", "连续失败"))
    for key in UPSTREAM:
        cur = (doc.get("apps") or {}).get(key) or {}
        c = cache.get(key) or {}
        print("  %-26s %-9s %-9s %-5s %d" % (
            key, normalize_ver(cur.get("version")) or "-", c.get("ver") or "-",
            "有" if (etags.get(key) or {}).get("etag") else "无",
            int(fails.get(key) or 0)))
    print("  日志       %s" % os.path.relpath(WATCH_LOG, REPO_ROOT))
    print("提示：确认漂移后 --write --emit-gz；远端只需更新 mate-default-index.json "
          "单文件（新装用户即生效），不必重打 mate 包。")
    return 0


def cmd_probe(doc, token, quiet=False, force=False):
    """探针：每天 03:00 后一轮（日期配额），ETag 条件请求，新漂移才写 HEARTBEAT。

    为什么必须带 token：本机 github.com 直连不通（releases.atom 实测超时），
    唯一可用通道是 api.github.com；匿名配额 60 次/小时会被本机其他任务吃光
    （实测 remaining=0），只有认证配额 5000/h 才稳定。每轮最多 6 次请求
    （5 个 latest + 偶发列表回退），304 时连配额都不消耗。

    密钥边界（硬约束）：token 仅来自环境变量或 ~/.moo-gh-token（0600，不在任何
    git 仓库内），脚本不打印、不写日志、不落盘、不产生任何副本。
    """
    state = load_state()
    if not force and not probe_due(state):
        if not quiet:
            print("探针：今日已完成（上次成功 %s，配额剩余 %s），跳过" % (
                state.get("last_ok_date") or "-", state.get("quota_remaining")))
        # 每 30 分钟落一次「cron 还活着」的时间戳，供 --status 判活；
        # 比每 15 分钟都写盘省 IO，也不会把 state 刷成日志。
        now_ts = int(time.time())
        if now_ts - int(state.get("last_check_ts") or 0) > 1800:
            state["last_check_ts"] = now_ts
            save_state(state)
        return 0
    lock = _acquire_lock()
    if lock is None:
        if not quiet:
            print("探针：另一个实例正在执行，跳过")
        return 0
    try:
        return _probe_once(doc, token, quiet, state)
    finally:
        _release_lock(lock)


def _probe_once(doc, token, quiet, state):
    from datetime import datetime
    cache = state.get("cache") or {}
    reported = state.get("reported") or {}
    etags = state.get("etags") or {}
    fails = state.get("fails") or {}
    fresh, failed = [], []
    ok = 0
    quota = None
    aborted = ""
    for key, repo in UPSTREAM.items():
        cur = doc["apps"].get(key) or {}
        try:
            status, latest, etag, quota = gh_latest(
                repo, token, (etags.get(key) or {}).get("etag", ""))
            if quota is not None and quota < RATE_FLOOR:
                raise QuotaLow("GitHub 配额不足（remaining=%d）" % quota)
            if status == 304 or latest is None:
                ok += 1
                fails[key] = 0
                continue
            if etag:
                etags[key] = {"etag": etag, "ts": int(time.time())}
            assets = pick_assets(latest, cur.get("platform") or [])
            if not assets:
                raise RuntimeError("最新 release 没有 .fpk 资产")
            main_arch = "all" if "all" in assets else ("x86" if "x86" in assets else "arm")
            digest = asset_sha(assets[main_arch])
            tag = str(latest.get("tag_name") or "")
            c = cache.get(key) or {}
            if c.get("tag") == tag and c.get("digest") == digest and c.get("ver"):
                ver = c["ver"]
            else:
                ver = resolve_version(key, tag, assets[main_arch], token)
                if not ver:
                    raise RuntimeError("无法确定版本号（tag 与包内 manifest 都不可用）")
                cache[key] = {"tag": tag, "digest": digest, "ver": ver}
            ok += 1
            fails[key] = 0
            oldver = normalize_ver(cur.get("version"))
            if ver == oldver and digest == (cur.get("sha256") or "").lower():
                reported.pop(key, None)
                continue
            stamp = "%s|%s" % (ver, digest[:12])
            if reported.get(key) == stamp:
                continue
            reported[key] = stamp
            fresh.append((key, oldver or "-", ver, str(latest.get("published_at") or "")[:10]))
        except QuotaLow as e:
            aborted = str(e)
            failed.append((key, aborted))
            break
        except Exception as e:
            fails[key] = int(fails.get(key) or 0) + 1
            failed.append((key, str(e)[:120]))
            if not quiet:
                print("  ! %s 探测失败（连续 %d 次）: %s" % (
                    key, fails[key], str(e)[:120]))

    now = datetime.now()
    ts = now.strftime("%Y-%m-%d %H:%M")
    ok_round = bool(ok) and not aborted
    state.update({
        "schema": 3,
        "updated_at": ts,
        "cache": cache,
        "reported": reported,
        "etags": etags,
        "fails": fails,
        "last_run_ts": int(time.time()),
        "last_run_date": now.strftime("%Y-%m-%d"),
        "last_ok_date": now.strftime("%Y-%m-%d") if ok_round else str(state.get("last_ok_date") or ""),
        "quota_remaining": quota,
    })
    if aborted:
        state["retry_after_ts"] = int(time.time()) + RETRY_BACKOFF
    else:
        state.pop("retry_after_ts", None)
    save_state(state)

    append_watch_log("%s probe: 成功 %d / 失败 %d / 新漂移 %d%s" % (
        ts, ok, len(failed), len(fresh),
        ("（%s）" % "; ".join("%s %s->%s" % (k, o, n) for k, o, n, _ in fresh)) if fresh else ""))
    if aborted and not quiet:
        print("探针中止：%s（不推进今日标记，1 小时后重试）" % aborted)

    if fresh:
        names = "、".join("%s %s → %s" % (k, o, n) for k, o, n, _ in fresh)
        line = ("- [ ] mate 默认清单上游漂移（探针 %s）：%s。修法：cd moo-325 && "
                "python3 tools/sync_upstream_defaults.py --write --emit-gz\n") % (ts, names)
        prev = ""
        if os.path.exists(HEARTBEAT):
            with open(HEARTBEAT, encoding="utf-8") as f:
                prev = f.read()
        if names not in prev:
            with open(HEARTBEAT, "a", encoding="utf-8") as f:
                f.write(line)
        print("探针发现 %d 个新漂移：%s" % (len(fresh), names))
    elif not quiet:
        print("探针：无新漂移（成功 %d / 失败 %d）" % (ok, len(failed)))

    # 连续失败 3 次只在「刚跨过阈值」那一轮报一次，避免天天刷屏
    for key, n in sorted(fails.items()):
        if int(n or 0) == MAX_CONSEC_FAIL:
            line = ("- [ ] mate 上游探针连续 %d 次失败：%s。查日志 %s\n" % (
                n, key, os.path.relpath(WATCH_LOG, REPO_ROOT)))
            with open(HEARTBEAT, "a", encoding="utf-8") as f:
                f.write(line)
    return 0

def main():
    ap = argparse.ArgumentParser(description="mate 默认清单上游跟版 / 探针")
    ap.add_argument("--check", action="store_true", help="只读比对（默认动作），有漂移退出码 1")
    ap.add_argument("--write", action="store_true", help="把漂移写回 mate-default-index.json")
    ap.add_argument("--fill", action="store_true", help="强制全量重取并补齐元数据（版本不变也写）")
    ap.add_argument("--emit-gz", action="store_true", help="同时重打内嵌 default_index.json.gz")
    ap.add_argument("--probe", action="store_true", help="探针模式：每日一轮（ETag）后写 HEARTBEAT")
    ap.add_argument("--status", action="store_true", help="只读状态：探针/配额/清单与上游对比")
    ap.add_argument("--verify", action="store_true", help="清单自检 8 条断言")
    ap.add_argument("--force", action="store_true", help="探针忽略日期配额与退避，立即探测")
    ap.add_argument("--quiet", action="store_true", help="只输出结论")
    args = ap.parse_args()

    doc = load_index()
    token = get_token()  # 认证配额 5000/h；缺失则退匿名（60/h，常被占满 → 本轮多半失败）
    if not token and not args.quiet:
        print("  ! 未取到 token（$GH_TOKEN / $MOO_GH_TOKEN_FILE / ~/.moo-gh-token 均不可用），将退匿名")

    if args.status:
        return cmd_status(doc)
    if args.verify:
        probs = cmd_verify(doc)
        if probs:
            print("VERIFY FAIL（%d 项）" % len(probs))
            for p in probs:
                print("  - " + p)
            return 1
        print("VERIFY PASS（%d 个应用 / %d 条断言全过）" % (len(doc.get("apps") or {}), 8))
        return 0
    if args.fill:
        return cmd_fill(doc, token, args.emit_gz)
    if args.write:
        return cmd_write(doc, token, args.emit_gz)
    if args.probe:
        return cmd_probe(doc, token, args.quiet, args.force)

    drifts, fails = cmd_check(doc, token, verbose=not args.quiet)
    for key, repo, drift, target in drifts:
        print("  漂移 %s: %s" % (key, ", ".join("%s %s->%s" % d for d in drift)))
    if fails:
        print("探测失败 %d 个" % len(fails))
    print("结果：%d 个待跟版 / %d 个失败" % (len(drifts), len(fails)))
    return 1 if (drifts or fails) else 0


if __name__ == "__main__":
    sys.exit(main())
