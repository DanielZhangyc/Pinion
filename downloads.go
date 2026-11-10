package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"pinion/internal/app"
	"pinion/internal/download"
	"pinion/internal/launch"
	"pinion/internal/settings"

	"github.com/egoist/mygo"
)

// These models are confined to the UI thread. Workers capture immutable job
// inputs and publish their results with Window.Update.
type downloadJob struct {
	state       app.DownloadState
	cancel      context.CancelFunc
	token       uint64
	started     bool
	installed   download.Record
	checkCancel context.CancelFunc
	checkedAt   time.Time
	update      app.GameUpdateState
	latest      *download.VersionInfo
	offer       downloadOffer
	forceFull   bool
}

// Signed URLs may refresh while the download choice and its cost stay fixed.
type downloadOffer struct {
	version, manifest  string
	differential       bool
	download, required int64
}

type downloads struct {
	window     *mygo.Window
	screen     *app.App
	store      *settings.Store
	ctx        context.Context
	directory  string
	choosing   bool
	generation uint64
	nextToken  uint64
	jobs       map[string]*downloadJob
	// saving serializes settings writers, each of which loads and replaces the file.
	saving sync.Mutex
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
	screen.OnFullUpdate(d.fullUpdate)
	screen.OnOpenDownload(d.openDirectory)
	screen.OnSelectChannel(d.selectChannel)
	window.OnFocus(func() { d.update(d.checkGameUpdates) })
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
			screen.SetArknightsChannel(prefs.ArknightsChannel)
			d.setDirectory(prefs.DownloadDirectory)
		})
	}()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.update(d.checkGameUpdates)
			}
		}
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
	for _, job := range d.jobs {
		if job.checkCancel != nil {
			job.checkCancel()
		}
	}
	d.directory = directory
	d.generation++
	generation := d.generation
	d.screen.SetDownloadDirectory(directory, false, nil)
	for _, game := range []download.GameID{download.Arknights, download.ArknightsBilibili, download.Endfield} {
		key := string(game)
		job := &downloadJob{}
		if directory != "" {
			job.state.Stage = "loading-record"
		}
		d.jobs[key] = job
		d.screen.SetDownload(key, job.state)
		d.screen.SetGameUpdate(key, app.GameUpdateState{})
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
					job.installed = record
				}
				d.screen.SetDownload(key, job.state)
				if err == nil {
					d.checkGameUpdate(key)
				}
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
				err = d.saveSettings(func(prefs *settings.Settings) { prefs.DownloadDirectory = directory })
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

// saveSettings changes one preference and keeps the others. Unreadable settings
// are replaced, as choosing a directory is how the user recovers from them.
func (d *downloads) saveSettings(change func(*settings.Settings)) error {
	d.saving.Lock()
	defer d.saving.Unlock()
	prefs, err := d.store.Load()
	if err != nil {
		prefs = settings.Settings{}
	}
	change(&prefs)
	return d.store.Save(prefs)
}

// selectChannel remembers the Arknights channel; running jobs are unaffected.
func (d *downloads) selectChannel(game string) {
	go func() {
		if err := d.saveSettings(func(prefs *settings.Settings) { prefs.ArknightsChannel = game }); err != nil {
			log.Printf("save Arknights channel: %v", err)
		}
	}()
}

func (d *downloads) fullUpdate(game string) {
	job := d.jobs[game]
	if job == nil || job.cancel != nil || job.installed.Version == "" || !job.state.Differential || (job.state.Stage != "ready" && job.state.Stage != "failed") || d.choosing || d.screen.GameBusy(game) {
		return
	}
	job.forceFull = true
	job.started = false
	job.offer = downloadOffer{}
	d.act(game)
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
	updating := job.installed.Version != ""
	if updating && d.screen.GameBusy(game) {
		job.update.ActionError = "请先退出游戏，再安装更新。"
		d.screen.SetGameUpdate(game, job.update)
		return
	}
	if job.state.Stage == "completed" && (job.latest == nil || !job.latest.UpdateAvailable(job.installed)) {
		return
	}
	for key, other := range d.jobs {
		if key != game && other.cancel != nil {
			message := "另一款游戏正在下载，请先暂停后再试。"
			if job.state.Stage == "completed" {
				job.update.ActionError = message
				d.screen.SetGameUpdate(game, job.update)
			} else {
				job.state.Error = message
				d.screen.SetDownload(game, job.state)
			}
			return
		}
	}
	install := job.state.Stage == "ready" || job.started
	job.update.ActionError = ""
	job.started = install
	directory := d.directory
	ctx, cancel := context.WithCancel(d.ctx)
	job.cancel = cancel
	if job.checkCancel != nil {
		job.checkCancel()
		job.checkCancel = nil
	}
	d.nextToken++
	job.token = d.nextToken
	token := job.token
	job.state.Stage, job.state.Error, job.state.BytesPerSecond = "checking", "", 0
	job.state.Updating = updating
	d.screen.SetDownload(game, job.state)
	go d.run(ctx, cancel, game, directory, token, install, updating, job.forceFull, job.offer)
}

