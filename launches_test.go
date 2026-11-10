package main

import (
	"testing"

	"pinion/internal/app"
	"pinion/internal/gamewatch"
)

func TestLaunchObservationIsolation(t *testing.T) {
	games := map[string]*gameLaunch{}
	for _, game := range launchGames {
		games[string(game.id)] = &gameLaunch{state: app.LaunchState{Stage: "preparing"}, active: true}
	}
	// Use actual kernel process observations; these tests do not start games.
	snapshot := gamewatch.Capture()
	for _, game := range launchGames {
		running, err := snapshot.Running(game.executable)
		if err != nil {
			t.Fatal(err)
		}
		g := games[string(game.id)]
		before := map[string]gameLaunch{}
		for id, other := range games {
			before[id] = *other
		}
		g.observe(running, "")
		for id, other := range games {
			if id != string(game.id) && *other != before[id] {
				t.Fatal("observing one game changed another game's launch state")
			}
		}
		if running && (g.state.Stage != "running" || !g.observed) {
			t.Fatal("an observed live game must be shown as running")
		}
		unchanged := g.state
		g.observe(running, "")
		if g.state != unchanged {
			t.Fatal("an unchanged process snapshot changed presentation state")
		}
	}
}
