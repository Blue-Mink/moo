#!/usr/bin/env python3
"""moo_invite.py — 生成「邀请开发者加 moo.json」的索引 + 一键链接 + 评论正文。

用法示例：
  python3 tools/moo_invite.py --owner yujian0815 --repo fnmedia-ext \
    --appname fnmedia-ext --display-name "飞牛影视在线扩展" \
    --version 0.2.43 --fpk-url https://github.com/OWNER/REPO/releases/download/v0.2.43/x.fpk \
    --size 132063010 --sha256 815bf... --platform all --categories 影音娱乐 \
    --outdir /tmp/invite_out

产物：moo.json / oneclick.txt / comment.md（含折叠 JSON 兜底）
"""
import argparse, json, os, urllib.parse

def build(args):
    app = {
        "display_name": args.display_name,
        "app_type": args.app_type,
        "platform": [args.platform],
        "categories": [c.strip() for c in args.categories.split(",") if c.strip()],
        "license": args.license,
        "author": args.owner,
        "author_url": f"https://github.com/{args.owner}",
        "homepage": f"https://github.com/{args.owner}/{args.repo}",
        "bug_report_url": f"https://github.com/{args.owner}/{args.repo}/issues",
        "icon_url": args.icon_url or f"https://raw.githubusercontent.com/{args.owner}/{args.repo}/{args.branch}/{args.icon_path}",
        "readme_url": f"https://raw.githubusercontent.com/{args.owner}/{args.repo}/{args.branch}/README.md",
        "desc": args.desc,
        "releases": {
            args.version: {
                "changelog": args.changelog,
                "packages": {
                    args.platform: {
                        "download_url": args.fpk_url,
                        "sha256": args.sha256,
                        "size": args.size,
                    }
                },
            }
        },
    }
    if args.min_fnos:
        app["min_fnos"] = args.min_fnos
    if args.install_type:
        app["install_type"] = args.install_type
    return {
        "schema_version": "moo",
        "source_info": {
            "name": f"{args.display_name}（{args.appname}）",
            "author": args.owner,
            "homepage": f"https://github.com/{args.owner}/{args.repo}",
            "description": args.source_desc or f"{args.display_name} 官方应用源。",
        },
        "apps": {args.appname: app},
    }

def main():
    p = argparse.ArgumentParser()
    p.add_argument("--owner", required=True)
    p.add_argument("--repo", required=True)
    p.add_argument("--appname", required=True)
    p.add_argument("--display-name", required=True)
    p.add_argument("--version", required=True)
    p.add_argument("--fpk-url", required=True)
    p.add_argument("--size", type=int, required=True, help="字节")
    p.add_argument("--sha256", required=True)
    p.add_argument("--platform", default="all")
    p.add_argument("--categories", default="实用效率")
    p.add_argument("--app-type", default="fpk", choices=["fpk", "native", "docker"])
    p.add_argument("--license", default="MIT")
    p.add_argument("--min-fnos", default="")
    p.add_argument("--install-type", default="")
    p.add_argument("--changelog", default="")
    p.add_argument("--desc", default="")
    p.add_argument("--source-desc", default="")
    p.add_argument("--icon-path", default="package/ICON_256.PNG")
    p.add_argument("--icon-url", default="")
    p.add_argument("--branch", default="main")
    p.add_argument("--outdir", default=".")
    a = p.parse_args()

    doc = build(a)
    body = json.dumps(doc, ensure_ascii=False, indent=2)
    os.makedirs(a.outdir, exist_ok=True)
    open(os.path.join(a.outdir, "moo.json"), "w", encoding="utf-8").write(body + "\n")

    url = (f"https://github.com/{a.owner}/{a.repo}/new/{a.branch}"
           f"?filename=moo.json&value={urllib.parse.quote(body, safe='')}")
    open(os.path.join(a.outdir, "oneclick.txt"), "w", encoding="utf-8").write(url + "\n")

    comment = f"""@{(a.owner)} 感谢回复 🙏 那我按你说的把收录这件事推进起来。先说清两件事，再给你一条**点开就能把索引写进仓库**的链接。

**一、索引必须由你自己提交，我不走 PR 代劳。**

开 PR 得先在我账号下建一个你仓库的 fork——那会在 GitHub 上留下「Blue-Mink/{a.repo} 是 {a.owner}/{a.repo} 的 fork」这种血缘关系，反而把来源搞浑。所以我改成在这里给你现成的东西：你自己点一下提交，索引从头到尾都在你的仓库里，我这边一个字都不碰。

**二、只需要一个文件：仓库根目录的 `moo.json`。**

用的是 Moo 原生协议（`schema_version: "moo"`），`apps` 里只有一条：键 `{a.appname}`（与你包内 `manifest` 完全一致），指向 `{a.version}` 的包：

- `sha256` = `{a.sha256}`
- `size` = `{a.size}` 字节（约 {a.size/1e6:.2f} MB）
- 用的是 `releases.<版本>.packages` 多版本写法；以后发版你只在 `releases` 里加一个版本节点，其余字段不用动。

**一键添加**（文件名和内容都已预填，点开 → 滚到底点 Commit 就完事）：

[＋ 创建 moo.json（内容已填好）]({url})

**后续我会做的两件事**（你索引放好我就办）：把你的仓库加入 Moo 的**精选应用源列表**；在 Moo 的**发现页推荐位**加上这个项目。之后你照常发 Release 即可，Moo 会自动识别新版本并给用户「有更新」提示。

如果哪一步不便（比如不想在根目录放、或想换个文件名），直接说，我们换个办法。祝迭代顺利。

—— Blue-Mink（Moo 作者）

---

<details>
<summary>备用：moo.json 全文（链接内容不全时用这份）</summary>

```json
{body}
```

</details>
"""
    open(os.path.join(a.outdir, "comment.md"), "w", encoding="utf-8").write(comment)
    print(f"moo.json {len(body)}B | 一键链接 {len(url)} 字符 | 评论 {len(comment)} 字符")
    print(f"产物目录：{os.path.abspath(a.outdir)}")

if __name__ == "__main__":
    main()
