package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"pinion/internal/app"
	"pinion/internal/download"
	"pinion/internal/gamewatch"
	"pinion/internal/launch"
	"pinion/internal/settings"

	"github.com/egoist/mygo"
)

// All fields except the immutable services/context belong to the UI thread.
type launches struct {
	window  *mygo.Window
	screen  *app.App
	ctx     context.Context
	store   *settings.Store
	games   map[string]*gameLaunch
	onGames func(bool, error)
}

type gameLaunch struct {
	state    app.LaunchState
	active   bool
	observed bool
}

var launchGames = []struct {
	id         download.GameID
	title      string
	executable string
}{
	{download.Arknights, "明日方舟", "Arknights.exe"},
	{download.ArknightsBilibili, "明日方舟（B 服）", "Arknights.exe"},
	{download.Endfield, "明日方舟：终末地", "Endfield.exe"},
}

func startLaunches(window *mygo.Window, screen *app.App, onGames func(bool, error)) error {
	path, err := settings.DefaultPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &launches{window: window, screen: screen, ctx: ctx, store: settings.New(path), games: make(map[string]*gameLaunch), onGames: onGames}
	for _, game := range launchGames {
		l.games[string(game.id)] = &gameLaunch{}
	}
	window.OnClosed(cancel)
	screen.OnLaunch(l.start)
	screen.OnOpenLaunchLog(l.openLog)
	go l.monitor()
	return nil
}

func (l *launches) update(fn func()) {
	l.window.Update(func() {
		if l.ctx.Err() == nil && !l.window.IsDestroyed() {
			fn()
		}
	})
}

func (l *launches) publish(game string) { l.screen.SetLaunch(game, l.games[game].state) }

// downloadRoot is empty when settings cannot be read, so detection falls back
// to matching each executable anywhere.
func (l *launches) downloadRoot() string {
	prefs, err := l.store.Load()
	if err != nil {
		return ""
	}
	return prefs.DownloadDirectory
}

func launchTitle(game string) string {
	for _, candidate := range launchGames {
		if string(candidate.id) == game {
			return candidate.title
		}
	}
	return game
}

func (l *launches) start(game string) {
	g, ok := l.games[game]
	if !ok || g.active || g.state.Stage == "running" || !l.screen.RuntimeReady() || !l.screen.GameLaunchable(game) {
		return
	}
	g.active = true
	g.observed = false
	g.state = app.LaunchState{Stage: "preparing"}
	l.publish(game)
	go func() {
		prefs, err := l.store.Load()
		if err != nil {
			l.finish(game, err)
			return
		}
		ctx, cancel := context.WithTimeout(l.ctx, 15*time.Minute)
		defer cancel()
		session, err := launch.Start(ctx, download.GameID(game), prefs.DownloadDirectory, func(path string) {
			l.update(func() { g.state.LogPath = path; l.publish(game) })
		})
		if err != nil {
			l.finish(game, err)
			return
		}
		// The monitor confirms the PE process separately. The Wine wrapper being
		// alive does not by itself establish a running game.
		l.finish(game, session.Wait())
	}()
}

func (l *launches) finish(game string, err error) {
	if err != nil {
		log.Printf("launch %s: %v", game, err)
	}
	// The cross-process lock can be held during preparation, before a PE
	// process exists. Only an actual process observation establishes running.
	var alreadyRunning bool
	if errors.Is(err, launch.ErrAlreadyRunning) {
		for _, candidate := range launchGames {
			if string(candidate.id) == game {
				var detectionErr error
				alreadyRunning, detectionErr = launch.Running(gamewatch.Capture(candidate.executable), candidate.id, l.downloadRoot())
				if detectionErr != nil {
					err = errors.Join(err, detectionErr)
				}
				break
			}
		}
	}
	l.update(func() {
		g := l.games[game]
		g.active = false
		if errors.Is(err, launch.ErrAlreadyRunning) {
			if alreadyRunning {
				g.state.Stage, g.state.Error, g.observed = "running", "", true
			} else {
				g.state.Stage, g.state.Error = "failed", fmt.Sprintf("%s正在其他启动器中启动或运行，请稍后重试。%v", launchTitle(game), err)
			}
		} else if err != nil {
			g.state.Stage, g.state.Error = "failed", fmt.Sprintf("%s无法启动或异常退出：%v", launchTitle(game), err)
		} else if !g.observed {
			g.state.Stage, g.state.Error = "failed", launchTitle(game)+"在启动过程中退出，请查看日志。"
		} else {
			g.state.Stage, g.state.Error = "exited", ""
		}
		l.publish(game)
	})
}

// Also reattaches presentation after restarting the launcher while the game
// stays open. No game is automatically started or terminated by this monitor.
func (l *launches) monitor() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	type observation struct {
		running bool
		message string
	}
	previous := make(map[string]observation)
	var previousOverall observation
	var initialized bool
	for {
		if l.ctx.Err() != nil {
			return
		}
		snapshot := gamewatch.Capture()
		if l.ctx.Err() != nil {
			return
		}
		root := l.downloadRoot()
		running, err := snapshot.Running()
		overall := observation{running: running}
		if err != nil {
			overall.message = err.Error()
		}
		if !initialized || previousOverall != overall {
			l.onGames(running, err)
			previousOverall, initialized = overall, true
		}
		for _, game := range launchGames {
			id := string(game.id)
			running, err := launch.Running(snapshot, game.id, root)
			next := observation{running: running}
			if err != nil {
				next.message = err.Error()
			}
			last, exists := previous[id]
			if exists && last == next {
				continue
			}
			previous[id] = next
			if !exists && next == (observation{}) {
				// With no prior observation, an idle game has no UI state to publish.
				// Later transitions to idle still need to report the exit.
				continue
			}
			l.update(func() {
				g := l.games[id]
				before := g.state
				if next.message != g.state.DetectionError && next.message != "" {
					log.Printf("detect %s: %s", id, next.message)
				}
				g.observe(next.running, next.message)
				if before != g.state {
					l.publish(id)
				}
			})
		}
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (g *gameLaunch) observe(running bool, detectionError string) {
	g.state.DetectionError = detectionError
	if detectionError != "" {
		// A failed snapshot cannot establish that the game has exited.
		return
	}
	if running {
		g.observed = true
		g.state.Stage, g.state.Error = "running", ""
	} else if g.active && g.observed {
		g.state.Stage = "stopping"
	} else if !g.active && g.state.Stage == "running" {
		g.state.Stage = "exited"
	}
}

func (l *launches) openLog(game string) {
	g, ok := l.games[game]
	if !ok || g.state.LogPath == "" {
		return
	}
	path := g.state.LogPath
	go func() {
		if err := mygo.Shell.OpenPath(path); err != nil {
			l.update(func() {
				g.state.Error = fmt.Sprintf("无法打开%s日志目录：%v", launchTitle(game), err)
				l.publish(game)
			})
		}
	}()
}
