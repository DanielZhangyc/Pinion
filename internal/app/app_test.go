package app

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNavigationAcrossRebuilds(t *testing.T) {
	for _, size := range [][2]int{{1080, 700}, {860, 560}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			a := New()
			view := ui.NewTester(a.View, size[0], size[1])
			if !view.HasText("尚未导入游戏") {
				t.Fatal("library did not show its initial state")
			}
			for range 3 {
				if err := view.Click("查看运行环境"); err != nil {
					t.Fatal(err)
				}
				view.Frame()
				if !view.HasText("尚未配置 Wine") {
					t.Fatal("runtime page did not open")
				}
				if err := view.Click("返回游戏库"); err != nil {
					t.Fatal(err)
				}
				view.Frame()
				if !view.HasText("尚未导入游戏") {
					t.Fatal("library did not return after rebuilding")
				}
			}
			if err := view.Click("运行环境"); err != nil {
				t.Fatal(err)
			}
			if !view.HasText("尚未配置 Wine") {
				t.Fatal("sidebar navigation did not open runtime page")
			}
		})
	}
}
