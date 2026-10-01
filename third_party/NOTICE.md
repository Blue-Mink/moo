# 第三方组件来源

## iStoreEnhance（kspeeder-engine）

- 来源：[kspeeder/docker_kspeeder](https://github.com/kspeeder/docker_kspeeder) v0.8.0（build 8664）
- 二进制：`iStoreEnhance`（linux amd64，静态链接，11.5MB）
- 获取方式：从 Blue-Mink/FnDepot 发布的 `kspeeder-0.8.0-fnos-amd64.fpk`
  （https://github.com/Blue-Mink/FnDepot/releases/download/v0.8.0/）中解出
  `app/bin/iStoreEnhance`
- 用途：Docker 镜像加速引擎（本地 registry 代理 + 多镜像站带宽叠加 + 缓存）
- SHA256：`6e165aeb2a1f81eefb0bf8db11c480abd3cf8fa4a8bfc2b98b1f7c15640af380`
- 许可说明：上游仓库未附 LICENSE（默认全权利保留）。本组件以独立 FPK
  （kspeeder-0.8.0-fnos-amd64.fpk）形式由 Blue-Mink 再分发于本商店，
  内嵌于 Moo 属同一分发渠道；如上游许可要求变化，以移除内嵌、
  改回独立应用（方案 B）方式应对。
- 升级：重新从上游 release 解包二进制替换本目录文件即可（接口/参数不变时
  无需改代码）。
