#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""discover_sources.py - 自建「FnDepot/Moo 应用源」自动发现（默认 dry-run，只增不删）.

来源：2026-11-25 对 EWEDLCM/FnDepot(上游) 与 710850609/fndepot(下游镜像) 两份自动化
脚本的复核结论（_prep_rl_wbj/AUTOSOURCING_CONCLUSION.md）。它们有四处硬缺陷：
  1) 无零幅度守卫 —— 限流时照样覆写，直接发布空清单
  2) 白名单压过黑名单 —— 同一仓同时出现在两边时仍被收录
  3) 硬编码 main 分支 —— 默认分支是 master 的源被静默漏掉
  4) 只验 schema 不验包 —— 名单里混着 download_url 为空的源

本脚本逐条对冲（编号对应上面的缺陷）：
  G0 零幅度守卫：校验通过为 0 或拟议总数 < 基线 90% 时拒绝写入，退出码 3（不产出半成品）
  G1 黑名单优先：仓库级排除名单先过滤，且任何白名单/候选都不能把它加回来
  G2 分支探测：一律用 search API 返回的 default_branch（失败才回退 main/master）
  G3 包可达：解析清单里的 download_url，缺空的计入 insecure_apps；--probe 时 HEAD 探测死链
  G4 只增不删：写出只在基线尾部追加新行，绝不删除/重排既有行；--write 才落盘
  G5 血缘/重合度查重：fork 血缘 + 应用名重合率，保留 canonical（应用多→星多）

用法：
  python3 tools/discover_sources.py                      # dry-run，出报告不写文件
  python3 tools/discover_sources.py --write              # 追加写回 --baseline
  python3 tools/discover_sources.py --selftest           # 离线自检（守卫/优先级/查重/只增）
  退出码：0 正常 / 3 守卫拒绝 / 4 网络或配额异常

约定：token 只从 $GH_TOKEN 或 --token-file 读取，绝不打印、不落盘、不进报告。
"""
import argparse
import json
from concurrent.futures import ThreadPoolExecutor
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

API = "https://api.github.com"
RAW = "https://raw.githubusercontent.com"
UA = "moo-discover/1.0"
MANIFESTS = ("fnpack.json", "moo.json", "apps.json", "fndepot.json", "repo_list.txt")
DIRS = ("apps", "packages")
API_FALLBACK = False
REPO_RE = re.compile(r"^https?://github\.com/([^/\s]+)/([^/\s#?]+?)(?:\.git)?/?$", re.I)


class Guard(Exception):
    """守卫拒绝：宁可不出名单，也不出错名单。"""


class Gh(object):
    def __init__(self, token):
        self.token = token
        self.remaining = None

    def get(self, url, raw=False, timeout=30):
        headers = {"User-Agent": UA}
        if not raw:
            headers["Accept"] = "application/vnd.github+json"
            if self.token:
                headers["Authorization"] = "Bearer " + self.token
        req = urllib.request.Request(url, headers=headers)
        last = None
        for attempt in range(3):
            try:
                with urllib.request.urlopen(req, timeout=timeout) as f:
                    body = f.read()
                    if not raw:
                        rem = f.headers.get("X-RateLimit-Remaining")
                        if rem is not None:
                            self.remaining = int(rem)
                    return body if raw else json.loads(body.decode("utf-8", "replace"))
            except urllib.error.HTTPError as e:
                rem = e.headers.get("X-RateLimit-Remaining") if e.headers else None
                if rem is not None:
                    self.remaining = int(rem)
                if e.code == 404:
                    return None
                last = e
                if e.code in (403, 429) and attempt < 2:
                    if self.remaining is not None and self.remaining <= 1:
                        raise Guard("GitHub 配额耗尽（remaining=%s），拒绝产出不完整名单" % self.remaining)
                    time.sleep(5 * (attempt + 1))
                    continue
                raise
            except urllib.error.URLError as e:
                last = e
                if attempt < 2:
                    time.sleep(3)
                    continue
                raise
        raise last


def norm(s):
    return (s or "").strip().lower()


def repo_of(url):
    m = REPO_RE.match((url or "").strip())
    if not m:
        return ""
    return (m.group(1) + "/" + m.group(2).replace(".git", "")).lower()


DEFAULT_QUERIES = (
    "fndepot in:name",
    "fndepot in:description",
    "fndepot in:name fork:true",
    '"fnOS" app source in:description',
    "topic:fndepot",
    "topic:fnos",
)


def search_candidates(gh, queries=DEFAULT_QUERIES, max_pages=2, per_page=100):
    """G2：search API 的 items 自带 default_branch，省掉逐仓 GET /repos。"""
    found, seen, failed = [], set(), []
    for q in queries:
        for page in range(1, max_pages + 1):
            url = "%s/search/repositories?q=%s&per_page=%d&page=%d&sort=stars&order=desc" % (
                API, urllib.parse.quote(q), per_page, page)
            try:
                data = gh.get(url)
            except Guard:
                raise
            except Exception as e:  # 单条查询失败不拖垮全局，但要留痕
                failed.append("%s p%d: %s" % (q, page, type(e).__name__))
                break
            items = (data or {}).get("items") or []
            if not items:
                break
            for it in items:
                fn = norm(it.get("full_name"))
                if not fn or fn in seen:
                    continue
                if it.get("archived") or it.get("disabled"):
                    continue
                seen.add(fn)
                found.append({
                    "full_name": fn,
                    "default_branch": it.get("default_branch") or "",
                    "fork": bool(it.get("fork")),
                    "stars": int(it.get("stargazers_count") or 0),
                    "size": int(it.get("size") or 0),
                    "pushed_at": it.get("pushed_at") or "",
                    "desc": (it.get("description") or "")[:160],
                    "query": q,
                })
            if len(items) < per_page:
                break
    return found, failed


def fetch_fork_parent(gh, full_name, budget):
    """仅对 fork 补充 parent 血缘；budget 控制 core 配额消耗。"""
    if budget[0] <= 0:
        return ""
    budget[0] -= 1
    try:
        d = gh.get("%s/repos/%s" % (API, full_name)) or {}
    except Exception:
        return ""
    p = (d.get("parent") or {}).get("full_name") or ""
    return norm(p)


def load_baseline(path):
    if not path or not os.path.exists(path):
        return []
    out = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            s = line.strip()
            if not s or s.startswith("#"):
                continue
            out.append(s)
    return out


def load_excludes(paths):
    """G1：仓库级黑名单。支持 # 注释与重复项；返回规范化 owner/repo 集合。"""
    ex = {}
    for p in paths or ():
        if not p or not os.path.exists(p):
            continue
        with open(p, encoding="utf-8") as f:
            for line in f:
                s = line.split("#", 1)[0].strip()
                if not s:
                    continue
                rp = repo_of(s) if s.lower().startswith("http") else norm(s)
                if rp:
                    ex[rp] = os.path.basename(p)
    return ex


def load_whitelist(paths):
    """白名单只用来「优先校验」，不能覆盖黑名单（G1 的强制语义）。"""
    wl = set()
    for p in paths or ():
        if not p or not os.path.exists(p):
            continue
        with open(p, encoding="utf-8") as f:
            for line in f:
                s = line.split("#", 1)[0].strip()
                if not s:
                    continue
                rp = repo_of(s) if s.lower().startswith("http") else norm(s)
                if rp:
                    wl.add(rp)
    return wl


