// Package app contains the launcher's native interface and navigation state.
package app

import (
	"time"

	"github.com/egoist/mygo/ui"
)

type page uint8

const (
	arknights page = iota
	endfield
	settings
)

const (
	sidebarWidth          = 232
	collapsedSidebarWidth = 82
)

// Presentation describes user choices, independently of native player objects.
type Presentation struct {
	Endfield     bool
	Background   string
	SidebarWidth float64
	Paused       bool
}

// GlassKind identifies a native backdrop material in window coordinates.
type GlassKind uint8

const (
	GlassSidebar GlassKind = iota
	GlassCard
)

// GlassPainter synchronizes native material geometry during painting.
type GlassPainter func(kind GlassKind, rect ui.Rect, radius float32, dark bool)

var drawerEase = ui.CubicBezier(.32, .72, 0, 1)

// App owns persistent state. UI elements belong only to their current build.
type App struct {
	page               page
	artwork            *Artwork
	collapsed          bool
	compactWidth       float32
	videoPaused        bool
	videoReady         bool
	videoAvailable     bool
	videoStatus        string
	mediaStatus        string
	mediaError         string
	playbackError      string
	gameError          string
	onPresentation     func(Presentation)
	glassPainter       GlassPainter
	nativeBackground   bool
	reduceTransparency bool
	downloadDirectory  string
	directoryBusy      bool
	directoryError     string
	downloads          map[string]DownloadState
	gameUpdates        map[string]GameUpdateState
	launches           map[string]LaunchState
	onLaunch           func(string)
	onOpenLaunchLog    func(string)
	onChooseDirectory  func()
	onDownload         func(string)
	onOpenDownload     func(string)
	runtime            RuntimeState
	onRuntimeAction    func()
	onInstallRosetta   func()
	update             UpdateState
	dismissedUpdate    string
	onCheckUpdate      func()
	onOpenUpdate       func()
}

func New() *App {
	return &App{page: endfield, compactWidth: collapsedSidebarWidth, mediaStatus: "正在检查官方素材…", downloads: make(map[string]DownloadState), gameUpdates: make(map[string]GameUpdateState), launches: make(map[string]LaunchState), runtime: RuntimeState{Stage: "checking"}}
}

func (a *App) Presentation() Presentation {
	width := float64(sidebarWidth)
	if a.collapsed {
		width = float64(a.compactWidth)
	}
	background := ""
	if a.page == arknights {
		background = "arknights"
	} else if a.page == endfield {
		background = "endfield"
	}
	return Presentation{Endfield: a.page == endfield, Background: background, SidebarWidth: width, Paused: a.videoPaused}
}

func (a *App) SetNativeBackground(ready bool) { a.nativeBackground = ready }

func (a *App) SetGlassPainter(painter GlassPainter) { a.glassPainter = painter }

func (a *App) SetReduceTransparency(reduce bool) { a.reduceTransparency = reduce }

func (a *App) OnPresentationChange(fn func(Presentation)) {
	a.onPresentation = fn
	a.present()
}

func (a *App) present() {
	if a.onPresentation != nil {
		a.onPresentation(a.Presentation())
	}
}

func (a *App) navigate(destination page) {
	a.page = destination
	a.present()
}

func (a *App) SetMediaStatus(status string, err error) {
	a.mediaStatus = status
	a.mediaError = ""
	if err != nil {
		a.mediaError = err.Error()
	}
}

func (a *App) SetVideoState(available, ready bool, status string, err error) {
	a.videoAvailable, a.videoReady, a.videoStatus = available, ready, status
	a.playbackError = ""
	if err != nil {
		a.playbackError = err.Error()
	}
}

func (a *App) SetGameDetectionError(err error) {
	a.gameError = ""
	if err != nil {
		a.gameError = err.Error()
	}
}

// SetArtwork publishes decoded assets on the UI thread through Window.Update.
func (a *App) SetArtwork(artwork *Artwork) {
	a.artwork = artwork
}

