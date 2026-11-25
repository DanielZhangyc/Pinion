package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pinion/internal/app"
	"pinion/internal/settings"

	"github.com/egoist/mygo/ui"
)

func TestChannelSaveKeepsLatestSelection(t *testing.T) {
	store := settings.New(filepath.Join(t.TempDir(), "settings.json"))
	d := &downloads{store: store}
	old := &channelSave{game: "arknights-bilibili"}
	latest := &channelSave{game: "arknights"}
	d.channel.Store(old)
	d.channel.Store(latest)
	// Deliberately run the older worker after the newer one has persisted.
	for _, request := range []*channelSave{latest, old} {
		if err := d.saveChannel(request); err != nil {
			t.Fatal(err)
		}
	}
	if prefs, err := store.Load(); err != nil || prefs.ArknightsChannel != latest.game {
		t.Fatalf("saved preferences = %+v, %v", prefs, err)
	}
}

func TestChannelLoadKeepsSelectionMadeWhileLoading(t *testing.T) {
	screen := app.New()
	d := &downloads{screen: screen}
	view := ui.NewTester(screen.View, 1080, 700)
	if err := view.Click("明日方舟"); err != nil {
		t.Fatal(err)
	}
	initial := d.channel.Load()
	screen.OnSelectChannel(func(game string) { d.channel.Store(&channelSave{game: game}) })
	if err := view.Click("B 服"); err != nil {
		t.Fatal(err)
	}
	view.Frame()
	if d.channel.Load() == initial {
		t.Fatal("channel selection was not delivered")
	}
	d.restoreChannel(initial, "arknights")
	if got := screen.SelectedGame(); got != "arknights-bilibili" {
		t.Fatalf("stale settings restored %q over the user's selection", got)
	}
}

func TestDirectoryRecoveryKeepsLatestChannel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	screen := app.New()
	store := settings.New(path)
	d := &downloads{screen: screen, store: store}
	view := ui.NewTester(screen.View, 1080, 700)
	if err := view.Click("明日方舟"); err != nil {
		t.Fatal(err)
	}
	request := &channelSave{game: "arknights-bilibili"}
	d.channel.Store(request)
	channelErr := d.saveChannel(request)
	if channelErr == nil {
		t.Fatal("channel write accepted corrupt settings")
	}
	d.channelSaved(request, channelErr)
	view.Frame()
	const warning = "渠道选择未保存，重启后可能恢复原选择"
	if !view.HasText(warning) {
		t.Fatal("current channel save failure did not appear")
	}
	directory := t.TempDir()
	saved, err := d.saveDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := store.Load()
	if err != nil || prefs != (settings.Settings{DownloadDirectory: directory, ArknightsChannel: request.game}) {
		t.Fatalf("recovered preferences = %+v, %v", prefs, err)
	}
	d.channelSaved(saved, nil)
	d.channelSaved(request, errors.New("late result from the earlier save"))
	view.Frame()
	if view.HasText(warning) {
		t.Fatal("stale channel save failure appeared after successful recovery")
	}
}