FETCH_ERRORS = []   # 记录「取不到」是网络失败还是 404 —— 别把网络抖动当成「没有清单」


def _get_text(url, timeout=8, retries=1):
    for attempt in range(retries + 1):
        try:
            b = Gh("").get(url, raw=True, timeout=timeout)
        except Exception as e:
            if attempt >= retries:
                FETCH_ERRORS.append((url, type(e).__name__))
                return None
            time.sleep(0.6)
            continue
        if not b:
            return None          # 404：文件确实不存在（Gh.get 对 404 返回 None）
        return b.decode("utf-8", "replace")
    return None


def _raw(gh, full_name, branch, name, timeout=8):
    """取仓库文件正文。raw.githubusercontent.com 在本机常被墙（SYN 不通，实测）。

    通道顺序：jsDelivr → gh-proxy → GitHub contents API。单通道超时 8s，
    不通就换下一条，绝不把「网络不通」当成「仓库无清单」。
    """
    qb = urllib.parse.quote(branch)
    urls = [
        "https://cdn.jsdelivr.net/gh/%s@%s/%s" % (full_name, branch, name),
        "https://gh-proxy.com/https://raw.githubusercontent.com/%s/%s/%s" % (full_name, branch, name),
    ]
    for u in urls:
        t = _get_text(u, timeout=timeout)
        if t:
            return t
    # 最后一条：带 token 的 contents API（新鲜、受限 5000/h）；默认关闭以免吃掉配额
    if not API_FALLBACK:
        return None
    try:
        d = gh.get("%s/repos/%s/contents/%s?ref=%s" % (API, full_name, name, qb), timeout=timeout + 4)
    except Exception:
        return None
    if isinstance(d, dict) and d.get("content"):
        try:
            import base64
            return base64.b64decode(d["content"]).decode("utf-8", "replace")
        except Exception:
            return None
    return None


def _scan_urls(node, acc, depth=0):
    """递归收集很像 FPK 下载地址的字符串（download_url / *.fpk / releases 里的 url）。"""
    if depth > 5:
        return
    if isinstance(node, dict):
        for k, v in node.items():
            if isinstance(v, str):
                lv = v.strip()
                if not lv:
                    continue
                low = lv.lower()
                if k in ("download_url", "fpk"):
                    acc.append((k, lv))          # 相对路径也收，是否 http 另判
                elif low.startswith("http") and ".fpk" in low:
                    acc.append((k, lv))
            else:
                _scan_urls(v, acc, depth + 1)
    elif isinstance(node, list):
        for v in node:
            _scan_urls(v, acc, depth + 1)


def _looks_like_v1_root(data):
    """v1 协议：fnpack.json 的根对象本身就是 app 名 -> 条目 的映射（无 apps 键）。

    实测样本：smartartian/fndepot(master)、ByronChen7/FnDepot(main) 都是这种，
    只认 data["apps"] 会把它们全部判成「无清单」，属于踩过的坑。
    """
    if not isinstance(data, dict) or not data:
        return False
    vals = list(data.values())
    if not all(isinstance(v, dict) for v in vals):
        return False
    return any(("download_url" in v) or ("version" in v) or ("display_name" in v) for v in vals)


def norm_url(u):
    """包地址归一：去 scheme/查询/锚点、转小写 —— 用作「包指纹」。
    jsDelivr 与 raw 的同一包会归一成同一串，镜像差异不会造成假不同。"""
    s = (u or "").strip()
    if not s:
        return ""
    s = re.sub(r"^[a-zA-Z][a-zA-Z0-9+.-]*://", "", s)
    s = s.split("#", 1)[0].split("?", 1)[0]
    return s.lower().rstrip("/")


def _scan_sha(node, acc, depth=0):
    """收集清单里出现的 sha256（v2 packages 的 sha256 字段），作为第二判据。"""
    if depth > 5:
        return
    if isinstance(node, dict):
        for k, v in node.items():
            if isinstance(v, str) and "sha256" in k.lower() and len(v.strip()) == 64:
                acc.add(v.strip().lower())
            else:
                _scan_sha(v, acc, depth + 1)
    elif isinstance(node, list):
        for v in node:
            _scan_sha(v, acc, depth + 1)


def parse_manifest(name, text):
    """解析清单，返回 dict（不再用元组，字段会继续长）。

    F4：三种真实形态都要认
      - v2      ：{"apps": {...}} 或 {"apps": [...]}
      - v1-root ：根对象本身就是 app 映射（smartartian / ByronChen7 实测）
      - apps 列表：{"apps":[{slug/appname, ...}]}（conversun/fnos-apps 的 apps.json）
    """
    res = {"names": [], "urls": [], "schema": "", "empty_dl": 0, "dl_keys": 0,
           "sigs": set(), "ok": False, "note": ""}
    if text is None:
        res["note"] = "missing"
        return res
    if name == "repo_list.txt":
        lines = [l.strip() for l in text.splitlines() if l.strip() and not l.strip().startswith("#")]
        if not lines:
            res["note"] = "empty-file"
            return res
        res.update(names=["repo:%s" % repo_of(l) for l in lines if repo_of(l)],
                   schema="repo_list", ok=True, note="repo_list")
        return res
    try:
        data = json.loads(text)
    except Exception:
        res["note"] = "bad-json"
        return res
    apps, schema = None, "v2"
    if isinstance(data, dict):
        apps = data.get("apps")
        if apps is None and _looks_like_v1_root(data):
            apps, schema = data, "v1-root"
        elif apps is None and isinstance(data.get("packages"), list):
            apps, schema = data["packages"], "packages"
    elif isinstance(data, list):
        apps, schema = data, "v2-list"
    names = []
    if isinstance(apps, dict):
        names = [norm(k) for k in apps.keys() if k]
    elif isinstance(apps, list):
        for it in apps:
            if isinstance(it, dict):
                k = (it.get("name") or it.get("appname") or it.get("slug")
                     or it.get("app") or it.get("package"))
                if k:
                    names.append(norm(k))
    if not names:
        res["note"] = "no-apps"
        return res
    urls, sigs = [], set()
    _scan_urls(apps, urls)
    if isinstance(apps, dict):
        _scan_sha(apps, sigs)
        for v in apps.values():
            if isinstance(v, dict) and "download_url" in v:
                res["dl_keys"] += 1
                if not str(v.get("download_url") or "").strip():
                    res["empty_dl"] += 1
    for _k, u in urls:
        s = norm_url(u)
        if s:
            sigs.add(s)
    res.update(names=names, urls=urls, schema=schema, sigs=sigs, ok=True, note=schema)
    return res


def usable_of(res):
    """F4：v1 清单本来就没有 download_url 字段 —— 那是协议形态，不是空源。

    真·空源 = 有 download_url 键但全部为空（v2 里的 68 条实测属于这类）。
    """
    if not res.get("ok"):
        return False, "no-manifest"
    apps, empty, urls, schema = len(res["names"]), res["empty_dl"], res["urls"], res["schema"]
    if schema in ("v1-root", "v1") and res["dl_keys"] == 0:
        return True, "v1-no-dl-field"
    if not urls or apps - empty <= 0:
        return False, "empty-all"
    if empty:
        return True, "partial-empty"
    return True, "ok"


