package launch

import (
	"os"
	"path/filepath"
	"testing"

	"pinion/internal/download"
)

// This check reads a genuine completed installation and its configured prefix.
// It does not execute Wine, the official launcher, or the game.
func TestEndfieldBootstrapInstallation(t *testing.T) {
	if os.Getenv("GRYPH_BOOTSTRAP_LAUNCH_INTEGRATION") != "1" {
		t.Skip("set GRYPH_BOOTSTRAP_LAUNCH_INTEGRATION=1 for the read-only installation check")
	}
	root := os.Getenv("GRYPH_DOWNLOAD_ROOT")
	if !filepath.IsAbs(root) {
		t.Fatal("GRYPH_DOWNLOAD_ROOT must identify the real absolute download directory")
	}
	record, err := download.New(root).LoadRecord(download.Endfield)
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(config, "Gryph Launcher", "Prefixes", "Endfield")
	entry, err := endfieldParent(prefix, record.Directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("official parent: %s; completed game: %s", entry, record.Directory)
	if err := logEndfieldPlayerLogs(prefix, os.Stdout); err != nil {
		t.Fatal(err)
	}
}
