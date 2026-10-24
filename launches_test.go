package main

import (
	"testing"

	"pinion/internal/app"
	"pinion/internal/gamewatch"
)

func TestLaunchObservationIsolation(t *testing.T) {
	games := map[string]*gameLaunch{
		"arknights": {state: app.LaunchState{Stage: "preparing"}, active: true},
		"endfield":  {state: app.LaunchState{Stage: "preparing"}, active: true},
	}
	// Use actual kernel process observations; these tests do not start games.
	snapshot := gamewatch.Capture()
	for _, game := range launchGames {
		running, err := snapshot.Running(game.executable)
		if err != nil {
			t.Fatal(err)
		}
		g := games[string(game.id)]
		other := "endfield"
		if other == string(game.id) {
			other = "arknights"
		}
		before := *games[other]
		g.observe(running, "")
		if *games[other] != before {
			t.Fatal("observing one game changed the other game's launch state")
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
