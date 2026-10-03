# web/ — 前端构建产物

- 由 `frontend/` 构建输出（Vite），后端 Go `embed` 进二进制
- M0 骨架阶段无真实 UI，`/` 返回占位文本
- 该目录产物随代码提交（与 New Store 一致），FPK 打包时整体拷入 `app/web/`
