# 真机 E2E 测试单（仅一次性测试设备）

> 红线：只在**可丢弃的测试设备**跑；不碰生产机、不碰测试机上的全量生产版。
> 应用名必须带 `e2e-` 前缀；先备份 `@appdata` 并记录数据指纹；跑完清理自己创建的测试应用/源。
> 不在脚本/文档里写真实口令、内网 IP、个人路径；凭据运行时注入。

## 0. 前置
- [ ] 设备是专用测试机，已获用户明确同意；生产/测试机的生产版不在范围内。
- [ ] 备份 `@appdata/<app>`（运行中归档加 `--warning=no-file-changed`）。
- [ ] 记录基线：应用是否已装、版本、端口、入口 URL、数据目录项数、关键文件 sha256。
- [ ] 准备 x86/arm 对应 FPK、`/tmp/e2e-<app>.env`（600，`KEY=VALUE`）。
- [ ] 记录回滚版本 FPK 与数据备份路径。

## 1. 首装（含向导）
```text
appcenter-cli install-fpk <fpk> --volume <N> --env /tmp/e2e-<app>.env
appcenter-cli status <app>        # 0=running, 3=not running
appcenter-cli check <app>         # 确认已装
curl -I http://127.0.0.1:<port>/  # 或网关 /app/<app>/
```
判据：
- [ ] 新 PID；`exe` 指向安装目录，不是旧进程。
- [ ] `manifest.version` = 运行进程版本；前端 bundle 文件名/指纹正确。
- [ ] 向导值真的生效（端口/路径/日志），不是只写了 `initValue`。
- [ ] 网关入口/桌面图标能打开；`ui/config` 的 key = `desktop_applaunchname`。
- [ ] 数据目录生成正确；日志无权限 000。

## 2. 配置改端口
- 从 AppCenter UI（或 `POST /app-center/v1/config/wizard`）改管理端口 → 保存。
- 判据：
  - [ ] `config_init` 回显新值；`config_callback` 重启服务。
  - [ ] 旧端口不再响应、新端口响应；`config/detail` / `app_service.url` / 实际监听三处一致。
  - [ ] 再改回旧端口，反向验证通过。

## 3. 升级（四步）
```text
备份 @appdata → appcenter-cli stop <app> → appcenter-cli uninstall <app>
→ appcenter-cli check <app>（确认没了）→ appcenter-cli install-fpk 新版 --env <同一份 env>
→ appcenter-cli start <app>（10500 忽略）→ 等冷启动
```
判据：
- [ ] 新版本、新二进制 sha；数据指纹与升级前逐字一致。
- [ ] 源数/卡数/已装数/禁用集合零回归；冷启动前几秒偏低是瞬态。
- [ ] 入口/端口/图标正常。

## 4. 卸载保留数据
- `wizard_delete_data=false` 卸载 → `appcenter-cli check` 确认卸载。
- 判据：
  - [ ] `@appdata/@appconf/@appmeta/@apphome` 关键文件仍在。
  - [ ] 重装后数据被复用；不会重新初始化/覆盖。
  - [ ] 用户下载目录未被删除。

## 5. 卸载删除数据
- `wizard_delete_data=true` 卸载。
- 判据：
  - [ ] 只删应用自己的精确目录/文件；父目录、用户下载目录、其他应用数据不动。
  - [ ] 外部副作用清理（hosts 标记行、docker 镜像条目、进程/容器）只删自己加的。
  - [ ] 重装得到干净初始状态。

## 6. 失败路径
- [ ] 错误许可/缺许可：安装被拒，错误可读，不落半装状态。
- [ ] 端口被占/越界：提示明确；不写坏配置。
- [ ] `install-fpk` 对已装应用 no-op：确认没有“假成功”。
- [ ] `start` 10500：按假错处理，不重试。
- [ ] 卸载 10150：走面板/人工路径，不反复重试。
- [ ] 回调超 190s：确认后台 worker 仍在跑/状态文件可读，`appcenter-cli install-fpk` 不段错误。

## 7. 商店 E2E
- [ ] 本地/测试 HTTP 服务提供 `moo.json` / `fnpack.json`；`curl -I` 200。
- [ ] Moo/FnDepot 添加源 → 同步无跳过应用。
- [ ] 详情页图标 ≥512、版本/sha256/size 正确。
- [ ] 真装一次；更新一次；旧版本文件仍可下。
- [ ] 自更新应用：API 限流/镜像兜底下仍能下载 + `.sha256` + `.minisig` 验签。

## 8. 清理与回滚
- [ ] 卸载测试应用；删除测试源；清理 `/tmp` 测试文件。
- [ ] 恢复基线数据/配置；核对数据指纹。
- [ ] 记录：设备、FPK sha256、结果、日志路径、失败项。
- [ ] 任何步骤失败：停止，不上生产；按回滚版本恢复。