def validate_repo(gh, repo, probe=False, probe_budget=None):
    """G2/G3/F4：用 default_branch 探测清单；v1 无字段、apps 列表、目录型都要能认。"""
    db = repo.get("default_branch") or ""
    branches = [db] if db else ["main", "master"]
    errs0 = len(FETCH_ERRORS)
    for br in branches:
        for mf in MANIFESTS:
            res = parse_manifest(mf, _raw(gh, repo["full_name"], br, mf))
            if not res["ok"]:
                continue
            usable, why = usable_of(res)
            if not usable:
                # 聚合索引形态：apps.json 只有 release_tag、没有直接 .fpk 地址
                # （conversun/fnos-apps 实测 192 条）→ 用 apps/ 子目录数当应用数
                n_dir = dir_apps(gh, repo["full_name"], br)
                if n_dir >= 2:
                    res.update(names=["dir:%d" % i for i in range(n_dir)], schema="directory",
                               sigs=set())
                    usable, why = True, "directory-index"
            dead = 0
            if probe and res["urls"] and probe_budget is not None:
                for _k, u in res["urls"][:3]:
                    if probe_budget[0] <= 0:
                        break
                    probe_budget[0] -= 1
                    if not head_ok(gh, u):
                        dead += 1
            repo.update({
                "valid": True, "manifest": mf, "branch": br, "schema": res["schema"],
                "app_names": res["names"], "url_count": len(res["urls"]),
                "dead_urls": dead, "apps": len(res["names"]),
                "empty_dl": res["empty_dl"], "dl_keys": res["dl_keys"],
                "sigs": sorted(res["sigs"]), "usable": usable, "usable_reason": why,
                "absolute_urls": sum(1 for _k, u in res["urls"] if u.lower().startswith("http")),
            })
            return repo
    # 目录型源：apps/ 下每个子目录一个应用（conversun/fnos-apps 形态）
    # 目录型至少要 2 个子目录才算「聚合源」：单个 apps/ 子目录的应用仓不是源
    n = dir_apps(gh, repo["full_name"], branches[0])
    if n >= 2:
        repo.update({"valid": True, "manifest": "apps/", "branch": branches[0], "schema": "directory",
                     "app_names": ["dir:%d" % i for i in range(n)], "url_count": 0, "dead_urls": 0,
                     "apps": n, "empty_dl": 0, "dl_keys": 0, "sigs": [],
                     "usable": True, "usable_reason": "directory", "absolute_urls": 0})
        return repo
    why = "fetch-error" if len(FETCH_ERRORS) > errs0 else "no-manifest"
    repo.update({"valid": False, "manifest": "", "branch": "", "app_names": [], "url_count": 0,
                 "dead_urls": 0, "apps": 0, "empty_dl": 0, "dl_keys": 0, "sigs": [],
                 "usable": False, "usable_reason": why})
    return repo


def dir_apps(gh, full_name, branch):
    try:
        d = gh.get("%s/repos/%s/contents/apps?ref=%s" % (API, full_name, branch), timeout=12)
    except Exception:
        return 0
    if not isinstance(d, list):
        return 0
    return sum(1 for x in d if isinstance(x, dict) and x.get("type") == "dir")


def head_ok(gh, url):
    try:
        req = urllib.request.Request(url, headers={"User-Agent": UA}, method="HEAD")
        with urllib.request.urlopen(req, timeout=15) as f:
            return 200 <= f.status < 400
    except Exception:
        return False


def overlap(a, b):
    if not a or not b:
        return 0.0
    return len(set(a) & set(b)) / float(min(len(a), len(b)))


def is_dup(r, k, fork_th=0.5, plain_th=0.9, sig_th=0.6, small_sig_th=0.5):
    """F3：判重要同时看「名字重合」和「包指纹重合」，小源另加保护。

    - 血缘：同一 fork 家族 + 名字 ≥0.5 → 重复（纯镜像/搬运）
    - 非血缘：名字 ≥0.9 且包指纹 ≥0.7 → 重复（改名不改包的复制）
    - 小源（任一侧 app < 3）：名字再像也必须有指纹重合才判重，否则两个无关的
      单应用源（都收了同一个热门应用）会互相残杀 —— 这是原版最大的误杀面。
    """
    name_rate = overlap(r.get("app_names") or [], k.get("app_names") or [])
    sig_rate = overlap(r.get("sigs") or [], k.get("sigs") or [])
    same_line = bool(
        (r.get("parent") and r["parent"] in (k["full_name"], k.get("parent"))) or
        (k.get("parent") and k["parent"] == r["full_name"]) or
        (r.get("parent") and r.get("parent") == k.get("parent")))
    small = min(r.get("apps") or 0, k.get("apps") or 0) < 3
    if same_line and name_rate >= fork_th:
        return True, "fork-lineage(name=%.2f,sig=%.2f)" % (name_rate, sig_rate)
    if name_rate >= plain_th and sig_rate >= sig_th:
        return True, "same-content(name=%.2f,sig=%.2f)" % (name_rate, sig_rate)
    if small and name_rate >= plain_th and sig_rate < small_sig_th:
        return False, ""
    return False, ""


def dedup(repos, fork_th=0.5, plain_th=0.9):
    """F3：非 fork 优先当 canonical（同分时更稳定），判重交给 is_dup。"""
    ordered = sorted(repos, key=lambda r: (bool(r.get("fork")), -r["apps"], -r["stars"], r["full_name"]))
    keep, dropped = [], []
    for r in ordered:
        hit = ""
        for k in keep:
            dup, why = is_dup(r, k, fork_th, plain_th)
            if dup:
                hit = (k, why)
                break
        if hit:
            r["duplicate_of"] = hit[0]["full_name"]
            r["dup_reason"] = hit[1]
            r["dup_apps"] = hit[0]["apps"]
            dropped.append(r)
        else:
            keep.append(r)
    return keep, dropped

def _old_dedup(repos, fork_th=0.5, plain_th=0.9):
    ordered = sorted(repos, key=lambda r: (-r["apps"], -r["stars"], r["full_name"]))
    keep, dropped = [], []
    for r in ordered:
        dup_of = ""
        for k in keep:
            same_line = (r.get("parent") and r["parent"] in (k["full_name"], k.get("parent"))) or \
                        (k.get("parent") and k["parent"] == r["full_name"]) or \
                        (r.get("parent") and r.get("parent") == k.get("parent"))
            rate = overlap(r["app_names"], k["app_names"])
            if (same_line and rate >= fork_th) or rate >= plain_th:
                dup_of = k["full_name"]
                break
        if dup_of:
            r["duplicate_of"] = dup_of
            r["overlap"] = round(overlap(r["app_names"], next(k["app_names"] for k in keep if k["full_name"] == dup_of)), 3)
            dropped.append(r)
        else:
            keep.append(r)
    return keep, dropped


