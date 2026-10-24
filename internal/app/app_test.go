package app

import (
	"bytes"
	"fmt"
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestNavigationAcrossRebuilds(t *testing.T) {
	artwork, err := LoadArtwork()
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{1080, 700}, {860, 560}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			a := New()
			a.SetArtwork(artwork)
			view := ui.NewTester(a.View, size[0], size[1])
			if a.Presentation().Background != "endfield" {
				t.Fatal("Endfield page did not open initially")
			}
			for range 3 {
				if err := view.Click("设置"); err != nil {
					t.Fatal(err)
				}
				view.Frame()
				if !view.HasText("游戏下载目录") {
					t.Fatal("settings page did not open")
				}
				if err := view.Click("明日方舟"); err != nil {
					t.Fatal(err)
				}
				view.Frame()
				if a.Presentation().Background != "arknights" {
					t.Fatal("Arknights page did not open")
				}
				if err := view.Click("明日方舟：终末地"); err != nil {
					t.Fatal(err)
				}
				view.Frame()
				if a.Presentation().Background != "endfield" {
					t.Fatal("Endfield page did not return after rebuilding")
				}
			}
			if err := view.Click("设置"); err != nil {
				t.Fatal(err)
			}
			if !view.HasText("游戏下载目录") {
				t.Fatal("sidebar navigation did not open download settings")
			}
		})
	}
}

func TestAppearanceChangesWhileOpen(t *testing.T) {
	a := New()
	view := ui.NewTester(a.View, 1080, 700)
	if err := view.Click("设置"); err != nil {
		t.Fatal(err)
	}
	view.SetDark(false)
	light := view.Image().RGBAAt(1060, 680)
	view.SetDark(true)
	dark := view.Image().RGBAAt(1060, 680)
	if brightness(dark) >= brightness(light) {
		t.Fatal("system dark appearance did not update the rendered interface")
	}
	view.SetDark(false)
	if view.Image().RGBAAt(1060, 680) != light {
		t.Fatal("system light appearance did not restore the rendered interface")
	}
}

