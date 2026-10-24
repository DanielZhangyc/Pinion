package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// UpdateState is published by the release checker on the UI thread.
type UpdateState struct {
	Stage, CurrentVersion, LatestVersion, Error string
}

func (a *App) SetUpdate(state UpdateState) { a.update = state }
func (a *App) OnCheckUpdate(fn func())     { a.onCheckUpdate = fn }
func (a *App) OnOpenUpdate(fn func())      { a.onOpenUpdate = fn }

func (a *App) updateNotice(c *ui.Context) {
	s := a.update
	if s.Stage != "available" || a.dismissedUpdate == s.LatestVersion || a.page == settings {
		return
	}
	t := c.Theme()
	ui.Column(c.Key("update-notice")).FillWidth().Padding(12, 36, 0).Children(func() {
		ui.Row(c).FillWidth().Padding(12).Gap(12).AlignItems(ui.Center).
			Background(t.Surface).Radius(controlRadius).Children(func() {
			ui.Text(c, fmt.Sprintf("Pinion %s 可用", s.LatestVersion)).Grow(1).FontSize(fontBody)
			a.openUpdateButton(c)
			button := ui.ButtonBase(c.Key("dismiss-update")).Height(36).Padding(0, 12).Label("稍后").
				OnClick(func() { a.dismissedUpdate = s.LatestVersion })
			a.buttonFeedback(button, t, false, false)
			button.Children(func() { ui.Text(c, "稍后").FontSize(fontBody) })
		})
		if s.Error != "" {
			ui.Text(c, s.Error).FillWidth().FontSize(fontSmall).TextColor(t.Danger)
		}
	})
}

func (a *App) updateSettings(c *ui.Context) {
	s := a.update
	t := c.Theme()
	ui.Column(c.Key("update-settings")).FillWidth().MaxWidth(640).Gap(12).Children(func() {
		ui.Text(c, "软件更新").FontSize(fontBody).Bold()
		version := s.CurrentVersion
		if version == "" {
			version = "开发版本"
		}
		ui.Text(c, fmt.Sprintf("Pinion %s", version)).FontSize(fontBody).TextColor(t.TextMuted)
		status := "启动时自动检查更新"
		switch s.Stage {
		case "checking":
			status = "正在检查更新…"
		case "available":
			status = fmt.Sprintf("发现新版本 %s", s.LatestVersion)
		case "current":
			status = "当前没有可用更新"
		case "unpublished":
			status = "暂未找到公开的正式版本"
		case "failed":
			status = "检查更新失败，请稍后重试"
		}
		ui.Text(c, status).FontSize(fontSmall).TextColor(t.TextMuted)
		if s.Error != "" {
			message := ui.Text(c, s.Error).FillWidth().FontSize(fontSmall).TextColor(t.Danger)
			tooltip(c, message, s.Error)
		}
		ui.Row(c).Gap(8).Children(func() {
			button := ui.ButtonBase(c.Key("check-update")).Height(36).Padding(0, 16).Label("检查更新").
				Disabled(s.Stage == "checking" || a.onCheckUpdate == nil).OnClick(func() { a.onCheckUpdate() })
			a.buttonFeedback(button, t, false, s.Stage != "available")
			button.Children(func() { ui.Text(c, "检查更新").FontSize(fontBody) })
			if s.Stage == "available" {
				a.openUpdateButton(c)
			}
		})
	})
}

func (a *App) openUpdateButton(c *ui.Context) {
	button := ui.ButtonBase(c.Key("open-update")).Height(36).Padding(0, 16).Label("查看更新").
		Disabled(a.onOpenUpdate == nil).OnClick(func() { a.onOpenUpdate() })
	a.buttonFeedback(button, c.Theme(), false, true)
	tooltip(c, button, "打开 GitHub Release，查看说明并下载安装包")
	button.Children(func() { ui.Text(c, "查看更新").FontSize(fontBody) })
}
