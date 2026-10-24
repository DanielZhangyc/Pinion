# 贡献指南

开始前请阅读 [AGENTS.md](AGENTS.md)。AI 辅助贡献还须遵守 [LLM_POLICY.md](LLM_POLICY.md)。问题、功能建议和 Pull Request 请使用仓库提供的模板，不提交游戏文件、存档、账号信息或本机日志。

## 开发环境

- macOS 与完整的 Xcode 26 或更新版本。分层应用图标需要 Xcode 的 `actool`，仅安装 Command Line Tools 不够。
- direnv、Go 和 MinGW-w64（提供 `x86_64-w64-mingw32-gcc`）。
- Go 版本由 `.envrc` 固定，MyGo 版本由 `go.mod` 固定。

Go 命令通过 `direnv exec .` 或 Makefile 执行：

```sh
direnv allow .
direnv exec . go mod download
make dev
make check
```

`make check` 执行默认测试、Go vet 和 MyGo 静态检查，不启动游戏。网络与 Wine 集成测试由各测试文件中的 `GRYPH_*` 环境变量显式启用；仅在独立测试目录运行。

## 构建应用

准备带有 `runtime.json` 的完整 FineWine 运行库。普通 Wine 或仅供 CrossOver 替换的模块补丁包不能直接使用。

无需维护者签名证书的本地构建：

```sh
make assets icon runtime-bundle RUNTIME_SOURCE="/完整运行环境的绝对路径"
direnv exec . go tool mygo build -platform darwin/arm64 -skip-dmg -sign - -o build/local
```

产物为 `build/local/darwin-arm64/Pinion.app`。持有 `mygo.json` 中签名证书时，可运行 `make build`，产物为 `build/darwin-arm64/Pinion.app`。

`make release` 另生成同目录下的签名 DMG，仅构建本地文件。版本号统一读取 `mygo.json`；更新检查使用 `DanielZhangyc/Pinion` 的最新稳定版 Release，发布标签使用 `v0.1.0` 这样的语义版本号。Apple 公证需另行配置。

`RUNTIME_SOURCE` 默认使用本机 `~/Library/Application Support/Gryph Launcher/Runtime/current`。打包只读取源运行库，在 `resources/runtime/` 生成固定版本描述符和完整归档；该目录不提交到 Git。首次打开应用时，Pinion 校验并安装归档，已有有效环境会复用。

首次下载模式可通过 `RUNTIME_URL` 固定 HTTPS 运行包地址。上传并核对归档后，在 `resources/runtime/` 中只保留描述符，再直接执行 MyGo 构建命令；`make build` 会重新内置归档。当前无公共运行包地址，正式分发仍需完成组件签名与 Apple 公证。

## 目录结构

| 路径 | 用途 |
| --- | --- |
| 根目录 Go 文件 | 应用入口与下载、启动、运行环境的协调 |
| `internal/` | UI、下载、Wine、游戏启动及媒体处理 |
| `assets/` | 应用图标与标志的源文件 |
| `resources/` | 随应用交付的资源、运行包和第三方许可 |
| `tools/` | Windows 启动辅助程序及相关检查工具 |
| `docs/screenshots/` | README 使用的界面截图 |
| `.github/` | Issue 与 Pull Request 模板 |

构建产物放在 `build/`，本机调试笔记、验证记录和设计探索稿放在 `.local/`；二者均由 Git 忽略，不作为发布内容。`resources/` 会整体打包，因此只放应用需要的文件。图标修改后运行 `make icon`；macOS 分层图标源文件为 `assets/AppIcon.icon`。

## 独立调试

应用数据保留历史目录名 `Gryph Launcher`，以兼容已有安装：配置和容器位于 `~/Library/Application Support/Gryph Launcher/`，启动日志位于 `~/Library/Logs/Gryph Launcher/`。更改应用显示名称时不要搬移这些目录。

可用绝对路径 `PINION_DATA_ROOT` 隔离配置、运行环境、容器、缓存和日志，游戏资源目录仍在界面中选择：

```sh
isolation_root="$(mktemp -d /tmp/pinion-first-run.XXXXXX)"
PINION_DATA_ROOT="$isolation_root" \
  ./build/local/darwin-arm64/Pinion.app/Contents/MacOS/Pinion
```

请使用专用测试资源目录，不复用正式容器或账号配置。这个选项不改变当前 macOS 和 Rosetta 的安装状态。
