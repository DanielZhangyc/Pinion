package wine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// This integration packages genuine FineWine and installs only in t.TempDir.
// It never executes Wine, reads a game, or modifies an existing installation.
func TestProvisionRealRuntime(t *testing.T) {
	source := os.Getenv("GRYPH_WINE_PACKAGE_SOURCE")
	if source == "" {
		t.Skip("set GRYPH_WINE_PACKAGE_SOURCE to a genuine complete runtime; no Wine is executed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	resources := filepath.Join(t.TempDir(), "Resources", "runtime")
	descriptor, err := PackageRuntime(ctx, source, resources, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real package: %s; archive=%d bytes, unpacked=%d bytes", descriptor.ID, descriptor.Size, descriptor.UnpackedSize)
	archivePath := filepath.Join(resources, descriptor.Archive)
	before, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	again, err := PackageRuntime(ctx, source, resources, "")
	if err != nil || again != descriptor {
		t.Fatalf("reuse genuine package: %+v %v", again, err)
	}
	after, err := os.Stat(archivePath)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("package reuse rewrote archive: %v", err)
	}
	root := filepath.Join(t.TempDir(), "Runtime")
	options := ProvisionOptions{InstallRoot: root, ResourcesDir: resources}
	stages := make(map[string]bool)
	runtime, err := Provision(ctx, options, func(p ProvisionProgress) { stages[p.Stage] = true })
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ID() != descriptor.ID || runtime.Root != filepath.Join(canonicalRoot, descriptor.ID) || !stages["extracting"] || !stages["ready"] {
		t.Fatalf("wrong install result: %+v stages=%v", runtime, stages)
	}
	data, err := os.ReadFile(filepath.Join(runtime.Root, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Files) <= 23 {
		t.Fatalf("package did not expand the critical-only manifest: %d", len(m.Files))
	}
	t.Logf("verified full manifest: %d files", len(m.Files))
	// A current runtime must not consult a now-unavailable application package.
	reused, err := Provision(ctx, ProvisionOptions{InstallRoot: root, ResourcesDir: filepath.Join(t.TempDir(), "missing")}, nil)
	if err != nil || reused != runtime {
		t.Fatalf("reuse installed runtime without resources: %+v %v", reused, err)
	}
	// Reproduce a crash after version rename and before current publication.
	if err := os.Remove(filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	recovered, err := Provision(ctx, options, nil)
	if err != nil || recovered != runtime {
		t.Fatalf("recover validated unpublished real runtime: %+v %v", recovered, err)
	}
	// A damaged existing current is an error, never an overwrite or reinstall.
	damage := filepath.Join(runtime.Root, "bin", "wine")
	info, err := os.Stat(damage)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(damage, info.Mode()&^0111); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(ctx, options, nil); err == nil {
		t.Fatal("damaged existing runtime was silently accepted or replaced")
	}
	if err := os.Chmod(damage, info.Mode()); err != nil {
		t.Fatal(err)
	}
	// Cross-process installation uses the builder's .install.lock convention.
	lockedRoot := t.TempDir()
	lock, err := os.OpenFile(filepath.Join(lockedRoot, ".install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	_, provisionErr := Provision(ctx, ProvisionOptions{InstallRoot: lockedRoot, ResourcesDir: resources}, nil)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(provisionErr, ErrProvisionBusy) {
		t.Fatalf("installation ignored builder lock: %v", provisionErr)
	}
	// Cancel while extracting the genuine package; incomplete payload is removed.
	cancelRoot := t.TempDir()
	cancelCtx, cancelExtraction := context.WithCancel(ctx)
	_, err = Provision(cancelCtx, ProvisionOptions{InstallRoot: cancelRoot, ResourcesDir: resources}, func(p ProvisionProgress) {
		if p.Stage == "extracting" && p.Completed > 0 {
			cancelExtraction()
		}
	})
	cancelExtraction()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("extraction cancellation: %v", err)
	}
	entries, err := os.ReadDir(cancelRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ".install.lock" {
		t.Fatalf("cancellation left installation payloads: %v", entries)
	}
	// Corrupt the actual archive after successful installation/reuse checks.
	archive, err := os.OpenFile(archivePath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := archive.WriteAt([]byte{0}, 0) // gzip's first byte is 0x1f.
	if err := errors.Join(writeErr, archive.Close()); err != nil {
		t.Fatal(err)
	}
	corruptRoot := t.TempDir()
	if _, err := Provision(ctx, ProvisionOptions{InstallRoot: corruptRoot, ResourcesDir: resources}, nil); err == nil {
		t.Fatal("corrupt genuine archive was accepted")
	}
	if _, err := os.Lstat(filepath.Join(corruptRoot, "current")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt archive published current: %v", err)
	}
	t.Log("real archive packaging/reuse, full-manifest install, installed reuse, crash recovery, damage refusal, flock exclusion, cancellation cleanup and corruption refusal verified; Wine not executed")
}

// TestBundledRuntimeIntegration reads the actual application resources, installs
// in a disposable directory, then uses the existing genuine-Wine integration
// checks against that installed copy. Both opt-ins are required to run Wine.
func TestBundledRuntimeIntegration(t *testing.T) {
	resources := os.Getenv("GRYPH_WINE_BUNDLED_RESOURCES")
	if resources == "" || os.Getenv("GRYPH_WINE_INTEGRATION") != "1" {
		t.Skip("set GRYPH_WINE_BUNDLED_RESOURCES and GRYPH_WINE_INTEGRATION=1 to validate installed package with real Wine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	runtime, err := Provision(ctx, ProvisionOptions{InstallRoot: filepath.Join(t.TempDir(), "Runtime"), ResourcesDir: resources}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRYPH_WINE_RUNTIME", runtime.Root)
	t.Logf("actual bundled package installed at %s", runtime.Root)
	t.Run("InstalledRuntime", TestRuntimeIntegration)
}
