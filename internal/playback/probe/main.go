//go:build darwin

// The probe exercises a real MyGo window and local AVFoundation movie.
// Run with: direnv exec . go run ./internal/playback/probe /absolute/movie.mp4
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"pinion/internal/playback"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	hold := flag.Duration("hold", 0, "keep the ready window visible for screenshot inspection")
	backdrop := flag.String("backdrop", "", "absolute image path for native poster and materials inspection")
	static := flag.Bool("static", false, "inspect the static poster without starting video")
	bankArknights := flag.String("bank-arknights", "", "absolute Arknights image path for bank switching verification")
	bankEndfield := flag.String("bank-endfield", "", "absolute Endfield image path for bank switching verification")
	bankSelection := flag.String("bank-selection", "endfield", "final bank image after rapid switching")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: probe /absolute/movie.mp4")
	}
	var result error
	mygo.App.WhenReady(func() {
		window := mygo.NewWindow(mygo.WindowOptions{Title: "Pinion native video probe", Width: 800, Height: 520, Transparent: true, Vibrancy: mygo.VibrancySidebar, Content: ui.View(func(c *ui.Context) {
			c.Root().Background(ui.Transparent)
			ui.Row(c).Fill().Children(func() {
				ui.Box(c).Width(232).FillHeight().Background(c.Theme().Surface.Alpha(0.18))
			})
		})})
		controller, err := playback.New(window)
		if err != nil {
			result = err
			mygo.App.Quit()
			return
		}
		if err = controller.SetSource(flag.Arg(0)); err != nil {
			result = err
			mygo.App.Quit()
			return
		}
		if status := controller.Status(); status.Loaded || status.Source != flag.Arg(0) {
			result = fmt.Errorf("source created a player on an inactive page: %+v", status)
			mygo.App.Quit()
			return
		}
		controller.SetPageVisible(true)
		controller.SetMaterials(playback.Materials{
			Sidebar:  playback.Surface{Rect: playback.Rect{Width: 232, Height: 520}, Visible: true},
			Card:     playback.Surface{Rect: playback.Rect{X: 280, Y: 280, Width: 420, Height: 180}, Radius: 18, Visible: true},
			Playback: playback.Surface{Rect: playback.Rect{X: 732, Y: 12, Width: 40, Height: 40}, Radius: 10, Visible: true},
			Toggle:   playback.Surface{Rect: playback.Rect{X: 244, Y: 12, Width: 40, Height: 40}, Radius: 10, Visible: true},
		})
		go func() {
			defer mygo.App.Quit()
			if *bankArknights != "" || *bankEndfield != "" {
				controller.SetPageVisible(false)
				if err := controller.SelectBackdrop("arknights"); err != nil {
					result = err
					return
				}
				if err := controller.LoadBackdrops(*bankArknights, *bankEndfield); err != nil {
					result = err
					return
				}
				started := time.Now()
				for index := range 200 {
					key := "arknights"
					if index%2 == 1 {
						key = "endfield"
					}
					if err := controller.SelectBackdrop(key); err != nil {
						result = err
						return
					}
					status := controller.Status()
					if !status.BackdropReady || status.BackdropKey != key {
						result = fmt.Errorf("rapid bank selection mismatch: %+v", status)
						return
					}
				}
				fmt.Printf("200 real bank selections completed in %s\n", time.Since(started))
				if err := controller.LoadBackdrops(*bankArknights, *bankEndfield); err != nil {
					result = err
					return
				}
				if err := controller.SelectBackdrop(*bankSelection); err != nil {
					result = err
					return
				}
				fmt.Printf("final backdrop: %+v\n", controller.Status())
			}
			if *backdrop != "" {
				if err := controller.SetBackdrop(*backdrop); err != nil {
					result = err
					return
				}
			}
			if *static {
				controller.SetPageVisible(false)
				time.Sleep(*hold)
				return
			}
			controller.SetPageVisible(true)
			result = exercise(window, controller, *hold)
		}()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	if result != nil {
		log.Fatal(result)
	}
	fmt.Println("native video probe passed")
}

func exercise(window *mygo.Window, controller *playback.Controller, hold time.Duration) error {
	wait := func(label string, predicate func(playback.Status) bool) error {
		deadline := time.Now().Add(20 * time.Second)
		var status playback.Status
		for time.Now().Before(deadline) {
			status = controller.Status()
			if status.Error != nil {
				return status.Error
			}
			if predicate(status) {
				fmt.Printf("%s: %+v\n", label, status)
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return fmt.Errorf("%s timed out: %+v", label, status)
	}
	playing := func(s playback.Status) bool { return s.ReadyForDisplay && s.ItemStatus == 1 && s.Rate > 0 }
	paused := func(s playback.Status) bool { return !s.Playing && s.Rate == 0 }
	unloaded := func(s playback.Status) bool {
		return paused(s) && !s.Loaded && !s.ReadyForDisplay && s.Source != ""
	}
	if err := wait("ready and playing", playing); err != nil {
		return err
	}
	if hold > 0 {
		time.Sleep(hold)
	}
	controller.SetPaused(true)
	if err := wait("user paused with last frame", func(s playback.Status) bool {
		return paused(s) && s.Loaded && s.ReadyForDisplay
	}); err != nil {
		return err
	}
	// Poll the real player throughout the grace interval. A foreground user
	// pause keeps its last native frame instead of discarding the decoder.
	for deadline := time.Now().Add(16 * time.Second); time.Now().Before(deadline); {
		status := controller.Status()
		if status.Error != nil {
			return status.Error
		}
		if !paused(status) || !status.Loaded || !status.ReadyForDisplay {
			return fmt.Errorf("user pause discarded its last frame: %+v", status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("foreground user pause retained the loaded frame for 16s")
	controller.SetPaused(false)
	if err := wait("user resumed", playing); err != nil {
		return err
	}
	controller.SetPageVisible(false)
	if err := wait("other page unloaded immediately", unloaded); err != nil {
		return err
	}
	controller.SetPageVisible(true)
	if err := wait("page resumed", playing); err != nil {
		return err
	}
	window.Minimize()
	if err := wait("minimized", paused); err != nil {
		return err
	}
	window.Restore()
	window.Show()
	if err := wait("restored", playing); err != nil {
		return err
	}
	window.Hide()
	if err := wait("hidden", paused); err != nil {
		return err
	}
	if err := wait("hidden decoder released after grace period", unloaded); err != nil {
		return err
	}
	window.Show()
	if err := wait("shown", playing); err != nil {
		return err
	}
	// Exercise the caller's game-running API with real video resources; this
	// does not claim to test discovery of an actual running game process.
	controller.SetGameRunning(true)
	if err := wait("game-running API paused", paused); err != nil {
		return err
	}
	if err := wait("game-running decoder released after grace period", unloaded); err != nil {
		return err
	}
	controller.SetGameRunning(false)
	if err := wait("game-running API resumed", playing); err != nil {
		return err
	}
	mygo.App.Hide()
	if err := wait("application hidden", paused); err != nil {
		return err
	}
	mygo.App.Show()
	window.Show()
	window.Focus()
	if err := wait("application shown", playing); err != nil {
		return err
	}
	var cover *mygo.Window
	mygo.RunOnMain(func() {
		cover = mygo.NewWindow(mygo.WindowOptions{Title: "Pinion native occlusion probe", Width: 1000, Height: 720, Content: ui.View(func(c *ui.Context) { c.Root().Background(ui.Hex("#202020")) })})
		bounds := window.Bounds()
		bounds.X -= 50
		bounds.Y -= 50
		bounds.Width += 100
		bounds.Height += 100
		cover.SetBounds(bounds)
		cover.SetAlwaysOnTop(true)
		cover.Focus()
	})
	if err := wait("fully occluded", paused); err != nil {
		cover.Destroy()
		return err
	}
	cover.Destroy()
	window.Focus()
	if err := wait("uncovered", playing); err != nil {
		return err
	}
	controller.SetSidebarWidth(88)
	if err := wait("collapsed sidebar", playing); err != nil {
		return err
	}
	// Replacing a real source exercises release of looper/item/player and
	// reuse of the original layer and notification observers.
	controller.SetPageVisible(false)
	source := controller.Status().Source
	if err := controller.SetSource(""); err != nil {
		return err
	}
	if err := controller.SetSource(source); err != nil {
		return err
	}
	if status := controller.Status(); !unloaded(status) {
		return fmt.Errorf("source reloaded on an inactive page: %+v", status)
	}
	controller.SetPageVisible(true)
	if err := wait("source reloaded", playing); err != nil {
		return err
	}
	controller.Close()
	controller.Close()
	if controller.Status().Reason != "closed" {
		return errors.New("controller did not close")
	}
	return nil
}
