package paths_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pinion/internal/paths"
	"pinion/internal/settings"
	"pinion/internal/wine"
)

func TestDefaultLayoutPreservesHistoricalPaths(t *testing.T) {
	t.Setenv(paths.DataRootEnvironment, "")
	if err := os.Unsetenv(paths.DataRootEnvironment); err != nil {
		t.Fatal(err)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := paths.Layout{
		Data:  filepath.Join(config, "Gryph Launcher"),
		Cache: filepath.Join(cache, "Gryph Launcher"),
		Logs:  filepath.Join(home, "Library", "Logs", "Gryph Launcher"),
	}
	got, err := paths.Resolve()
	if err != nil || got != want {
		t.Fatalf("Resolve() = %+v, %v; want %+v", got, err, want)
	}
}

func TestIsolatedLayoutAndFreshSettings(t *testing.T) {
	homeBefore, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	root := filepath.Join(base, "first")
	t.Setenv(paths.DataRootEnvironment, root+string(filepath.Separator)+".")
	layout, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	want := paths.Layout{Data: root, Cache: filepath.Join(root, "Cache"), Logs: filepath.Join(root, "Logs"), UserData: filepath.Join(root, "UserData")}
	if layout != want {
		t.Fatalf("Resolve() = %+v; want %+v", layout, want)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Resolve created data root: %v", err)
	}
	path, err := settings.DefaultPath()
	if err != nil || path != filepath.Join(root, "settings.json") {
		t.Fatalf("DefaultPath() = %q, %v", path, err)
	}
	store := settings.New(path)
	saved := settings.Settings{DownloadDirectory: base}
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || got != saved {
		t.Fatalf("saved settings = %+v, %v", got, err)
	}
	second := filepath.Join(base, "second")
	t.Setenv(paths.DataRootEnvironment, second)
	path, err = settings.DefaultPath()
	if err != nil || path != filepath.Join(second, "settings.json") {
		t.Fatalf("fresh DefaultPath() = %q, %v", path, err)
	}
	if got, err := settings.New(path).Load(); err != nil || got != (settings.Settings{}) {
		t.Fatalf("fresh settings = %+v, %v", got, err)
	}
	if _, err := os.Stat(second); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loading fresh settings created data root: %v", err)
	}
	if homeAfter, err := os.UserHomeDir(); err != nil || homeAfter != homeBefore {
		t.Fatalf("HOME changed: %q, %v; was %q", homeAfter, err, homeBefore)
	}
}

func TestInvalidRootDoesNotFallBack(t *testing.T) {
	for _, root := range []string{"", ".", "relative", "~/isolated"} {
		t.Run(root, func(t *testing.T) {
			t.Setenv(paths.DataRootEnvironment, root)
			if _, err := paths.Resolve(); err == nil {
				t.Fatal("relative or empty root accepted")
			}
			if _, err := settings.DefaultPath(); err == nil {
				t.Fatal("settings fell back from invalid root")
			}
			if _, err := wine.Discover(context.Background()); err == nil || !strings.Contains(err.Error(), paths.DataRootEnvironment) {
				t.Fatalf("runtime discovery did not reject invalid root: %v", err)
			}
			if _, err := wine.Provision(context.Background(), wine.ProvisionOptions{}, nil); err == nil || !strings.Contains(err.Error(), paths.DataRootEnvironment) {
				t.Fatalf("runtime provisioning did not reject invalid root: %v", err)
			}
		})
	}
}

func TestFreshRuntimeDoesNotReadInstalledRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fresh")
	t.Setenv(paths.DataRootEnvironment, root)
	_, err := wine.Discover(context.Background())
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), root) {
		t.Fatalf("fresh Discover() = %v", err)
	}
	resources := t.TempDir()
	_, err = wine.Provision(context.Background(), wine.ProvisionOptions{ResourcesDir: resources}, nil)
	if !errors.Is(err, wine.ErrPackageUnavailable) {
		t.Fatalf("fresh Provision() = %v; want missing package", err)
	}
	if info, err := os.Stat(filepath.Join(root, "Runtime")); err != nil || !info.IsDir() {
		t.Fatalf("runtime staging was not isolated: %v", err)
	}
}
