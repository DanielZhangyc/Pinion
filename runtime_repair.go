package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"pinion/internal/app"
	"pinion/internal/launch"
	"pinion/internal/wine"
)

func (r *runtimeSetup) repair() {
	if r.ctx.Err() != nil || r.cancel != nil || !r.screen.RuntimeRepairAvailable() {
		return
	}
	r.screen.SetRuntimeRepair(app.RuntimeRepairState{Stage: "repairing"})
	go func() {
		ctx, cancel := context.WithTimeout(r.ctx, time.Minute)
		defer cancel()
		result, err := repairRuntimeLaunchCache(ctx)
		state := app.RuntimeRepairState{Stage: "completed", Message: "启动缓存检查完成，未发现损坏。"}
		if result.Repaired > 0 {
			state.Message = fmt.Sprintf("已修复 %d 项启动缓存，请重新启动游戏。", result.Repaired)
		}
		if err != nil {
			log.Printf("repair launch cache: %v (backup: %s)", err, result.BackupPath)
			state.Stage = "failed"
			state.Message = "修复未完成：" + err.Error()
			if result.Repaired > 0 {
				state.Message = fmt.Sprintf("已修复 %d 项，其余修复未完成：%v", result.Repaired, err)
			}
		} else if result.BackupPath != "" {
			log.Printf("repaired %d launch cache bundles; backup: %s", result.Repaired, result.BackupPath)
		}
		r.update(func() { r.screen.SetRuntimeRepair(state) })
	}()
}

// Use the same locks as launch and installation, in a fixed order. Both games
// share this loader cache, including when another Pinion process is open.
func repairRuntimeLaunchCache(ctx context.Context) (result wine.CacheRepairResult, resultErr error) {
	var locks []*os.File
	defer func() {
		for _, lock := range locks {
			resultErr = errors.Join(resultErr, lock.Close())
		}
	}()
	for _, game := range launchGames {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		lock, err := launch.LockForUpdate(game.id)
		if err != nil {
			return result, fmt.Errorf("请先退出游戏并等待更新结束，再重试修复：%w", err)
		}
		locks = append(locks, lock)
	}
	runtime, err := wine.Discover(ctx)
	if err != nil {
		return result, err
	}
	return runtime.RepairLaunchCache(ctx)
}
