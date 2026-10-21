// Package app contains the launcher's native interface and navigation state.
package app

import "github.com/egoist/mygo/ui"

type page uint8

const (
	library page = iota
	runtime
)

// App owns persistent state. UI elements belong only to their current build.
type App struct {
	page  page
	theme *ui.Theme
}

func New() *App {
	return &App{theme: ui.DarkTheme()}
}

func (a *App) View(c *ui.Context) {
	c.SetTheme(a.theme)
	t := c.Theme()
	ui.Row(c).Fill().Background(t.Background).TextColor(t.Text).Children(func() {
		ui.Column(c).Width(220).FillHeight().Padding(24, 16).Gap(12).
			BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "GRYPH").FontSize(25).Bold().Padding(0, 12)
			ui.Text(c, "Launcher").TextColor(t.TextMuted).Padding(0, 12)
			ui.Divider(c).Margin(16, 0)
			a.navigation(c, "library", "游戏库", library)
			a.navigation(c, "runtime", "运行环境", runtime)
			ui.Spacer(c)
			ui.Text(c, "本地开发版").FontSize(12).TextColor(t.TextMuted).Padding(0, 12)
		})
		ui.Column(c).Grow(1).FillHeight().Padding(36).Gap(24).Children(func() {
			switch a.page {
			case library:
				a.library(c)
			case runtime:
				a.runtime(c)
			}
		})
	})
}

func (a *App) navigation(c *ui.Context, key, label string, destination page) {
	t := c.Theme()
	button := ui.ButtonBase(c.Key(key)).FillWidth().Padding(12).Radius(8).
		Label(label).OnClick(func() { a.page = destination })
	if a.page == destination {
		button.Background(t.Surface).TextColor(t.Text)
	} else {
		button.TextColor(t.TextMuted)
		if button.Hovered() {
			button.Background(t.SurfaceHover)
		}
	}
	button.Children(func() { ui.Text(c, label) })
}

func (a *App) library(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, "游戏库").FontSize(24).Bold()
	ui.Column(c).FillWidth().Grow(1).Padding(32).Gap(16).Radius(16).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Text(c, "ARKNIGHTS: ENDFIELD").FontSize(14).TextColor(t.TextMuted)
		ui.Text(c, "明日方舟：终末地").FontSize(32).Bold()
		ui.Spacer(c)
		ui.Text(c, "尚未导入游戏").FontSize(20).Bold()
		ui.Text(c, "导入游戏并配置运行环境后，即可在这里启动。").TextColor(t.TextMuted)
		ui.Row(c).Gap(12).Margin(8, 0, 0, 0).Children(func() {
			ui.PrimaryButton(c.Key("show-runtime"), "查看运行环境").
				OnClick(func() { a.page = runtime })
		})
	})
}

func (a *App) runtime(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, "运行环境").FontSize(24).Bold()
	ui.Column(c).FillWidth().Grow(1).Padding(32).Gap(16).Radius(16).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Text(c, "尚未配置 Wine").FontSize(24).Bold()
		ui.Text(c, "运行环境用于在 macOS 上启动 Windows 游戏。").TextColor(t.TextMuted)
		ui.Text(c, "运行环境导入与游戏启动功能正在开发。").TextColor(t.TextMuted)
		ui.Spacer(c)
		ui.Button(c.Key("back-to-library"), "返回游戏库").AlignSelf(ui.Start).
			OnClick(func() { a.page = library })
	})
}
