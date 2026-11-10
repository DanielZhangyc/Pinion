package download

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOwnedDirectory(t *testing.T) {
	m := New(t.TempDir())
	for _, game := range []GameID{Arknights, ArknightsBilibili, Endfield} {
		root, err := m.openGame(game, true)
		if err != nil {
			t.Fatalf("create empty %s directory: %v", game, err)
		}
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := m.LoadRecord(game); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing completion: %v", err)
		}
		root, err = m.openGame(game, true)
		if err != nil {
			t.Fatalf("reopen owned directory: %v", err)
		}
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// These opt-in tests use current official servers and genuine published files.
// They never download a complete game or use a local HTTP stand-in.
func TestOfficialDownloads(t *testing.T) {
	if os.Getenv("GRYPH_DOWNLOAD_INTEGRATION") != "1" {
		t.Skip("set GRYPH_DOWNLOAD_INTEGRATION=1 for bounded official network checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	m := New(t.TempDir())
	traffic := &trafficTransport{base: m.client.Transport, limit: 40 << 20}
	m.client.Transport = traffic
	for _, game := range []GameID{Arknights, ArknightsBilibili, Endfield} {
		t.Run(string(game), func(t *testing.T) {
			plan, err := m.Resolve(ctx, game)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("game=%s version=%s packs=%d download=%d expanded=%d manifest=%d files=%d", game, plan.Version, len(plan.Packs), plan.DownloadBytes, plan.UnpackedBytes, plan.ManifestBytes, len(plan.Files))
			if game != Endfield {
				t.Run("manager_pause_and_resume", func(t *testing.T) {
					installer := New(t.TempDir())
					installer.client = m.client
					var previous int64
					for attempt := 0; attempt < 2; attempt++ {
						paused, stop := context.WithCancel(ctx)
						_, installErr := installer.Install(paused, plan, func(p Progress) {
							if p.Stage == StageDownloading && p.DoneBytes >= previous+(32<<10) {
								stop()
							}
						})
						stop()
						if installErr != nil && strings.Contains(installErr.Error(), "insufficient disk space") {
							t.Skipf("real full plan space preflight: %v", installErr)
						}
						if !errors.Is(installErr, context.Canceled) {
							t.Fatalf("Install attempt %d: %v", attempt+1, installErr)
						}
						owned, err := installer.openGame(game, false)
						if err != nil {
							t.Fatal(err)
						}
						info, err := owned.Stat(packName(path.Join("versions", plan.Version), 0) + ".part")
						owned.Close()
						if err != nil {
							t.Fatal(err)
						}
						if info.Size() <= previous {
							t.Fatalf("Install did not resume: previous=%d current=%d", previous, info.Size())
						}
						previous = info.Size()
						if _, err := installer.LoadRecord(game); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("partial installation has completion record: %v", err)
						}
						t.Logf("actual Install attempt=%d retained first package=%d bytes", attempt+1, previous)
					}
				})
			}
			rootDir := t.TempDir()
			root, err := os.OpenRoot(rootDir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			p := Pack{URL: plan.ManifestURL, MD5: plan.ManifestMD5, Size: plan.ManifestBytes}
			paused, stop := context.WithCancel(ctx)
			err = m.fetchPack(paused, root, p, "game_files", func(n int64) {
				if n >= 32<<10 {
					stop()
				}
			})
			stop()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("pause: got %v", err)
			}
			partial, err := root.Stat("game_files.part")
			if err != nil {
				t.Fatal(err)
			}
			if partial.Size() <= 0 || partial.Size() >= p.Size {
				t.Fatalf("unexpected paused size %d / %d", partial.Size(), p.Size)
			}
			if _, err := root.Stat("game_files"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial marked complete: %v", err)
			}
			if err := m.fetchPack(ctx, root, p, "game_files", nil); err != nil {
				t.Fatalf("Range resume: %v", err)
			}
			if err := verifyFile(ctx, root, "game_files", p.Size, p.MD5); err != nil {
				t.Fatal(err)
			}
			if _, err := root.Stat("game_files.part"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial not committed: %v", err)
			}
			t.Logf("paused=%d bytes; exact Content-Range resumed; final MD5=%s", partial.Size(), p.MD5)
			// Reusing a completed package rechecks its size and full MD5 without HTTP.
			before := traffic.bytes
			if err := m.fetchPack(ctx, root, p, "game_files", nil); err != nil {
				t.Fatal(err)
			}
			if traffic.bytes != before {
				t.Fatal("completed cache unexpectedly downloaded")
			}
			// Deliberately corrupt a genuine downloaded cache, then prove that a
			// checksum error removes only that damaged cache and permits retry.
			f, err := root.OpenFile("game_files", os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteAt([]byte{0}, p.Size-1); err != nil {
				f.Close()
				t.Fatal(err)
			}
			if err := f.Truncate(p.Size - 1); err != nil {
				f.Close()
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if err := m.fetchPack(ctx, root, p, "game_files", nil); !errors.Is(err, ErrChecksum) {
				t.Fatalf("corruption: %v", err)
			}
			if _, err := root.Stat("game_files"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("corrupt cache retained: %v", err)
			}
			if err := m.fetchPack(ctx, root, p, "game_files", nil); err != nil {
				t.Fatalf("retry damaged cache: %v", err)
			}
			// Prove a real Range read can span the physical .001/.002 boundary.
			remote := &remoteArchive{ctx: ctx, manager: m, packs: plan.Packs, budget: 4 << 20}
			boundary := make([]byte, 64)
			if n, err := remote.ReadAt(boundary, plan.Packs[0].Size-32); err != nil || n != 64 {
				t.Fatalf("split range: n=%d err=%v", n, err)
			}
			zr, err := zip.NewReader(remote, plan.DownloadBytes)
			if err != nil {
				t.Fatal(err)
			}
			known := map[string]File{}
			for _, f := range plan.Files {
				known[f.Path] = f
			}
			var selected *zip.File
			var expected File
			for _, f := range zr.File {
				if mf, ok := known[f.Name]; ok && mf.Size > 0 && mf.Size <= 16<<10 && (selected == nil || mf.Size < expected.Size) {
					selected = f
					expected = mf
				}
			}
			if selected == nil {
				t.Fatal("no genuine small ZIP entry available for bounded extraction")
			}
			if err := extractOne(ctx, root, selected, "extracted", "extracting.tmp", nil); err != nil {
				t.Fatal(err)
			}
			if err := verifyFile(ctx, root, "extracted", expected.Size, expected.MD5); err != nil {
				t.Fatal(err)
			}
			item := archiveItem{selected.Name, expected.Size, selected.CRC32, false}
			if ok, err := matchesArchiveFile(ctx, root, "extracted", item); err != nil || !ok {
				t.Fatalf("resume extracted entry: valid=%v err=%v", ok, err)
			}
			if err := root.Symlink(rootDir, "link"); err != nil {
				t.Fatal(err)
			}
			if err := regular(root, "link/game_files"); err == nil {
				t.Fatal("followed a symlink ancestor")
			}
			t.Logf("extracted genuine ZIP entry %s (%d bytes), CRC and manifest MD5 verified", selected.Name, expected.Size)
			// A selected existing nonempty directory must not be adopted.
			unsafeRoot := t.TempDir()
			if err := os.Mkdir(filepath.Join(unsafeRoot, string(game)), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(unsafeRoot, string(game), "keep"), []byte(plan.Version), 0644); err != nil {
				t.Fatal(err)
			}
			if owned, err := New(unsafeRoot).openGame(game, true); err == nil {
				owned.Close()
				t.Fatal("adopted unowned existing directory")
			}
		})
	}
	t.Logf("actual HTTP body bytes read=%d; maximum=%d", traffic.bytes, traffic.limit)
}

const fullInstallMarker = ".pinion-full-install-acceptance"

// This opt-in acceptance downloads, extracts and fully verifies one genuine
// official game in a dedicated directory. Interrupted runs resume from the
// retained packages; a repeated run re-verifies the completed installation.
func TestOfficialFullInstall(t *testing.T) {
	if os.Getenv("GRYPH_FULL_INSTALL_INTEGRATION") != "1" {
		t.Skip("set GRYPH_FULL_INSTALL_INTEGRATION=1, GRYPH_FULL_INSTALL_GAME and GRYPH_DOWNLOAD_ROOT for a complete official installation")
	}
	game := GameID(os.Getenv("GRYPH_FULL_INSTALL_GAME"))
	root := os.Getenv("GRYPH_DOWNLOAD_ROOT")
	if !filepath.IsAbs(root) {
		t.Fatal("GRYPH_DOWNLOAD_ROOT must identify a dedicated absolute download directory")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	// Only an empty directory is initialized, and only a directory carrying the
	// acceptance marker is resumed, so a real download root is never used.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, fullInstallMarker)
	if len(entries) == 0 {
		if err := os.WriteFile(marker, []byte(fullInstallMarker+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	} else if info, err := os.Lstat(marker); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s is neither empty nor a marked acceptance directory: %v", root, err)
	} else if data, err := os.ReadFile(marker); err != nil || string(data) != fullInstallMarker+"\n" {
		t.Fatalf("acceptance marker mismatch in %s: %v", root, err)
	}
	m := New(root)
	defer m.client.CloseIdleConnections()
	plan, err := m.Resolve(t.Context(), game)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("game=%s version=%s download=%d expanded=%d files=%d", game, plan.Version, plan.DownloadBytes, plan.UnpackedBytes, len(plan.Files))
	var stage string
	var logged int64
	started := time.Now()
	record, err := m.Install(t.Context(), plan, func(p Progress) {
		if p.Stage != stage || p.DoneBytes-logged >= 1<<30 {
			stage, logged = p.Stage, p.DoneBytes
			t.Logf("%s %d/%d bytes after %s", p.Stage, p.DoneBytes, p.TotalBytes, time.Since(started).Round(time.Second))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened := New(root)
	defer reopened.client.CloseIdleConnections()
	loaded, err := reopened.LoadRecord(game)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Game != game || loaded.Version != plan.Version || loaded.ManifestMD5 != plan.ManifestMD5 ||
		loaded.Directory != record.Directory || !loaded.CompletedAt.Equal(record.CompletedAt) {
		t.Fatalf("reloaded record %+v, installed %+v", loaded, record)
	}
	if rel, err := filepath.Rel(root, loaded.Directory); err != nil || !strings.HasPrefix(rel, string(game)+"/versions/") {
		t.Fatalf("record directory %s is outside %s: %v", loaded.Directory, game, err)
	}
	update, err := reopened.ResolveUpdate(t.Context(), game)
	if err != nil {
		t.Fatal(err)
	}
	if update.Version == loaded.Version && update.ManifestMD5 == loaded.ManifestMD5 {
		again, err := reopened.Install(t.Context(), update, nil)
		if err != nil {
			t.Fatal(err)
		}
		if again.Directory != loaded.Directory || !again.CompletedAt.Equal(loaded.CompletedAt) {
			t.Fatalf("current installation was not reused: %+v", again)
		}
	}
	t.Logf("installed %s %s at %s in %s; reopened record verified", game, loaded.Version, loaded.Directory, time.Since(started).Round(time.Second))
}

type trafficTransport struct {
	base  http.RoundTripper
	bytes int64
	limit int64
}

func (t *trafficTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.bytes >= t.limit {
		return nil, errors.New("integration network budget exhausted")
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &trafficBody{ReadCloser: resp.Body, traffic: t}
	return resp, nil
}

type trafficBody struct {
	io.ReadCloser
	traffic *trafficTransport
}

func (b *trafficBody) Read(p []byte) (int, error) {
	remaining := b.traffic.limit - b.traffic.bytes
	if remaining <= 0 {
		return 0, errors.New("integration network budget exhausted")
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.traffic.bytes += int64(n)
	return n, err
}
