package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMissingSettingsDoesNotCreateFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "settings.json")
	got, err := New(path).Load()
	if err != nil || got != (Settings{}) {
		t.Fatalf("Load() = %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("Load created configuration directory: %v", err)
	}
}

func TestSaveAndLoad(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "config", "settings.json")
	store := New(path)
	if err := store.Save(Settings{DownloadDirectory: filepath.Join(root, ".")}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got.DownloadDirectory != root {
		t.Fatalf("Load() = %+v, %v; want %q", got, err, root)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory validation left files: %v, %v", entries, err)
	}
	if err := store.Save(Settings{DownloadDirectory: root, ArknightsChannel: "arknights-bilibili"}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load()
	if err != nil || got != (Settings{DownloadDirectory: root, ArknightsChannel: "arknights-bilibili"}) {
		t.Fatalf("channel Load() = %+v, %v", got, err)
	}
	if err := store.Save(Settings{}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load()
	if err != nil || got != (Settings{}) {
		t.Fatalf("cleared Load() = %+v, %v", got, err)
	}
	entries, err = os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatalf("Save left temporary files: %v, %v", entries, err)
	}
}

func TestValidateDirectory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "  ", file, filepath.Join(root, "missing")} {
		if _, err := ValidateDirectory(path); err == nil {
			t.Errorf("ValidateDirectory(%q) succeeded", path)
		}
	}
	relative, err := filepath.Rel(mustWorkingDirectory(t), root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ValidateDirectory(relative)
	if err != nil || got != root {
		t.Fatalf("ValidateDirectory(relative) = %q, %v; want %q", got, err, root)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("existing file changed: %q, %v", data, err)
	}
}

func TestUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write directories without write permission")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(root, 0o700); err != nil {
			t.Error(err)
		}
	})
	if _, err := ValidateDirectory(root); err == nil {
		t.Fatal("read-only directory accepted")
	}
}

func TestMalformedSettingsAreReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	for _, data := range []string{"{", "null", " null \n", "[]", `{"download_directory":12}`} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(path).Load(); err == nil {
			t.Errorf("Load accepted malformed settings %q", data)
		}
	}
}

func TestInvalidSavePreservesSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store := New(path)
	root := t.TempDir()
	if err := store.Save(Settings{DownloadDirectory: root}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(Settings{DownloadDirectory: filepath.Join(root, "missing")}); err == nil {
		t.Fatal("Save accepted missing directory")
	}
	got, err := store.Load()
	if err != nil || got.DownloadDirectory != root {
		t.Fatalf("failed Save changed settings: %+v, %v", got, err)
	}
}

func mustWorkingDirectory(t *testing.T) string {
	t.Helper()
	path, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return path
}
