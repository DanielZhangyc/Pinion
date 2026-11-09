package app

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"pinion/internal/download"

	"github.com/egoist/mygo/ui"
)

// Official metadata supplies the displayed versions. These are presentation
// checks; no installation or Wine process is created.
func TestGameUpdateInterfaceIntegration(t *testing.T) {
	if os.Getenv("PINION_GAME_UPDATE_INTEGRATION") != "1" {
		t.Skip("set PINION_GAME_UPDATE_INTEGRATION=1 for official version queries")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, game := range []struct {
		id    download.GameID
		label string
	}{
		{download.Endfield, "明日方舟：终末地"},
		{download.Arknights, "明日方舟"},
	} {
		latest, err := download.New("").LatestVersion(ctx, game.id)
		if err != nil {
			t.Fatal(err)
		}
		for _, size := range [][2]int{{1080, 700}, {860, 560}} {
			t.Run(fmt.Sprintf("%s_%dx%d", game.id, size[0], size[1]), func(t *testing.T) {
				a := New()
				id := string(game.id)
				a.SetRuntime(RuntimeState{Stage: "ready"})
				a.SetDownloadDirectory(t.TempDir(), false, nil)
				a.SetDownload(id, DownloadState{Stage: "completed", Version: latest.Version})
				launches, updates, fullUpdates := 0, 0, 0
				a.OnLaunch(func(requested string) {
					if requested != id {
						t.Fatalf("launched %q, want %q", requested, id)
					}
					launches++
				})
				a.OnDownload(func(requested string) {
					if requested != id {
						t.Fatalf("updated %q, want %q", requested, id)
					}
					updates++
				})
				a.OnFullUpdate(func(requested string) {
					if requested != id {
						t.Fatalf("full update requested %q, want %q", requested, id)
					}
					fullUpdates++
				})
				view := ui.NewTester(a.View, size[0], size[1])
				if err := view.Click(game.label); err != nil {
					t.Fatal(err)
				}
				for _, stage := range []string{"checking", "current"} {
					a.SetGameUpdate(id, GameUpdateState{Stage: stage, InstalledVersion: latest.Version, LatestVersion: latest.Version})
					view.Frame()
					for _, text := range []string{"检查游戏更新", "正在检查更新", "当前无需更新", "更新游戏"} {
						if view.HasText(text) {
							t.Fatalf("%s exposed unwanted update UI %q", stage, text)
						}
					}
					if _, found := view.Find("启动游戏"); !found {
						t.Fatalf("%s check hid launch", stage)
					}
				}
				a.SetGameUpdate(id, GameUpdateState{Stage: "available", InstalledVersion: latest.Version, LatestVersion: latest.Version})
				view.Frame()
				if !view.HasText(fmt.Sprintf("发现游戏更新 %s", latest.Version)) {
					t.Fatal("available update is hidden")
				}
				if err := view.Click("更新游戏"); err != nil || updates != 1 {
					t.Fatalf("update action: count=%d err=%v", updates, err)
				}
				if err := view.Click("启动当前版本"); err != nil || launches != 1 {
					t.Fatalf("previous version: count=%d err=%v", launches, err)
				}
				for _, stage := range []string{"preparing", "running", "stopping"} {
					a.SetLaunch(id, LaunchState{Stage: stage})
					view.Frame()
					if _, found := view.Find("更新游戏"); found {
						t.Fatalf("%s still offered installation", stage)
					}
				}
				a.SetLaunch(id, LaunchState{Stage: "exited"})
				for _, tc := range []struct {
					stage, action string
					launchable    bool
				}{
					{"ready", "开始更新", true},
					{"downloading", "暂停", false},
					{"extracting", "暂停", false},
					{"verifying", "暂停", false},
					{"paused", "继续更新", true},
					{"failed", "重试", true},
				} {
					a.SetDownload(id, DownloadState{Stage: tc.stage, Version: latest.Version, Updating: true})
					view.Frame()
					before := updates
					if err := view.Click(tc.action); err != nil || updates != before+1 {
						t.Fatalf("%s action: count=%d err=%v", tc.stage, updates, err)
					}
					if a.GameLaunchable(id) != tc.launchable {
						t.Fatalf("%s launchability: got %v, want %v", tc.stage, a.GameLaunchable(id), tc.launchable)
					}
					if tc.launchable {
						if err := view.Click("启动当前版本"); err != nil {
							t.Fatal(err)
						}
					} else if _, found := view.Find("启动当前版本"); found {
						t.Fatalf("%s allowed launch during installation", tc.stage)
					}
					for range 3 {
						view.Frame()
					}
				}
				for _, mode := range []struct {
					differential bool
					label        string
				}{{true, "差分更新"}, {false, "完整更新"}} {
					a.SetDownload(id, DownloadState{Stage: "ready", Version: latest.Version, Updating: true, Differential: mode.differential})
					view.Frame()
					if !view.HasText(mode.label) {
						t.Fatalf("update mode %s is hidden", mode.label)
					}
					_, offered := view.Find("改用完整更新")
					if offered != mode.differential {
						t.Fatal("full update choice does not match the displayed mode")
					}
					if mode.differential {
						if err := view.Click("改用完整更新"); err != nil || fullUpdates != 1 {
							t.Fatalf("full update choice: count=%d err=%v", fullUpdates, err)
						}
					}
				}
				a.SetDownload(id, DownloadState{Stage: "completed", Version: latest.Version})
				a.SetGameUpdate(id, GameUpdateState{Stage: "current", InstalledVersion: latest.Version})
				view.Frame()
				if view.HasText("更新游戏") || view.HasText("当前无需更新") || !view.HasText("启动游戏") {
					t.Fatal("completed update kept stale controls")
				}
			})
		}
	}
}
