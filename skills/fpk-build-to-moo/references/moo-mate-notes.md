# Moo / Moo Mate 实战差异（从真实包反推）

> 本文只记录“这两个应用实际怎么配”，不是所有 FPK 的通用规范。通用字段仍以
> `references/fpk-build-spec.md` 与 `fn-fpk-builder` 官方契约为准。

## 1. 对照表
| 项 | Moo | Moo Mate |
|---|---|---|
| appname | `moo` | `moo-mate` |
| 端口 | `38100`（loopback 调试） | `38108` |
| `checkport` | `true`（按实际监听复核；官方常驻入口建议 false） | `false` |
| `micro_app` | `true` | `true` |
| `ctl_stop` | `true` | `true` |
| `beta` | `no` | `no` |
| `run-as` | `root` | `root` |
| `config/resource` | `api-scope: ["trim.app.center"]`、`port-config` + `moo.sc`、`data-share` 空、`systemd-unit` 空 | `port-config` + `moo-mate.sc`、`data-share` 空、`systemd-unit` 空 |
| `ui/config` | `type=iframe`、`gatewaySocket=app.sock`、`gatewayPrefix=/app/moo`、`url=/app/moo/`、`allUsers=false` | `type=iframe`、`protocol=http`、`gatewaySocket=mate.sock`、`gatewayPrefix=/app/moo-mate`、`url=/app/moo-mate/`、`allUsers=false` |
| 二进制位置 | `app.tgz` 内（按包结构） | **`app.tgz` 顶层 `moo-mate`**（不是 `app/moo-mate`） |
| 前端 | 构建产物嵌入/包内 | `web/index.html` `go:embed` |
| 默认数据 | `index.json` 等 | `default_index.json.gz` + `index.json` |
| 数据目录指针 | — | `${PKGVAR}/datadir`（`MOO_MATE_DATA`） |
| 资产 | 6 手工 + CI 二进制 = 7 | 6 |
| tag | `vX.Y.Z` | `mate-vX.Y.Z` |

## 2. Moo 关键点
- `config/resource` 的 `api-scope` 让平台注入 `TRIM_API_TOKEN`，用于免登录调应用中心 API；没有它，官方源同步会缺 token。
- `port-config` 指向 `moo.sc`，`port_forward="no"`；`service_port=38100`，但监听在 loopback，`checkport=true` 不触发平台端口占用（与常驻公网入口不同，需实测确认）。
- 网关：`app.sock` 由服务在 `${TRIM_APPDEST}` 创建；`gatewayPrefix=/app/moo`；身份头 `X-Trim-Userid`/`X-Trim-Isadmin`/`X-Trim-Username`。
- 构建：`build.sh` 里前端构建 + Go 编译 + `repack_fpk`（gzip -9 -n + 回写 MD5）；changelog 从 `moo.json` 的 `releases.<ver>.changelog` 内嵌。
- 发布：`moo.json` 与 FnDepot `fnpack.json` 双索引；`moo.json` 的 `apps.moo` 顶层字段必须跟版。

## 3. Mate 关键点
- **顶层二进制**：`app.tgz` 成员是 `./moo-mate` + `./web/...`；带 `app/` 前缀会装到错误位置（典型 10266/入口 404）。
- `web/index.html` 用 `go:embed`；改磁盘 `web/` 对已嵌入内容无效，前端有 diff 必须整包重建。
- 数据：首次启动从 `default_index.json.gz` 解出 `index.json`；数据目录指针 `datadir` 写到 `${PKGVAR}/datadir`，启动时读 `MOO_MATE_DATA`。
- 卸载：自定义数据目录只删应用自己的 `index.json`/`config.json`/`backups/`，不删父目录；默认目录按 `wizard_delete_data` 处理。
- 自更新：0.2.35+ 强制 minisign；release 6 件；`mate-v` tag；客户端会校验版本匹配，资产清单版本不符要丢弃重取。
- `checkport=false`：常驻入口端口 7×24 在线，`true` 会让停用后再启用报 11000。

## 4. 两者共同的坑
- `appcenter-cli install-fpk` 对已装应用 no-op；换版走 停→卸→`check`→装→启 或 daemon RPC。
- `start` 报 10500 是假错；`uninstall` rc=0 也可能没卸；`check` 确认。
- root 运行：`appcenter-cli` 非 root 会 `panic: ApplyPermission … rpcbroker: permission denied`。
- 图标三条通道：FPK 槽位、根 `ICON.PNG`、商店 `icon_url`；三处都要 ≥512，只换一处仍糊。
- 索引更新晚于 release；同版本不替换包；`releases` 只留 2 版。
