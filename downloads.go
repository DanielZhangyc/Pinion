package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"pinion/internal/app"
	"pinion/internal/download"
	"pinion/internal/settings"

	"github.com/egoist/mygo"
)

// These models are confined to the UI thread. Workers capture immutable job
// inputs and publish their results with Window.Update.
type downloadJob struct {
	state   app.DownloadState
	cancel  context.CancelFunc
	token   uint64
	started bool
}

type downloads struct {
	window     *mygo.Window
	screen     *app.App
	store      *settings.Store
	ctx        context.Context
	directory  string
	choosing   bool
	generation uint64
	jobs       map[string]*downloadJob
}

func startDownloads(window *mygo.Window, screen *app.App) error {
	path, err := settings.DefaultPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &downloads{window: window, screen: screen, store: settings.New(path), ctx: ctx, jobs: make(map[string]*downloadJob), choosing: true}
	window.OnClosed(cancel)
	screen.OnChooseDirectory(d.chooseDirectory)
	screen.OnDownload(d.act)
	screen.OnOpenDownload(d.openDirectory)
	screen.SetDownloadDirectory("", true, nil)
	go func() {
		prefs, err := d.store.Load()
		d.update(func() {
			d.choosing = false
			if err != nil {
				log.Printf("load download settings: %v", err)
				screen.SetDownloadDirectory("", false, fmt.Errorf("无法读取下载目录设置，请重新选择目录：%w", err))
				return
			}
			d.setDirectory(prefs.DownloadDirectory)
		})
	}()
	return nil
}

func (d *downloads) update(fn func()) {
	d.window.Update(func() {
		if !d.window.IsDestroyed() && d.ctx.Err() == nil {
			fn()
		}
	})
}

func (d *downloads) setDirectory(directory string) {
	d.directory = directory
	d.generation++
	generation := d.generation
	d.screen.SetDownloadDirectory(directory, false, nil)
	for _, game := range []download.GameID{download.Arknights, download.Endfield} {
		key := string(game)
		job := &downloadJob{}
		if directory != "" {
			job.state.Stage = "loading-record"
		}
		d.jobs[key] = job
		d.screen.SetDownload(key, job.state)
		if directory == "" {
			continue
		}
		go func() {
			record, err := download.New(directory).LoadRecord(game)
			d.update(func() {
				if d.generation != generation || job.token != 0 {
					return
				}
				if errors.Is(err, os.ErrNotExist) {
					job.state = app.DownloadState{}
					d.screen.SetDownload(key, job.state)
					return
				}
				if err != nil {
					job.state = app.DownloadState{Stage: "failed", Error: fmt.Sprintf("无法读取下载记录：%v", err)}
				} else {
					job.state = app.DownloadState{Stage: "completed", Version: record.Version, Directory: record.Directory}
				}
				d.screen.SetDownload(key, job.state)
			})
		}()
	}
}

func (d *downloads) chooseDirectory() {
	if d.choosing {
		return
	}
	for _, job := range d.jobs {
		if job.cancel != nil {
			d.screen.SetDownloadDirectory(d.directory, false, errors.New("请先暂停下载，再更改目录。"))
			return
		}
	}
	d.choosing = true
	d.screen.SetDownloadDirectory(d.directory, true, nil)
	previous := d.directory
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: d.window, Title: "选择游戏下载目录", ButtonLabel: "选择目录",
			DefaultPath: previous, Directory: true, CreateDirectories: true,
		})
		directory := previous
		if err == nil && len(paths) > 0 && d.ctx.Err() == nil {
			directory, err = settings.ValidateDirectory(paths[0])
			if err == nil {
				err = d.store.Save(settings.Settings{DownloadDirectory: directory})
			}
		}
		d.update(func() {
			d.choosing = false
			if err != nil {
				log.Printf("choose download directory: %v", err)
				d.screen.SetDownloadDirectory(previous, false, fmt.Errorf("目录未保存，请选择可写入的文件夹：%w", err))
				return
			}
			if len(paths) == 0 {
				d.screen.SetDownloadDirectory(previous, false, nil)
				return
			}
			d.setDirectory(directory)
		})
	}()
}

func (d *downloads) act(game string) {
	job := d.jobs[game]
	if job == nil || job.state.Stage == "loading-record" || d.directory == "" || d.choosing {
		return
	}
	if job.cancel != nil {
		job.state.Stage = "pausing"
		d.screen.SetDownload(game, job.state)
		job.cancel()
		return
	}
	for key, other := range d.jobs {
		if key != game && other.cancel != nil {
			job.state.Error = "另一款游戏正在下载，请先暂停后再试。"
			d.screen.SetDownload(game, job.state)
			return
		}
	}
	install := job.state.Stage == "ready" || job.started
	job.started = install
	directory := d.directory
	ctx, cancel := context.WithCancel(d.ctx)
	job.cancel = cancel
	job.token++
	token := job.token
	job.state.Stage, job.state.Error, job.state.BytesPerSecond = "checking", "", 0
	d.screen.SetDownload(game, job.state)
	go d.run(ctx, cancel, game, directory, token, install)
}

