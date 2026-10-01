# 操作流程：从构建到发布 Moo

> 这是**主流程**：每一步都有「做完的判据」，没达到判据不要进下一步。
> 协议细节见 [docs/MOO-PROTOCOL.md](moo-protocol.md)；构建细节见 [fpk-build.md](fpk-build.md)。

## 0. 判断你要发布什么

| 情况 | 起点 |
|---|---|
| 手里只有源码 | 从 §1 构建 |
| 已有 FPK 包（自己打的或上游给的） | 跳到 §2 本地验证 |
| 是 Docker 应用（给镜像/compose） | 打包成 FPK（fnOS 上以 FPK 分发），仍走 §1 |
| 已有整个应用仓库（多应用） | 跳到 §3 生成索引 |

## 1. 构建 FPK（源码 → 包）

```bash
./build.sh x86                # 或 fnpack build -d <开发目录>
sha256sum <appname>.fpk       # 记下校验和，后面写进 moo.json
```

**判据**：产出 `*.fpk`；`manifest` 里 `appname` / `version` / `platform` 明确；`ICON.PNG` 是 256×256 PNG。
详见 [fpk-build.md](fpk-build.md)。

## 2. 本地/测试机验证（**不要跳过**）

```bash
scp <appname>.fpk root@<NAS>:/tmp/
ssh root@<NAS> 'appcenter-cli install /tmp/<appname>.fpk'   # 或面板手动安装
```

**判据**：能在面板里装上 → 能启动 → 应用功能可用 → 版本号与 `manifest` 一致 → 卸载/重装数据符合预期。

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

**判据**：`curl -sI <moo.json 链接>` 返回 **200**；图标 / 预览 / README / 包链接同样能匿名取到。
（私有仓库必须放开匿名读，或让 Moo 拿到访问令牌。）

## 5. 自测（不添加源也能验）

把 `moo.json` 链接**粘到 Moo 首页搜索框** →

**判据**：能即时列出该源的应用，且展示名 / 版本 / 图标正确。

## 6. 添加为源并验收

Moo → 设置 → **应用源设置** → 添加应用源 → 粘同一链接。

**判据**（打开该应用详情页逐项核对）：

- 图标、展示名、版本；
- **安装包大小**与 **SHA256**（单位写对没）；
- 预览图（可点开、可双指缩放）、README（默认约 10 行、可展开）；
- 更新日志（默认最近 3 条、可展开）、分类标签、运行方式/安装位置；
- 点「安装」**真装一次**，确认下载与校验通过。

## 7. 发布新版（迭代）

1. 改代码 → 重新构建（版本号按协议 §5.3 递增，例如 `1.2.0` → `1.2.1`）；
2. 重新生成索引（多版本仓库用 `gen-repo-moo-json.py` 会自动把新版写进 `releases`）；
3. 推送 → 等 Moo 下一轮同步（默认 1 小时）或手动点「刷新页面」；
4. **判据**：已装用户看到「有更新」，且更新包来自**原先安装的那个源**。

## 8. 多应用仓库维护

- 一份 `moo.json` 管一个源；新增应用就往 `apps` 里加一条；
- 应用多时用 `releases` + 生成器，别手写几百条；
- 一条线路一个索引（稳定版与测试版分开），否则版本比较会把测试版当"最新"（协议 §5.3）。

## 9. 卡住了看哪

| 阶段 | 参考 |
|---|---|
| 构建 / 安装 / 运行时 | [fpk-build.md §八](fpk-build.md) + 姊妹技能 `fn-fpk-builder` |
| 源不生效 / 搜不到 / 没图标 / 体积 0.0MB / 更新红点 | [docs/MOO-PROTOCOL.md §10 FAQ](moo-protocol.md#10-faq--排查手册) |
| 版本比较与"谁是最新版" | [docs/MOO-PROTOCOL.md §5.3](moo-protocol.md#53-版本比较规则重要影响谁是最新版有没有更新) |
