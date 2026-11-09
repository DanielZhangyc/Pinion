package wine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"pinion/internal/paths"
)

var ErrLaunchCacheBusy = errors.New("Wine 启动缓存仍被进程使用，请退出所有 Wine 程序后重试")

type CacheRepairResult struct {
	Repaired   int
	BackupPath string
}

// RepairLaunchCache quarantines only incomplete bundles belonging to this
// validated loader. Wine recreates them on the next normal launch.
func (r Runtime) RepairLaunchCache(ctx context.Context) (CacheRepairResult, error) {
	if err := ctx.Err(); err != nil {
		return CacheRepairResult{}, err
	}
	if !filepath.IsAbs(r.Root) || r.id == "" {
		return CacheRepairResult{}, errors.New("Wine 运行环境尚未验证")
	}
	// Wine uses confstr(_CS_DARWIN_USER_TEMP_DIR), independently of TMPDIR.
	data, err := exec.CommandContext(ctx, "/usr/bin/getconf", "DARWIN_USER_TEMP_DIR").Output()
	if err != nil {
		return CacheRepairResult{}, fmt.Errorf("读取 Wine 原生临时目录：%w", err)
	}
	temp := strings.TrimSpace(string(data))
	if !filepath.IsAbs(temp) {
		return CacheRepairResult{}, errors.New("Wine 原生临时目录无效")
	}
	layout, err := paths.Resolve()
	if err != nil {
		return CacheRepairResult{}, err
	}
	return repairLaunchCache(ctx, filepath.Join(r.Root, "lib", "wine", "x86_64-unix", "wine"), temp, layout.Logs)
}

func loaderCachePath(loader, temp string) (string, os.FileInfo, error) {
	info, err := os.Lstat(loader)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&0100 == 0 {
		return "", nil, errors.New("Wine loader 不是普通可执行文件")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", nil, errors.New("无法读取 Wine loader 文件标识")
	}
	return filepath.Join(temp, fmt.Sprintf("winetemp-%d-%d-%d-%d", st.Ino, st.Size, st.Mtimespec.Sec, st.Mtimespec.Nsec)), info, nil
}

func repairLaunchCache(ctx context.Context, loader, temp, backups string) (result CacheRepairResult, resultErr error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	temp, err := filepath.EvalSymlinks(temp)
	if err != nil {
		return result, err
	}
	cache, loaderInfo, err := loaderCachePath(loader, temp)
	if err != nil {
		return result, fmt.Errorf("读取 Wine loader：%w", err)
	}
	cacheInfo, err := ownedCacheDirectory(cache)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if err := validateCacheNTDLL(cache, loader); err != nil {
		return result, err
	}
	if err := idleLaunchCache(ctx, cache); err != nil {
		return result, err
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		return result, err
	}
	var apps []string
	var locks []*os.File
	defer func() {
		for _, lock := range locks {
			resultErr = errors.Join(resultErr, lock.Close())
		}
	}()
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".app") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".app")
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
			return result, errors.New("Wine 启动缓存包含异常应用包名称")
		}
		app := filepath.Join(cache, entry.Name())
		lock, err := lockCacheApp(app + ".lock")
		if err != nil {
			return result, err
		}
		locks = append(locks, lock)
		apps = append(apps, app)
	}
	loaderData, err := os.ReadFile(loader)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(loaderData)
	type damagedApp struct {
		path string
		info os.FileInfo
	}
	var damaged []damagedApp
	for _, app := range apps {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		complete, info, err := inspectCacheApp(app, loaderInfo.Size(), digest)
		if err != nil {
			return result, err
		}
		if !complete {
			damaged = append(damaged, damagedApp{app, info})
		}
	}
	if len(damaged) == 0 {
		return result, nil
	}
	if err := os.MkdirAll(backups, 0700); err != nil {
		return result, fmt.Errorf("创建 Wine 缓存备份目录：%w", err)
	}
	if _, err := ownedCacheDirectory(backups); err != nil {
		return result, err
	}
	backup, err := os.MkdirTemp(backups, "loader-cache-recovery-*")
	if err != nil {
		return result, err
	}
	defer func() {
		if result.Repaired == 0 {
			resultErr = errors.Join(resultErr, os.Remove(backup))
		}
	}()
	for _, app := range damaged {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		current, err := ownedCacheDirectory(cache)
		if err != nil || !os.SameFile(cacheInfo, current) {
			return result, errors.New("Wine 缓存目录在修复期间发生变化，已停止")
		}
		_, currentLoader, err := loaderCachePath(loader, temp)
		if err != nil || !os.SameFile(loaderInfo, currentLoader) || !loaderInfo.ModTime().Equal(currentLoader.ModTime()) || loaderInfo.Size() != currentLoader.Size() {
			return result, errors.New("Wine loader 在修复期间发生变化，已停止")
		}
		complete, currentApp, err := inspectCacheApp(app.path, loaderInfo.Size(), digest)
		if err != nil {
			return result, err
		}
		if !os.SameFile(app.info, currentApp) {
			return result, errors.New("Wine 应用缓存在修复期间发生变化，已停止")
		}
		if complete {
			continue
		}
		for _, lock := range locks {
			opened, err := lock.Stat()
			current, pathErr := os.Lstat(lock.Name())
			if err != nil || pathErr != nil || !os.SameFile(opened, current) {
				return result, errors.New("Wine 缓存锁在修复期间发生变化，已停止")
			}
		}
		if err := validateCacheNTDLL(cache, loader); err != nil {
			return result, err
		}
		if err := idleLaunchCache(ctx, cache); err != nil {
			return result, err
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := os.Rename(app.path, filepath.Join(backup, filepath.Base(app.path))); err != nil {
			return result, fmt.Errorf("备份损坏 Wine 缓存 %s：%w", filepath.Base(app.path), err)
		}
		result.Repaired++
		result.BackupPath = backup
	}
	return result, nil
}

