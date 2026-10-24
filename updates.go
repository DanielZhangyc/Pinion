package main

import (
	"context"
	"log"
	"time"

	"pinion/internal/app"
	"pinion/internal/update"

	"github.com/egoist/mygo"
)

// Mutable controller and screen state belong to the window's UI thread.
type updateChecks struct {
	window  *mygo.Window
	screen  *app.App
	ctx     context.Context
	state   app.UpdateState
	release *update.Release
}

func startUpdateChecks(window *mygo.Window, screen *app.App) {
	ctx, cancel := context.WithCancel(context.Background())
	u := &updateChecks{window: window, screen: screen, ctx: ctx,
		state: app.UpdateState{CurrentVersion: mygo.App.Version()}}
	window.OnClosed(cancel)
	screen.OnCheckUpdate(u.check)
	screen.OnOpenUpdate(u.openRelease)
	u.check()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				u.update(u.check)
			}
		}
	}()
}

func (u *updateChecks) update(fn func()) {
	u.window.Update(func() {
		if u.ctx.Err() == nil && !u.window.IsDestroyed() {
			fn()
		}
	})
}

func (u *updateChecks) publish(state app.UpdateState) {
	u.state = state
	u.screen.SetUpdate(state)
}

func (u *updateChecks) check() {
	if u.ctx.Err() != nil || u.state.Stage == "checking" {
		return
	}
	version := u.state.CurrentVersion
	if version == "" {
		u.publish(app.UpdateState{Stage: "failed", Error: "无法读取应用版本，无法检查更新。"})
		return
	}
	u.release = nil
	u.publish(app.UpdateState{Stage: "checking", CurrentVersion: version})
	go func() {
		result, err := update.Check(u.ctx, update.Repository, version)
		if err != nil && u.ctx.Err() == nil {
			log.Printf("check updates: %v", err)
		}
		u.update(func() {
			state := app.UpdateState{Stage: "current", CurrentVersion: version}
			switch {
			case err != nil:
				state.Stage, state.Error = "failed", err.Error()
			case result.Latest == nil:
				state.Stage = "unpublished"
			case result.Available:
				state.Stage, state.LatestVersion = "available", result.Latest.Version
				u.release = result.Latest
			}
			u.publish(state)
		})
	}()
}

func (u *updateChecks) openRelease() {
	if u.ctx.Err() != nil || u.release == nil {
		return
	}
	url := u.release.URL
	state := u.state
	state.Error = ""
	u.publish(state)
	go func() {
		if err := mygo.Shell.OpenExternal(url); err != nil {
			log.Printf("open update release: %v", err)
			u.update(func() {
				if u.release != nil && u.release.URL == url {
					state := u.state
					state.Error = "无法打开更新页面，请稍后重试。"
					u.publish(state)
				}
			})
		}
	}()
}
