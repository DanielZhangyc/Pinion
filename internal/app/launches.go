package app

import "github.com/egoist/mygo/ui"

// LaunchState is presentation data, published only on the UI thread.
type LaunchState struct {
	Stage, Error, LogPath string
	DetectionError        string
}

func (a *App) OnLaunch(fn func(string))        { a.onLaunch = fn }
func (a *App) OnOpenLaunchLog(fn func(string)) { a.onOpenLaunchLog = fn }
func (a *App) SetLaunch(game string, state LaunchState) {
	a.launches[game] = state
}

func (a *App) launchDetails(c *ui.Context) {
	s := a.launches[a.selectedGame()]
	if s.Stage == "failed" {
		failure := ui.Text(c, "启动失败").FontSize(fontBody).TextColor(c.Theme().Danger)
		tooltip(c, failure, s.Error)
	}
	if detail := a.downloads[a.selectedGame()].Error; detail != "" {
		failure := ui.Text(c, "无法打开目录").FontSize(fontBody).TextColor(c.Theme().Danger)
		tooltip(c, failure, detail)
	}
}

func (a *App) launchButtons(c *ui.Context) {
	game := a.selectedGame()
	s := a.launches[game]
	label, disabled := "启动游戏", a.onLaunch == nil
	switch s.Stage {
	case "preparing":
		label, disabled = "准备启动", true
	case "running", "stopping":
		label, disabled = "游戏运行中", true
	case "failed":
		label = "重试"
	}
	if !a.RuntimeReady() && s.Stage != "running" && s.Stage != "stopping" && s.Stage != "preparing" {
		label, disabled = "等待运行环境", true
	}
	button := ui.ButtonBase(c.Key("launch-action")).Height(40).Padding(0, 16).
		Label(label).Disabled(disabled).OnClick(func() { a.onLaunch(game) })
	a.buttonFeedback(button, c.Theme(), false, true)
	button.Children(func() { ui.Text(c, label).FontSize(fontBody) })

	open := ui.ButtonBase(c.Key("open-game-directory")).Height(40).Padding(0, 12).
		Label("打开目录").Disabled(a.onOpenDownload == nil).OnClick(func() { a.onOpenDownload(game) })
	a.buttonFeedback(open, c.Theme(), false, false)
	open.Children(func() { ui.Text(c, "打开目录").FontSize(fontBody) })
	if s.Stage == "failed" && s.LogPath != "" {
		log := ui.ButtonBase(c.Key("open-launch-log")).Height(40).Padding(0, 12).
			Label("查看日志").Disabled(a.onOpenLaunchLog == nil).OnClick(func() { a.onOpenLaunchLog(game) })
		a.buttonFeedback(log, c.Theme(), false, false)
		log.Children(func() { ui.Text(c, "查看日志").FontSize(fontBody) })
	}
	if a.page == endfield && a.videoAvailable {
		ui.Spacer(c)
		a.playback(c)
	}
}
