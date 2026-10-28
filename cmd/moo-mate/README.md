# Moo Mate

Moo 的伴侣工具 —— **免上传 GitHub 的飞牛应用源生成器**。

贴一个第三方 GitHub 仓库链接，Moo Mate 自动发现其中打包的飞牛（fnOS）应用，生成 `moo.json` 索引并在本机托管，一键加源到同机的 Moo，应用随后就能在 Moo 里浏览 / 安装 / 更新。

- 版本：**0.2.31**　平台：**x86 / arm**　端口：**38108**（仅本机回环，经飞牛面板网关访问）
- 维护者：[Blue-Mink](https://github.com/Blue-Mink)　许可：MIT

## 它解决什么问题

想把自建或别人仓库里的 FPK 应用放进 Moo，传统做法是手工写 `moo.json`、传到 GitHub、再到 Moo 里加源。Moo Mate 把这三步合并成：**贴链接 → 自动发现 → 一键加源**，索引由 Moo Mate 自己托管，不需要 GitHub 仓库、不需要 token 权限。

## 主要功能

| 功能 | 说明 |
|---|---|
| 仓库发现 | 贴 GitHub 仓库 / Release / 目录链接，自动探测 FPK 与应用信息 |
| 索引托管 | 生成 `moo.json` 并由本机 HTTP 直接对外提供，免上传 |
| 一键加源 | 写入同机 Moo 的源列表，装好即可在 Moo 浏览 |
| 定时同步 | 按设定间隔同步应用源（也可手动触发） |
| 数据备份 / 恢复 | 备份目录与数据路径均可自定义；支持恢复到本 NAS 或从手机 / 电脑上传恢复 |
| 下载备份到本机 | 「本 NAS」列历史备份点击即下载；「本机设备」现场生成并下载到手机 / 电脑浏览器 |
| GitHub Token | **自动复用本机 Moo 设置的 Token**，伴侣侧无需重复填写 |

## 安装

任选其一：

1. **Moo 商店**：在 Moo 里添加源 `https://raw.githubusercontent.com/Blue-Mink/moo/main/moo.json`，搜索「Moo Mate」安装。
2. **FnDepot 源**：添加源 `https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/fnpack.json`，在列表中找到 Moo Mate。
3. **手动**：下载 `moo-mate_0.2.31_x86.fpk`（或 `_arm.fpk`）与同名 `.sha256` 校验后在飞牛应用中心手动安装。

## 首次使用

1. 打开 Moo Mate（应用中心「打开」或桌面图标），端口 38108。
2. 在「探索」页粘贴要收录的 GitHub 仓库链接，等待探测完成。
3. 确认发现的应用 → 生成索引 → 一键加源到本机 Moo。
4. 回到 Moo 刷新目录，即可看到并安装这些应用。

## 数据与备份

- 数据目录默认在应用数据卷，可在「应用设置」里改到任意 `/volN` 可见目录，切换后即时生效（重启仍生效）。
- 备份文件格式 `moo-mate-backup-YYYYMMDD-HHMMSS.tar.gz`，含索引与配置；不含 Moo 的 GitHub Token（Token 始终只在 Moo 侧加密保存）。
- 恢复支持「本 NAS 历史备份」与「本机设备上传备份」两种来源。

## 说明

- Moo Mate **不带自更新**，升级请重新安装新版本 FPK（数据保留）。
- GitHub Token 只在请求 GitHub API 时使用，不出现在页面与日志中。