def guard(baseline_count, valid_count, additions, min_ratio=0.9, replace=False):
    """G0：宁可不出名单。valid=0（限流/网络全挂）或拟议总数跌破基线 90% 即拒绝。

    replace=False（默认，只增不删）：拟议总数 = 基线 + 新增，只可能变大；
    replace=True（整份覆写模式，如未来改成「重新生成」）：拟议总数 = len(additions)，
    此时骤减守卫才真正生效 —— 这正是 EWEDLCM / 710850609 缺的那道闸。
    """
    if valid_count == 0 and baseline_count > 0:
        raise Guard("校验通过 0 个（疑似限流/网络故障），拒绝写入（基线 %d 条）" % baseline_count)
    proposed = len(additions) if replace else baseline_count + len(additions)
    if baseline_count > 0 and proposed < int(baseline_count * min_ratio):
        raise Guard("拟议总数 %d < 基线 %d 的 %.0f%%，拒绝写入" % (proposed, baseline_count, min_ratio * 100))
    return proposed


def write_append(path, additions):
    """G4：只追加，不重写既有行（保留原顺序与注释）。"""
    if not additions:
        return 0
    old = ""
    if os.path.exists(path):
        with open(path, encoding="utf-8") as f:
            old = f.read()
    tail = "" if old.endswith("\n") or not old else "\n"
    with open(path, "a", encoding="utf-8") as f:
        f.write(tail + "\n".join(additions) + "\n")
    return len(additions)


def read_token(args):
    if args.token_file:
        with open(os.path.expanduser(args.token_file), encoding="utf-8") as f:
            return f.read().strip()
    return (os.environ.get("GH_TOKEN") or os.environ.get("MOO_GH_TOKEN") or "").strip()


def load_lineage(path):
    if path and os.path.exists(path):
        try:
            with open(path, encoding="utf-8") as f:
                return json.load(f)
        except Exception:
            return {}
    return {}


def save_lineage(path, lin):
    if not path:
        return
    try:
        with open(path, "w", encoding="utf-8") as f:
            json.dump(lin, f, ensure_ascii=False, indent=1, sort_keys=True)
    except Exception:
        pass


def lineage_rec(gh, full, lin, budget):
    """取一条仓库血缘记录（parent / fork / default_branch），带缓存与配额。"""
    if full in lin:
        return lin[full]
    if budget[0] <= 0:
        return {}
    budget[0] -= 1
    try:
        d = gh.get("%s/repos/%s" % (API, full), timeout=15) or {}
    except Exception:
        return {}
    rec = {"parent": ((d.get("parent") or {}).get("full_name") or "").lower(),
           "fork": bool(d.get("fork")),
           "default_branch": d.get("default_branch") or ""}
    lin[full] = rec
    return rec


def ancestors(full, lin, depth=3):
    """F1：沿 parent 链上溯（最多 depth 层），用于判断「这个候选是不是基线源的 fork」。"""
    out, cur = [], full
    for _ in range(depth):
        p = (lin.get(cur) or {}).get("parent") or ""
        if not p or p in out:
            break
        out.append(p)
        cur = p
    return out


def compare_ahead(gh, full, parent, lin, branch=""):
    """F2：ahead_by —— 0 表示纯镜像（一个自己的提交都没有）。"""
    pbr = (lin.get(parent) or {}).get("default_branch") or "main"
    cbr = branch or (lin.get(full) or {}).get("default_branch") or "main"
    try:
        d = gh.get("%s/repos/%s/compare/%s...%s:%s" % (API, parent, pbr, full.split("/")[0], cbr),
                   timeout=20) or {}
    except Exception:
        return -1
    try:
        return int(d.get("ahead_by", -1))
    except Exception:
        return -1


def fork_verdict(ahead):
    """F2：fork 的去留判据。0 = 纯镜像；>0 = 有自己的提交（人工看）；<0 = 取不到。"""
    if ahead == 0:
        return "mirror"
    if ahead and ahead > 0:
        return "review"
    return "unknown"


def attach_lineage(gh, repos, lin, budget):
    """给待校验候选补血缘（候选是 fork 才要查；父的父也查一层，用于隔代识别）。"""
    for c in repos:
        if not c.get("fork"):
            continue
        rec = lineage_rec(gh, c["full_name"], lin, budget)
        c["parent"] = rec.get("parent") or ""
        if c["parent"]:
            lineage_rec(gh, c["parent"], lin, budget)


def audit_baseline(gh, urls, workers=8, out_dir=""):
    """F5：体检已收录清单 —— 死仓、空源、部分空、v1 无下载字段，一次性列清。

    这是 BACKLOG `B-SRC-CARE-DEADFEED` 的只读半边：只产报告，不动清单、不摘源。
    """
    def one(u):
        full = repo_of(u) or u
        rec = {"repo": full, "url": u, "status": "", "apps": 0, "empty_dl": 0,
               "dl_keys": 0, "urls": 0, "schema": "", "manifest": "", "usable": False}
        if "/" not in full:
            rec["status"] = "bad-url"
            return rec
        try:
            info = gh.get("%s/repos/%s" % (API, full), timeout=15)
        except Exception:
            info = None
        if not info:
            rec["status"] = "repo-404"
            return rec
        rec["default_branch"] = info.get("default_branch") or ""
        rec["pushed_at"] = info.get("pushed_at") or ""
        rec["fork"] = bool(info.get("fork"))
        r = {"full_name": full, "default_branch": rec["default_branch"],
             "fork": rec["fork"], "stars": 0}
        validate_repo(gh, r)
        rec.update(status=r.get("usable_reason") or ("ok" if r.get("valid") else "no-manifest"),
                   apps=r.get("apps", 0), empty_dl=r.get("empty_dl", 0), dl_keys=r.get("dl_keys", 0),
                   urls=r.get("url_count", 0), schema=r.get("schema", ""),
                   manifest=r.get("manifest", ""), usable=bool(r.get("usable")))
        return rec

    with ThreadPoolExecutor(max_workers=max(1, workers)) as pool:
        rows = list(pool.map(one, urls))

    import collections
    cnt = collections.Counter(r["status"] for r in rows)
    for k, v in sorted(cnt.items(), key=lambda x: -x[1]):
        print("  %-16s %d" % (k, v))
    bad = [r for r in rows if not r["usable"]]
    print("不可用/可疑 %d 条：" % len(bad))
    for r in bad:
        print("  %-34s %-14s apps=%-3d empty=%-3d schema=%s" % (
            r["repo"], r["status"], r["apps"], r["empty_dl"], r["schema"] or "-"))
    if out_dir:
        os.makedirs(out_dir, exist_ok=True)
        jp = os.path.join(out_dir, "baseline_health.json")
        with open(jp, "w", encoding="utf-8") as f:
            json.dump(rows, f, ensure_ascii=False, indent=1)
        print("report:", jp)
    return 0


UPSTREAMS = (
    ("EWEDLCM/FnDepot", "EWEDLCM/FnDepot", "main", "valid_sources.txt"),
    ("710850609/fndepot", "710850609/fndepot", "main", "repo_list.txt"),
)


def fetch_upstream(gh, spec):
    """取上游名单：spec = (name, repo, branch, path)；返回 (name, [repo...], err)。"""
    name, repo, br, path = spec
    if path.startswith("http") or path.startswith("/"):
        url, err = path, ""
        try:
            b = Gh("").get(url, raw=True, timeout=20) if url.startswith("http") else None
            text = b.decode("utf-8", "replace") if b else (open(url, encoding="utf-8").read() if os.path.exists(url) else None)
        except Exception as e:
            text, err = None, type(e).__name__
    else:
        text, err = _raw(gh, repo, br, path), ""
    if not text:
        return name, [], err or "fetch-failed"
    out = []
    for line in text.splitlines():
        s = line.split("#", 1)[0].strip()
        if not s:
            continue
        r = repo_of(s) or (norm(s) if "/" in s and not s.startswith("http") else "")
        if r:
            out.append(r)
    return name, out, ""


