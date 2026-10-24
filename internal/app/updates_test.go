package app

import (
	"context"
	"fmt"
	"os"
	"testing"

	"pinion/internal/update"

	"github.com/egoist/mygo/ui"
)

// Exercise notification lifetime and manual checks with real release metadata.
func TestUpdateInterfaceIntegration(t *testing.T) {
	if os.Getenv("PINION_UPDATE_INTEGRATION") != "1" {
		t.Skip("set PINION_UPDATE_INTEGRATION=1 for real GitHub API checks")
	}
	result, err := update.Check(context.Background(), "cli/cli", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if result.Latest == nil || !result.Available {
		t.Fatal("expected a published stable release")
	}
	for _, size := range [][2]int{{1080, 700}, {860, 560}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			a := New()
			state := UpdateState{Stage: "available", CurrentVersion: "0.0.0", LatestVersion: result.Latest.Version}
			a.SetUpdate(state)
			view := ui.NewTester(a.View, size[0], size[1])
			if !view.HasText(fmt.Sprintf("Pinion %s 可用", result.Latest.Version)) {
				t.Fatal("automatic update notification is missing")
			}
			if err := view.Click("稍后"); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				view.Frame()
			}
			if _, found := view.Find("稍后"); found {
				t.Fatal("dismissed notification returned")
			}
			if err := view.Click("设置"); err != nil {
				t.Fatal(err)
			}
			if !view.HasText(fmt.Sprintf("发现新版本 %s", result.Latest.Version)) {
				t.Fatal("dismissed update is no longer available in settings")
			}
			if _, found := view.Find("查看更新"); !found {
				t.Fatal("release page action is missing")
			}
			a.OnCheckUpdate(func() {
				checked, err := update.Check(context.Background(), "cli/cli", result.Latest.Version)
				if err != nil {
					t.Fatal(err)
				}
				if checked.Latest == nil || checked.Available {
					t.Fatal("manual check did not recognize the current release")
				}
				a.SetUpdate(UpdateState{Stage: "current", CurrentVersion: result.Latest.Version})
			})
			a.SetUpdate(UpdateState{Stage: "checking", CurrentVersion: result.Latest.Version})
			view.Frame()
			if err := view.Click("检查更新"); err != nil {
				t.Fatal(err)
			}
			if a.update.Stage != "checking" {
				t.Fatal("checking button allowed a concurrent request")
			}
			a.SetUpdate(state)
			view.Frame()
			if err := view.Click("检查更新"); err != nil {
				t.Fatal(err)
			}
			if !view.HasText("当前没有可用更新") {
				t.Fatal("manual check result is missing")
			}
			for range 3 {
				view.Frame()
			}
			if _, found := view.Find("查看更新"); found {
				t.Fatal("up-to-date state retained the old update action")
			}
		})
	}
}
