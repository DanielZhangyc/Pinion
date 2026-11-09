package wine

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// These checks copy genuine loader bundles into disposable keyed directories.
// They never execute Wine or modify the source runtime, cache, or backup.
func TestLaunchCacheRepairRealBundles(t *testing.T) {
	samples := os.Getenv("GRYPH_WINE_CACHE_REPAIR_SAMPLES")
	complete := os.Getenv("GRYPH_WINE_CACHE_REPAIR_COMPLETE")
	if samples == "" || complete == "" {
		t.Skip("set GRYPH_WINE_CACHE_REPAIR_SAMPLES to genuine damaged backup and GRYPH_WINE_CACHE_REPAIR_COMPLETE to a genuine complete .app")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	runtime, err := Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	copyReal := func(source, dest string) {
		t.Helper()
		if data, err := exec.CommandContext(ctx, "/bin/cp", "-pR", source, dest).CombinedOutput(); err != nil {
			t.Fatalf("copy genuine sample: %v: %s", err, data)
		}
	}
	root := t.TempDir()
	loader := filepath.Join(root, "wine")
	copyReal(filepath.Join(runtime.Root, "lib", "wine", "x86_64-unix", "wine"), loader)
	if err := os.Symlink(filepath.Join(runtime.Root, "lib", "wine", "x86_64-unix", "ntdll.so"), filepath.Join(root, "ntdll.so")); err != nil {
		t.Fatal(err)
	}
	cache, _, err := loaderCachePath(loader, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(runtime.Root, "lib", "wine", "x86_64-unix", "ntdll.so"), filepath.Join(cache, "ntdll.so")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(samples)
	if err != nil {
		t.Fatal(err)
	}
	originals := make(map[string]os.FileInfo)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".app" {
			continue
		}
		path := filepath.Join(cache, entry.Name())
		copyReal(filepath.Join(samples, entry.Name()), path)
		originals[entry.Name()], err = os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(originals) == 0 {
		t.Fatal("genuine damaged sample directory has no applications")
	}
	completeName := filepath.Base(complete)
	if _, collision := originals[completeName]; collision {
		t.Fatal("complete sample name collides with damaged sample")
	}
	completePath := filepath.Join(cache, completeName)
	copyReal(complete, completePath)
	completeInfo, err := os.Lstat(completePath)
	if err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(root, "Logs")
	assertUnchanged := func() {
		t.Helper()
		for name, before := range originals {
			after, err := os.Lstat(filepath.Join(cache, name))
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("sample changed before successful repair: %s: %v", name, err)
			}
		}
		if _, err := os.Lstat(backups); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed/no-op repair created backups: %v", err)
		}
	}
	cancelled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if result, err := repairLaunchCache(cancelled, loader, root, backups); !errors.Is(err, context.Canceled) || result.Repaired != 0 {
		t.Fatalf("cancellation: %+v %v", result, err)
	}
	assertUnchanged()
	var first string
	for name := range originals {
		first = name
		break
	}
	lock, err := lockCacheApp(filepath.Join(cache, first) + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	result, repairErr := repairLaunchCache(ctx, loader, root, backups)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(repairErr, ErrLaunchCacheBusy) || result.Repaired != 0 {
		t.Fatalf("native flock exclusion: %+v %v", result, repairErr)
	}
	assertUnchanged()
	// A real external process holds a genuine cached executable open. No Wine
	// process or synthetic executable is needed to verify lsof exclusion.
	held := filepath.Join(cache, first, "Contents", "MacOS", first[:len(first)-len(".app")])
	reader := exec.CommandContext(ctx, "/usr/bin/tail", "-f", held)
	reader.Stdout, reader.Stderr = io.Discard, io.Discard
	if err := reader.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Process.Kill(); _ = reader.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := idleLaunchCache(ctx, cache)
		if errors.Is(err, ErrLaunchCacheBusy) {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("real process was not detected: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	result, repairErr = repairLaunchCache(ctx, loader, root, backups)
	if !errors.Is(repairErr, ErrLaunchCacheBusy) || result.Repaired != 0 {
		t.Fatalf("external process exclusion: %+v %v", result, repairErr)
	}
	assertUnchanged()
	if err := reader.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Wait()
	result, err = repairLaunchCache(ctx, loader, root, backups)
	if err != nil || result.Repaired != len(originals) || result.BackupPath == "" {
		t.Fatalf("repair genuine damaged samples: %+v %v", result, err)
	}
	for name, before := range originals {
		after, err := os.Lstat(filepath.Join(result.BackupPath, name))
		if err != nil || !os.SameFile(before, after) {
			t.Fatalf("backup is not original renamed sample: %s: %v", name, err)
		}
		if _, err := os.Lstat(filepath.Join(cache, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("damaged sample not isolated: %s: %v", name, err)
		}
	}
	after, err := os.Lstat(completePath)
	if err != nil || !os.SameFile(completeInfo, after) {
		t.Fatalf("genuine complete application was changed: %v", err)
	}
	again, err := repairLaunchCache(ctx, loader, root, backups)
	if err != nil || again.Repaired != 0 || again.BackupPath != "" {
		t.Fatalf("complete cache repeat: %+v %v", again, err)
	}
	missing, err := repairLaunchCache(ctx, loader, t.TempDir(), backups)
	if err != nil || missing.Repaired != 0 || missing.BackupPath != "" {
		t.Fatalf("missing keyed cache: %+v %v", missing, err)
	}
	t.Logf("%d genuine damaged bundles atomically backed up; complete bundle preserved; cancellation, native lock, external process, idempotence and missing-cache behavior verified; Wine not executed", result.Repaired)
}
