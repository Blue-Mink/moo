#!/usr/bin/env python3
"""校验 Moo Mate release 资产与 moo.json 索引一致（只读：不构建、不上传、不覆盖）。

判定项：moo.json `apps.moo-mate.releases.<ver>.packages` 里的每个架构
  - release 里存在同名 .fpk 资产，size 与索引逐字相符；
  - 资产 digest（服务端 sha256）与索引 sha256 一致；
  - 同名 `.sha256` 侧车存在；
  - 同名 `.minisig` 签名存在（0.2.35 起自更新强制验签，缺了客户端会拒绝升级）。
任一不符 → 退出码 1（流水线红），便于发现「资产被覆盖 / 索引写错 / 漏签名」。

职责边界（RELEASE_GATE S2）：本脚本只做「索引 ↔ 资产枚举 + 三件套在场」，
不涉及下载 / 验签 / 解包。Moo 本体那套「客户端同款验收」规则的唯一实现是
Go 侧 `internal/release`（`VerifyAssets` 远端 / `VerifyLocal` 本地），由
`moo-release` 二进制执行；mate 的验签实现是客户端 `cmd/moo-mate/selfupdate.go`
（内嵌公钥），此处刻意不复制那份逻辑，避免两处规则漂移。
"""
import base64
import json
import os
import re
import sys
import time
import urllib.request

REPO = os.environ["REPO"]
TAG = os.environ["TAG"]
VER = os.environ["VER"]
TOK = os.environ["GH_TOKEN"]
API = f"https://api.github.com/repos/{REPO}"
HDR = {"Authorization": f"Bearer {TOK}", "Accept": "application/vnd.github+json",
       "X-GitHub-Api-Version": "2022-11-28"}


def get(path):
    req = urllib.request.Request(API + path, headers=HDR)
    with urllib.request.urlopen(req, timeout=60) as r:
        return json.load(r)


rel = None
for i in range(30):  # 发布机是「建 release → 推 tag」，这里最多等 5 分钟
    try:
        rel = get(f"/releases/tags/{TAG}")
        break
    except Exception as e:  # noqa: BLE001
        print(f"[wait] {TAG} 暂不可读（{e}），10s 后重试（{i + 1}/30）", flush=True)
        time.sleep(10)
if not rel:
    sys.exit(f"FAIL: release {TAG} 不存在")

def pkgs_of(idx, ver):
    return ((idx.get("apps", {}).get("moo-mate", {}).get("releases", {}).get(ver) or {})
            .get("packages") or {})


def main_branch_pkgs():
    """tag 树索引未跟版时回落到 main 分支索引（索引后置属发布设计）。"""
    try:
        req = urllib.request.Request(API + "/contents/moo.json?ref=main", headers=HDR)
        with urllib.request.urlopen(req, timeout=60) as r:
            blob = json.load(r)
        return pkgs_of(json.loads(base64.b64decode(blob["content"])), VER)
    except Exception as e:  # noqa: BLE001
        print(f"[warn] 读 main 分支 moo.json 失败：{e}")
        return {}


def summary(line):
    p = os.environ.get("GITHUB_STEP_SUMMARY")
    if p:
        try:
            with open(p, "a", encoding="utf-8") as f:
                f.write(line + "\n")
        except OSError:
            pass


idx = json.load(open("moo.json", encoding="utf-8"))
pkgs = pkgs_of(idx, VER)
src = "tag 树 moo.json"
if not pkgs:
    pkgs = main_branch_pkgs()
    if pkgs:
        src = "main 分支 moo.json（tag 树索引尚未跟版）"

assets = {a["name"]: a for a in rel["assets"]}
print("release 资产：")
for n in sorted(assets):
    print(f"  {n:42s} {assets[n]['size']:>10d}")

