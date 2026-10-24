package main

import (
	"log"
	"os"

	"pinion/internal/app"
	"pinion/internal/paths"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	layout, err := paths.Resolve()
	if err != nil {
		log.Fatal(err)
	}
	if layout.UserData != "" {
		if err := os.MkdirAll(layout.UserData, 0700); err != nil {
			log.Fatal(err)
		}
		mygo.App.SetPath(mygo.PathUserData, layout.UserData)
		mygo.App.SetPath(mygo.PathCache, layout.Cache)
		mygo.App.SetPath(mygo.PathLogs, layout.Logs)
	}
	screen := app.New()
	mygo.Theme.SetSource(mygo.ThemeSystem)
	mygo.App.WhenReady(func() {
		window := mygo.NewWindow(mygo.WindowOptions{
			Title:         "Pinion",
			Width:         1080,
			Height:        700,
			MinWidth:      860,
			MinHeight:     560,
			StateKey:      "main",
			TitleBarStyle: mygo.TitleBarHidden,
			Transparent:   true,
			Content:       ui.View(screen.View),
		})
		startUpdateChecks(window, screen)
		if err := startRuntimeSetup(window, screen); err != nil {
			log.Fatal(err)
		}
		if err := startDownloads(window, screen); err != nil {
			log.Fatal(err)
		}
		backgrounds, err := startBackgrounds(window, screen)
		if err != nil {
			log.Fatal(err)
		}
		if err := startLaunches(window, screen, backgrounds.observeGames); err != nil {
			log.Fatal(err)
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