func (d *downloads) run(ctx context.Context, cancel context.CancelFunc, game, directory string, token uint64, install bool) {
	defer cancel()
	if _, err := settings.ValidateDirectory(directory); err != nil {
		d.finish(game, token, err, download.Record{})
		return
	}
	manager := download.New(directory)
	plan, err := manager.Resolve(ctx, download.GameID(game))
	if err != nil {
		d.finish(game, token, err, download.Record{})
		return
	}
	if !install {
		d.update(func() {
			job := d.jobs[game]
			if job.token != token {
				return
			}
			job.cancel = nil
			if job.state.Stage == "pausing" {
				job.state.Stage = "paused"
			} else {
				job.state = app.DownloadState{Stage: "ready", Version: plan.Version, Directory: directory, TotalBytes: plan.DownloadBytes, RequiredBytes: plan.DownloadBytes + plan.UnpackedBytes}
			}
			d.screen.SetDownload(game, job.state)
		})
		return
	}
	var lastUpdate, speedStart time.Time
	var previousStage, previousFile string
	var speedStartBytes int64
	var speed float64
	record, err := manager.Install(ctx, plan, func(progress download.Progress) {
		now := time.Now()
		stage := string(progress.Stage)
		// The first event for each package includes any bytes already on disk.
		// Reset the speed baseline there so cache reuse is not network traffic.
		if stage != previousStage || progress.File != previousFile {
			speedStart, speedStartBytes, speed = now, progress.DoneBytes, 0
		} else if elapsed := now.Sub(speedStart).Seconds(); elapsed >= 1 {
			speed = float64(progress.DoneBytes-speedStartBytes) / elapsed
			speedStart, speedStartBytes = now, progress.DoneBytes
		}
		// Each update rebuilds the native window. Bound progress redraws even
		// when extraction or verification advances through many small files;
		// phase changes and the final total still appear immediately.
		if stage == previousStage && now.Sub(lastUpdate) < 500*time.Millisecond && progress.DoneBytes != progress.TotalBytes {
			return
		}
		lastUpdate, previousStage, previousFile = now, stage, progress.File
		state := app.DownloadState{Stage: stage, Version: plan.Version, Directory: directory, File: progress.File, DoneBytes: progress.DoneBytes, TotalBytes: progress.TotalBytes, BytesPerSecond: max(0, speed)}
		d.publishProgress(game, token, state)
	})
	d.finish(game, token, err, record)
}

func (d *downloads) publishProgress(game string, token uint64, state app.DownloadState) {
	mygo.RunOnMain(func() {
		if d.window.IsDestroyed() || d.ctx.Err() != nil {
			return
		}
		apply := func() {
			job := d.jobs[game]
			if job.token == token && job.cancel != nil && job.state.Stage != "pausing" {
				job.state = state
				d.screen.SetDownload(game, state)
			}
		}
		if d.screen.Presentation().Background == game {
			d.update(apply)
		} else {
			// Keep the model current on the UI thread without rebuilding an
			// unrelated page. Navigation will render the latest progress.
			apply()
		}
	})
}

func (d *downloads) finish(game string, token uint64, err error, record download.Record) {
	d.update(func() {
		job := d.jobs[game]
		if job.token != token {
			return
		}
		job.cancel = nil
		job.state.BytesPerSecond = 0
		switch {
		case errors.Is(err, context.Canceled):
			job.state.Stage, job.state.Error = "paused", ""
		case err != nil:
			log.Printf("download %s: %v", game, err)
			job.state.Stage, job.state.Error = "failed", err.Error()
		default:
			job.state = app.DownloadState{Stage: "completed", Version: record.Version, Directory: record.Directory}
		}
		d.screen.SetDownload(game, job.state)
	})
}

func (d *downloads) openDirectory(game string) {
	job := d.jobs[game]
	if job == nil || job.state.Stage != "completed" {
		return
	}
	directory := job.state.Directory
	go func() {
		err := mygo.Shell.OpenPath(directory)
		if err != nil {
			d.update(func() {
				if job == d.jobs[game] {
					job.state.Error = fmt.Sprintf("无法打开游戏目录：%v", err)
					d.screen.SetDownload(game, job.state)
				}
			})
		}
	}()
}
