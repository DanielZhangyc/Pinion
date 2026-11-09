package app

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestRuntimeRepairEntryAndLaunchExclusion(t *testing.T) {
	for _, size := range [][2]int{{1080, 700}, {860, 560}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			a := New()
			a.SetDownload("endfield", DownloadState{Stage: "completed"})
			repairs, launches := 0, 0
			a.OnRuntimeRepair(func() {
				repairs++
				a.SetRuntimeRepair(RuntimeRepairState{Stage: "repairing"})
			})
			a.OnLaunch(func(string) { launches++ })
			view := ui.NewTester(a.View, size[0], size[1])
			if err := view.Click("设置"); err != nil {
				t.Fatal(err)
			}
			if err := view.Click("修复启动环境"); err != nil {
				t.Fatal(err)
			}
			if repairs != 0 {
				t.Fatal("repair started before runtime setup completed")
			}
			a.SetRuntime(RuntimeState{Stage: "ready"})
			for _, game := range []string{"arknights", "endfield"} {
				a.SetLaunch(game, LaunchState{Stage: "running"})
				view.Frame()
				if err := view.Click("修复启动环境"); err != nil {
					t.Fatal(err)
				}
				if repairs != 0 || !view.HasText("请先退出游戏，再修复启动环境。") {
					t.Fatal("active game did not block repair with a recovery instruction")
				}
				a.SetLaunch(game, LaunchState{Stage: "exited"})
			}
			a.SetDownload("arknights", DownloadState{Stage: "extracting", Updating: true})
			view.Frame()
			if err := view.Click("修复启动环境"); err != nil {
				t.Fatal(err)
			}
			if repairs != 0 {
				t.Fatal("repair started during update installation")
			}
			a.SetDownload("arknights", DownloadState{Stage: "completed"})
			view.Frame()
			button, ok := view.Find("修复启动环境")
			if !ok || button.X < sidebarWidth || button.X+button.W > float32(size[0]) || button.Y+button.H > float32(size[1]) {
				t.Fatalf("repair action is outside the visible settings area: %+v", button)
			}
			if err := view.Click("修复启动环境"); err != nil {
				t.Fatal(err)
			}
			if err := view.Click("正在修复启动环境"); err != nil {
				t.Fatal(err)
			}
			if repairs != 1 || a.RuntimeReady() {
				t.Fatal("repair allowed duplicate work or left launch enabled")
			}
			if err := view.Click("明日方舟：终末地"); err != nil {
				t.Fatal(err)
			}
			if err := view.Click("等待运行环境"); err != nil {
				t.Fatal(err)
			}
			if launches != 0 {
				t.Fatal("navigation allowed a game launch during repair")
			}
			a.SetRuntimeRepair(RuntimeRepairState{Stage: "failed", Message: "请先退出 Wine，再重试修复。"})
			if err := view.Click("设置"); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				view.Frame()
			}
			if !view.HasText("请先退出 Wine，再重试修复。") || !a.RuntimeReady() {
				t.Fatal("repair failure hid recovery guidance or permanently disabled launch")
			}
			if err := view.Click("修复启动环境"); err != nil {
				t.Fatal(err)
			}
			if repairs != 2 {
				t.Fatal("failed repair could not be retried")
			}
			a.SetRuntimeRepair(RuntimeRepairState{Stage: "completed", Message: "已修复启动缓存，请重新启动游戏。"})
			view.Frame()
			if !view.HasText("已修复启动缓存，请重新启动游戏。") || !a.RuntimeReady() {
				t.Fatal("completion feedback or restored launch access is missing")
			}
		})
	}
}
