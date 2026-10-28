# /vol1 数据卷损坏记录（2026-09-24）

本仓库是在主 NAS（<NAS_IP>）`/vol1` btrfs 卷被内核强制 remount 为
**read-only**（伴随大面积文件数据丢失）时抢救出来的。备份范围 = 项目全量源码
（FPK/build/dist/node_modules 按项目自带 .gitignore 排除，属可再生构建产物）。

## 扫描结论（全卷 587 个文件损坏，meta 大小 ≠ 实际可读字节）

- **frontend/node_modules（~458 个文件）**：第三方依赖，`npm ci` 可完整重建，无损失。
- **历史 FPK / build/moo-server-x86 / fnos/app/moo-server（~95+ 个）**：构建产物，
  可从源码重建，无损失。
- **tools/*.png（10 张 UI 参考截图）**：部分或全部字节丢失，不可恢复（仅作开发参考）。
- **third_party/iStoreEnhance（kspeeder 引擎 0.8.0，11.5MB）**：仅存 794KB，
  可从 kspeeder 官方源重新获取（Blue-Mink 源 0.8.0 标准管线）。
- **internal/source/fndepot_test.go**：唯一受损的源码文件（6369 → 4096 字节，
  尾部 2273 字节丢失）。

## fndepot_test.go 重建说明

丢失段 = `TestTranslateEntryArchDiff` 尾部 + 其后可能存在的测试函数。
已按 `fndepot.go` 中 `translateEntry` 的现行实现（arch_diff 当前架构列优先 →
all 列回退；无链接时 GitHub 仓库约定直链）重建该文件尾部，恢复为可编译、
全绿状态（go1.25.0 `go test ./internal/source/` 全 PASS）。
重建内容不是原始字节，如有需要可对照实现再校。

## 教训

- 本卷 0.6.26 时期就发生过 `store_catalog.go` 神秘截断事故，当时已建议 git init，
  拖到 0.6.74 才落地——**源码改动当天就该进 git 并推送**。
- `/vol1/@appconf/.../go/pkg/mod` 的 go1.25.0 工具链二进制也已被损坏（0 字节可读），
  工具链缓存放数据卷同样有损。