func (d *downloads) run(ctx context.Context, cancel context.CancelFunc, game, directory string, token uint64, install, updating, forceFull bool, accepted downloadOffer) {
	defer cancel()
	if _, err := settings.ValidateDirectory(directory); err != nil {
		d.finish(game, token, err, download.Record{}, false)
		return
	}
	manager := download.New(directory)
	var plan download.Plan
	var err error
	if updating && !forceFull {
		plan, err = manager.ResolveUpdate(ctx, download.GameID(game))
	} else {
		plan, err = manager.Resolve(ctx, download.GameID(game))
	}
	if err != nil {
		d.finish(game, token, err, download.Record{}, plan.Differential)
		return
	}
	offer := downloadOffer{plan.Version, plan.ManifestMD5, plan.Differential, plan.DownloadBytes, plan.RequiredBytes}
	if !install || offer != accepted {
		d.update(func() {
			job := d.jobs[game]
			if job.token != token {
				return
			}
			job.cancel = nil
			if job.state.Stage == "pausing" {
				job.state.Stage = "paused"
			} else {
				job.offer = offer
				job.started = false
				job.state = app.DownloadState{Stage: "ready", Version: plan.Version, Directory: directory, TotalBytes: plan.DownloadBytes, RequiredBytes: plan.RequiredBytes, Updating: updating, Differential: plan.Differential}
			}
			d.screen.SetDownload(game, job.state)
		})
		return
	}
	lock, err := launch.LockForUpdate(download.GameID(game), directory)
	if err != nil {
		d.finish(game, token, fmt.Errorf("无法开始安装，请确认游戏已退出：%w", err), download.Record{}, plan.Differential)
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
		state := app.DownloadState{Stage: stage, Version: plan.Version, Directory: directory, File: progress.File, DoneBytes: progress.DoneBytes, TotalBytes: progress.TotalBytes, BytesPerSecond: max(0, speed), Updating: updating, Differential: plan.Differential}
		d.publishProgress(game, token, state)
	})
	d.finish(game, token, errors.Join(err, lock.Close()), record, plan.Differential)
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
		if d.screen.SelectedGame() == game {
			d.update(apply)
		} else {
			// Keep the model current on the UI thread without rebuilding an
			// unrelated page. Navigation will render the latest progress.
			apply()
		}
	})
}

func (d *downloads) finish(game string, token uint64, err error, record download.Record, differential bool) {
	d.update(func() {
		job := d.jobs[game]
		if job.token != token {
			return
		}
		job.cancel = nil
		job.state.BytesPerSecond = 0
		job.state.Differential = differential
		switch {
		case errors.Is(err, context.Canceled):
			job.state.Stage, job.state.Error = "paused", ""
		case err != nil:
			log.Printf("download %s: %v", game, err)
			job.state.Stage, job.state.Error = "failed", err.Error()
		default:
			job.state = app.DownloadState{Stage: "completed", Version: record.Version, Directory: record.Directory}
			job.installed = record
			job.started = false
			job.offer = downloadOffer{}
			job.forceFull = false
			job.latest = nil
			job.update = app.GameUpdateState{}
			d.screen.SetGameUpdate(game, job.update)
		}
		d.screen.SetDownload(game, job.state)
		if err == nil {
			d.checkGameUpdate(game)
		}
	})
}

// Failures retry automatically; a successful check stays quiet for 30 minutes.
func (d *downloads) checkGameUpdates() {
	for game, job := range d.jobs {
		interval := 30 * time.Minute
		if job.update.Error != "" {
			interval = time.Minute
		}
		if time.Since(job.checkedAt) >= interval {
			d.checkGameUpdate(game)
		}
	}
}

func (d *downloads) checkGameUpdate(game string) {
	job := d.jobs[game]
	if d.ctx.Err() != nil || d.choosing || job == nil || job.state.Stage != "completed" || job.checkCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(d.ctx)
	job.checkCancel = cancel
	job.checkedAt = time.Now()
	installed := job.installed
	token := job.token
	if job.update.Stage != "available" {
		job.update = app.GameUpdateState{Stage: "checking", InstalledVersion: installed.Version}
	}
	d.screen.SetGameUpdate(game, job.update)
	go func() {
		defer cancel()
		latest, err := download.New("").LatestVersion(ctx, download.GameID(game))
		d.update(func() {
			if d.jobs[game] != job || job.token != token || job.state.Stage != "completed" || job.installed != installed {
				return
			}
			job.checkCancel = nil
			state := app.GameUpdateState{Stage: "current", InstalledVersion: installed.Version, LatestVersion: latest.Version}
			if err != nil {
				log.Printf("check game update %s: %v", game, err)
				state.Stage, state.Error = "failed", err.Error()
				if job.latest != nil && job.latest.UpdateAvailable(installed) {
					state.Stage, state.LatestVersion = "available", job.latest.Version
				}
			} else if latest.UpdateAvailable(installed) {
				state.Stage = "available"
			}
			if err == nil {
				job.latest = &latest
			}
			job.update = state
			d.screen.SetGameUpdate(game, state)
		})
	}()
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
