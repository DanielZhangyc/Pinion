package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestBothGamesBecomeLaunchableAfterDownload(t *testing.T) {
	a := New()
	a.SetRuntime(RuntimeState{Stage: "ready"})
	a.SetVideoState(true, true, "", nil)
	var requested string
	a.OnLaunch(func(game string) { requested = game })
	view := ui.NewTester(a.View, 1080, 700)
	for _, game := range []struct{ id, label, action string }{
		{"endfield", "明日方舟：终末地", "启动游戏"},
		{"arknights", "明日方舟", "启动游戏"},
	} {
		if err := view.Click(game.label); err != nil {
			t.Fatal(err)
		}
		if _, found := view.Find(game.action); found {
			t.Fatalf("%s became launchable before download completion", game.id)
		}
		a.SetDownload(game.id, DownloadState{Stage: "completed"})
		view.Frame()
		if game.id == "endfield" {
			if _, found := view.Find("暂停视频背景"); !found {
				t.Fatal("download completion hid the Endfield video control")
			}
		}
		if err := view.Click(game.action); err != nil {
			t.Fatalf("%s launch action missing: %v", game.id, err)
		}
		if requested != game.id {
			t.Fatalf("launch requested %q, expected %q", requested, game.id)
		}
		a.SetLaunch(game.id, LaunchState{Stage: "preparing"})
		view.Frame()
		if !view.HasText("准备启动") || view.HasText("游戏运行中") {
			t.Fatal("preparation must not imply a running game")
		}
		a.SetLaunch(game.id, LaunchState{Stage: "running"})
		view.Frame()
		if _, found := view.Find(game.action); found {
			t.Fatalf("%s still offers launch while running", game.id)
		}
		for range 3 {
			view.Frame()
		}
	}
	if err := view.Click("明日方舟：终末地"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("游戏运行中") {
		t.Fatal("Endfield launch state did not survive navigation and rebuilds")
	}
}

func TestLaunchPresentationPreservesBusyStatesAndFailureActions(t *testing.T) {
	a := New()
	a.SetRuntime(RuntimeState{Stage: "ready"})
	a.SetDownload("endfield", DownloadState{Stage: "completed", Directory: "/games/endfield"})
	launches, logs, directories := 0, 0, 0
	a.OnLaunch(func(string) { launches++ })
	a.OnOpenLaunchLog(func(string) { logs++ })
	a.OnOpenDownload(func(string) { directories++ })
	view := ui.NewTester(a.View, 1080, 700)

	for _, state := range []struct{ stage, label string }{
		{"preparing", "准备启动"},
		{"running", "游戏运行中"},
		{"stopping", "游戏运行中"},
	} {
		a.SetLaunch("endfield", LaunchState{Stage: state.stage, LogPath: "/logs/endfield.log", DetectionError: "process query failed"})
		view.Frame()
		if err := view.Click(state.label); err != nil {
			t.Fatal(err)
		}
		if launches != 0 {
			t.Fatalf("%s allowed a duplicate launch", state.stage)
		}
		if view.HasText("查看日志") || view.HasText("/games/endfield") || view.HasText("无法检测游戏运行状态") {
			t.Fatalf("%s exposed internal launch details", state.stage)
		}
		for range 3 {
			view.Frame()
		}
	}

	a.SetLaunch("endfield", LaunchState{Stage: "failed", Error: "Wine failed to start", LogPath: "/logs/endfield.log"})
	view.Frame()
	if !view.HasText("启动失败") {
		t.Fatal("launch failure is hidden")
	}
	if err := view.Click("查看日志"); err != nil {
		t.Fatal(err)
	}
	if logs != 1 {
		t.Fatal("failure log is inaccessible")
	}
	if err := view.Click("重试"); err != nil {
		t.Fatal(err)
	}
	if launches != 1 {
		t.Fatal("failed launch cannot be retried")
	}

	a.SetLaunch("endfield", LaunchState{Stage: "exited", LogPath: "/logs/endfield.log"})
	view.Frame()
	if view.HasText("启动失败") || view.HasText("游戏运行中") || view.HasText("查看日志") || view.HasText("游戏已退出，可再次启动") {
		t.Fatal("completed launch left stale status or diagnostics")
	}
	if err := view.Click("启动游戏"); err != nil {
		t.Fatal(err)
	}
	if launches != 2 {
		t.Fatal("exited launch cannot be restarted")
	}
	if err := view.Click("打开目录"); err != nil {
		t.Fatal(err)
	}
	if directories != 1 {
		t.Fatal("game directory is inaccessible")
	}
	a.SetDownload("endfield", DownloadState{Stage: "completed", Error: "无法打开游戏目录：permission denied"})
	view.Frame()
	if !view.HasText("无法打开目录") {
		t.Fatal("opening the game directory failed without visible feedback")
	}
}

func TestDownloadPresentationKeepsProgressAndInstallRequirements(t *testing.T) {
	a := New()
	a.SetDownloadDirectory("/games", false, nil)
	a.OnDownload(func(string) {})
	view := ui.NewTester(a.View, 1080, 700)
	a.SetDownload("endfield", DownloadState{Stage: "ready", Version: "1.0", TotalBytes: 2048, RequiredBytes: 8192, Directory: "/games/endfield"})
	view.Frame()
	if !view.HasText("下载 2.0 KiB") || !view.HasText("所需空间 8.0 KiB") {
		t.Fatal("download and installation space requirements are hidden")
	}
	a.SetDownload("endfield", DownloadState{Stage: "downloading", TotalBytes: 2048, DoneBytes: 1024, BytesPerSecond: 1024, Directory: "/games/endfield", File: "internal-package.zip"})
	view.Frame()
	if !view.HasText("50.0%") || !view.HasText("剩余 1.0 KiB") || !view.HasText("1.0 KiB/s") {
		t.Fatal("download progress, remaining size or speed is hidden")
	}
	if view.HasText("/games/endfield") || view.HasText("internal-package.zip") {
		t.Fatal("download view exposed permanent path or internal filename")
	}
	a.SetDownload("endfield", DownloadState{Stage: "failed", Error: "permission denied"})
	view.Frame()
	if !view.HasText("下载失败") {
		t.Fatal("download failure is hidden")
	}
	if _, found := view.Find("重试"); !found {
		t.Fatal("failed download has no retry action")
	}
}