func (a *App) View(c *ui.Context) {
	// Copy the current system theme on each build so appearance changes keep
	// propagating after the application opens.
	colors := a.palette(*c.Theme())
	theme := colors.Theme
	c.SetTheme(&theme)
	// Native imagery and blur sit below the UI's tinted palette surfaces.
	c.Root().Background(ui.Transparent)
	root := ui.Column(c.Key("window")).Fill().TextColor(theme.Text)
	bar := c.TitleBar()
	compactWidth := compactSidebarWidth(bar.Left)
	targetWidth := float32(sidebarWidth)
	if a.collapsed {
		targetWidth = compactWidth
	}
	width := root.AnimateWith("sidebar-width", targetWidth, 240*time.Millisecond, drawerEase)
	root.Draw(func(p *ui.Painter, r ui.Rect) {
		if !a.nativeBackground || a.page == settings {
			p.Fill(r, theme.Background, 0)
			if a.page != settings && a.artwork != nil {
				p.Image(a.artwork.background(a.page), r, ui.Cover)
			}
		}
		sidebar := ui.Rect{X: r.X, Y: r.Y, W: width, H: r.H}
		a.glass(GlassSidebar, sidebar, 0, theme.Dark)
		p.Fill(sidebar, a.materialTint(colors.panel, .4), 0)
		p.Fill(ui.Rect{X: r.X + width - hairline, Y: r.Y, W: hairline, H: r.H}, colors.divider, 0)
	})
	root.Children(func() {
		// The header remains draggable; the compact toggle sits below the lights.
		ui.Row(c.Key("header")).FillWidth().Height(52).Gap(0).Children(func() {
			ui.Box(c).Fill().DragWindow()
		})
		ui.Row(c.Key("body")).FillWidth().Grow(1).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c.Key("sidebar")).Label("游戏导航").Width(width).Shrink(0).FillHeight().Clip().
				Padding(max(16, max(bar.Height, 28)+40-52), 12, 16).Gap(6).Children(func() {
				a.navigation(c, "arknights", "明日方舟", arknights, arknightsIcon, width)
				a.navigation(c, "endfield", "明日方舟：终末地", endfield, endfieldIcon, width)
				ui.Spacer(c)
				a.navigation(c, "settings", "设置", settings, settingsIcon, width)
			})
			content := ui.Column(c.Key("main")).Grow(1).FillHeight().Clip()
			if a.page == settings {
				content.Background(theme.Background)
			}
			content.Children(func() {
				a.updateNotice(c)
				var body ui.Element
				if a.page == settings {
					body = ui.Scroll(c).FillWidth().Grow(1)
				} else {
					body = ui.Column(c).FillWidth().Grow(1)
				}
				body.Children(func() {
					inner := ui.Column(c).FillWidth().Padding(12, 36, 36).Gap(28)
					if a.page != settings {
						inner.FillHeight()
					}
					inner.Children(func() {
						switch a.page {
						case arknights:
							a.game(c, "明日方舟", arknightsIcon)
						case endfield:
							a.game(c, "明日方舟：终末地", endfieldIcon)
						case settings:
							a.settings(c)
						}
					})
				})
			})
		})
		a.sidebarToggle(c, compactWidth, max(bar.Height, 28))
	})
}

// TitleBar.Left ends at the zoom button's right edge, without trailing padding.
func compactSidebarWidth(trafficLightsRight float32) float32 {
	if trafficLightsRight <= 0 {
		trafficLightsRight = 70
	}
	return trafficLightsRight + 12
}

func (a *App) sidebarToggle(c *ui.Context, compactWidth, top float32) {
	t := c.Theme()
	label := "收起侧边栏"
	if a.collapsed {
		label = "展开侧边栏"
	}
	button := ui.ButtonBase(c.Key("toggle-sidebar")).Absolute().Left((compactWidth-32)/2).Top(top).
		Size(32, 32).Label(label)
	keyboard := button.FocusVisible()
	button.OnClick(func() {
		if keyboard {
			c.DisableAnimations()
		}
		a.compactWidth = compactWidth
		a.collapsed = !a.collapsed
		a.present()
	})
	a.buttonFeedback(button, t, false, false)
	if !button.Hovered() && !button.Pressed() {
		button.Background(ui.Transparent).Border(0, ui.Transparent)
	}
	tooltip(c, button, label)
	button.Children(func() { ui.Icon(c, sidebarIcon).Size(16, 16) })
}

func (a *App) navigation(c *ui.Context, key, label string, destination page, icon *ui.SVG, width float32) {
	t := c.Theme()
	selected := a.page == destination
	iconPadding := max(0, (compactSidebarWidth(c.TitleBar().Left)-24)/2-12)
	button := ui.ButtonBase(c.Key(key)).FillWidth().Height(44).Padding(0, iconPadding).
		Gap(12).Clip().Label(label).Selected(selected).
		OnClick(func() { a.navigate(destination) })
	if a.collapsed {
		tooltip(c, button, label)
	}
	a.buttonFeedback(button, t, selected, false)
	button.Border(0, ui.Transparent)
	if !selected && !button.Hovered() && !button.Pressed() {
		button.Background(ui.Transparent)
	}
	button.Children(func() {
		ui.Icon(c, icon).Size(24, 24).Shrink(0)
		// Fixed text geometry prevents squeezed glyphs while the sidebar clips.
		text := ui.Text(c, label).FontSize(fontBody).SingleLine().Width(144).Shrink(0).
			Opacity(min(1, max(0, (width-100)/132)))
		if selected {
			text.Bold()
		}
	})
}

