package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// RuntimeState is shared by both game pages and published on the UI thread.
type RuntimeState struct {
	Stage, ID, Error      string
	DoneBytes, TotalBytes int64
}

func (a *App) SetRuntime(state RuntimeState) { a.runtime = state }
func (a *App) RuntimeReady() bool            { return a.runtime.Stage == "ready" }
func (a *App) OnRuntimeAction(fn func())     { a.onRuntimeAction = fn }
func (a *App) OnInstallRosetta(fn func())    { a.onInstallRosetta = fn }

func (s RuntimeState) busy() bool {
	switch s.Stage {
	case "checking", "downloading", "verifying", "extracting", "pausing":
		return true
	}
	return false
}

func (a *App) runtimeDetails(c *ui.Context, expanded bool) {
	s := a.runtime
	if s.Stage == "ready" && !expanded {
		return
	}
	status := "正在检查运行环境"
	switch s.Stage {
	case "downloading":
		status = "正在下载运行环境"
	case "verifying":
		status = "正在校验运行环境"
	case "extracting":
		status = "正在安装运行环境"
	case "ready":
		status = "FineWine 已就绪"
	case "failed":
		status = "运行环境准备失败"
	case "paused":
		status = "运行环境准备已暂停"
	case "pausing":
		status = "正在暂停运行环境准备"
	case "rosetta":
		status = "需要安装 Rosetta"
	}
	color := c.Theme().TextMuted
	if s.Stage == "failed" || s.Stage == "rosetta" {
		color = c.Theme().Danger
	}
	label := ui.Text(c, status).FontSize(fontSmall).TextColor(color)
	if s.Error != "" {
		if expanded {
			ui.Text(c, s.Error).FontSize(fontSmall).FillWidth().TextColor(c.Theme().TextMuted)
		} else {
			tooltip(c, label, s.Error)
		}
	}
	if expanded && s.Stage == "ready" {
		ui.Text(c, s.ID).FontSize(fontSmall).TextColor(c.Theme().TextMuted)
	}
	if s.busy() && s.TotalBytes > 0 {
		ratio := max(0, min(1, float64(s.DoneBytes)/float64(s.TotalBytes)))
		ui.Progress(c.Key("runtime-progress"), ratio).FillWidth().Label(status)
		ui.Text(c, fmt.Sprintf("%.1f%% · %s / %s", ratio*100, formatBytes(s.DoneBytes), formatBytes(s.TotalBytes))).FontSize(fontSmall).TextColor(c.Theme().TextMuted)
	}
	if !expanded && (s.Stage == "failed" || s.Stage == "paused" || s.Stage == "rosetta") {
		button := ui.ButtonBase(c.Key("runtime-settings-link")).Height(32).Padding(0, 8).Label("设置运行环境").OnClick(func() { a.navigate(settings) })
		a.buttonFeedback(button, c.Theme(), false, false)
		button.Children(func() { ui.Text(c, "设置运行环境").FontSize(fontSmall) })
	}
}

func (a *App) runtimeActions(c *ui.Context) {
	s := a.runtime
	if s.Stage == "ready" {
		return
	}
	ui.Row(c).Gap(8).Children(func() {
		if s.Stage == "rosetta" {
			button := ui.ButtonBase(c.Key("install-rosetta")).Height(40).Padding(0, 16).Label("安装 Rosetta").Disabled(a.onInstallRosetta == nil).OnClick(func() { a.onInstallRosetta() })
			a.buttonFeedback(button, c.Theme(), false, true)
			button.Children(func() { ui.Text(c, "安装 Rosetta").FontSize(fontBody) })
		}
		label := "重新准备"
		if s.busy() {
			label = "暂停准备"
		}
		if s.Stage == "paused" {
			label = "继续准备"
		}
		if s.Stage == "rosetta" {
			label = "重新检查"
		}
		button := ui.ButtonBase(c.Key("runtime-action")).Height(40).Padding(0, 16).Label(label).Disabled(a.onRuntimeAction == nil || s.Stage == "pausing").OnClick(func() { a.onRuntimeAction() })
		a.buttonFeedback(button, c.Theme(), false, s.Stage != "rosetta")
		button.Children(func() { ui.Text(c, label).FontSize(fontBody) })
	})
}