func TestCollapsedNavigation(t *testing.T) {
	artwork, err := LoadArtwork()
	if err != nil {
		t.Fatal(err)
	}
	a := New()
	a.SetArtwork(artwork)
	view := ui.NewTester(a.View, 860, 560)
	expanded, ok := view.Find("收起侧边栏")
	if !ok {
		t.Fatal("sidebar control missing")
	}
	if err := view.Click("收起侧边栏"); err != nil {
		t.Fatal(err)
	}
	view.Advance(300 * time.Millisecond)
	collapsed, ok := view.Find("展开侧边栏")
	if !ok || collapsed.X != expanded.X || collapsed.Y != expanded.Y {
		t.Fatal("window header control moved after sidebar collapsed")
	}
	sidebar, ok := view.Find("游戏导航")
	if !ok || sidebar.W != collapsedSidebarWidth {
		t.Fatal("sidebar did not reach its collapsed width")
	}
	if err := view.Click("明日方舟"); err != nil {
		t.Fatal(err)
	}
	view.Frame()
	if a.Presentation().Background != "arknights" {
		t.Fatal("collapsed game navigation failed")
	}
	if err := view.Click("设置"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("游戏下载目录") {
		t.Fatal("collapsed settings navigation failed")
	}
	if err := view.Click("明日方舟：终末地"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("展开侧边栏"); err != nil {
		t.Fatal(err)
	}
	view.Advance(300 * time.Millisecond)
	restored, ok := view.Find("收起侧边栏")
	if !ok || restored.X != expanded.X || restored.Y != expanded.Y {
		t.Fatal("expanded sidebar geometry was not restored")
	}
	view.Key(0, ui.KeySpace)
	view.Frame()
	if _, ok := view.Find("展开侧边栏"); !ok {
		t.Fatal("focused sidebar control did not respond to the Space key")
	}
	if sidebar, _ := view.Find("游戏导航"); sidebar.W != collapsedSidebarWidth {
		t.Fatal("keyboard-initiated collapse should finish immediately")
	}
}

func TestSidebarMotionReversesContinuously(t *testing.T) {
	a := New()
	view := ui.NewTester(a.View, 1080, 700)
	navigation, ok := view.Find("设置")
	if !ok {
		t.Fatal("settings navigation missing")
	}
	labelPixels := func() []byte {
		// The label stays inside the sidebar throughout the first motion frame;
		// changes here measure text fading rather than clipping at its edge.
		var pixels []byte
		for y := int(navigation.Y + 12); y < int(navigation.Y+32); y++ {
			for x := int(navigation.X + 53); x < int(navigation.X+81); x++ {
				p := view.Image().RGBAAt(x, y)
				pixels = append(pixels, p.R, p.G, p.B, p.A)
			}
		}
		return pixels
	}
	expandedLabel := labelPixels()
	width := func() float32 {
		r, ok := view.Find("游戏导航")
		if !ok {
			t.Fatal("navigation region missing")
		}
		return r.W
	}
	if err := view.Click("收起侧边栏"); err != nil {
		t.Fatal(err)
	}
	if width() != sidebarWidth {
		t.Fatal("pointer collapse jumped at the start")
	}
	if !bytes.Equal(expandedLabel, labelPixels()) {
		t.Fatal("navigation text disappeared before the sidebar started moving")
	}
	view.Advance(32 * time.Millisecond)
	view.Frame()
	first := width()
	if bytes.Equal(expandedLabel, labelPixels()) {
		t.Fatal("navigation text did not fade with the sidebar width")
	}
	view.Advance(32 * time.Millisecond)
	view.Frame()
	second := width()
	if !(collapsedSidebarWidth < second && second < first && first < sidebarWidth) {
		t.Fatalf("collapse did not pass through continuous widths: %v, %v", first, second)
	}
	if err := view.Click("展开侧边栏"); err != nil {
		t.Fatal(err)
	}
	if width() != second {
		t.Fatal("reversing the sidebar jumped to an endpoint")
	}
	view.Advance(32 * time.Millisecond)
	if reversed := width(); reversed <= second || reversed >= sidebarWidth {
		t.Fatalf("reversed sidebar did not move from its current width: %v", reversed)
	}
	view.Advance(300 * time.Millisecond)
	if width() != sidebarWidth {
		t.Fatal("reversed sidebar did not return to its expanded width")
	}
	view.SetPreferences(ui.Preferences{ReduceMotion: true})
	if err := view.Click("收起侧边栏"); err != nil {
		t.Fatal(err)
	}
	if width() != collapsedSidebarWidth {
		t.Fatal("reduced-motion collapse should finish immediately")
	}
}

func TestCompactSidebarTracksTrafficLightsWithSpacing(t *testing.T) {
	for _, lightsRight := range []float32{0, 70, 94} {
		t.Run(fmt.Sprint(lightsRight), func(t *testing.T) {
			a := New()
			view := ui.NewTester(a.View, 1080, 700)
			view.SetTitleBar(ui.TitleBar{Left: lightsRight, Height: 28})
			before, ok := view.Find("收起侧边栏")
			if !ok || before.W != 32 || before.H != 32 || before.Y != 28 {
				t.Fatalf("compact toggle must sit directly below the reserved traffic-light row: %+v", before)
			}
			if err := view.Click("收起侧边栏"); err != nil {
				t.Fatal(err)
			}
			view.Move(1070, 10)
			view.Advance(300 * time.Millisecond)
			region, _ := view.Find("游戏导航")
			toggle, _ := view.Find("展开侧边栏")
			right := lightsRight
			if right == 0 {
				right = 70
			}
			if region.X+region.W-right != 12 || toggle.X+toggle.W/2 != region.W/2 {
				t.Fatalf("sidebar must end 12pt after the last light and center its toggle: sidebar=%+v toggle=%+v", region, toggle)
			}
			if toggle.X != before.X || toggle.Y != before.Y || a.Presentation().SidebarWidth != float64(region.W) {
				t.Fatal("toggle moved or presentation width disagreed with native traffic-light geometry")
			}
			game, _ := view.Find("明日方舟")
			if game.Y < toggle.Y+toggle.H+8 {
				t.Fatal("navigation overlaps the compact toggle row")
			}
		})
	}
}

func TestLaunchCardsStayAtBottomRight(t *testing.T) {
	a := New()
	view := ui.NewTester(a.View, 1080, 700)
	for _, label := range []string{"明日方舟：终末地", "明日方舟"} {
		if err := view.Click(label); err != nil {
			t.Fatal(err)
		}
		card, ok := view.Find("游戏启动卡片")
		if !ok || card.X+card.W != 1080-36 || card.Y+card.H != 700-36 || card.W > 460 {
			t.Fatalf("%s card is not right aligned with 36pt padding: %+v", label, card)
		}
	}
}

func TestButtonsRenderHoverAndPressFeedback(t *testing.T) {
	a := New()
	a.SetVideoState(true, true, "", nil)
	view := ui.NewTester(a.View, 1080, 700)
	for _, label := range []string{"收起侧边栏", "明日方舟", "设置", "暂停视频背景"} {
		t.Run(label, func(t *testing.T) {
			view.Move(1070, 10)
			view.Advance(200 * time.Millisecond)
			r, ok := view.Find(label)
			if !ok {
				t.Fatal("button missing")
			}
			before := append([]byte(nil), view.Image().Pix...)
			x, y := r.X+r.W/2, r.Y+r.H/2
			view.Move(x, y)
			view.Advance(160 * time.Millisecond)
			if bytes.Equal(before, view.Image().Pix) || view.Cursor() != ui.CursorPointer {
				t.Fatal("hover did not visibly change the button and pointer")
			}
			hover := append([]byte(nil), view.Image().Pix...)
			view.Press(x, y)
			view.Advance(130 * time.Millisecond)
			pressed, _ := view.Find(label)
			if pressed != r || bytes.Equal(hover, view.Image().Pix) {
				t.Fatalf("press must change fill without moving or scaling the button: before=%+v pressed=%+v", r, pressed)
			}
			// Release outside so the feedback check does not invoke navigation.
			view.Release(1070, 10)
			view.Advance(200 * time.Millisecond)
		})
	}
	card, _ := view.Find("游戏启动卡片")
	playback, _ := view.Find("暂停视频背景")
	if playback.X <= card.X+card.W/2 || playback.Y <= card.Y+card.H/2 || playback.Y+playback.H > card.Y+card.H {
		t.Fatal("playback button must sit inside the bottom right of the launch card")
	}
}

func TestPaletteSurfacesStayReadableOverNativeBackdrop(t *testing.T) {
	a := New()
	a.SetNativeBackground(true)
	view := ui.NewTester(a.View, 1080, 700)
	card, ok := view.Find("游戏启动卡片")
	if !ok {
		t.Fatal("launch card missing")
	}
	cardX, cardY := int(card.X+card.W-8), int(card.Y+card.H/2)
	if view.Image().RGBAAt(1070, 20).A != 0 {
		t.Fatal("game backdrop must remain unobstructed outside the controls")
	}
	if view.Image().RGBAAt(8, 200).A != 255 || view.Image().RGBAAt(cardX, cardY).A != 255 {
		t.Fatal("panels must remain opaque until a native material painter is connected")
	}
	// Observe the real UI paint contract; the native platform renders the
	// corresponding effect views separately, outside this headless renderer.
	var sidebarMaterial, cardMaterial ui.Rect
	a.SetGlassPainter(func(kind GlassKind, rect ui.Rect, radius float32, dark bool) {
		switch kind {
		case GlassSidebar:
			sidebarMaterial = rect
		case GlassCard:
			cardMaterial = rect
			if radius != panelRadius {
				t.Fatal("native card material radius disagrees with the UI")
			}
		}
	})
	view.Frame()
	if sidebarMaterial != (ui.Rect{W: sidebarWidth, H: 700}) || cardMaterial != card {
		t.Fatalf("native materials must follow committed UI geometry: sidebar=%+v card=%+v", sidebarMaterial, cardMaterial)
	}
	if alpha := view.Image().RGBAAt(8, 200).A; alpha == 0 || alpha == 255 {
		t.Fatal("sidebar must leave native blur visible beneath its palette tint")
	}
	if alpha := view.Image().RGBAAt(cardX, cardY).A; alpha == 0 || alpha == 255 {
		t.Fatal("launch card must leave native blur visible beneath its palette tint")
	}
	if err := view.Click("收起侧边栏"); err != nil {
		t.Fatal(err)
	}
	view.Advance(32 * time.Millisecond)
	sidebar, _ := view.Find("游戏导航")
	if sidebarMaterial.W != sidebar.W || !(collapsedSidebarWidth < sidebarMaterial.W && sidebarMaterial.W < sidebarWidth) {
		t.Fatal("native sidebar material did not follow the animated width")
	}
	a.SetReduceTransparency(true)
	view.Frame()
	if view.Image().RGBAAt(8, 200).A != 255 || view.Image().RGBAAt(cardX, cardY).A != 255 {
		t.Fatal("reduce transparency must restore opaque panels")
	}
	if err := view.Click("设置"); err != nil {
		t.Fatal(err)
	}
	if a.Presentation().Background != "" || view.Image().RGBAAt(1070, 680).A != 255 {
		t.Fatal("settings must clear the native background and keep an opaque main surface")
	}
}

func brightness(c color.RGBA) int {
	return int(c.R) + int(c.G) + int(c.B)
}
