package app

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestArknightsChannelsKeepSeparateState(t *testing.T) {
	for _, size := range [][2]int{{1080, 700}, {860, 560}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			a := New()
			a.SetRuntime(RuntimeState{Stage: "ready"})
			a.SetDownloadDirectory("/games", false, nil)
			var selected, launched, downloaded []string
			a.OnSelectChannel(func(game string) { selected = append(selected, game) })
			a.OnLaunch(func(game string) { launched = append(launched, game) })
			a.OnDownload(func(game string) { downloaded = append(downloaded, game) })
			a.SetDownload("arknights", DownloadState{Stage: "completed", Directory: "/games/arknights"})
			a.SetDownload("arknights-bilibili", DownloadState{Stage: "downloading", DoneBytes: 1 << 30, TotalBytes: 4 << 30})
			view := ui.NewTester(a.View, size[0], size[1])
			if view.HasText("B 服") {
				t.Fatal("Endfield page offers an Arknights channel")
			}
			if err := view.Click("明日方舟"); err != nil {
				t.Fatal(err)
			}
			if a.SelectedGame() != "arknights" || !view.HasText("官服") {
				t.Fatal("Arknights page did not default to the official channel")
			}
			if err := view.Click("启动游戏"); err != nil {
				t.Fatal(err)
			}
			if err := view.Click("B 服"); err != nil {
				t.Fatal(err)
			}
			view.Frame()
			if a.SelectedGame() != "arknights-bilibili" || len(selected) != 1 || selected[0] != "arknights-bilibili" {
				t.Fatalf("channel switch: selected=%v game=%s", selected, a.SelectedGame())
			}
			if a.Presentation().Background != "arknights" {
				t.Fatal("Bilibili channel changed the Arknights background")
			}
			if _, found := view.Find("启动游戏"); found || !view.HasText("正在下载") || !view.HasText("25.0%") {
				t.Fatal("Bilibili channel showed the official installation instead of its download")
			}
			if err := view.Click("暂停"); err != nil {
				t.Fatal(err)
			}
			// Official state changes while hidden must not leak into Bilibili.
			a.SetLaunch("arknights", LaunchState{Stage: "running"})
			view.Frame()
			if view.HasText("游戏运行中") {
				t.Fatal("official launch state appeared on the Bilibili channel")
			}
			for _, page := range []string{"明日方舟：终末地", "设置", "明日方舟"} {
				if err := view.Click(page); err != nil {
					t.Fatal(err)
				}
				view.Frame()
			}
			if a.SelectedGame() != "arknights-bilibili" || !view.HasText("正在下载") {
				t.Fatal("channel choice did not survive navigation and rebuilds")
			}
			a.SetDownload("arknights-bilibili", DownloadState{Stage: "completed", Directory: "/games/arknights-bilibili"})
			view.Frame()
			if err := view.Click("启动游戏"); err != nil {
				t.Fatal(err)
			}
			if err := view.Click("官服"); err != nil {
				t.Fatal(err)
			}
			view.Frame()
			if !view.HasText("游戏运行中") {
				t.Fatal("official channel lost its running state")
			}
			want := []string{"arknights", "arknights-bilibili"}
			if fmt.Sprint(launched) != fmt.Sprint(want) || fmt.Sprint(downloaded) != fmt.Sprint([]string{"arknights-bilibili"}) {
				t.Fatalf("actions launched=%v downloaded=%v", launched, downloaded)
			}
			if fmt.Sprint(selected) != fmt.Sprint([]string{"arknights-bilibili", "arknights"}) {
				t.Fatalf("channel notifications %v", selected)
			}
		})
	}
}

func TestRestoreArknightsChannel(t *testing.T) {
	a := New()
	view := ui.NewTester(a.View, 1080, 700)
	if err := view.Click("明日方舟"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ saved, want string }{
		{"arknights-bilibili", "arknights-bilibili"},
		{"", "arknights"},
		{"unknown", "arknights"},
	} {
		a.SetArknightsChannel(tc.saved)
		view.Frame()
		if a.SelectedGame() != tc.want {
			t.Fatalf("restored %q selected %q, want %q", tc.saved, a.SelectedGame(), tc.want)
		}
	}
	var selected []string
	a.OnSelectChannel(func(game string) { selected = append(selected, game) })
	if err := view.Click("B 服"); err != nil {
		t.Fatal(err)
	}
	view.Key(0, ui.KeyLeft)
	view.Frame()
	if a.SelectedGame() != "arknights" || fmt.Sprint(selected) != fmt.Sprint([]string{"arknights-bilibili", "arknights"}) {
		t.Fatalf("keyboard channel switch: game=%s notifications=%v", a.SelectedGame(), selected)
	}
	a.SetArknightsChannel("arknights-bilibili")
	if err := view.Click("明日方舟：终末地"); err != nil {
		t.Fatal(err)
	}
	if a.SelectedGame() != "endfield" {
		t.Fatal("a saved Arknights channel changed the Endfield page")
	}
}
