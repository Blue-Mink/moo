# 发布、自更新与签名（minisign）

> 适用：Moo / Moo Mate 这类“应用内自更新”的 FPK。普通应用可只发 FPK，但一旦客户端会自更新，
> 就必须按本文的三件套与索引顺序发布；缺侧车会 fail-closed，用户看到的是“更新失败”。

## 1. 信任锚
- 签名方案：**minisign**。私钥离线保管（口令加密），**不入 git、不上传 GitHub、不进 CI**。
- 公钥 `go:embed` 进二进制（如 `internal/api/self_update_pubkey.txt`）；验签**不联网取公钥**（否则攻击者可连公钥一起换）。
- 签名只证明“发布者身份”；`.sha256` 与包同仓可被同时替换，**只做 sha256 等于没做安全校验**。sha256 用来防传输损坏/老客户端兼容，minisign 才是信任锚。

## 2. 资产矩阵
| 应用 | 每架构 | 双架构合计 | tag |
|---|---|---|---|
| Moo | `FPK` + `.sha256` + `.minisig` | 6 手工 + CI 二进制 = 7 | `vX.Y.Z` |
| Moo Mate | `FPK` + `.sha256` + `.minisig` | 6 | `mate-vX.Y.Z` |

- 文件名：`<app>_<version>_<arch>.fpk`（Moo `moo_…_x86.fpk` / `_arm.fpk`；Mate `moo-mate_…`）。
- `.sha256`：`sha256sum` 输出格式（`<sha>  <文件名>\n`）。
- `.minisig`：minisign 分离签名（默认 untrusted comment）。
- Moo Mate 0.2.35+ 才第一次有自更新，没有老客户端兼容负担，但仍必须三件套；Moo ≤0.6.328 的老客户端只认 `.sha256`，缺了会永远升不上来。
- release 必须 public；prerelease 不参与正式索引/巡检。
- GitHub CI 可构建并上传 FPK/裸二进制，但**签名只在持私钥的发布机做**；CI 不碰私钥。

## 3. 签名与验签
```bash
# 私钥在离线发布机；口令只走 stdin，不写命令行/日志
minisign -S -s <secret.key> -m <app>_<ver>_x86.fpk -x <app>_<ver>_x86.fpk.minisig
minisign -V -p <pubkey.txt> -m <app>_<ver>_x86.fpk -x <app>_<ver>_x86.fpk.minisig
# 必须回：Signature and comment signature verified
```
- 对 x86/arm 各签一次；`.sha256` 同时生成。
- 验签失败/缺 `.minisig`：新客户端拒绝自更新（正确行为）；缺 `.sha256`：≤0.6.328 老客户端卡死、永远升不上来。
- 私钥泄露：生成新密钥对 → 新公钥 embed 进下一版 → 发新版，旧公钥随版本淘汰。

## 4. 发布顺序（不可颠倒）
```text
构建 FPK → 生成 .sha256 → minisign 签名 → 建/更新 GitHub Release（public）
→ 上传 6/7 件资产 → 回下核对 asset.digest/sha256
→ 更新 moo.json / fnpack.json（releases 只留最近 2 版）
→ 推索引 → 等 CDN/客户端缓存 → 真机自更新验收
```
- **索引永远晚于 release**：索引先出现 → 三件套 404 → 用户报“release 未附签名资产”。
- **但 tag 必须落在含索引的提交上**：两条同时成立的做法是——先上传资产 → 再提交索引跟版 → **同一次推送里推索引提交 + tag**（索引提交只改 JSON，代码与构建产物不变）。tag 先推、索引后补，`mate-release` / `release` 的 `Verify release assets` 会直接判红 `FAIL: moo.json 缺 apps.<key>.releases.<ver>.packages`（2026-11-04 mate 0.2.50 实测）。
- **tag 移动后以最后一次 run 为准**：补索引/修测试后把 tag 移到新提交会重跑 CI，只有移动后那次绿才算绿，别拿前一次的结论顶替。
- **推 tag 前先跑发布机同款门禁**（`bash cmd/moo-mate/gate.sh` / `bash .github/scripts/moo-gate.sh`），CI 跑的是同一脚本。本地绿、CI 红的常见原因是**测试依赖 tar/文件顺序**——同一份代码，两次打包的条目顺序不同就会翻脸。
- 同版本不替换包；已发布“版本号+架构”文件不可变。改内容发新版本或新地址。
- `releases` 只留最近 2 版；顶层 `version/download_url/size/sha256/updated_at/changelog` 跟版。
- changelog 三处同源：仓库自索引、构建内嵌“关于页”、release 正文。

