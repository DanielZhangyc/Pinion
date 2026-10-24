package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestRuntimePreparationGatesBothGameLaunches(t *testing.T) {
	a := New()
	a.SetDownload("endfield", DownloadState{Stage: "completed"})
	a.SetDownload("arknights", DownloadState{Stage: "completed"})
	launches := 0
	a.OnLaunch(func(string) { launches++ })
	view := ui.NewTester(a.View, 860, 560)
	for _, game := range []string{"明日方舟：终末地", "明日方舟"} {
		if err := view.Click(game); err != nil {
			t.Fatal(err)
		}
		for _, stage := range []string{"checking", "downloading", "verifying", "extracting", "paused", "failed", "rosetta"} {
			a.SetRuntime(RuntimeState{Stage: stage})
			view.Frame()
			if err := view.Click("等待运行环境"); err != nil {
				t.Fatal(err)
			}
			if launches != 0 {
				t.Fatal("game launched before the runtime was ready")
			}
			for range 3 {
				view.Frame()
			}
		}
	}
	a.SetRuntime(RuntimeState{Stage: "ready", ID: "finewine-26.3-1.2.5-r3"})
	view.Frame()
	if err := view.Click("启动游戏"); err != nil {
		t.Fatal(err)
	}
	if launches != 1 {
		t.Fatal("ready runtime did not enable launch")
	}
}

func TestRuntimeRecoveryRemainsAvailableAcrossNavigation(t *testing.T) {
	a := New()
	a.SetRuntime(RuntimeState{Stage: "failed", Error: "应用未包含 FineWine 安装包"})
	actions, installs := 0, 0
	a.OnRuntimeAction(func() { actions++ })
	a.OnInstallRosetta(func() { installs++ })
	view := ui.NewTester(a.View, 860, 560)
	if err := view.Click("设置运行环境"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("应用未包含 FineWine 安装包") {
		t.Fatal("setup error is not visible in settings")
	}
	if err := view.Click("重新准备"); err != nil {
		t.Fatal(err)
	}
	if actions != 1 {
		t.Fatal("setup cannot be retried")
	}
	a.SetRuntime(RuntimeState{Stage: "downloading", DoneBytes: 1024, TotalBytes: 2048})
	view.Frame()
	if !view.HasText("50.0% · 1.0 KiB / 2.0 KiB") {
		t.Fatal("download progress is hidden")
	}
	if err := view.Click("暂停准备"); err != nil {
		t.Fatal(err)
	}
	a.SetRuntime(RuntimeState{Stage: "paused"})
	view.Frame()
	if err := view.Click("继续准备"); err != nil {
		t.Fatal(err)
	}
	if actions != 3 {
		t.Fatal("setup cannot be paused and resumed")
	}
	a.SetRuntime(RuntimeState{Stage: "rosetta"})
	view.Frame()
	if err := view.Click("安装 Rosetta"); err != nil {
		t.Fatal(err)
	}
	if installs != 1 {
		t.Fatal("Rosetta installation action is inaccessible")
	}
	if err := view.Click("重新检查"); err != nil {
		t.Fatal(err)
	}
	a.SetRuntime(RuntimeState{Stage: "ready", ID: "finewine-26.3-1.2.5-r3"})
	view.Frame()
	if !view.HasText("FineWine 已就绪") {
		t.Fatal("successful setup is not visible")
	}
	for range 3 {
		view.Frame()
	}
	if _, ok := view.Find("重新准备"); ok {
		t.Fatal("ready runtime still offers installation")
	}
}
