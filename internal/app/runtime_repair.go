package app

import "github.com/egoist/mygo/ui"

type RuntimeRepairState struct {
	Stage, Message string
}

func (a *App) SetRuntimeRepair(state RuntimeRepairState) { a.runtimeRepair = state }
func (a *App) OnRuntimeRepair(fn func())                 { a.onRuntimeRepair = fn }

func (a *App) RuntimeRepairAvailable() bool {
	return a.runtimeRepair.Stage != "repairing" && a.runtimeRepairBlocked() == ""
}

func (a *App) runtimeRepairBlocked() string {
	if a.runtime.Stage != "ready" {
		return "请先完成运行环境准备。"
	}
	for _, game := range []string{"arknights", "endfield"} {
		if a.GameBusy(game) {
			return "请先退出游戏，再修复启动环境。"
		}
		switch a.downloads[game].Stage {
		case "preparing", "downloading", "extracting", "verifying", "pausing":
			return "请等待游戏下载或更新结束，再修复启动环境。"
		}
	}
	return ""
}

func (a *App) runtimeRepairAction(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("runtime-repair")).FillWidth().Gap(8).Children(func() {
		ui.Text(c, "用于修复点启动后立即失败的问题。游戏文件和存档会保留。").FillWidth().FontSize(fontSmall).TextColor(t.TextMuted)
		label := "修复启动环境"
		if a.runtimeRepair.Stage == "repairing" {
			label = "正在修复启动环境"
		}
		button := ui.ButtonBase(c.Key("runtime-repair-action")).Height(40).Padding(0, 16).Label(label).
			Disabled(a.onRuntimeRepair == nil || !a.RuntimeRepairAvailable()).OnClick(func() { a.onRuntimeRepair() })
		a.buttonFeedback(button, t, false, false)
		button.Children(func() { ui.Text(c, label).FontSize(fontBody) })
		message := a.runtimeRepair.Message
		if reason := a.runtimeRepairBlocked(); reason != "" && a.runtimeRepair.Stage != "repairing" {
			message = reason
		}
		if message != "" {
			color := t.TextMuted
			if a.runtimeRepair.Stage == "failed" {
				color = t.Danger
			}
			ui.Text(c, message).FillWidth().FontSize(fontSmall).TextColor(color)
		}
	})
}
