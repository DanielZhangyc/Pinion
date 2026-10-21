# 项目约定

- 使用 MyGo 原生 `ui`，保持 Go 模块工程；当前无需 WebView 或 JS 前端。
- Go 命令通过 `direnv exec .` 执行，或使用 Makefile；不要通过 `go env -w` 修改全局环境。
- UI 修改先阅读 `.agents/skills/mygo-maintenance/SKILL.md`，再核对 `go.mod` 固定版本的 API。
- 保持渲染函数无外部副作用；耗时任务在后台执行，通过 `Window.Update` 更新界面。
- 当前项目初始化阶段不自动读取、修改或启动现有游戏及 CrossOver 容器。
- 相关检查使用 `make check`；本地 macOS 应用使用 `make build` 构建。
