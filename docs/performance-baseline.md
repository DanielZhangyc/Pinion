# 原生界面初始性能记录

测量时间：2026-10-21，Asia/Singapore。本记录对应初始化版本，仅包含游戏库、运行环境两个页面及导航，没有游戏、Wine、下载任务、大图、模糊或持续动画。

## 环境与结果

- 本机：Apple M5 Pro，48 GiB 内存。
- Go 1.27.2，MyGo v0.5.5，darwin/arm64，CGO_ENABLED=0。
- 生产构建，ad-hoc 签名，默认窗口 1080×700 逻辑尺寸，NSHighResolutionCapable=true。
- 测量时设置 MYGO_FRAME_STATS=all；日志明确记录 drawn on the GPU。

| 指标 | 本次结果 | 范围 |
|---|---|---|
| 应用包磁盘占用 | 约 8.9 MiB | `du -sh`，包括图标和包内资源，不包括 Go 工具链及构建缓存 |
| 空闲物理内存 | 约 54 MB | `footprint -p`，同一进程的 `top` MEM 也为 54M |
| 物理内存峰值 | 约 178 MB | `footprint` 的 phys_footprint_peak，包含启动与界面交互阶段 |
| 空闲 CPU | 0.0% | `top` 的两次采样，间隔约两秒；是短时基线，不是长期平均值 |
| 首次记录的首帧 | 561.8 ms | CPU 侧总帧耗时，其中 present 调用 545.3 ms；日志显示管线仍在准备 |
| 后续简单交互帧 | 多数约 0.2–0.7 ms | 观察到的日志范围，初次激活还出现 12.9 ms；不等价于完整 GPU 执行计时 |

`ps` 同期 RSS 约 123 MiB。RSS 与 macOS physical footprint 口径不同，不能把它们混成同一个内存数字。初始峰值也说明“空闲 54 MB”不表示整个生命周期都只有这个占用。

## 复现

```sh
make build
direnv exec . env MYGO_FRAME_STATS=all \
  './build/darwin-arm64/Gryph Launcher.app/Contents/MacOS/Gryph Launcher'
```

另开终端，根据上述实际进程的 PID 执行：

```sh
footprint -p <PID>
top -l 2 -s 2 -pid <PID> -stats pid,command,cpu,mem,threads
```

保持界面静止后再读取空闲采样。框架只在输入、状态变化或动画期间请求绘制；日志中的帧耗时是框架报告的 CPU 侧阶段耗时，不能当作 Metal GPU timestamp 或高刷新率稳定性的证明。

这个结果验证了当前基础界面的开销，没有比较 SwiftUI／GPUI，也不能推导完整下载器、复杂视觉效果或游戏运行时的占用。后续应在真实图片、窗口缩放、后台下载、解压和游戏同时运行的条件下复测。
