#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""check_refs.py — 「被引用即须入库」的静态断言（2026-10-31，P0 治本）

为什么需要：0.6.380 推前发现 4 个 CI 脚本 + 3 个 //go:embed 数据文件 + 1 个
eslint flat config 全是 untracked。它们被代码/流水线**真实引用**，但没有任何
检查拦着，一旦漏 git add，只会在远端炸（CI 找不到脚本 / 编译期 embed 缺文件）。
靠人记清单必然出错，所以把这件事变成机器判定。

检查三类「确定被引用」的关系，判不了的一律 SKIP（fail-open）——
宁可漏报也不误伤，避免把发布流程改坏：
  1) //go:embed 引用的文件（被 .gitignore 的构建产物如 web/dist 除外）
  2) workflow 里 `bash|sh|python3 .github/scripts/…` 引用的脚本
  3) 前端 lint/build 配置（package.json 有 lint → eslint flat config 必须入库）

退出码：0 全部满足；1 存在「被引用但不存在 / 未入库」。无参数。
"""
import os
import re
import subprocess
import sys
import glob

ROOT = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                      capture_output=True, text=True).stdout.strip() or "."
FAILS, SKIPS, OKS = [], [], []


def sh(*args):
    r = subprocess.run(args, cwd=ROOT, capture_output=True, text=True)
    return r.returncode, (r.stdout or "").strip(), (r.stderr or "").strip()


def tracked(path):
    """relative to ROOT"""
    rc, _, _ = sh("git", "ls-files", "--error-unmatch", "--", path)
    return rc == 0


def ignored(path):
    rc, _, _ = sh("git", "check-ignore", "-q", "--", path)
    if rc == 0:
        return True
    # 目录型忽略规则（如 `dist/`）对**不存在**的目录不生效：CI 全新 checkout 里没有
    # web/dist，`git check-ignore -q -- web/dist` 会返回「未忽略」→ refs 假失败
    # （2026-10-31 实测：本地有构建产物时通过、CI 必红）。补一次带尾斜杠的探测。
    rc2, _, _ = sh("git", "check-ignore", "-q", "--", path.rstrip("/") + "/")
    return rc2 == 0


def rel(p):
    return os.path.relpath(p, ROOT).replace(os.sep, "/")


def ok(msg):
    OKS.append(msg)
    print("[refs] ✓ " + msg)


def bad(msg, detail=""):
    FAILS.append(msg)
    print("[refs] ✗ " + msg + (("  → " + detail) if detail else ""))


def skip(msg):
    SKIPS.append(msg)
    print("[refs] – " + msg)


EMBED_RE = re.compile(r"^\s*//go:embed\s+(.+?)\s*$")


def go_files():
    rc, out, _ = sh("git", "ls-files", "-co", "--exclude-standard", "--", "*.go")
    if rc != 0:
        return []
    return [f for f in out.splitlines() if f.strip() and not f.startswith("vendor/")]


def check_embeds():
    """1) //go:embed 引用的每个路径都必须真实存在且已入库。"""
    seen = 0
    for gf in go_files():
        try:
            lines = open(os.path.join(ROOT, gf), encoding="utf-8", errors="ignore").read().splitlines()
        except OSError:
            continue
        base = os.path.dirname(os.path.join(ROOT, gf))
        for line in lines:
            m = EMBED_RE.match(line)
            if not m:
                continue
            seen += 1
            raw = m.group(1).strip().strip('"`')
            raw = raw[4:] if raw.startswith("all:") else raw
            for pat in raw.split():
                target = os.path.normpath(os.path.join(base, pat))
                trel = rel(target)
                if glob.has_magic(pat):
                    hits = glob.glob(target)
                    if not hits:
                        bad("%s: go:embed 通配符无匹配 (%s)" % (gf, pat))
                    continue
                if not os.path.exists(target):
                    if ignored(trel):
                        skip("%s: go:embed %s 是构建产物（已忽略）" % (gf, trel))
                    else:
                        bad("%s: go:embed 引用不存在 (%s)" % (gf, trel))
                    continue
                if os.path.isdir(target):
                    rc, out, _ = sh("git", "ls-files", "--", trel)
                    if out.strip():
                        continue
                    if ignored(trel):
                        skip("%s: go:embed %s 是构建产物目录（已忽略）" % (gf, trel))
                    else:
                        bad("%s: go:embed 目录 %s 内没有任何已入库文件" % (gf, trel))
                    continue
                if not tracked(trel):
                    if ignored(trel):
                        skip("%s: go:embed %s 被忽略" % (gf, trel))
                    else:
                        bad("%s: go:embed 引用的 %s 未入库（git add 漏了）" % (gf, trel))
    if seen == 0:
        skip("未发现 //go:embed 指令")
    else:
        ok("go:embed 引用检查（%d 条指令）" % seen)


SCRIPT_REF_RE = re.compile(r"(?:^|[\s&|;(])(?:bash|sh|python3?)\s+((?:\./)?\.github/scripts/[A-Za-z0-9_./-]+)")