def sync_upstream(args):
    """同步链路：上游名单 → 校验（F1~F4）→ 只增不删地追加到本地基线。

    刻意不做的事：不推送、不改远端（moo-dev-lists / FnDepot 的同步只出计划，需单独口令）。
    """
    token = read_token(args)
    gh = Gh(token)
    baseline = load_baseline(args.baseline)
    base_norm = set(repo_of(u) or norm(u) for u in baseline)
    excludes = load_excludes(args.exclude)

    specs = []
    for item in (args.upstream or []):
        if "=" not in item:
            print("WARN: --upstream 需 name=url|path，忽略 %s" % item, file=sys.stderr)
            continue
        nm, val = item.split("=", 1)
        specs.append((nm, "", "", val))
    if not specs:
        specs = list(UPSTREAMS)

    per_up, union, errs = {}, set(), {}
    for spec in specs:
        name, repos, err = fetch_upstream(gh, spec)
        per_up[name] = len(repos)
        if err:
            errs[name] = err
            print("upstream %-22s 取失败: %s" % (name, err))
            continue
        union |= set(repos)
        print("upstream %-22s %d 条" % (name, len(repos)))

    if not union and args.baseline:
        raise Guard("上游名单全为空（%s），拒绝产出" % (errs or "empty"))

    cands = sorted(union - base_norm - set(excludes))
    skipped_ex = sorted(union & set(excludes))
    print("合并去重 %d 条；已在基线 %d 条；黑名单 %d 条；待校验 %d 条" % (
        len(union), len(union & base_norm), len(skipped_ex), len(cands)))

    lin = load_lineage(args.lineage_cache)
    lb = [args.lineage_budget]
    worked = cands[:args.max_validate]
    for full in worked:
        lineage_rec(gh, full, lin, lb)
        p = (lin.get(full) or {}).get("parent") or ""
        if p:
            lineage_rec(gh, p, lin, lb)
    save_lineage(args.lineage_cache, lin)

    kept, skipped_lineage = [], []
    for full in worked:
        hit = "" if args.no_lineage_skip else next(
            (a for a in ancestors(full, lin) if a in base_norm), "")
        if hit:
            skipped_lineage.append({"repo": full, "ancestor_in_baseline": hit})
            continue
        kept.append({"full_name": full, "default_branch": (lin.get(full) or {}).get("default_branch") or "",
                     "fork": bool((lin.get(full) or {}).get("fork")), "stars": 0,
                     "parent": (lin.get(full) or {}).get("parent") or ""})

    probe_budget = [args.probe_limit]

    def work(c):
        try:
            return validate_repo(gh, c, probe=args.probe, probe_budget=probe_budget)
        except Exception as e:
            c.update({"valid": False, "apps": 0, "usable": False,
                      "usable_reason": type(e).__name__})
            return c

    with ThreadPoolExecutor(max_workers=max(1, args.workers)) as pool:
        rows = list(pool.map(work, kept))
    valid = [r for r in rows if r.get("valid")]
    usable = [r for r in valid if r.get("usable")]
    print("校验 %d 条 → 有效 %d（可用 %d）" % (len(rows), len(valid), len(usable)))

    keep, dropped = dedup(usable)
    cb = [args.compare_budget]
    add_list, mirrors, review = [], [], []
    for r in keep:
        if r.get("fork") and args.mirror_check:
            parent = r.get("parent") or (lin.get(r["full_name"]) or {}).get("parent") or ""
            if parent and cb[0] > 0:
                cb[0] -= 1
                ahead = compare_ahead(gh, r["full_name"], parent, lin, r.get("branch"))
                r["ahead_by"] = ahead
                v = fork_verdict(ahead)
                if v == "mirror":
                    r["mirror_of"] = parent
                    mirrors.append(r)
                    continue
                r["needs_review"] = "fork_with_own_commits" if v == "review" else "compare_unavailable"
                review.append(r)
                continue
            r["needs_review"] = "fork_parent_unknown"
            review.append(r)
            continue
        add_list.append(r)

    additions = ["https://github.com/" + r["full_name"] for r in add_list]
    if worked:
        proposed = guard(len(baseline), len(usable), additions)
    else:
        # 上游名单 100% 已在基线覆盖 = 正常结果，不是「取不到数据」
        proposed = len(baseline)
        print("上游名单全部已在基线覆盖，无新增候选（守卫不适用于空候选）")
    not_in_upstream = sorted(base_norm - union)

    report = {
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "upstreams": per_up, "upstream_errors": errs, "union": len(union),
        "already_in_baseline": len(union & base_norm), "blacklisted": skipped_ex,
        "skipped_fork_of_baseline": skipped_lineage,
        "valid": len(valid), "usable": len(usable),
        "duplicates": [{"repo": d["full_name"], "of": d["duplicate_of"], "reason": d.get("dup_reason", "")} for d in dropped],
        "pure_mirrors": [{"repo": r["full_name"], "of": r["mirror_of"]} for r in mirrors],
        "needs_review": [{"repo": r["full_name"], "why": r["needs_review"],
                          "ahead_by": r.get("ahead_by"), "apps": r["apps"]} for r in review],
        "rejected": [{"repo": r["full_name"], "why": r.get("usable_reason") or "no-manifest"}
                     for r in rows if not r.get("usable")],
        "additions": additions, "proposed_total": proposed,
        "baseline_not_in_upstream": len(not_in_upstream),
    }
    if args.out_dir:
        os.makedirs(args.out_dir, exist_ok=True)
        stamp = time.strftime("%Y%m%d-%H%M%S")
        jp = os.path.join(args.out_dir, "sync_%s.json" % stamp)
        with open(jp, "w", encoding="utf-8") as f:
            json.dump(report, f, ensure_ascii=False, indent=2)
        print("report:", jp)

    print("结果：上限 %d，校验 %d，可用 %d，查重丢 %d，镜像丢 %d，待人工 %d，新增 %d，拟议总数 %d" % (
        len(union), len(rows), len(usable), len(dropped), len(mirrors), len(review),
        len(additions), proposed))
    print("反向信息：基线里有 %d 条不在上游任何名单中（只统计，不删）" % len(not_in_upstream))
    if args.write:
        n = write_append(args.baseline, additions)
        print("WRITE: 追加 %d 行 -> %s" % (n, args.baseline))
    else:
        print("DRY-RUN: 未写文件（--write 才落盘）")
    for a in additions:
        print("  + " + a)
    return 0


def _load_list_any(spec):
    """读一份清单：spec 可为 http(s) URL 或本地路径；返回 set(owner/repo)。"""
    text, err = None, ""
    if spec.startswith("http"):
        try:
            b = Gh("").get(spec, raw=True, timeout=20)
            text = b.decode("utf-8", "replace") if b else None
        except Exception as e:
            err = type(e).__name__
    elif os.path.exists(spec):
        text = open(spec, encoding="utf-8").read()
    else:
        err = "not-found"
    if text is None:
        return set(), err or "empty"
    out = set()
    for line in text.splitlines():
        s = line.split("#", 1)[0].strip()
        if not s:
            continue
        r = repo_of(s) or (norm(s) if "/" in s and not s.startswith("http") else "")
        if r:
            out.add(r)
    return out, ""