## 5. 兜底通道
国内出口 `api.github.com` 常限流（`remaining=0`），线上几乎恒走兜底。**每条兜底通道都必须同时给出 FPK + `.sha256` + `.minisig` 三个 URL**：
- 缺任一个，0.6.329+ 的 fail-closed 会让兜底 100% 失败，且报错误导为“release 未附签名资产”。
- 旧单测曾把 `shaURL==""` 写成期望值，等于把 bug 固化成断言；改通道必须同步改/加断言。
- 验收必须覆盖兜底路径：单测断言侧车非空 + 真机在 API 限流时仍能自更新。

## 6. 资产上传与校验
- GitHub 资产上传走 release 的 `upload_url`（uploads.github.com），不要用旧 `api.github.com/.../assets` POST。
- 上传后可能“201 + 正确 size”但实际截断；必须回下 `asset.digest`/sha256 与本地比对。
- 新资产 ~60s CDN 传播延迟，期间 404 属正常；持续 404 查仓库可见性/token 权限。
- 删除/替换 release 资产受账号策略限制；同 tag 替换 release 可能不是所有账号都允许，优先发新版本。

## 7. 发布前检查
- [ ] x86/arm 各有 `FPK + .sha256 + .minisig`，验签 PASS
- [ ] `asset.digest`/sha256 与本地逐字一致
- [ ] release public；tag 与包内 manifest 版本一致
- [ ] 索引 `releases` 只留 2 版；顶层字段跟版
- [ ] 兜底通道三个 URL 都非空
- [ ] 真机在 API 限流/镜像通道下自更新成功
- [ ] 索引更新晚于 release；同版本未替换
- [ ] **索引跟版提交与 tag 同一次推送**（tag 打在含索引的提交上）；补推索引后确认移动 tag 产生的新 run 为绿
- [ ] 干净树跑过发布机同款门禁（`cmd/moo-mate/gate.sh` / `.github/scripts/moo-gate.sh`），tar/打包类断言与条目顺序无关

## 7.1 CI 红叉对照（发布链路）

| 报错 | 根因 | 处置 |
|---|---|---|
| `Verify release assets`：`FAIL: moo.json 缺 apps.<key>.releases.<ver>.packages` | tag 指向索引跟版**之前**的提交（典型：先推 tag 再补索引） | 提交索引 → 把 tag 移到含索引的提交（或索引+tag 同推）→ 确认新 run 绿 |
| `Gate`：`TestExtractFpkIconStream_*` 期望包根图标（如 860B）实际命中包内图标（如 213B） | 测试依赖 tar 条目顺序（`app.tgz` 与包根图标谁先谁后取决于打包顺序） | 断言改顺序无关：两种命中都允许并分别断言，或按构造顺序显式指定 |
| `Gate` 本地绿 / CI 红且只有打包类用例 | 同一代码不同打包顺序 → 用例不确定 | 同上去顺序化；修完重跑 gate 两次确认稳定 |
| 手动 `workflow_dispatch` 绿、tag push 红 | 两者 checkout 的提交不同（手动跑的是默认分支 HEAD） | 以 tag 对应提交为准；先把 tag 移到含索引/修复的提交 |
