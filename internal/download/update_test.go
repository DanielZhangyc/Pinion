package download

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"sync"
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

// Both Arknights channels publish packages under one download root. Real paused
// installs must keep separate caches and records, and a held installation lock
// must exclude only its own distribution, including from another Manager.
func TestOfficialDistributionIsolation(t *testing.T) {
	if os.Getenv("GRYPH_DOWNLOAD_INTEGRATION") != "1" {
		t.Skip("set GRYPH_DOWNLOAD_INTEGRATION=1 for bounded official distribution isolation checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	dir := t.TempDir()
	m := New(dir)
	defer m.client.CloseIdleConnections()
	traffic := &trafficTransport{base: m.client.Transport, limit: 16 << 20}
	m.client.Transport = traffic
	official, err := m.Resolve(ctx, Arknights)
	if err != nil {
		t.Fatal(err)
	}
	bilibili, err := m.Resolve(ctx, ArknightsBilibili)
	if err != nil {
		t.Fatal(err)
	}
	if official.ManifestMD5 == bilibili.ManifestMD5 || official.Packs[0].URL == bilibili.Packs[0].URL {
		t.Fatal("official and Bilibili channels resolved the same package")
	}
	pause := func(installer *Manager, plan Plan) {
		t.Helper()
		paused, stop := context.WithCancel(ctx)
		defer stop()
		_, err := installer.Install(paused, plan, func(p Progress) {
			if p.Stage == StageDownloading && p.DoneBytes >= 32<<10 {
				stop()
			}
		})
		if err != nil && strings.Contains(err.Error(), "insufficient disk space") {
			t.Skipf("real full-plan space preflight: %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pause %s Install: %v", plan.Game, err)
		}
	}
	partial := func(plan Plan) (os.FileInfo, string) {
		t.Helper()
		root, err := m.openGame(plan.Game, false)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		name := packName(path.Join("versions", plan.Version), 0) + ".part"
		info, err := root.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		data, err := root.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return info, sumMD5(data)
	}

	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	go func() {
		paused, stop := context.WithCancel(ctx)
		defer stop()
		_, err := m.Install(paused, bilibili, func(p Progress) {
			if p.Stage == StageDownloading && p.DoneBytes >= 32<<10 && paused.Err() == nil {
				close(held)
				<-release
				stop()
			}
		})
		done <- err
	}()
	select {
	case <-held:
	case err := <-done:
		if err != nil && strings.Contains(err.Error(), "insufficient disk space") {
			t.Skipf("real full-plan space preflight: %v", err)
		}
		t.Fatalf("Bilibili Install ended before holding its lock: %v", err)
	}
	other := New(dir)
	other.client = m.client
	if _, err := other.Install(ctx, bilibili, nil); err == nil || !strings.Contains(err.Error(), "another installation is active") {
		t.Fatalf("second Manager entered a held Bilibili installation: %v", err)
	}
	pause(other, official)
	officialInfo, officialMD5 := partial(official)
	unblock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("held Bilibili Install: %v", err)
	}
	firstInfo, _ := partial(bilibili)
	pause(m, bilibili)
	bilibiliInfo, _ := partial(bilibili)
	if bilibiliInfo.Size() <= firstInfo.Size() {
		t.Fatalf("Bilibili Install did not resume: %d -> %d", firstInfo.Size(), bilibiliInfo.Size())
	}
	afterInfo, afterMD5 := partial(official)
	if !os.SameFile(officialInfo, afterInfo) || officialInfo.Size() != afterInfo.Size() || officialMD5 != afterMD5 {
		t.Fatal("Bilibili Install changed the official package cache")
	}
	if os.SameFile(officialInfo, bilibiliInfo) {
		t.Fatal("official and Bilibili installs share a package cache")
	}
	for _, game := range []GameID{Arknights, ArknightsBilibili} {
		if _, err := m.LoadRecord(game); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("paused %s install has completion record: %v", game, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != "arknights,arknights-bilibili" {
		t.Fatalf("download root entries = %v", names)
	}
	t.Logf("version official=%s bilibili=%s; official partial=%d unchanged; Bilibili partial %d -> %d; HTTP bytes=%d", official.Version, bilibili.Version, officialInfo.Size(), firstInfo.Size(), bilibiliInfo.Size(), traffic.bytes)
}
