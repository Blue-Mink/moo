#!/usr/bin/env python3
"""校验 Moo Mate release 资产与 moo.json 索引一致（只读：不构建、不上传、不覆盖）。

判定项：moo.json `apps.moo-mate.releases.<ver>.packages` 里的每个架构
  - release 里存在同名 .fpk 资产，size 与索引逐字相符；
  - 资产 digest（服务端 sha256）与索引 sha256 一致；
  - 同名 `.sha256` 侧车存在。
任一不符 → 退出码 1（流水线红），便于发现「资产被覆盖 / 索引写错」。
"""
import json
import os
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

idx = json.load(open("moo.json", encoding="utf-8"))
pkgs = ((idx.get("apps", {}).get("moo-mate", {}).get("releases", {}).get(VER) or {})
        .get("packages") or {})
if not pkgs:
    sys.exit(f"FAIL: moo.json 缺 apps.moo-mate.releases.{VER}.packages")

assets = {a["name"]: a for a in rel["assets"]}
print("release 资产：")
for n in sorted(assets):
    print(f"  {n:42s} {assets[n]['size']:>10d}")
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

if bad:
    print("\n".join("FAIL: " + b for b in bad))
    sys.exit(1)
print(f"OK: {TAG} 的 {len(pkgs)} 个架构资产与 moo.json 索引逐字一致")
