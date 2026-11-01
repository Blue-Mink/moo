# 邀请开发者做 Moo 应用源 · issue/评论模板

> 来源：2026-11-01 给 yujian0815/fnmedia-ext 的正式版文案
> （https://github.com/yujian0815/fnmedia-ext/issues/12#issuecomment-6553395639）。
> 以后发现「没做 FnDepot / Moo 索引」的新项目，按本模板提交，**不代写、不代提 PR**。

## 使用步骤

1. 先在对方仓库开一个 issue（标题：`邀请：把「<应用名>」做成一个应用源（仓库根目录放一份索引即可）`），正文用下面「开场白」段。
2. 对方回复后（或已有 issue 可直接评论），用 `tools/moo_invite.py` 生成：
   - `moo.json` 正文（v2 `schema_version` + `source_info` + `apps.<appname>.releases`）
   - 一键新建链接（`https://github.com/<owner>/<repo>/new/<branch>?filename=moo.json&value=<urlencoded>`）
   - 评论全文（含折叠 JSON 兜底）
3. 发评论 → 对方点链接提交 → 我们拉 raw 校验 → 加入 `repo_list.txt` 精选源 + `recommend_list.txt` 推荐位 → 推 GitHub/Gitea。

## 硬约束（写文案时必须保留）

- **索引必须由作者自己提交**：不建 fork、不代提 PR，否则 Moo 溯源会看到 fork 血缘。
- **只要求一个文件**：仓库根 `moo.json`（Moo 原生协议，`schema_version: "moo"`）。
- **appname 必须等于包内 manifest 的 appname**，否则装上后更新匹配会错。
- **sha256 与 size 必填**：sha 不匹配会拒绝安装；size 写字节（条目级 `size` 是 MB，别混）。
- **一键链接 + 折叠 JSON 双保险**：链接过长/被截断时用折叠块里的全文。
- 末尾写清：收录后我们会做「精选源 + 发现页推荐位」，之后照常发 Release 即可。

## 开场白（首条 issue 正文）

> 你好，我叫 Blue-Mink，写了一个飞牛第三方应用中心 [Moo](https://github.com/Blue-Mink/moo)，
> 另外按 FnDepot 规范维护了一个外部源。先说明一下身份：FnDepot 协议不是我定的，
> 规范由 [EWEDLCM/FnDepot](https://github.com/EWEDLCM/FnDepot) 维护，这封邀请只代表我自己。
>
> 我想诚心邀请你把「<应用名>」也做成一个应用源。**不用你做两份东西，只要在仓库根目录放一份索引就够了**：
> - 走 Moo 协议：`moo.json`，规范见 [Moo 应用源协议](https://github.com/Blue-Mink/moo/blob/main/docs/Moo%E5%BA%94%E7%94%A8%E6%BA%90%E5%8D%8F%E8%AE%AE.md)
> - 走 FnDepot 协议：`fnpack.json`，规范见 [FnDepot 外部应用源 V2 编写说明](https://github.com/EWEDLCM/FnDepot/blob/main/README.md)
>
> 放好之后，任何人把你的仓库地址粘进 Moo 搜索框就能识别为源，支持同协议的客户端也都能加。

## 正式回复（对方同意后）

结构固定为：

1. **一、索引必须由你自己提交，我不走 PR 代劳**（写明 fork 血缘问题）。
2. **二、只需要一个文件：仓库根目录的 `moo.json`**（appname / 版本 / sha256 / size / releases 结构）。
3. **一键添加**（预填链接）。
4. **后续我会做的两件事**：加入精选应用源列表；在发现页推荐位加上该项目。
5. **以后发新版**：只需在 `releases` 里加一个版本节点，其余字段不用动。
6. `<details><summary>备用：moo.json 全文</summary>` 折叠块。

## 可复用资源

- 生成器：`tools/moo_invite.py`
- 已发正式版样例：`GIT_AUDIT_1101/invite_comment_6553395639.md`
- 协议文档：`moo-325/docs/Moo应用源协议.md`
