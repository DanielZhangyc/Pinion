package main

import (
	"log"

	"gryph-launcher/internal/app"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	screen := app.New()
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:     "Gryph Launcher",
			Width:     1080,
			Height:    700,
			MinWidth:  860,
			MinHeight: 560,
			StateKey:  "main",
			Content:   ui.View(screen.View),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
