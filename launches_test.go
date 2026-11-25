package main

import (
	"testing"

	"pinion/internal/app"
	"pinion/internal/gamewatch"
)

func TestLaunchObservationWaitsForMainProcessAndCleanup(t *testing.T) {
	const game = "arknights-bilibili"
	t.Run("helper never establishes startup", func(t *testing.T) {
		g := gameLaunch{state: app.LaunchState{Stage: "preparing"}, active: true}
		// launch.Running reports false for a helper-only snapshot; see the
		// real child process cases in TestDistributionProcessAttribution.
		g.observe(false, "")
		if g.observed || g.state.Stage != "preparing" {
			t.Fatalf("launch was treated as running: %+v", g)
		}
		g.finish(game, false, nil)
		if g.active || g.state.Stage != "failed" || g.state.Error == "" {
			t.Fatalf("launch without a main process did not fail: %+v", g)
		}
	})
	t.Run("cleanup after main exits", func(t *testing.T) {
		g := gameLaunch{state: app.LaunchState{Stage: "preparing"}, active: true}
		g.observe(true, "")
		g.observe(false, "scan failed")
		if g.state.Stage != "running" {
			t.Fatalf("scan failure changed running state: %+v", g)
		}
		g.observe(false, "")
		if g.state.Stage != "stopping" || !g.active {
			t.Fatalf("main process exit skipped cleanup state: %+v", g)
		}
		g.finish(game, false, nil)
		if g.active || g.state.Stage != "exited" || g.state.Error != "" {
			t.Fatalf("cleanup did not finish the launch: %+v", g)
		}
	})
}

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
