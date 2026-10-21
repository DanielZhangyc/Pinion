# Gryph Launcher

基于 MyGo 原生模式的非官方 macOS 启动器项目，面向明日方舟与明日方舟：终末地的后续集成。

当前完成工程初始化和原生界面基础：游戏库、运行环境页面及页面导航。游戏导入、下载更新、自建 Wine 和游戏启动尚未实现。现有 CrossOver 容器与 FineWine 部署不由本项目自动读取或修改。

## 开发环境

- macOS，当前构建目标为 Apple Silicon。
- Go 1.27.2；MyGo v0.5.5，由 `go.mod` / `go.sum` 固定。
- direnv；原生模式无需 Node、Bun 或 WebView 前端。

```sh
brew install go direnv
cd /Users/yuchenzhang/Documents/Projects/gryph-launcher
direnv allow .
make dev
```

Shell 需要启用 direnv hook，例如 zsh 的 `eval "$(direnv hook zsh)"`。本机已启用。Makefile 通过 `direnv exec .` 加载项目环境，因此从非交互终端执行也使用同一配置。

`.envrc` 固定 `GOTOOLCHAIN=go1.27.2` 和 `CGO_ENABLED=0`，将 Go 模块缓存、构建缓存和工具目录放在 `.direnv/` 下。离开目录后，direnv 恢复原环境；`.direnv/` 不纳入 Git。以后升级 Go 时，同时调整 `.envrc` 和 `go.mod`。

## 检查与构建

```sh
make fmt
make check
make build
```

`make check` 执行真实 MyGo UI 导航测试、Go vet 和 MyGo 生命周期检查。测试使用 MyGo 的 headless renderer，不启动游戏。

构建结果：`build/darwin-arm64/Gryph Launcher.app`，本地 ad-hoc 签名，可在 Finder 中双击。开发构建不包含 Wine、官方启动器或游戏资源，也没有完成 Developer ID 签名与公证。

## 代码与后续集成

- `main.go`：应用和窗口生命周期；窗口使用逻辑尺寸，默认 1080×700。
- `internal/app/`：原生 UI 和导航状态；UI 元素只在当前构建期间有效。
- `mygo.json`：名称、bundle ID、版本与构建目录。
- `.agents/skills/mygo-maintenance/`：固定版本脚手架随附的维护指引。

后续将下载／更新、鹰角协议、运行时和 prefix 管理从界面中独立出来。明日方舟与终末地采用各自的客户端适配，不能假设二者共用 Windows/Wine 运行路线。自建 Wine 是单独的交付项，现有 FineWine 替换模块包不等于完整运行时。

## 性能边界

MyGo 在 macOS 原生模式中使用 Metal，静止时按需停止重绘。大图、模糊、动画和高 DPI 窗口会影响实际 GPU／内存开销；下载、解压和校验应放在 UI 线程之外，并限制并发和内存缓冲。游戏运行时应暂停不可见的动效。

官方性能数据是特定机器上的框架微基准，不能当作本项目成品占用或游戏帧率的保证。参考：[原生模式](https://mygo.egoist.dev/docs/ui)、[渲染机制](https://mygo.egoist.dev/docs/ui/rendering)、[性能测量范围](https://mygo.egoist.dev/docs/ui/performance)。

本机初始化版本的实测和复现命令见 [性能基线](docs/performance-baseline.md)。
