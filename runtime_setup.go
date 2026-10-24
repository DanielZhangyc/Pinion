package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"pinion/internal/app"
	"pinion/internal/wine"

	"github.com/egoist/mygo"
)

// Mutable setup state belongs to the UI thread. Workers publish via Update.
type runtimeSetup struct {
	window    *mygo.Window
	screen    *app.App
	ctx       context.Context
	resources string
	cancel    context.CancelFunc
	state     app.RuntimeState
}

var errRosettaUnavailable = errors.New("Rosetta 尚不可用")

func startRuntimeSetup(window *mygo.Window, screen *app.App) error {
	resources, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &runtimeSetup{window: window, screen: screen, ctx: ctx, resources: resources}
	window.OnClosed(cancel)
	window.OnFocus(func() {
		if r.cancel == nil && r.state.Stage == "rosetta" {
			r.start()
		}
	})
	screen.OnRuntimeAction(r.act)
	screen.OnInstallRosetta(r.installRosetta)
	r.start()
	return nil
}

func (r *runtimeSetup) update(fn func()) {
	r.window.Update(func() {
		if r.ctx.Err() == nil && !r.window.IsDestroyed() {
			fn()
		}
	})
}

func (r *runtimeSetup) publish(state app.RuntimeState) {
	r.state = state
	r.screen.SetRuntime(state)
}

func (r *runtimeSetup) act() {
	if r.cancel != nil {
		r.cancel()
		r.state.Stage = "pausing"
		r.screen.SetRuntime(r.state)
		return
	}
	r.start()
}

func (r *runtimeSetup) start() {
	if r.cancel != nil || r.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.ctx)
	r.cancel = cancel
	r.publish(app.RuntimeState{Stage: "checking"})
	go func() {
		defer cancel()
		var installed wine.Runtime
		err := checkTranslation(ctx)
		if err == nil {
			var lastStage string
			var lastUpdate time.Time
			installed, err = wine.Provision(ctx, wine.ProvisionOptions{ResourcesDir: filepath.Join(r.resources, "runtime")}, func(progress wine.ProvisionProgress) {
				if progress.Stage == "ready" {
					return
				}
				now := time.Now()
				if progress.Stage == lastStage && now.Sub(lastUpdate) < 500*time.Millisecond {
					return
				}
				lastStage, lastUpdate = progress.Stage, now
				state := app.RuntimeState{Stage: progress.Stage, DoneBytes: progress.Completed, TotalBytes: progress.Total}
				r.update(func() {
					if r.state.Stage != "pausing" {
						r.publish(state)
					}
				})
			})
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("prepare runtime: %v", err)
		}
		r.update(func() {
			r.cancel = nil
			switch {
			case errors.Is(err, context.Canceled):
				r.publish(app.RuntimeState{Stage: "paused"})
			case errors.Is(err, errRosettaUnavailable):
				r.publish(app.RuntimeState{Stage: "rosetta", Error: "点击“安装 Rosetta”，在终端中按 Apple 提示完成安装，再返回 Pinion。"})
			case err != nil:
				r.publish(app.RuntimeState{Stage: "failed", Error: err.Error()})
			default:
				r.publish(app.RuntimeState{Stage: "ready", ID: installed.ID()})
			}
		})
	}()
}

// Running a system Intel binary detects translation availability, including
// systems that support it without a separately installed package receipt.
func checkTranslation(ctx context.Context) error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("此运行环境需要 Apple Silicon Mac")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion").Output()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("检查 macOS 版本：%w", err)
	}
	major, err := strconv.Atoi(strings.SplitN(strings.TrimSpace(string(version)), ".", 2)[0])
	if err != nil {
		return fmt.Errorf("读取 macOS 版本：%w", err)
	}
	if major < 15 {
		return errors.New("此 FineWine 运行环境需要 macOS 15 或更新版本")
	}
	output, err := exec.CommandContext(ctx, "/usr/bin/arch", "-x86_64", "/usr/bin/true").CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%w：%v %s", errRosettaUnavailable, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (r *runtimeSetup) installRosetta() {
	if r.state.Stage != "rosetta" || r.cancel != nil {
		return
	}
	go func() {
		if err := mygo.Shell.OpenPath(filepath.Join(r.resources, "install-rosetta.command")); err != nil {
			r.update(func() {
				if r.state.Stage != "rosetta" || r.cancel != nil {
					return
				}
				r.publish(app.RuntimeState{Stage: "rosetta", Error: fmt.Sprintf("无法打开 Rosetta 安装程序：%v。可在终端执行 softwareupdate --install-rosetta。", err)})
			})
		}
	}()
}