func (a *App) game(c *ui.Context, title string, icon *ui.SVG) {
	t := c.Theme()
	colors := radixPalette(*t)
	ui.Spacer(c)
	ui.Column(c.Key("launch-card")).Label("游戏启动卡片").FillWidth().MaxWidth(460).AlignSelf(ui.End).
		Padding(panelInset).Gap(12).Radius(panelRadius).Background(a.materialTint(colors.panel, .4)).Border(hairline, colors.divider).
		Draw(func(_ *ui.Painter, r ui.Rect) { a.glass(GlassCard, r, panelRadius, t.Dark) }).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Padding(4).Children(func() {
			ui.Icon(c, icon).Size(24, 24).Shrink(0)
			ui.Text(c, title).Label("当前游戏").FontSize(fontTitle).Bold()
		})
		launchReady := a.downloads[a.selectedGame()].Stage == "completed"
		a.runtimeDetails(c, false)
		if launchReady {
			a.launchDetails(c)
		} else {
			a.downloadDetails(c)
		}
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if launchReady {
				a.launchButtons(c)
				return
			}
			a.downloadButton(c)
			a.launchPreviousVersion(c)
			ui.Spacer(c)
			if a.page == endfield && a.videoAvailable {
				a.playback(c)
			}
		})
	})
}

func (a *App) glass(kind GlassKind, rect ui.Rect, radius float32, dark bool) {
	if a.glassPainter != nil {
		a.glassPainter(kind, rect, radius, dark)
	}
}

func (a *App) materialTint(color ui.Color, opacity float32) ui.Color {
	if a.nativeBackground && a.glassPainter != nil && !a.reduceTransparency {
		return color.Alpha(opacity)
	}
	return color
}

// buttonFeedback keeps the native accessibility and keyboard behavior of ButtonBase.
func (a *App) buttonFeedback(button ui.Element, t *ui.Theme, selected, primary bool) {
	colors := radixPalette(*t)
	fill, border, text := t.Surface, t.Border, t.Text
	if selected {
		fill = t.SurfacePressed
	}
	if button.Hovered() {
		fill, border = t.SurfaceHover, colors.borderHover
	}
	if button.Pressed() {
		fill = t.SurfacePressed
	}
	if primary {
		fill, border, text = t.Accent, t.Accent, t.AccentText
		if button.Hovered() {
			fill, border = t.AccentHover, t.AccentHover
		}
		if button.Pressed() {
			fill, border = t.AccentPressed, t.AccentPressed
		}
	}
	button.Radius(controlRadius).Background(fill).TextColor(text).Border(hairline, border).FocusRing(false)
	if !button.IsDisabled() {
		button.Cursor(ui.CursorPointer)
	}
	focused := button.FocusVisible()
	button.DrawOver(func(p *ui.Painter, r ui.Rect) {
		if focused {
			p.Stroke(ui.Rect{X: r.X - 2, Y: r.Y - 2, W: r.W + 4, H: r.H + 4}, t.Focus, controlRadius+2, hairline)
		}
	})
}

func (a *App) playback(c *ui.Context) {
	t := c.Theme()
	label, icon := "暂停视频背景", pauseIcon
	if a.videoPaused {
		label, icon = "播放视频背景", playIcon
	}
	if c.Preferences().ReduceMotion {
		label = "已按系统设置暂停背景"
	}
	button := ui.ButtonBase(c.Key("toggle-video")).Size(40, 40).Label(label).
		Disabled(c.Preferences().ReduceMotion).
		OnClick(func() { a.videoPaused = !a.videoPaused; a.present() })
	a.buttonFeedback(button, t, false, false)
	tooltip(c, button, label)
	button.Children(func() { ui.Icon(c, icon).Size(18, 18) })
}

func (a *App) settings(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, "设置").FontSize(fontTitle).Bold()
	a.updateSettings(c)
	ui.Column(c.Key("runtime-settings")).FillWidth().MaxWidth(640).Gap(12).Children(func() {
		ui.Text(c, "游戏运行环境").FontSize(fontBody).Bold()
		a.runtimeDetails(c, true)
		a.runtimeActions(c)
	})
	ui.Column(c).FillWidth().MaxWidth(640).Gap(16).Children(func() {
		ui.Text(c, "游戏下载目录").FontSize(fontBody).Bold()
		path := a.downloadDirectory
		if path == "" {
			path = "未选择目录"
		}
		label := "选择下载目录"
		if a.downloadDirectory != "" {
			label = "更改目录"
		}
		if a.directoryBusy {
			label = "正在检查目录"
		}
		ui.Row(c).FillWidth().Gap(16).AlignItems(ui.Center).Children(func() {
			location := ui.Text(c, path).FillWidth().SingleLine().FontSize(fontBody).TextColor(t.TextMuted)
			tooltip(c, location, path)
			button := ui.ButtonBase(c.Key("choose-directory")).Height(40).Padding(0, 16).Label(label).
				Disabled(a.directoryBusy || a.onChooseDirectory == nil).OnClick(func() { a.onChooseDirectory() })
			a.buttonFeedback(button, t, false, true)
			tooltip(c, button, "选择原目录可继续下载；更改目录不会移动文件")
			button.Children(func() { ui.Text(c, label).FontSize(fontBody) })
		})
		if a.directoryError != "" {
			message := ui.Text(c, "无法使用此目录").FontSize(fontSmall).TextColor(t.Danger)
			tooltip(c, message, a.directoryError)
		}
	})
}
