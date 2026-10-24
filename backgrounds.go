package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"pinion/internal/app"
	"pinion/internal/media"
	"pinion/internal/paths"
	"pinion/internal/playback"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type backgrounds struct {
	window *mygo.Window
	screen *app.App
	player *playback.Controller
	// Presentation and reducedMotion are confined to the UI thread.
	presentation  app.Presentation
	reducedMotion bool
	materials     playback.Materials
	gameRunning   atomic.Bool
	gameReady     chan struct{}
	// Only the shared launch-monitor worker writes these fields.
	gameObserved  bool
	lastGameError string
}

func startBackgrounds(window *mygo.Window, screen *app.App) (*backgrounds, error) {
	layout, err := paths.Resolve()
	if err != nil {
		return nil, fmt.Errorf("locate media cache: %w", err)
	}
	player, err := playback.New(window)
	if err != nil {
		return nil, err
	}
	b := &backgrounds{window: window, screen: screen, player: player, gameReady: make(chan struct{})}
	// Until the first process snapshot arrives, do not start background video.
	b.gameRunning.Store(true)
	player.SetGameRunning(true)
	b.reducedMotion = mygo.Theme.Preferences().ReduceMotion
	screen.SetReduceTransparency(mygo.Theme.Preferences().ReduceTransparency)
	// The adapter updates only committed native material bounds and appearance.
	screen.SetGlassPainter(func(kind app.GlassKind, r ui.Rect, radius float32, dark bool) {
		surface := playback.Surface{Rect: playback.Rect{X: float64(r.X), Y: float64(r.Y), Width: float64(r.W), Height: float64(r.H)}, Radius: float64(radius), Visible: r.W > 0 && r.H > 0}
		switch kind {
		case app.GlassSidebar:
			b.materials.Sidebar = surface
		case app.GlassCard:
			b.materials.Card = surface
		}
		b.materials.Dark = dark
		b.materials.ReduceTransparency = mygo.Theme.Preferences().ReduceTransparency
		b.player.SetMaterials(b.materials)
	})
	screen.OnPresentationChange(func(p app.Presentation) {
		b.presentation = p
		b.applyPresentation()
	})
	offTheme := mygo.Theme.OnUpdated(func() {
		b.reducedMotion = mygo.Theme.Preferences().ReduceMotion
		b.screen.SetReduceTransparency(mygo.Theme.Preferences().ReduceTransparency)
		b.materials.ReduceTransparency = mygo.Theme.Preferences().ReduceTransparency
		b.player.SetMaterials(b.materials)
		b.applyPresentation()
	})
	ctx, cancel := context.WithCancel(context.Background())
	window.OnClosed(func() { cancel(); offTheme() })
	go b.monitor(ctx)
	go b.refreshMedia(ctx, media.New(filepath.Join(layout.Cache, "official-media")))
	return b, nil
}

func (b *backgrounds) applyPresentation() {
	b.player.SetVisible(b.presentation.Background != "")
	if b.presentation.Background != "" {
		if err := b.player.SelectBackdrop(b.presentation.Background); err != nil {
			log.Printf("select game background: %v", err)
			b.screen.SetMediaStatus("游戏背景无法显示", err)
		}
	}
	b.player.SetPaused(b.presentation.Paused || b.reducedMotion)
	b.player.SetPageVisible(b.presentation.Endfield)
}

func (b *backgrounds) update(fn func()) {
	b.window.Update(func() {
		if !b.window.IsDestroyed() {
			fn()
		}
	})
}

