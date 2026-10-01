# frontend/ — 前端（React + TypeScript）

MVP 阶段从 **Blue-Mink/New-Store（MIT 授权）** 移植 UI：

- 目标页面：发现 / 全部 / 已安装 / 更新 / 应用详情（5 页）
- 组件：AppCard / AppRowList / AppDetailDialog / SettingsPage / BackgroundTasksIndicator
- 主题：亮暗双主题 + 移动端 dock + PC 侧栏
- 构建：Vite → 输出到仓库根 `web/`（后端 embed）

## 移植边界（重要）

| 来源 | 授权 | 处理方式 |
|---|---|---|
| New Store 我们的增量代码（1.9.6 之后的 29+ 提交） | MIT | 可直接移植 |
| conversun 原仓库代码（fork 点 75b7a02 之前） | **无 LICENSE** | 交互行为可参照，代码重新实现（clean room） |

## 脚手架

M1 阶段执行：`npm create vite@latest` + tailwind + shadcn/ui 基线，然后逐页移植。
