# 操作流程：从构建到发布 Moo

> 这是**主流程**：每一步都有「做完的判据」，没达到判据不要进下一步。
> 协议细节见 [docs/Moo应用源协议.md](../../../docs/Moo应用源协议.md)；构建细节见 [fpk-build.md](fpk-build.md)。

## 0. 判断你要发布什么

| 情况 | 起点 |
|---|---|
| 手里只有源码 | 从 §1 构建 |
| 已有 FPK 包（自己打的或上游给的） | 跳到 §2 本地验证 |
| 是 Docker 应用（给镜像/compose） | 打包成 FPK（fnOS 上以 FPK 分发），仍走 §1 |
| 已有整个应用仓库（多应用） | 跳到 §3 生成索引 |

## 1. 构建 FPK（源码 → 包）

```bash
./build.sh x86                # 项目脚本支持时：./build.sh arm / ./build.sh both
# 或 fnpack build -d <开发目录>（按 manifest.platform 出单架构包）
sha256sum *.fpk               # 每个架构的包都要记校验和，后面写进 moo.json
```

**判据**：产出 `*.fpk`；`manifest` 里 `appname` / `version` / `platform` 明确；支持 ARM 的项目必须同时有 `_x86.fpk` 与 `_arm.fpk`（或 `all`），不能只发 x86；`ICON.PNG` 是 ≥512 PNG（全槽位高清见 [fpk-build.md](fpk-build.md) 第三节）。
详见 [fpk-build.md](fpk-build.md)。

## 2. 本地/测试机验证（**不要跳过**）

```bash
scp <appname>.fpk root@<NAS>:/tmp/
# 必须 root；wizard 应用用 --env 文件（每行 KEY=VALUE），不是字符串
ssh root@<NAS> 'appcenter-cli install-fpk /tmp/<appname>.fpk --volume <N> --env /tmp/<app>.env'
ssh root@<NAS> 'appcenter-cli check <appname>'   # 卸载/重装后确认真的没了
```

**判据**：能在面板里装上 → 能启动 → 应用功能可用 → 版本号与 `manifest` 一致 → 卸载/重装数据符合预期。

> `appcenter-cli install-fpk` 对已装应用是 no-op；换版走 停→卸→`check`→装→启 或 daemon RPC。`start` 报 10500 是“已在运行”的假错。完整向导/数据边界/190s 看门狗见 [wizard-lifecycle.md](wizard-lifecycle.md)。

## 3. 生成 `moo.json`

**一个应用、几条命令**：

```bash
# 从 FPK 目录生成（自动读 manifest、算 sha256 与字节体积、带图标）
go run ./tools/gen-moo-json -dir <fpk目录> \
  -name "我的应用仓" -distributor "MyOrg" \
  -base-url "https://example.com/apps" -out moo.json
```

**以 release 发布的仓库（多版本）**：

```bash
python3 tools/gen-repo-moo-json.py --repo <仓库地址> --user <user> --password <token> \
  --app <appname> --name "我的应用" --labels "系统工具" --install-type root \
  --preview "docs/shot-1.png" --readme-file "README.md" --out moo.json
```

**判据**：`moo.json` 能 `json.load` 通过；每个应用都有 `download_url` + `sha256`；
体积单位正确（条目级 MB / 包级字节）；分类取自协议固定清单。

## 4. 放到仓库 / 服务

把 `moo.json`（以及图标、预览图、README 若用相对路径）放到能被 HTTP 取到的位置：

```bash
git add moo.json && git commit -m "chore(moo.json): 发布 vX.Y.Z" && git push
```

**判据**：`curl -sI <moo.json 链接>` 返回 **200**；图标（≥512）/ 预览 / README / 包链接同样能匿名取到。商店卡片用的是 `moo.json` 的 `icon_url` 指向的树内 `ICON.PNG`，不是 FPK 里那份，二者都要 ≥512。
（私有仓库必须放开匿名读，或让 Moo 拿到访问令牌。）

## 5. 自测（不添加源也能验）

把 `moo.json` 链接**粘到 Moo 首页搜索框** →

**判据**：能即时列出该源的应用，且展示名 / 版本 / 图标（≥512）正确。

## 6. 添加为源并验收

Moo → 设置 → **应用源设置** → 添加应用源 → 粘同一链接。

**判据**（打开该应用详情页逐项核对）：

- 图标（≥512）、展示名、版本；
- **安装包大小**与 **SHA256**（单位写对没）；
- 预览图（可点开、可双指缩放）、README（默认约 10 行、可展开）；
- 更新日志（默认最近 3 条、可展开）、分类标签、运行方式/安装位置；
- 点「安装」**真装一次**，确认下载与校验通过。

## 7. 发布新版（迭代）

1. 改代码 → 重新构建（版本号按协议 §5.3 递增，例如 `1.2.0` → `1.2.1`）；同版本不替换包。
2. 重新生成索引（多版本仓库用 `gen-repo-moo-json.py` 会自动把新版写进 `releases`）；`releases` 只留最近 2 版。
3. 自更新应用：release 先上传 `FPK + .sha256 + .minisig`（双架构 6 件），核对 `asset.digest`/sha256；**索引晚于 release**。
4. 推送 → 等 Moo 下一轮同步（默认 1 小时）或手动点「刷新页面」。
5. **判据**：已装用户看到「有更新」，更新包来自**原先安装的那个源**；真机点更新能下载 + 验签 + 安装。
6. 兜底通道（API/镜像/FnDepot）每条都要给 FPK + `.sha256` + `.minisig` 三个 URL；缺一侧车 fail-closed。详见 [self-update-signing.md](self-update-signing.md)。

## 8. 多应用仓库维护

- 一份 `moo.json` 管一个源；新增应用就往 `apps` 里加一条；
- 应用多时用 `releases` + 生成器，别手写几百条；
- 一条线路一个索引（稳定版与测试版分开）：0.6.330 起 `rc.N` 排序**低于**同底正式版，混放会让测试版永远不提示更新（协议 §5.3 / §5.4）。

## 9. 卡住了看哪

| 阶段 | 参考 |
|---|---|
| 构建 / 安装 / 运行时 | [fpk-build.md](fpk-build.md) + 姊妹技能 `fn-fpk-builder` |
| 安装/卸载/配置/升级向导、数据删除、190s 看门狗 | [wizard-lifecycle.md](wizard-lifecycle.md) |
| `moo.json` 字段（bug_report_url/wizard/min_fnos 等） | [moo-json-fields.md](moo-json-fields.md) |
| 源不生效 / 搜不到 / 没图标 / 体积 0.0MB / 更新红点 | [docs/Moo应用源协议.md §10 FAQ](../../../docs/Moo应用源协议.md#10-faq--排查手册) |
| 版本比较与"谁是最新版" | [docs/Moo应用源协议.md §5.3](../../../docs/Moo应用源协议.md#53-版本比较规则重要影响谁是最新版有没有更新) |
