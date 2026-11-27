#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""curate_repo_list.py —— 精选源清单（repo_list.txt）curation pass（只读，出建议）。

背景：repo_list.txt 是 Moo 对外发布的「精选源」清单（2026-11-25 为 105 条），
combine_list.txt 是「全部源」合并清单（212 条）。本脚本对全量做一次完整判定：
  1) 已精选条目的体检：404 / 空源 / 是另一条精选源的 fork / 与其它精选源高度重复 → 建议移出
  2) 未精选条目的候选判定：是否值得进精选
判据（沿用 2026-11-25 定下的精选口径）：
  收录 = 仓库可访问 + 清单可用 + 非黑名单 + 非「精选源的 fork / 纯镜像(ahead_by=0)」
         + 至少 1 个应用名不在精选池里（纯重复源不收）
  不收 = 404 / 空源 / 黑名单 / fork of curated / 纯镜像 / 0 新增
输出：JSON + Markdown 报告；--write-proposal 另写一份拟议清单（不动真实文件）。

用法：
  python3 tools/curate_repo_list.py --out-dir ../_prep_rl_wbj/curate
  python3 tools/curate_repo_list.py --out-dir ../_prep_rl_wbj/curate --write-proposal
"""
import argparse
import collections
import json
import os
import sys
import time
from concurrent.futures import ThreadPoolExecutor

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import discover_sources as ds  # noqa: E402

API = ds.API


def load_urls(path):
    out = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            s = line.split("#", 1)[0].strip()
            if s and ds.repo_of(s):
                out.append(s)
    return out


def _age_days(pushed_at):
    """pushed_at → 距今天数；取不到返回 None。"""
    if not pushed_at:
        return None
    try:
        t = time.mktime(time.strptime(pushed_at[:19], "%Y-%m-%dT%H:%M:%S"))
        return int((time.time() - t) / 86400)
    except Exception:
        return None


def norm_names(names):
    out = set()
    for n in names or []:
        n = ds.norm(n)
        if n and not n.startswith("dir:") and not n.startswith("repo:"):
            out.add(n)
    return out


def expand_dir_names(gh, full, branch, names):
    """目录型源：dir:N 换成真实子目录名（否则判重永远失效）。"""
    if not names or not all(n.startswith("dir:") for n in names):
        return names
    try:
        d = gh.get("%s/repos/%s/contents/apps?ref=%s" % (API, full, branch or "main"), timeout=15)
    except Exception:
        return names
    if not isinstance(d, list):
        return names
    out = [ds.norm(x.get("name")) for x in d
           if isinstance(x, dict) and x.get("type") == "dir" and x.get("name")]
    return out or names


def collect(gh, urls, workers=8):
    """逐仓取元信息 + 清单解析；返回记录列表（含 app_names）。"""

    def one(u):
        full = ds.repo_of(u) or ds.norm(u)
        rec = {"url": u, "repo": full, "status": "bad-url", "apps": 0, "names": [],
               "empty_dl": 0, "dl_keys": 0, "schema": "", "manifest": "", "usable": False,
               "fork": False, "parent": "", "branch": "", "pushed_at": "", "stars": 0}
        if "/" not in full:
            return rec
        try:
            info = gh.get("%s/repos/%s" % (API, full), timeout=15)
        except Exception:
            info = None
        if not info:
            rec["status"] = "repo-404"
            return rec
        rec.update(branch=info.get("default_branch") or "",
                   pushed_at=info.get("pushed_at") or "",
                   fork=bool(info.get("fork")),
                   parent=((info.get("parent") or {}).get("full_name") or "").lower(),
                   stars=info.get("stargazers_count") or 0)
        r = {"full_name": full, "default_branch": rec["branch"],
             "fork": rec["fork"], "stars": rec["stars"]}
        try:
            ds.validate_repo(gh, r)
        except Exception:
            r["usable"], r["usable_reason"] = False, "validate-error"
        names = r.get("app_names") or []
        names = expand_dir_names(gh, full, rec["branch"], [ds.norm(n) for n in names])
        rec.update(status=r.get("usable_reason") or "no-manifest", apps=len(names) or r.get("apps", 0),
                   names=names, empty_dl=r.get("empty_dl", 0),
                   dl_keys=r.get("dl_keys", 0), schema=r.get("schema", ""),
                   manifest=r.get("manifest", ""), usable=bool(r.get("usable")),
                   url_count=r.get("url_count", 0), absolute_urls=r.get("absolute_urls", 0))
        return rec

    with ThreadPoolExecutor(max_workers=max(1, workers)) as pool:
        return list(pool.map(one, urls))


def blacklist_repos(path):
    """黑名单里的仓库级条目（只认 owner/repo 形态）。"""
    out = set()
    if not path or not os.path.exists(path):
        return out
    try:
        d = json.load(open(path, encoding="utf-8"))
    except Exception:
        return out
    for e in (d.get("entries") or []):
        if not isinstance(e, dict):
            continue
        for k in ("repo", "source", "url"):
            v = str(e.get(k) or "")
            r = ds.repo_of(v) or (ds.norm(v) if "/" in v and " " not in v else "")
            if r and "/" in r:
                out.add(r)
    return out


def curated_reasons(r, curated_set, black, base_pool):
    """已精选条目的硬问题（留空 = 保留）。

    fork 只在「应用名已被其它非 fork 精选源全覆盖」时才建议移出 ——
    有独有应用的 fork 仍然要留（否则会丢应用）。
    """
    out = []
    if r["repo"] in black:
        out.append("在仓库级黑名单")
    if r["status"] == "repo-404":
        out.append("仓库 404")
    elif not r["usable"]:
        out.append("清单不可用：%s" % r["status"])
    par = r.get("parent") or ""
    if r["fork"] and par in curated_set and par != r["repo"]:
        mine = norm_names(r["names"])
        if mine and mine <= base_pool:
            out.append("是精选源 %s 的 fork，且应用名已被其它精选源全覆盖" % par)
        else:
            r["note"] = "是 %s 的 fork，但含 %d 个别人没有的应用名 → 保留" % (
                par, len(mine - base_pool))
    return out


def judge(r, curated_set, pool, black, gh, lin, cmp_budget, dup_index=None, stale_days=180):
    """候选判定：drop（硬问题或 0 新增）/ review（需人工）/ keep。

    dup_index 用「精选源逐源应用名集合」识别同源复制（例如把别人 apps.json
    转成 fnpack 的搬运仓 —— 它不是 API fork，只看血缘会漏）。
    """
    hard = []
    if r["repo"] in black:
        hard.append("在仓库级黑名单")
    if r["status"] == "repo-404":
        hard.append("仓库 404")
    elif not r["usable"]:
        hard.append("清单不可用：%s" % r["status"])
    if not hard and r["fork"]:
        anc = ds.ancestors(r["repo"], lin)
        if not anc and r["parent"]:
            anc = [r["parent"]]
        hit = [a for a in anc if a in curated_set]
        if hit:
            hard.append("是精选源 %s 的 fork" % hit[0])
    new = norm_names(r["names"]) - pool
    r["new_names"] = sorted(new)
    dup = ""
    mine = norm_names(r["names"])
    if not hard and len(mine) >= 3:
        for crepo, cnames in (dup_index or []):
            if len(cnames) < 3:
                continue
            rate = ds.overlap(mine, cnames)
            if rate >= 0.85:
                dup = "与精选源 %s 应用名重合 %.0f%%（同源复制）" % (crepo, rate * 100)
                break
    if hard:
        r["verdict"], r["reasons"] = "drop", hard
    elif dup:
        r["verdict"], r["reasons"] = "drop", [dup]
    elif r.get("schema") == "repo_list":
        r["verdict"], r["reasons"] = "review", ["聚合源（repo_list.txt 指向其它源），需人工确认"]
    elif r.get("schema") == "directory":
        r["verdict"], r["reasons"] = "review", ["目录型源（apps/ 子目录 %d 个），需人工确认" % r["apps"]]
    elif not new:
        r["verdict"], r["reasons"] = "drop", ["0 新增（应用名全部已在精选池）"]
    elif r["fork"]:
        ahead = -1
        if r["parent"] and cmp_budget[0] > 0:
            cmp_budget[0] -= 1
            ahead = ds.compare_ahead(gh, r["repo"], r["parent"], lin, r["branch"])
        if ahead == 0:
            r["verdict"], r["reasons"] = "drop", ["纯镜像 fork（ahead_by=0）"]
        else:
            r["verdict"] = "review"
            r["reasons"] = ["fork（ahead_by=%s）但有 %d 个精选池没有的应用名" % (ahead, len(new))]
    else:
        age = _age_days(r["pushed_at"])
        if age is not None and stale_days and age >= stale_days:
            r["verdict"] = "review"
            r["reasons"] = ["%d 个应用名不在精选池，但最近提交 %d 天前（≥%d 天）"
                            % (len(new), age, stale_days)]
        else:
            r["verdict"], r["reasons"] = "keep", ["%d 个应用名不在精选池" % len(new)]
    return r


def plan_additions(cands, pool):
    """按新增数从多到少收；后收的源与已收源重复部分不计入增量。"""
    pool = set(pool)
    cands = sorted(cands, key=lambda r: (-len(r["new_names"]), r["repo"]))
    added = []
    for r in cands:
        inc = norm_names(r["names"]) - pool
        if not inc:
            r["verdict"], r["reasons"] = "drop", r["reasons"] + ["与其它待收源重复"]
            continue
        r["inc_names"], r["inc_count"] = sorted(inc), len(inc)
        pool |= inc
        added.append(r)
    return added, pool


def write_report(out_dir, meta, keep, drop_cur, drop_cand, review, add, orphan):
    os.makedirs(out_dir, exist_ok=True)
    ts = time.strftime("%Y%m%d-%H%M%S")
    rep = {"generated_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "meta": meta,
           "counts": {"full_list": meta["total_full"], "curated_now": meta["curated_now"],
                      "keep": len(keep), "drop_curated": len(drop_cur),
                      "drop_candidate": len(drop_cand), "review": len(review),
                      "add": len(add), "proposal_add_only": len(keep) + len(drop_cur) + len(add),
                      "proposal_clean": len(keep) + len(add),
                      "orphan_names": len(orphan)},
           "keep": [{"repo": r["repo"], "apps": r["apps"], "note": r.get("note", "")} for r in keep],
           "drop_curated": [{"repo": r["repo"], "reasons": r["reasons"], "apps": r["apps"],
                             "status": r["status"]} for r in drop_cur],
           "drop_candidate": [{"repo": r["repo"], "reasons": r["reasons"], "apps": r["apps"],
                               "status": r["status"]} for r in drop_cand],
           "review": [{"repo": r["repo"], "reasons": r["reasons"], "apps": r["apps"],
                       "new_names": r.get("new_names", [])} for r in review],
           "add": [{"repo": r["repo"], "url": r["url"], "apps": r["apps"],
                    "inc_count": r.get("inc_count", 0), "inc_names": r.get("inc_names", []),
                    "stars": r["stars"], "pushed_at": r["pushed_at"]} for r in add],
           "orphan_names": orphan}
    jp = os.path.join(out_dir, "curation_%s.json" % ts)
    with open(jp, "w", encoding="utf-8") as f:
        json.dump(rep, f, ensure_ascii=False, indent=1)

    L = ["# 精选源清单 curation 报告（只读，未改动任何清单）", "",
         "- 生成时间：%s" % rep["generated_at"],
         "- 全量清单 `%s`：%d 条" % (meta["full_list"], meta["total_full"]),
         "- 现有精选 `%s`：%d 条" % (meta["curated_list"], meta["curated_now"]),
         "- 现有精选判定：保留 %d / **建议移出 %d**" % (len(keep), len(drop_cur)),
         "- 候选判定：**建议新增 %d** / 待人工 %d / 不收 %d" % (len(add), len(review), len(drop_cand)),
         "- 拟议条数：只增不减 **%d 条**；含移出 **%d 条**" % (
             len(keep) + len(drop_cur) + len(add), len(keep) + len(add)), "",
         "## 一、建议移出精选（%d）" % len(drop_cur), ""]
    if drop_cur:
        L += ["| 源 | 应用数 | 状态 | 原因 |", "|---|---|---|---|"]
        L += ["| `%s` | %d | %s | %s |" % (r["repo"], r["apps"], r["status"], "；".join(r["reasons"]))
              for r in drop_cur]
    else:
        L.append("无")
    L += ["", "## 二、候选不收（%d，按原因归类）" % len(drop_cand), ""]
    if drop_cand:
        grp = collections.OrderedDict()
        for r in drop_cand:
            k = r["reasons"][0].split("（")[0]
            grp.setdefault(k, []).append(r["repo"])
        for k, v in sorted(grp.items(), key=lambda x: -len(x[1])):
            L.append("- **%s**（%d）：%s" % (k, len(v), "、".join("`%s`" % x for x in v)))
    else:
        L.append("无")
    L += ["", "## 三、待人工确认（%d）" % len(review), ""]
    if review:
        L += ["| 源 | 应用数 | 新应用名（精选池没有的） | 原因 |", "|---|---|---|---|"]
        for r in review:
            nm = "、".join((r.get("new_names") or [])[:5]) or "-"
            L.append("| `%s` | %d | %s | %s |" % (r["repo"], r["apps"], nm, "；".join(r["reasons"])))
    else:
        L.append("无")
    L += ["", "## 四、建议新增（%d）" % len(add), ""]
    if add:
        L += ["| 源 | 应用数 | 增量应用数 | 新增应用名 | stars | 最近提交 |", "|---|---|---|---|---|---|"]
        for r in add:
            nm = "、".join(r.get("inc_names", [])[:6]) + ("…" if r.get("inc_count", 0) > 6 else "")
            L.append("| `%s` | %d | %d | %s | %d | %s |" % (
                r["repo"], r["apps"], r.get("inc_count", 0), nm, r["stars"], (r["pushed_at"] or "")[:10]))
    else:
        L.append("无")
    L += ["", "## 五、移出后可能失去的应用名（%d，提示用）" % len(orphan), "",
          ("、".join(orphan[:100]) + ("…" if len(orphan) > 100 else "")) if orphan else "无", "",
          "## 六、口径", "",
          "- 收录 = 仓库可访问 + 清单可用 + 非黑名单 + 非精选源的 fork/纯镜像 + 至少 1 个应用名不在精选池",
          "- 移出 = 404 / 清单不可用 / 黑名单 / fork 且应用名已被其它精选源全覆盖；**只出建议，不自动改清单**",
          "- 应用名按小写归一；目录型与 repo_list 聚合源的特殊名字（dir:/repo:）不参与判重"]
    mp = os.path.join(out_dir, "CURATION_REPORT.md")
    with open(mp, "w", encoding="utf-8") as f:
        f.write("\n".join(L) + "\n")
    return jp, mp


def write_proposal(path, curated_path, add, keep=None):
    """拟议清单：keep=None 只增不减；keep 给定时按 keep 过滤掉建议移出的源。"""
    raw = open(curated_path, encoding="utf-8").read().rstrip("\n")
    if keep is None:
        base = raw
    else:
        keepset = {r["repo"] for r in keep}
        base = "\n".join(
            l for l in raw.split("\n")
            if (not l.strip().lower().startswith("http")) or ds.repo_of(l.strip()) in keepset)
        base = base.rstrip("\n")
    blk = ["", "# %s 追加：curation 判定「有实质新增」的 %d 条（非精选源 fork、非纯重复、非黑名单）"
           % (time.strftime("%Y-%m-%d"), len(add))]
    blk += ["https://github.com/" + r["repo"] for r in add]
    with open(path, "w", encoding="utf-8") as f:
        f.write(base + "\n" + "\n".join(blk) + "\n")


def main(argv=None):
    ap = argparse.ArgumentParser(description="精选源清单 curation（只读，出建议）")
    ap.add_argument("--full", default="combine_list.txt", help="全量清单（只读）")
    ap.add_argument("--curated", default="lists/repo_list.txt", help="现有精选清单（只读）")
    ap.add_argument("--blacklist", default="blacklist.json")
    ap.add_argument("--out-dir", default="")
    ap.add_argument("--workers", type=int, default=8)
    ap.add_argument("--token-file", default="~/.moo-gh-token")
    ap.add_argument("--compare-budget", type=int, default=60)
    ap.add_argument("--write-proposal", action="store_true", help="另写拟议清单（不动真实文件）")
    args = ap.parse_args(argv)

    ds.API_FALLBACK = True  # 镜像不通时用 contents API 兜底（吃 core 配额）
    token = ds.read_token(args)
    if not token:
        print("缺少 token（$GH_TOKEN 或 --token-file）")
        return 4
    gh = ds.Gh(token)
    full, curated = load_urls(args.full), load_urls(args.curated)
    cset = {ds.repo_of(u) for u in curated}
    fset = {ds.repo_of(u) for u in full}
    print("全量 %d / 精选 %d / 候选 %d" % (len(fset), len(cset), len(fset - cset)))

    rows = collect(gh, full, args.workers)
    for r in rows:
        r["curated"] = r["repo"] in cset
    lin = {r["repo"]: {"parent": r["parent"], "fork": r["fork"],
                       "default_branch": r["branch"]} for r in rows}
    pool = set()
    for r in rows:
        if r["curated"] and r["usable"]:
            pool |= norm_names(r["names"])
    print("精选池应用名 %d 个" % len(pool))

    black = blacklist_repos(args.blacklist)
    base_pool = set()
    for r in rows:
        if r["curated"] and r["usable"] and not r["fork"]:
            base_pool |= norm_names(r["names"])
    dup_index = [(r["repo"], norm_names(r["names"])) for r in rows
                 if r["curated"] and r["usable"] and not r["fork"]]
    keep, drop_cur, drop_cand, review, cand = [], [], [], [], []
    for r in rows:
        if r["curated"]:
            r["reasons"] = curated_reasons(r, cset, black, base_pool)
            (drop_cur if r["reasons"] else keep).append(r)
        else:
            judge(r, cset, pool, black, gh, lin, [args.compare_budget], dup_index)
            {"keep": cand, "review": review, "drop": drop_cand}[r["verdict"]].append(r)

    add, _ = plan_additions(cand, pool)
    drop_cand += [r for r in cand if r["verdict"] == "drop"]
    live = set()
    for r in keep + add:
        live |= norm_names(r["names"])
    orphan = sorted(set().union(*[norm_names(r["names"]) for r in rows if r["curated"]]) - live)

    meta = {"full_list": args.full, "curated_list": args.curated, "total_full": len(fset),
            "curated_now": len(cset)}
    print("现有精选：保留 %d / 建议移出 %d" % (len(keep), len(drop_cur)))
    print("候选：建议新增 %d / 待人工 %d / 不收 %d" % (len(add), len(review), len(drop_cand)))
    if args.out_dir:
        jp, mp = write_report(args.out_dir, meta, keep, drop_cur, drop_cand, review, add, orphan)
        print("report:", jp)
        print("report:", mp)
        if args.write_proposal:
            pp = os.path.join(args.out_dir, "repo_list.proposed.add.txt")
            write_proposal(pp, args.curated, add, keep=None)
            print("proposal(只增不减):", pp)
            pp2 = os.path.join(args.out_dir, "repo_list.proposed.clean.txt")
            write_proposal(pp2, args.curated, add, keep=keep)
            print("proposal(含移出):", pp2)
    print("只读模式：未改任何清单、未提交、未推送")
    return 0


if __name__ == "__main__":
    sys.exit(main())
