# 第三方声明

## 组件

Pinion 使用 MyGo、FineWine / Wine、DXMT、MoltenVK 等开源组件。Go 依赖版本记录在 `go.mod` 和 `go.sum`；FineWine 运行包保留各组件的许可证及来源记录。

官方二进制差分的合成使用 [HDiffPatch](https://github.com/sisong/HDiffPatch) v5.1.3 的 HPatch 核心，固定提交 `3b9dca715ca492873bf2c49e22e5d5b7d2a78620`。必要源码及来源记录位于 `third_party/hdiffpatch/`，MIT 许可全文随应用提供于 [hdiffpatch.txt](hdiffpatch.txt)。当前仅构建无压缩 `HDIFFSF20` 合成所需代码，不包含差分生成器。

更新检查使用 Go 官方 `golang.org/x/mod/semver` 比较版本，BSD 许可全文见 [go-mod.txt](go-mod.txt)。

界面色板采用 [Radix Colors](https://github.com/radix-ui/colors) 的 sRGB 色值，MIT 许可全文见同目录的 [radix-colors.txt](radix-colors.txt)。游戏强调色来自[明日方舟官网](https://ak.hypergryph.com/)和[终末地官网](https://endfield.hypergryph.com/)。

## 游戏素材

游戏名称、标志、背景及截图中的游戏内容属于其各自权利人。Pinion 仅用这些素材识别和展示对应游戏，不表示获得官方认可；项目代码的 GPL 许可不覆盖这些素材。

内置背景保持官方 CDN 文件内容：

- `internal/app/artwork/arknights-background.png`：[明日方舟官方背景](https://hg-utils-public.hycdn.cn/hg-utils/prod/eppcsuwqpaueijqk/GzD1CpaWgmSq1wew/6b/87/6b87cccb699178d4179461386770abfc.png)。
- `internal/app/artwork/endfield-background.webp`：[终末地官方背景](https://hg-utils-public.hycdn.cn/hg-utils/prod/eppcsuwqpaueijqk/6LL0KJuqHBVz33WK/15/d4/15d43bac17c83b83d53f667aeb639952.webp)。

更新的背景图片和视频来自鹰角启动器公开接口 `https://launcher.hypergryph.com/api/proxy/web/batch_proxy`。

游戏标志取自以下社区矢量仓库，保留原几何形状并适配 `currentColor` 与图标画布：

| 本地文件 | 原始文件与固定版本 |
| --- | --- |
| `internal/app/icons/arknights.svg` | [Yue-plus/arknights_icons](https://github.com/Yue-plus/arknights_icons/blob/44ff5143304101fa15ee3418455656bfbc3d1f31/site-icons/logo_rhodes_island.svg) |
| `internal/app/icons/endfield.svg` | [Yue-plus/endfield_icons](https://github.com/Yue-plus/endfield_icons/blob/2e8d430fb505a427ffbeb4a3d22aeb943e475302/svg/endfield-industries.svg) |

两个仓库的 MIT 许可适用于其可许可的贡献，不构成游戏品牌授权。许可全文如下：

```text
MIT License

Copyright (c) 2022 Yue_plus
Copyright (c) 2023 Yue_plus

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
