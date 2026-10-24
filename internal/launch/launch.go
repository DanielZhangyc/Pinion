// Package launch adapts installed games to the Wine runtime.
package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"pinion/internal/download"
	"pinion/internal/gamewatch"
	"pinion/internal/paths"
	"pinion/internal/wine"
)

type Session struct {
	LogPath   string
	command   *exec.Cmd
	log, lock *os.File
	runtime   wine.Runtime
	prefix    string
}

var ErrAlreadyRunning = errors.New("游戏正在启动或运行")

type gameProfile struct {
	id         download.GameID
	title      string
	executable string
	prefix     string
	backend    string
	graphics   string
	argument   string
}

func profile(game download.GameID) (gameProfile, error) {
	switch game {
	case download.Arknights:
		return gameProfile{game, "明日方舟", "Arknights.exe", "Arknights", "dxmt", "DXMT / D3D11", "-force-d3d11"}, nil
	case download.Endfield:
		return gameProfile{game, "明日方舟：终末地", "Endfield.exe", "Endfield", "vulkan", "Vulkan", "-force-vulkan"}, nil
	default:
		return gameProfile{}, fmt.Errorf("不支持启动的游戏：%q", game)
	}
}

// StartArknights uses the completed installation record, not a guessed version
// path. No game payload, account data, or existing CrossOver bottle is copied.
func StartArknights(ctx context.Context, downloadRoot string, logReady func(string)) (*Session, error) {
	return Start(ctx, download.Arknights, downloadRoot, logReady)
}

func StartEndfield(ctx context.Context, downloadRoot string, logReady func(string)) (*Session, error) {
	return Start(ctx, download.Endfield, downloadRoot, logReady)
}

// Start uses each game's completed installation record and private prefix.
// Existing CrossOver bottles and game payloads are never copied or changed.
func Start(ctx context.Context, game download.GameID, downloadRoot string, logReady func(string)) (_ *Session, resultErr error) {
	p, err := profile(game)
	if err != nil {
		return nil, err
	}
	layout, err := paths.Resolve()
	if err != nil {
		return nil, err
	}
	root := layout.Data
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	lock, err := acquireLock(filepath.Join(root, string(p.id)+"-launch.lock"))
	if err != nil {
		return nil, fmt.Errorf("%s：%w", p.title, err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, lock.Close())
		}
	}()
	running, err := gamewatch.Running(p.executable)
	if err != nil {
		return nil, fmt.Errorf("检查游戏进程：%w", err)
	}
	if running {
		return nil, fmt.Errorf("%s：%w", p.title, ErrAlreadyRunning)
	}
	if downloadRoot == "" {
		return nil, errors.New("请先选择游戏下载目录。")
	}
	record, err := download.New(downloadRoot).LoadRecord(p.id)
	if err != nil {
		return nil, fmt.Errorf("读取%s安装记录：%w", p.title, err)
	}
	executable := filepath.Join(record.Directory, p.executable)
	info, err := os.Stat(executable)
	if err != nil {
		return nil, fmt.Errorf("找不到%s游戏文件：%w", p.title, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s 不是普通文件。", p.executable)
	}
	logs := filepath.Join(layout.Logs, string(p.id))
	if err := os.MkdirAll(logs, 0700); err != nil {
		return nil, err
	}
	logPath, err := os.MkdirTemp(logs, time.Now().Format("20060102-150405")+"-")
	if err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(logPath, "wine.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_, writeErr := fmt.Fprintf(logFile, "\nLaunch failed: %v\n", resultErr)
			resultErr = errors.Join(resultErr, writeErr, logFile.Close())
		}
	}()
	if logReady != nil {
		logReady(logPath)
	}
	runtime, err := wine.Discover(ctx)
	if err != nil {
		return nil, err
	}
	prefix := filepath.Join(root, "Prefixes", p.prefix)
	if _, err := fmt.Fprintf(logFile, "Started: %s\nRuntime: %s\nPrefix: %s\nGame: %s\nVersion: %s\nGraphics: %s\n", time.Now().Format(time.RFC3339), runtime.Root, prefix, executable, record.Version, p.graphics); err != nil {
		return nil, err
	}
	if err := runtime.Prepare(ctx, prefix, p.backend, logFile); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry := executable
	args := []string{p.argument}
	if game == download.Endfield {
		parent, err := prepareEndfieldParent(ctx, root, prefix, record.Directory, logFile)
		if err != nil {
			return nil, err
		}
		entry, err = deployEndfieldBootstrap(root)
		if err != nil {
			return nil, fmt.Errorf("准备终末地启动组件：%w", err)
		}
		args = []string{parent, endfieldWindowsExecutable, endfieldWindowsDirectory}
		if _, err := fmt.Fprintf(logFile, "Bootstrap: version=%s sha256=%s\nBootstrap path: %s\nOfficial parent: %s\nMapped game: %s\n", bootstrapVersion, bootstrapIdentity(), entry, parent, endfieldWindowsExecutable); err != nil {
			return nil, err
		}
		if err := logEndfieldPlayerLogs(prefix, logFile); err != nil {
			return nil, err
		}
	} else {
		unityLog := "Z:" + strings.ReplaceAll(filepath.Join(logPath, "unity.log"), "/", "\\")
		args = append(args, "-logFile", unityLog)
	}
	cmd := runtime.Command(prefix, entry, args, logFile)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动%s失败：%w", p.title, err)
	}
	return &Session{LogPath: logPath, command: cmd, log: logFile, lock: lock, runtime: runtime, prefix: prefix}, nil
}

func acquireLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(ErrAlreadyRunning, err, lock.Close())
		}
		return nil, errors.Join(err, lock.Close())
	}
	return lock, nil
}

// Wait must be called exactly once, even when the launcher window closes.
func (s *Session) Wait() error {
	err := s.command.Wait()
	// SDK/browser children can outlive the main PE process. Hold their log and
	// the game lock until Wine confirms the entire prefix has become idle.
	waitErr := s.runtime.Wait(context.Background(), s.prefix, s.log)
	result := errors.Join(err, waitErr)
	_, logErr := fmt.Fprintf(s.log, "\nExited: %s\nResult: %v\n", time.Now().Format(time.RFC3339), result)
	return errors.Join(result, logErr, s.log.Close(), s.lock.Close())
}