if not pkgs:
    # 索引后置（RELEASE_GATE F）：tag 推送时 tag 树里的 moo.json 按设计仍是上一版，
    # 若 main 也还没跟版则「无索引可比」——此时不做索引比对，退化为资产自洽检查：
    # 三件套齐全 + `.sha256` 侧车与服务端 digest 一致。索引一致性由索引提交后的
    # `prepush_check.py --mate` 与夜间 `mate-watch.yml`（客户端同款，含验签）覆盖。
    msg = (f"moo.json（tag 树与 main）均无 apps.moo-mate.releases.{VER}.packages"
           " → 索引后置属设计，跳过索引比对，仅校验资产自洽")
    print("::notice::" + msg)
    summary(f"**索引未跟版（属设计）**：{msg}。")
    fpks = [n for n in assets if n.endswith(".fpk")]
    bad = []
    if not fpks:
        bad.append("release 没有任何 .fpk 资产")
    for n in sorted(fpks):
        m = re.match(r"^moo-mate_%s_(x86|arm)\.fpk$" % re.escape(VER), n)
        if not m:
            bad.append(f"资产名不符合 moo-mate_{VER}_<x86|arm>.fpk 约定：{n}")
        if n + ".sha256" not in assets:
            bad.append(f"缺侧车 {n}.sha256")
        if n + ".minisig" not in assets:
            bad.append(f"缺签名 {n}.minisig（0.2.35+ 客户端会拒绝自更新）")
        dg = assets[n].get("digest") or ""
        if dg and n + ".sha256" in assets:
            try:
                u = assets[n + ".sha256"]["browser_download_url"]
                with urllib.request.urlopen(
                        urllib.request.Request(u, headers={"User-Agent": "moo-ci"}), timeout=60) as r:
                    side = r.read().decode("utf-8", "replace").split()[0].strip().lower()
                if side != dg.split(":", 1)[-1].lower():
                    bad.append(f"{n}: .sha256 侧车 {side[:12]}… != 服务端 digest {dg[:16]}…")
            except Exception as e:  # noqa: BLE001
                print(f"[warn] 读侧车 {n}.sha256 失败：{e}")
    if bad:
        print("\n".join("FAIL: " + b for b in bad))
        summary("**资产自洽检查失败**：" + "；".join(bad))
        sys.exit(1)
    print(f"OK: {TAG} 资产自洽（{len(fpks)} 个 FPK，侧车/签名齐全；索引尚未跟版，"
          "一致性待索引提交后由 prepush_check / mate-watch 覆盖）")
    summary(f"**OK（资产自洽）**：{TAG} 的 {len(fpks)} 个 FPK + 侧车/签名齐全。")
    sys.exit(0)

print(f"索引来源：{src}")
print("索引登记架构：" + ", ".join(sorted(pkgs)))

bad = []
for arch, spec in sorted(pkgs.items()):
    name = spec["download_url"].rsplit("/", 1)[-1]
    a = assets.get(name)
    if not a:
        bad.append(f"{arch}: 缺资产 {name}")
        continue
    if a["size"] != spec["size"]:
        bad.append(f"{arch}: {name} size {a['size']} != 索引 {spec['size']}")
    digest = a.get("digest") or ""
    if digest and digest != f"sha256:{spec['sha256']}":
        bad.append(f"{arch}: {name} digest {digest} != sha256:{spec['sha256']}")
    if not digest:
        print(f"[warn] {arch}: 服务端未返回 digest，跳过 sha256 比对（仅比 size）")
    if name + ".sha256" not in assets:
        bad.append(f"{arch}: 缺侧车 {name}.sha256")
    # 0.2.35 起 mate 自更新强制 minisign 验签（fail-closed）：
    # 缺 .minisig 时客户端会拒绝升级，等于发布无效，所以这里必须拦。
    if name + ".minisig" not in assets:
        bad.append(f"{arch}: 缺签名 {name}.minisig（0.2.35+ 客户端会拒绝自更新）")

if bad:
    print("\n".join("FAIL: " + b for b in bad))
    sys.exit(1)
print(f"OK: {TAG} 的 {len(pkgs)} 个架构资产与 moo.json 索引逐字一致")