func validateCacheNTDLL(cache, loader string) error {
	link := filepath.Join(cache, "ntdll.so")
	target, err := os.Readlink(link)
	if errors.Is(err, os.ErrNotExist) {
		return nil // Wine recreates its root link before publishing an app.
	}
	if err != nil || !filepath.IsAbs(target) {
		return errors.New("Wine 缓存根 ntdll 链接异常")
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(loader), "ntdll.so"))
	if err != nil {
		return err
	}
	actual, err := filepath.EvalSymlinks(target)
	if err != nil || actual != expected {
		return errors.New("Wine 缓存根 ntdll 链接不属于当前运行环境")
	}
	return nil
}

func ownedCacheDirectory(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || st.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("拒绝处理异常 Wine 缓存目录：%s", path)
	}
	return info, nil
}

func lockCacheApp(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("打开 Wine 缓存锁：%w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || !ok || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
			err = errors.New("Wine 缓存锁文件异常")
		}
	}
	if err == nil {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			err = ErrLaunchCacheBusy
		}
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

// A matching executable digest establishes provenance even when Info.plist is
// absent. Refuse unexplained files and symlinks rather than moving arbitrary data.
func inspectCacheApp(app string, loaderSize int64, digest [sha256.Size]byte) (bool, os.FileInfo, error) {
	info, err := ownedCacheDirectory(app)
	if err != nil {
		return false, nil, err
	}
	name := strings.TrimSuffix(filepath.Base(app), ".app")
	allowed := map[string]bool{".": true, "Contents": true, "Contents/MacOS": true, "Contents/Info.plist": true, "Contents/MacOS/" + name: true, "Contents/MacOS/ntdll.so": true}
	err = filepath.WalkDir(app, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(app, path)
		if err != nil {
			return err
		}
		if !allowed[rel] || entry.Type()&os.ModeSymlink != 0 && rel != "Contents/MacOS/ntdll.so" {
			return fmt.Errorf("拒绝处理含异常文件或链接的 Wine 缓存：%s", path)
		}
		if rel == "." || rel == "Contents" || rel == "Contents/MacOS" {
			_, err = ownedCacheDirectory(path)
			return err
		}
		return nil
	})
	if err != nil {
		return false, nil, err
	}
	executable := filepath.Join(app, "Contents", "MacOS", name)
	exeInfo, err := os.Lstat(executable)
	if err != nil || !exeInfo.Mode().IsRegular() || exeInfo.Size() != loaderSize {
		return false, nil, fmt.Errorf("无法确认 Wine 缓存归属：%s", app)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		return false, nil, err
	}
	if sha256.Sum256(data) != digest {
		return false, nil, fmt.Errorf("Wine 缓存可执行文件与当前 loader 不符：%s", app)
	}
	complete := exeInfo.Mode()&0100 != 0
	plist, err := os.Lstat(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, nil, err
	}
	complete = complete && err == nil && plist.Mode().IsRegular() && plist.Size() > 0
	link := filepath.Join(app, "Contents", "MacOS", "ntdll.so")
	target, err := os.Readlink(link)
	if errors.Is(err, os.ErrNotExist) {
		return false, info, nil
	}
	if err != nil || target != "../../../ntdll.so" {
		return false, nil, fmt.Errorf("Wine 缓存 ntdll 链接异常：%s", link)
	}
	ntdll, err := os.Stat(link)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, nil, err
	}
	complete = complete && err == nil && ntdll.Mode().IsRegular()
	return complete, info, nil
}

func idleLaunchCache(ctx context.Context, cache string) error {
	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-Fpcn", "+D", cache)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if stderr.Len() != 0 {
		return fmt.Errorf("无法确认 Wine 缓存空闲：%s", strings.TrimSpace(stderr.String()))
	}
	if err != nil {
		var exit *exec.ExitError
		// macOS lsof can return 1 with valid matching records (including our
		// own lock descriptors). Parse those records before deciding it is idle.
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return fmt.Errorf("检查 Wine 缓存进程：%w", err)
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "p") {
			pid, err := strconv.Atoi(strings.TrimPrefix(line, "p"))
			if err != nil {
				return errors.New("无法解析 Wine 缓存进程信息")
			}
			if pid != os.Getpid() {
				return ErrLaunchCacheBusy
			}
		}
	}
	return nil
}