// observeGames receives the shared launch monitor's first snapshot and changes.
// An unknown snapshot keeps video and media refresh paused conservatively.
func (b *backgrounds) observeGames(running bool, err error) {
	b.gameRunning.Store(running || err != nil)
	b.player.SetGameRunning(running || err != nil)
	if !b.gameObserved {
		b.gameObserved = true
		close(b.gameReady)
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	if message != b.lastGameError {
		if err != nil {
			log.Printf("game detection: %v", err)
		}
		b.update(func() { b.screen.SetGameDetectionError(err) })
		b.lastGameError = message
	}
}

// Native notifications stop video immediately. This monitor only publishes
// changed video status; process state arrives from the shared launch monitor.
func (b *backgrounds) monitor(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	type viewState struct {
		source, reason, failure string
		ready, backdropReady    bool
	}
	var previous viewState
	for {
		if ctx.Err() != nil {
			return
		}
		status := b.player.Status()
		state := viewState{source: status.Source, reason: status.Reason, ready: status.ReadyForDisplay && status.Error == nil, backdropReady: status.BackdropReady}
		if status.Error != nil {
			state.failure = status.Error.Error()
		}
		if state != previous {
			if state.failure != "" && state.failure != previous.failure {
				log.Printf("video background: %s", state.failure)
			}
			b.update(func() {
				current := b.player.Status()
				b.publishVideoState(current)
			})
			previous = state
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func videoDescription(reason string) string {
	switch reason {
	case "playing":
		return "正在播放视频背景"
	case "user-paused":
		return "视频背景已暂停"
	case "game-running":
		return "游戏运行期间暂停视频背景"
	case "window-not-visible":
		return "窗口不可见时暂停视频背景"
	case "other-page":
		return "切换到终末地页面时播放视频背景"
	case "error":
		return "视频背景播放失败"
	default:
		return ""
	}
}

func (b *backgrounds) refreshMedia(ctx context.Context, manager *media.Manager) {
	arknights, endfield := app.BuiltinBackgrounds()
	if err := b.player.LoadBackdropData(arknights, endfield); err != nil {
		b.mediaFailure("内置背景无法解码", err)
		return
	}
	b.update(func() { b.screen.SetNativeBackground(b.player.Status().BackdropReady) })
	var current media.Snapshot
	cached, err := manager.Cached()
	if err == nil {
		if err := b.publishMedia(ctx, cached); err != nil {
			b.mediaFailure("缓存背景无法加载", err)
			return
		}
		current = cached
	} else if !errors.Is(err, os.ErrNotExist) {
		b.mediaFailure("官方素材缓存校验失败", err)
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-b.gameReady:
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if b.gameRunning.Load() {
			b.update(func() { b.screen.SetMediaStatus("游戏运行中，稍后检查素材更新", nil) })
			timer.Reset(30 * time.Second)
			continue
		}
		b.update(func() { b.screen.SetMediaStatus("正在检查官方素材…", nil) })
		snapshot, err := manager.Refresh(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			b.mediaFailure("官方素材更新失败", err)
		} else {
			if snapshot != current {
				if err := b.publishMedia(ctx, snapshot); err != nil {
					b.mediaFailure("官方素材无法加载", err)
					return
				}
				current = snapshot
			}
			b.update(func() { b.screen.SetMediaStatus("官方素材已更新", nil) })
		}
		timer.Reset(15 * time.Minute)
	}
}

func (b *backgrounds) publishMedia(ctx context.Context, snapshot media.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.player.LoadBackdrops(snapshot.Arknights.ImagePath, snapshot.Endfield.ImagePath); err != nil {
		return fmt.Errorf("load official backgrounds: %w", err)
	}
	if err := b.player.SetSource(snapshot.Endfield.VideoPath); err != nil {
		return fmt.Errorf("load official video: %w", err)
	}
	b.update(func() {
		status := b.player.Status()
		b.publishVideoState(status)
	})
	return nil
}

// Called only on the UI thread, with a current native snapshot.
func (b *backgrounds) publishVideoState(status playback.Status) {
	b.screen.SetNativeBackground(status.BackdropReady)
	b.screen.SetVideoState(status.Source != "", status.ReadyForDisplay && status.Error == nil,
		videoDescription(status.Reason), status.Error)
}

func (b *backgrounds) mediaFailure(status string, err error) {
	log.Printf("%s: %v", status, err)
	b.update(func() { b.screen.SetMediaStatus(status, err) })
}
