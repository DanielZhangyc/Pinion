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

func (a *App) GameBusy(game string) bool {
	switch a.launches[game].Stage {
	case "preparing", "running", "stopping":
		return true
	}
	return false
}

// A paused or failed update keeps the previous completed record launchable.
func (a *App) GameLaunchable(game string) bool {
	s := a.downloads[game]
	return s.Stage == "completed" || s.Updating && (s.Stage == "ready" || s.Stage == "paused" || s.Stage == "failed")
}

func (a *App) launchDetails(c *ui.Context) {
	a.gameUpdateDetails(c)
	game := a.selectedGame()
	s := a.launches[game]
	if s.Stage == "failed" {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			failure := ui.Text(c, "启动失败").FillWidth().FontSize(fontBody).TextColor(c.Theme().Danger)
			tooltip(c, failure, s.Error)
			if s.LogPath != "" {
				button := ui.ButtonBase(c.Key("open-launch-log")).Height(40).Padding(0, 12).
					Label("查看日志").Disabled(a.onOpenLaunchLog == nil).OnClick(func() { a.onOpenLaunchLog(game) })
				a.buttonFeedback(button, c.Theme(), false, false)
				button.Children(func() { ui.Text(c, "查看日志").FontSize(fontBody) })
			}
		})
	}
	if detail := a.downloads[a.selectedGame()].Error; detail != "" {
		failure := ui.Text(c, "无法打开目录").FontSize(fontBody).TextColor(c.Theme().Danger)
		tooltip(c, failure, detail)
	}
}

func (a *App) launchButtons(c *ui.Context) {
	game := a.selectedGame()
	s := a.launches[game]
	updating := a.gameUpdates[game].Stage == "available" && !a.GameBusy(game)
	if updating {
		button := ui.ButtonBase(c.Key("update-game")).Height(40).Padding(0, 16).
			Label("更新游戏").Disabled(a.onDownload == nil || a.directoryBusy).
			OnClick(func() { a.onDownload(game) })
		a.buttonFeedback(button, c.Theme(), false, true)
		button.Children(func() { ui.Text(c, "更新游戏").FontSize(fontBody) })
	}
	label, disabled := "启动游戏", a.onLaunch == nil
	switch s.Stage {
	case "preparing":
		label, disabled = "准备启动", true
	case "running", "stopping":
		label, disabled = "游戏运行中", true
	case "failed":
		label = "重试"
	}
	if updating {
		label = "启动当前版本"
	}
	if !a.RuntimeReady() && s.Stage != "running" && s.Stage != "stopping" && s.Stage != "preparing" {
		label, disabled = "等待运行环境", true
	}
	button := ui.ButtonBase(c.Key("launch-action")).Height(40).Padding(0, 16).
		Label(label).Disabled(disabled).OnClick(func() { a.onLaunch(game) })
	a.buttonFeedback(button, c.Theme(), false, !updating)
	button.Children(func() { ui.Text(c, label).FontSize(fontBody) })

	open := ui.ButtonBase(c.Key("open-game-directory")).Height(40).Padding(0, 12).
		Label("打开目录").Disabled(a.onOpenDownload == nil).OnClick(func() { a.onOpenDownload(game) })
	a.buttonFeedback(open, c.Theme(), false, false)
	open.Children(func() { ui.Text(c, "打开目录").FontSize(fontBody) })
	if a.page == endfield && a.videoAvailable {
		ui.Spacer(c)
		a.playback(c)
	}
}

func (a *App) launchPreviousVersion(c *ui.Context) {
	game := a.selectedGame()
	if !a.downloads[game].Updating || !a.GameLaunchable(game) {
		return
	}
	label := "启动当前版本"
	if a.GameBusy(game) {
		label = "游戏运行中"
	}
	button := ui.ButtonBase(c.Key("launch-previous")).Height(40).Padding(0, 12).
		Label(label).Disabled(a.onLaunch == nil || !a.RuntimeReady() || a.GameBusy(game)).
		OnClick(func() { a.onLaunch(game) })
	a.buttonFeedback(button, c.Theme(), false, false)
	button.Children(func() { ui.Text(c, label).FontSize(fontBody) })
}