def check_workflows():
    """2) workflow 与门禁脚本里调用的仓库内脚本必须存在且已入库。

    扫描源 = .github/workflows/*.yml + .github/scripts/*.sh：
    后者是为了覆盖「门禁脚本互相调用」（moo-gate.sh 里 `bash .github/scripts/sanitize.sh`），
    否则 sanitize.sh 漏 add 时只会在 CI 执行到那一步才炸。
    """
    sources = []
    wfdir = os.path.join(ROOT, ".github", "workflows")
    if os.path.isdir(wfdir):
        sources += [(n, os.path.join(wfdir, n)) for n in sorted(os.listdir(wfdir))
                    if n.endswith((".yml", ".yaml"))]
    sdir = os.path.join(ROOT, ".github", "scripts")
    if os.path.isdir(sdir):
        sources += [(n, os.path.join(sdir, n)) for n in sorted(os.listdir(sdir))
                    if n.endswith(".sh")]
    if not sources:
        skip("无 workflow / 门禁脚本可扫")
        return
    checked = 0
    for name, path in sources:
        try:
            lines = open(path, encoding="utf-8", errors="ignore").read().splitlines()
        except OSError:
            continue
        self_rel = rel(path)
        refs = set()
        for line in lines:
            # 注释行（含脚本头部的「用法」示例）不是真实调用，跳过——
            # 否则 moo-gate.sh 头注释里的 `bash .github/scripts/moo-gate.sh` 会被当成自引用。
            if line.lstrip().startswith("#"):
                continue
            refs.update(SCRIPT_REF_RE.findall(line))
        for ref in refs:
            # 注意：不能用 lstrip("./")——它是按字符集剥离，会把 ".github" 的点也吃掉
            ref = ref[2:] if ref.startswith("./") else ref
            if ref == self_rel:
                continue
            checked += 1
            if not os.path.exists(os.path.join(ROOT, ref)):
                bad("%s 引用不存在的脚本 %s" % (name, ref))
            elif not tracked(ref):
                bad("%s 引用的 %s 未入库（CI 上会找不到）" % (name, ref))
    if checked == 0:
        skip("workflow 未引用 .github/scripts 下脚本")
    else:
        ok("workflow 脚本引用检查（%d 处）" % checked)


def check_frontend_config():
    """3) package.json 里 lint/build 用到的配置必须入库（否则 CI 首次跑就红）。"""
    pkg = os.path.join(ROOT, "frontend", "package.json")
    if not os.path.isfile(pkg):
        skip("无 frontend/package.json")
        return
    import json as _json
    try:
        scripts = (_json.load(open(pkg, encoding="utf-8")).get("scripts") or {})
    except Exception as e:  # noqa: BLE001
        skip("frontend/package.json 解析失败（%s）" % e)
        return
    lint = scripts.get("lint", "")
    if "eslint" in lint:
        cands = [p for p in ("frontend/eslint.config.js", "frontend/eslint.config.mjs",
                             "frontend/eslint.config.cjs") if os.path.exists(os.path.join(ROOT, p))]
        if not cands:
            bad("frontend 有 lint=eslint 但没有任何 eslint.config.*（ESLint 9 会直接报错）")
        elif not any(tracked(p) for p in cands):
            bad("eslint flat config %s 未入库（CI lint 会失败）" % cands[0])
        else:
            ok("eslint flat config 已入库（%s）" % [p for p in cands if tracked(p)][0])
    else:
        skip("frontend 未配置 eslint lint")


def check_generated_versions():
    """4) 生成物记录的版本必须与 manifest 一致（防止忘记重跑生成脚本）。

    目前唯一生成物 = docs/badges/*.png（README 徽章）。PNG 读不出版本，故由
    gen_badges.py 同时写 docs/badges/fpk.version 侧车；这里断言它与 fnos/manifest
    的 version 相同。侧车不存在时 SKIP（fail-open，不误伤旧状态）。
    """
    man = os.path.join(ROOT, "fnos", "manifest")
    side = os.path.join(ROOT, "docs", "badges", "fpk.version")
    if not os.path.isfile(side):
        skip("无 docs/badges/fpk.version（徽章未带版本侧车）")
        return
    if not os.path.isfile(man):
        skip("无 fnos/manifest")
        return
    m = re.search(r"^version\s*=\s*(\S+)", open(man, encoding="utf-8", errors="ignore").read(), re.M)
    if not m:
        skip("fnos/manifest 解析不到 version")
        return
    want, got = m.group(1), open(side, encoding="utf-8", errors="ignore").read().strip()
    if want != got:
        bad("徽章版本落后：docs/badges/fpk.version=%s，manifest=%s（重跑 "
            "python3 docs/badges/gen_badges.py %s）" % (got, want, want))
    else:
        ok("徽章版本与 manifest 一致（%s）" % want)


def main():
    print("[refs] 仓库：%s" % ROOT)
    check_embeds()
    check_workflows()
    check_frontend_config()
    check_generated_versions()
    print("\n[refs] ============ 汇总 ============")
    print("[refs] 通过 %d / 跳过 %d / 失败 %d" % (len(OKS), len(SKIPS), len(FAILS)))
    if FAILS:
        print("[refs] 有「被引用但未入库/不存在」的项，先补齐再推：")
        for f in FAILS:
            print("  - " + f)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
