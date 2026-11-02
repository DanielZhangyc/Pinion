package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// DownloadState is presentation data, published only on the UI thread.
type DownloadState struct {
	Stage, Version, Directory, File, Error string
	DoneBytes, TotalBytes, RequiredBytes   int64
	BytesPerSecond                         float64
	Updating                               bool
}

func (a *App) OnChooseDirectory(fn func())                  { a.onChooseDirectory = fn }
func (a *App) OnDownload(fn func(string))                   { a.onDownload = fn }
func (a *App) OnOpenDownload(fn func(string))               { a.onOpenDownload = fn }
func (a *App) SetDownload(game string, state DownloadState) { a.downloads[game] = state }

func (a *App) SetDownloadDirectory(path string, busy bool, err error) {
	a.downloadDirectory, a.directoryBusy, a.directoryError = path, busy, ""
	if err != nil {
		a.directoryError = err.Error()
	}
}

func (a *App) selectedGame() string {
	if a.page == arknights {
		return "arknights"
	}
	return "endfield"
}

func (a *App) downloadDetails(c *ui.Context) {
	t := c.Theme()
	s := a.downloads[a.selectedGame()]
	status := ""
	switch s.Stage {
	case "loading-record":
		status = "读取下载记录"
	case "checking":
		status = "获取下载信息"
	case "preparing":
		status = "检查下载目录"
	case "downloading":
		status = "正在下载"
	case "extracting":
		status = "正在解压"
	case "verifying":
		status = "正在校验"
	case "pausing":
		status = "正在暂停"
	case "paused":
		status = "下载已暂停"
	case "failed":
		status = "下载失败"
	}
	if s.Updating {
		switch s.Stage {
		case "downloading":
			status = "正在下载更新"
		case "extracting":
			status = "正在安装更新"
		case "verifying":
			status = "正在校验更新"
		case "paused":
			status = "更新已暂停"
		}
	}
	if s.Error != "" || s.Stage == "failed" {
		label := "下载失败"
		if s.Updating {
			label = "更新失败，当前版本已保留"
		}
		failure := ui.Text(c, label).FontSize(fontBody).TextColor(t.Danger)
		tooltip(c, failure, s.Error)
	} else if status != "" {
		ui.Text(c, status).FontSize(fontBody)
	}
	switch s.Stage {
	case "ready":
		ui.Row(c).FillWidth().Gap(16).Children(func() {
			ui.Text(c, "下载 "+formatBytes(s.TotalBytes)).FontSize(fontSmall).TextColor(t.TextMuted)
			ui.Text(c, "所需空间 "+formatBytes(s.RequiredBytes)).FontSize(fontSmall).TextColor(t.TextMuted)
		})
	case "downloading", "extracting", "verifying", "paused", "pausing":
		if s.TotalBytes > 0 {
			ratio := max(0, min(1, float64(s.DoneBytes)/float64(s.TotalBytes)))
			ui.Progress(c.Key("download-progress"), ratio).FillWidth().Label(status)
			ui.Row(c).FillWidth().Gap(16).Children(func() {
				ui.Text(c, fmt.Sprintf("%.1f%%", ratio*100)).FontSize(fontSmall).TextColor(t.TextMuted)
				ui.Text(c, "剩余 "+formatBytes(max(0, s.TotalBytes-s.DoneBytes))).FontSize(fontSmall).TextColor(t.TextMuted)
				ui.Spacer(c)
				if s.Stage == "downloading" && s.BytesPerSecond > 0 {
					ui.Text(c, formatBytes(int64(s.BytesPerSecond))+"/s").FontSize(fontSmall).TextColor(t.TextMuted)
				}
			})
		}
	}
}

func (a *App) downloadButton(c *ui.Context) {
	game := a.selectedGame()
	s := a.downloads[game]
	label, disabled := "下载游戏", a.onDownload == nil || a.directoryBusy
	if a.downloadDirectory == "" {
		label, disabled = "选择下载目录", false
	} else {
		switch s.Stage {
		case "loading-record":
			label, disabled = "读取记录中", true
		case "checking", "preparing", "downloading", "extracting", "verifying":
			label = "暂停"
		case "ready":
			label = "开始下载"
			if s.Updating {
				label = "开始更新"
			}
		case "paused":
			label = "继续下载"
			if s.Updating {
				label = "继续更新"
			}
		case "pausing":
			label, disabled = "正在暂停", true
		case "failed":
			label = "重试"
		case "completed":
			label, disabled = "打开目录", a.onOpenDownload == nil
		}
	}
	if s.Updating && a.GameBusy(game) {
		label, disabled = "游戏运行中", true
	}
	button := ui.ButtonBase(c.Key("download-action")).Height(40).Padding(0, 16).
		Label(label).Disabled(disabled).OnClick(func() {
		if a.downloadDirectory == "" {
			a.navigate(settings)
		} else if s.Stage == "completed" {
			a.onOpenDownload(game)
		} else {
			a.onDownload(game)
		}
	})
	a.buttonFeedback(button, c.Theme(), false, true)
	button.Children(func() { ui.Text(c, label).FontSize(fontBody) })
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), "B"
	for _, suffix = range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}
