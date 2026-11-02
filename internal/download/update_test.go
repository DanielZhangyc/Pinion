package download

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"testing"
	"time"
)

func TestCompletionRecordSymlinkRejected(t *testing.T) {
	m := New(t.TempDir())
	root, err := m.openGame(Arknights, true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := root.ReadFile(".gryph-download.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink(".gryph-download.json", "completed.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.LoadRecord(Arknights); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("linked completion record: %v", err)
	}
	after, err := root.ReadFile(".gryph-download.json")
	if err != nil || string(after) != string(before) {
		t.Fatalf("ownership metadata changed: %v", err)
	}
}

// This checks the actual Install path with genuine official metadata and bytes.
// Corrupting the first candidate's manifest marker exercises a directory identity
// conflict without inventing a game package, completion record or release.
func TestOfficialUpdateDirectoryIsolation(t *testing.T) {
	if os.Getenv("GRYPH_DOWNLOAD_INTEGRATION") != "1" {
		t.Skip("set GRYPH_DOWNLOAD_INTEGRATION=1 for bounded official update-directory checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	m := New(t.TempDir())
	defer m.client.CloseIdleConnections()
	traffic := &trafficTransport{base: m.client.Transport, limit: 8 << 20}
	m.client.Transport = traffic
	plan, err := m.Resolve(ctx, Arknights)
	if err != nil {
		t.Fatal(err)
	}
	pause := func() {
		t.Helper()
		paused, stop := context.WithCancel(ctx)
		_, err := m.Install(paused, plan, func(p Progress) {
			if p.Stage == StageDownloading && p.DoneBytes >= 32<<10 {
				stop()
			}
		})
		stop()
		if err != nil && strings.Contains(err.Error(), "insufficient disk space") {
			t.Skipf("real full-plan space preflight: %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pause Install: %v", err)
		}
	}
	pause()
	root, err := m.openGame(Arknights, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	legacy := path.Join("versions", plan.Version)
	oldPartial, err := root.Stat(packName(legacy, 0) + ".part")
	if err != nil {
		t.Fatal(err)
	}
	marker, err := root.OpenFile(path.Join(legacy, "manifest.md5"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := marker.Truncate(0); err != nil {
		marker.Close()
		t.Fatal(err)
	}
	if err := marker.Close(); err != nil {
		t.Fatal(err)
	}
	pause()
	qualified := legacy + "-" + plan.ManifestMD5
	newPartial, err := root.Stat(packName(qualified, 0) + ".part")
	if err != nil || newPartial.Size() <= 0 {
		t.Fatalf("manifest-qualified candidate was not retained: %v", err)
	}
	retained, err := root.Stat(packName(legacy, 0) + ".part")
	if err != nil || !os.SameFile(oldPartial, retained) || oldPartial.Size() != retained.Size() {
		t.Fatalf("previous candidate changed: %v", err)
	}
	oldMarker, err := root.Stat(path.Join(legacy, "manifest.md5"))
	if err != nil || oldMarker.Size() != 0 {
		t.Fatalf("previous marker was overwritten: %v", err)
	}
	if _, err := m.LoadRecord(Arknights); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("paused candidate was marked complete: %v", err)
	}
	// Reopening the manifest-qualified candidate must resume the same identity.
	base, err := prepareVersion(root, plan)
	if err != nil || base != qualified {
		t.Fatalf("resume candidate = %q: %v", base, err)
	}
	t.Logf("official version=%s legacy partial=%d new partial=%d; retained both candidates; HTTP bytes=%d", plan.Version, oldPartial.Size(), newPartial.Size(), traffic.bytes)
}