def sync_plan(args):
    """只读：主清单 vs 各派生副本的差异计划（不做任何写入、不推送）。"""
    master = load_baseline(args.baseline)
    mset = set(repo_of(u) or norm(u) for u in master)
    base_dir = os.path.dirname(os.path.abspath(args.baseline)) or "."
    targets = []
    for item in (args.target or []):
        if "=" in item:
            nm, val = item.split("=", 1)
        else:
            nm, val = item, item
        targets.append((nm, val))
    if not targets:
        targets = [("lists/repo_list.txt", os.path.join(base_dir, "lists", "repo_list.txt"))]

    plan = {"generated_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "master": args.baseline, "master_count": len(mset), "targets": []}
    for name, spec in targets:
        tset, err = _load_list_any(spec)
        rec = {"name": name, "spec": spec, "count": len(tset), "error": err}
        if err:
            print("target %-24s 读取失败: %s" % (name, err))
            plan["targets"].append(rec)
            continue
        missing = sorted(mset - tset)
        extra = sorted(tset - mset)
        rec.update(missing_in_target=len(missing), extra_in_target=len(extra),
                   missing_sample=missing[:10], extra_sample=extra[:10])
        plan["targets"].append(rec)
        print("target %-24s %d 条；主清单有而它没有 %d 条；它有而主清单没有 %d 条" % (
            name, len(tset), len(missing), len(extra)))
        for r in extra[:10]:
            print("   ! 目标独有: " + r)

    if args.out_dir:
        os.makedirs(args.out_dir, exist_ok=True)
        jp = os.path.join(args.out_dir, "sync_plan_%s.json" % time.strftime("%Y%m%d-%H%M%S"))
        with open(jp, "w", encoding="utf-8") as f:
            json.dump(plan, f, ensure_ascii=False, indent=2)
        print("report:", jp)
    print("只读模式：未写任何清单、未推送远端（推 Gitea/GitHub/FnDepot 需单独口令）")
    return 0


def run(args):
    token = read_token(args)
    if not token:
        print("WARN: 无 token（set GH_TOKEN 或 --token-file），以匿名 60/h 运行", file=sys.stderr)
    gh = Gh(token)
    baseline = load_baseline(args.baseline)
    base_norm = set(repo_of(u) or norm(u) for u in baseline)
    excludes = load_excludes(args.exclude)
    whitelist = load_whitelist(args.whitelist)

    cands, failed = search_candidates(gh, max_pages=args.max_pages)
    print("stage1 候选 %d 条（查询失败 %d）" % (len(cands), len(failed)))

    blocked = [c for c in cands if c["full_name"] in excludes]
    cands = [c for c in cands if c["full_name"] not in excludes]
    print("stage2 黑名单剔除 %d 条（黑名单 > 白名单，白名单不豁免）" % len(blocked))

    todo = [c for c in cands if c["full_name"] not in base_norm]
    known = [c for c in cands if c["full_name"] in base_norm]

    def likely_source(c):
        """命中 source 语义关键词的优先校验：真实源往往星少，纯按 stars 排会被应用仓淹掉。"""
        s = (c["full_name"] + " " + (c.get("desc") or "")).lower()
        return 1 if any(k in s for k in ("depot", "store", "source", "index", "fnpack", "list")) else 0

    todo.sort(key=lambda c: (c["full_name"] not in whitelist, -likely_source(c), -c["stars"]))

    # F1：先补血缘再截断 —— 基线源的 fork 直接跳过，不占校验名额
    lin = load_lineage(args.lineage_cache)
    lb = [args.lineage_budget]
    window = todo[:args.max_validate * 3]
    forks = [c for c in window if c.get("fork")][:args.lineage_budget]

    def _rec(c):
        r = lineage_rec(gh, c["full_name"], lin, lb)
        c["parent"] = r.get("parent") or ""
        return c

    with ThreadPoolExecutor(max_workers=max(1, args.workers)) as pool:
        list(pool.map(_rec, forks))
    parents = sorted({c.get("parent") for c in forks if c.get("parent")} - set(lin))
    with ThreadPoolExecutor(max_workers=max(1, args.workers)) as pool:
        list(pool.map(lambda p: lineage_rec(gh, p, lin, lb), parents[:args.lineage_budget]))
    save_lineage(args.lineage_cache, lin)

    todo_ok, skipped_lineage = [], []
    for c in todo:
        if len(todo_ok) >= args.max_validate:
            break
        hit = "" if args.no_lineage_skip else next(
            (a for a in ancestors(c["full_name"], lin) if a in base_norm), "")
        if hit:
            c["skip_ancestor"] = hit
            skipped_lineage.append({"repo": c["full_name"], "ancestor_in_baseline": hit})
            continue
        todo_ok.append(c)
    todo = todo_ok
    save_lineage(args.lineage_cache, lin)
    save_lineage(args.lineage_cache, lin)
    print("stage3 待校验 %d 条（已在基线 %d 条；基线源的 fork 跳过 %d 条）" % (
        len(todo), len(known), len(skipped_lineage)))

    probe_budget = [args.probe_limit]

    def work(c):
        try:
            return validate_repo(gh, c, probe=args.probe, probe_budget=probe_budget)
        except Exception as e:
            c.update({"valid": False, "manifest": "", "branch": "", "app_names": [],
                      "url_count": 0, "dead_urls": 0, "apps": 0, "empty_dl": 0,
                      "usable": False, "error": type(e).__name__})
            return c

    valid, rejected = [], []
    with ThreadPoolExecutor(max_workers=max(1, args.workers)) as pool:
        for i, c in enumerate(pool.map(work, todo), 1):
            if c.get("valid"):
                valid.append(c)
                if args.verbose:
                    print("  OK  %-45s %s@%s apps=%d" % (c["full_name"], c["manifest"], c["branch"], c["apps"]), flush=True)
            else:
                rejected.append({"repo": c["full_name"],
                                 "why": c.get("error") or c.get("usable_reason") or "no-manifest"})
                if args.verbose:
                    print("  --  %-45s %s" % (c["full_name"], c.get("error") or "no-manifest"), flush=True)
            if i % 20 == 0:
                print("  ... %d/%d valid=%d" % (i, len(todo), len(valid)), flush=True)

    if args.min_apps > 1:
        dropped_small = [r for r in valid if r.get("usable") and r["apps"] < args.min_apps]
        valid = [r for r in valid if r not in dropped_small]
        print("stage3.5 应用数 < %d 的源按 --min-apps 跳过 %d 条" % (args.min_apps, len(dropped_small)))
    usable = [r for r in valid if r.get("usable")]
    unusable = [r for r in valid if not r.get("usable")]
    keep, dropped = dedup(usable)

    # F2：fork 但有自己的提交 → needs_review；ahead==0 的纯镜像 → 丢弃
    cb = [args.compare_budget]
    add_list, mirrors, needs_review = [], [], []
    for r in keep:
        if r.get("fork") and args.mirror_check:
            parent = r.get("parent") or (lin.get(r["full_name"]) or {}).get("parent") or ""
            if parent and cb[0] > 0:
                cb[0] -= 1
                ahead = compare_ahead(gh, r["full_name"], parent, lin, r.get("branch"))
                r["ahead_by"] = ahead
                verdict = fork_verdict(ahead)
                if verdict == "mirror":
                    r["mirror_of"] = parent
                    mirrors.append(r)
                    continue
                if verdict == "review":
                    r["needs_review"] = "fork_with_own_commits"
                    needs_review.append(r)
                    continue
                r["needs_review"] = "compare_unavailable"
                needs_review.append(r)
                continue
            r["needs_review"] = "fork_parent_unknown"
            needs_review.append(r)
            continue
        add_list.append(r)
    additions = ["https://github.com/" + r["full_name"] for r in add_list]
    proposed = guard(len(baseline), len(usable), additions)
    print("stage4 有效 %d 条（可用 %d / 不可用 %d），查重丢弃 %d 条" % (
        len(valid), len(usable), len(unusable), len(dropped)))
    print("stage5 纯镜像丢弃 %d 条，需人工确认(fork 有自己提交) %d 条，最终新增 %d 条，拟议总数 %d" % (
        len(mirrors), len(needs_review), len(additions), proposed))

    report = {
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "baseline": args.baseline, "baseline_count": len(baseline),
        "candidates": len(cands) + len(blocked), "blocked_by_blacklist": blocked,
        "search_failed": failed, "valid_count": len(valid),
        "duplicates": [{"repo": d["full_name"], "of": d["duplicate_of"], "reason": d.get("dup_reason", "")}
                       for d in dropped],
        "skipped_fork_of_baseline": skipped_lineage,
        "pure_mirrors": [{"repo": r["full_name"], "of": r["mirror_of"]} for r in mirrors],
        "needs_review": [{"repo": r["full_name"], "why": r.get("needs_review"),
                          "ahead_by": r.get("ahead_by", None), "apps": r["apps"]} for r in needs_review],
        "additions": additions, "proposed_total": proposed,
        "dead_urls": sum(r["dead_urls"] for r in valid),
        "empty_download_urls": sum(r.get("empty_dl") or 0 for r in valid),
        "unusable_sources": [{"repo": r["full_name"], "apps": r["apps"], "empty_dl": r.get("empty_dl") or 0}
                             for r in unusable],
        "rejected": rejected,
        "kept": [{k: r.get(k) for k in ("full_name", "manifest", "branch", "schema", "apps",
                                        "url_count", "empty_dl", "dl_keys", "usable",
                                        "usable_reason", "fork", "parent", "ahead_by", "stars")}
                 for r in add_list],
    }
    if args.out_dir:
        os.makedirs(args.out_dir, exist_ok=True)
        stamp = time.strftime("%Y%m%d-%H%M%S")
        jp = os.path.join(args.out_dir, "discover_%s.json" % stamp)
        with open(jp, "w", encoding="utf-8") as f:
            json.dump(report, f, ensure_ascii=False, indent=2)
        print("report:", jp)

    if args.write:
        n = write_append(args.baseline, additions)
        print("WRITE: 追加 %d 行 -> %s" % (n, args.baseline))
    else:
        print("DRY-RUN: 未写文件（--write 才落盘）")
    for a in additions:
        print("  + " + a)
    return 0


FN_SAMPLE = '{"schema_version":"2","apps":{"demo":{"download_url":"https://x/y/demo.fpk","releases":{"1.0":{"packages":[{"url":"https://x/y/demo.fpk"}]}}}}}'


def selftest():
    """离线自检：守卫 / 黑名单优先 / 查重 / 只增不删 / 清单解析。任一条失败即退出 1。"""
    import tempfile
    ok = []

    assert repo_of("https://github.com/Foo/Bar.git") == "foo/bar"
    assert repo_of("https://github.com/foo/bar/tree/main") == ""
    ok.append("repo_of 归一化")

    r2 = parse_manifest("fnpack.json", FN_SAMPLE)
    assert r2["ok"] and r2["names"] == ["demo"] and r2["empty_dl"] == 0
    assert any(".fpk" in u for _k, u in r2["urls"]) and r2["sigs"]
    assert not parse_manifest("fnpack.json", "{bad")["ok"]
    assert parse_manifest("repo_list.txt", "# c\nhttps://github.com/a/b\n")["names"] == ["repo:a/b"]
    v1 = '{"minio":{"version":"1.0.0","download_url":""},"llm":{"version":"2","download_url":"https://x/a.fpk"}}'
    r1 = parse_manifest("fnpack.json", v1)
    assert r1["schema"] == "v1-root" and r1["names"] == ["minio", "llm"] and r1["empty_dl"] == 1
    # F4：v1 完全没有 download_url 字段 = 协议形态，不是空源
    nov = '{"linker":{"version":"1.9","display_name":"Linker"}}'
    nv = parse_manifest("fnpack.json", nov)
    assert nv["schema"] == "v1-root" and nv["dl_keys"] == 0
    assert usable_of(nv) == (True, "v1-no-dl-field"), usable_of(nv)
    # F4：真·空源（有键但全空）
    alle = '{"a":{"version":"1","download_url":""},"b":{"version":"2","download_url":""}}'
    assert usable_of(parse_manifest("fnpack.json", alle))[0] is False
    # F4：apps 列表形态（conversun/fnos-apps 的 apps.json）
    lst = '{"schema_version":1,"apps":[{"slug":"1panel","appname":"1panel"},{"slug":"alist"}]}'
    rl = parse_manifest("apps.json", lst)
    assert rl["ok"] and rl["names"] == ["1panel", "alist"], rl
    ok.append("F4 清单解析：v2 / v1根 / v1无字段≠空源 / 全空=空源 / apps列表")

    d = tempfile.mkdtemp()
    ex = os.path.join(d, "ex.txt")
    wl = os.path.join(d, "wl.txt")
    with open(ex, "w", encoding="utf-8") as f:
        f.write("# 注释\nhttps://github.com/12hgl/FnDepot\n")
    with open(wl, "w", encoding="utf-8") as f:
        f.write("12hgl/FnDepot\nother/repo\n")
    excludes, whitelist = load_excludes([ex]), load_whitelist([wl])
    cands = [{"full_name": "12hgl/fndepot"}, {"full_name": "other/repo"}]
    left = [c for c in cands if c["full_name"] not in excludes]
    assert [c["full_name"] for c in left] == ["other/repo"], left
    assert "12hgl/fndepot" in whitelist and "12hgl/fndepot" in excludes
    ok.append("G1 黑名单压过白名单")

    raised = 0
    for kwargs in ({"baseline_count": 100, "valid_count": 0, "additions": []},
                   {"baseline_count": 100, "valid_count": 3, "additions": ["u"], "replace": True}):
        try:
            guard(**kwargs)
        except Guard:
            raised += 1
    assert raised == 2, raised
    assert guard(100, 50, ["u1", "u2"]) == 102
    assert guard(100, 95, ["u"] * 95, replace=True) == 95
    assert guard(0, 0, []) == 0
    ok.append("G0 零幅度/骤减守卫")

    a = {"full_name": "a/one", "apps": 10, "stars": 50, "app_names": ["x", "y", "z"],
         "sigs": ["h/a.fpk", "h/b.fpk"], "parent": "", "fork": False}
    b = {"full_name": "b/two", "apps": 3, "stars": 900, "app_names": ["x", "y"],
         "sigs": ["h/a.fpk"], "parent": "a/one", "fork": True}
    c = {"full_name": "c/three", "apps": 2, "stars": 1, "app_names": ["p", "q"],
         "sigs": ["h/c.fpk"], "parent": "", "fork": False}
    keep, dropped = dedup([b, a, c])
    assert [r["full_name"] for r in keep] == ["a/one", "c/three"], [r["full_name"] for r in keep]
    assert dropped[0]["full_name"] == "b/two" and "fork-lineage" in dropped[0]["dup_reason"]
    # F3-a：两个无关的单应用源，只是都收了同一个应用名 —— 不许互杀
    s1 = {"full_name": "s1/x", "apps": 1, "stars": 1, "app_names": ["qwenpaw"], "sigs": ["h/q-2.3.fpk"], "parent": "", "fork": False}
    s2 = {"full_name": "s2/x", "apps": 1, "stars": 2, "app_names": ["qwenpaw"], "sigs": ["h/qwenpaw-2.3.0.fpk"], "parent": "", "fork": False}
    k2, d2 = dedup([s1, s2])
    assert len(k2) == 2 and not d2, ([r["full_name"] for r in k2], d2)
    # F3-b：改名不改包的多应用复制 —— 名字+指纹都重合才判重
    p1 = {"full_name": "p1/x", "apps": 5, "stars": 1, "app_names": ["a", "b", "c"], "sigs": ["h/1.fpk", "h/2.fpk", "h/3.fpk"], "parent": "", "fork": False}
    p2 = {"full_name": "p2/x", "apps": 5, "stars": 2, "app_names": ["a", "b", "c"], "sigs": ["h/1.fpk", "h/2.fpk", "h/9.fpk"], "parent": "", "fork": False}
    k3, d3 = dedup([p1, p2])
    assert len(k3) == 1 and "same-content" in d3[0]["dup_reason"], d3
    # F3-c：同名但包不同（各自打包）不判重
    q1 = {"full_name": "q1/x", "apps": 5, "stars": 1, "app_names": ["a", "b", "c"], "sigs": ["h/1.fpk", "h/2.fpk"], "parent": "", "fork": False}
    q2 = {"full_name": "q2/x", "apps": 5, "stars": 2, "app_names": ["a", "b", "c"], "sigs": ["o/7.fpk", "o/8.fpk"], "parent": "", "fork": False}
    k4, d4 = dedup([q1, q2])
    assert len(k4) == 2 and not d4, (len(k4), d4)
    ok.append("F3 查重：血缘 / 小源不互杀 / 指纹确认复制 / 同名不同包不判重")

    bl = os.path.join(d, "base.txt")
    with open(bl, "w", encoding="utf-8") as f:
        f.write("https://github.com/keep/one\nhttps://github.com/keep/two")
    before = open(bl, encoding="utf-8").read()
    assert write_append(bl, []) == 0 and open(bl, encoding="utf-8").read() == before
    write_append(bl, ["https://github.com/new/x"])
    after = open(bl, encoding="utf-8").read().splitlines()
    assert after[:2] == before.splitlines(), after
    assert after[2] == "https://github.com/new/x" and len(after) == 3
    ok.append("G4 只增不删（既有行零改动）")

    lin = {"x/fork": {"parent": "y/mid"}, "y/mid": {"parent": "z/base"}, "z/base": {"parent": ""}}
    assert ancestors("x/fork", lin) == ["y/mid", "z/base"], ancestors("x/fork", lin)
    assert "y/mid" in ancestors("x/fork", lin) and ancestors("z/base", lin) == []
    assert ancestors("a/b", {}) == []
    assert [fork_verdict(v) for v in (0, 3, -1)] == ["mirror", "review", "unknown"]
    ok.append("F1 血缘上溯（隔代命中基线）/ F2 ahead_by 判定")

    for line in ok:
        print("PASS " + line)
    print("SELFTEST OK (%d 项)" % len(ok))
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description="自建 FnDepot/Moo 应用源自动发现（只增不删，默认 dry-run）")
    ap.add_argument("--baseline", default="combine_list.txt", help="基线清单（只追加到这里）")
    ap.add_argument("--exclude", action="append", default=[], help="仓库级黑名单文件（可多次，优先级最高）")
    ap.add_argument("--whitelist", action="append", default=[], help="优先校验名单（不能豁免黑名单）")
    ap.add_argument("--out-dir", default="", help="报告输出目录")
    ap.add_argument("--token-file", default="~/.moo-gh-token")
    ap.add_argument("--max-pages", type=int, default=2)
    ap.add_argument("--max-validate", type=int, default=120)
    ap.add_argument("--lineage-budget", type=int, default=120, help="血缘 GET /repos 配额上限")
    ap.add_argument("--compare-budget", type=int, default=80, help="compare(ahead_by) 配额上限")
    ap.add_argument("--lineage-cache", default="", help="血缘缓存 JSON（跨次复用，省配额）")
    ap.add_argument("--no-lineage-skip", action="store_true",
                    help="关闭 F1（只用于验证 F2 的镜像判定）")
    ap.add_argument("--no-mirror-check", dest="mirror_check", action="store_false",
                    help="关闭 F2 镜像判定（默认开启）")
    ap.add_argument("--probe", action="store_true", help="HEAD 探测下载地址（死链）")
    ap.add_argument("--probe-limit", type=int, default=40)
    ap.add_argument("--workers", type=int, default=8)
    ap.add_argument("--min-apps", type=int, default=1, help="只收录应用数 >= N 的源（默认 1）")
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument("--api-fallback", action="store_true",
                    help="镜像都不通时用带 token 的 contents API 兜底（吃 core 配额）")
    ap.add_argument("--write", action="store_true", help="把新增行追加写回 --baseline")
    ap.add_argument("--sync-upstream", action="store_true",
                    help="同步链路：上游名单 → 校验 → 只增不删追加（默认 dry-run）")
    ap.add_argument("--upstream", action="append", default=[],
                    help="覆盖上游：name=url 或 name=/local/path（可多次）")
    ap.add_argument("--sync-plan", action="store_true",
                    help="只读：主清单 vs 派生副本的差异计划（不写、不推）")
    ap.add_argument("--target", action="append", default=[],
                    help="派生副本：name=path|url（默认 lists/repo_list.txt）")
    ap.add_argument("--audit-baseline", action="store_true",
                    help="体检已收录清单（F5，只出报告，不改清单）")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args(argv)
    global API_FALLBACK
    API_FALLBACK = bool(args.api_fallback)
    if args.selftest:
        return selftest()
    try:
        if args.sync_plan:
            return sync_plan(args)
        if args.sync_upstream:
            return sync_upstream(args)
        if args.audit_baseline:
            gh = Gh(read_token(args))
            return audit_baseline(gh, load_baseline(args.baseline), args.workers, args.out_dir)
    except Guard as e:
        print("GUARD REFUSED: %s" % e, file=sys.stderr)
        return 3
    try:
        return run(args)
    except Guard as e:
        print("GUARD REFUSED: %s" % e, file=sys.stderr)
        return 3


if __name__ == "__main__":
    sys.exit(main())
