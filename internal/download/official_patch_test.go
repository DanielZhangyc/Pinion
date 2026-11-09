package download

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"strings"
	"testing"
	"time"
)

// These are real server requests; no installed Record or response is fabricated.
func TestOfficialNullPatchPlans(t *testing.T) {
	if os.Getenv("GRYPH_DOWNLOAD_INTEGRATION") != "1" {
		t.Skip("set GRYPH_DOWNLOAD_INTEGRATION=1 for official null-patch checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	m := New(t.TempDir())
	defer m.client.CloseIdleConnections()
	traffic := &trafficTransport{base: m.client.Transport, limit: 12 << 20}
	m.client.Transport = traffic
	latest, err := m.LatestVersion(ctx, Endfield)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{latest.Version, "1.6.2"} {
		t.Run(version, func(t *testing.T) {
			metadata, patch, err := m.officialPatch(ctx, Endfield, version)
			if err != nil {
				t.Fatal(err)
			}
			if patch != nil || metadata.Version != latest.Version {
				t.Fatalf("expected official null patch for %s→%s", version, metadata.Version)
			}
			p, err := m.resolveDifferential(ctx, Endfield, version, "")
			if err != nil {
				t.Fatal(err)
			}
			if p.Differential || p.patch != nil || p.Version != latest.Version || len(p.Files) == 0 || len(p.Packs) == 0 || p.RequiredBytes != p.DownloadBytes+p.UnpackedBytes {
				t.Fatal("null patch did not produce a validated full plan")
			}
			t.Logf("official %s→%s null patch resolved full package: %d files, %d bytes download", version, p.Version, len(p.Files), p.DownloadBytes)
		})
	}
	if _, _, err := m.officialPatch(ctx, Endfield, "999.0.0"); err == nil || !strings.Contains(err.Error(), "response state 1") {
		t.Fatalf("official unsupported newer-version state: %v", err)
	}
	t.Logf("official higher-version state rejected; total HTTP bytes=%d", traffic.bytes)
}

// This uses published metadata and actual encrypted patch bytes. It does not
// invent an installed game or completion record and never opens a user install.
func TestOfficialDifferentialPlans(t *testing.T) {
	if os.Getenv("GRYPH_DOWNLOAD_INTEGRATION") != "1" {
		t.Skip("set GRYPH_DOWNLOAD_INTEGRATION=1 for genuine official differential checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	m := New(t.TempDir())
	defer m.client.CloseIdleConnections()
	traffic := &trafficTransport{base: m.client.Transport, limit: 24 << 20}
	m.client.Transport = traffic
	for _, tc := range []struct {
		game GameID
		old  string
	}{{Arknights, "78.0.0"}, {Endfield, "1.6.3"}} {
		t.Run(string(tc.game), func(t *testing.T) {
			p, err := m.resolveDifferential(ctx, tc.game, tc.old, "")
			if err != nil {
				t.Fatal(err)
			}
			if !p.Differential || p.patch == nil || p.RequiredBytes <= p.UnpackedBytes || p.DownloadBytes <= 0 {
				t.Fatalf("unexpected official differential plan: %+v", p)
			}
			t.Logf("%s %s→%s: target=%d files, operations=%d, download=%d, target bytes=%d, required=%d", tc.game, tc.old, p.Version, len(p.Files), len(p.patch.recipes), p.DownloadBytes, p.UnpackedBytes, p.RequiredBytes)
			if tc.game != Endfield {
				// Bounded protocol research validates the archive's small metadata
				// entries by their AES authentication. Install separately requires
				// every complete volume's MD5 before opening any encrypted entry.
				remote := &remoteArchive{ctx: ctx, manager: m, packs: p.Packs, budget: 4 << 20}
				zr, err := zip.NewReader(remote, p.DownloadBytes)
				if err != nil {
					t.Fatal(err)
				}
				entries := map[string]*zip.File{}
				for _, f := range zr.File {
					entries[f.Name] = f
				}
				if err := checkPackageFiles(ctx, entries, p.patch.password, p); err != nil {
					t.Fatal(err)
				}
				root, err := os.OpenRoot(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				checkWrittenOfficialSidecars(t, ctx, root, p, entries)
				return
			}
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := makeDirs(root, "sample/cache"); err != nil {
				t.Fatal(err)
			}
			for i, pack := range p.Packs {
				if err := m.fetchPack(ctx, root, pack, packName("sample", i), nil); err != nil {
					t.Fatal(err)
				}
			}
			local := &localArchive{root: root, packs: p.Packs, base: "sample"}
			defer local.Close()
			zr, err := zip.NewReader(local, p.DownloadBytes)
			if err != nil {
				t.Fatal(err)
			}
			entries := map[string]*zip.File{}
			for _, f := range zr.File {
				entries[f.Name] = f
			}
			if err := checkPackageFiles(ctx, entries, p.patch.password, p); err != nil {
				t.Fatal(err)
			}
			checkWrittenOfficialSidecars(t, ctx, root, p, entries)
			count := 0
			for _, f := range p.Files {
				r, ok := p.patch.recipes[f.Path]
				if !ok {
					continue
				}
				name := path.Join("sample", f.Path)
				if err := makeDirs(root, path.Dir(name)); err != nil {
					t.Fatal(err)
				}
				if err := extractPatchOne(ctx, root, entries[r.archive], p.patch.password, name); err != nil {
					t.Fatal(err)
				}
				if err := verifyFile(ctx, root, name, f.Size, f.MD5); err != nil {
					t.Fatal(err)
				}
				count++
			}
			if count == 0 {
				t.Fatal("no official replacement files validated")
			}
			// Real patch files exercise independent-copy behavior and cancellation.
			for _, f := range p.Files {
				if _, ok := p.patch.recipes[f.Path]; !ok {
					continue
				}
				source := path.Join("sample", f.Path)
				if err := copyPatchBase(ctx, root, source, "copy.bin", f); err != nil {
					t.Fatal(err)
				}
				a, err := root.Stat(source)
				if err != nil {
					t.Fatal(err)
				}
				b, err := root.Stat("copy.bin")
				if err != nil {
					t.Fatal(err)
				}
				if os.SameFile(a, b) {
					t.Fatal("reused source by hardlink")
				}
				paused, stop := context.WithCancel(ctx)
				stop()
				if err := copyPatchBase(paused, root, source, "cancel.bin", f); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel copy: %v", err)
				}
				if err := verifyFile(ctx, root, source, f.Size, f.MD5); err != nil {
					t.Fatal(err)
				}
				break
			}
			t.Logf("authenticated archive and validated %d real replacement files; HTTP bytes=%d", count, traffic.bytes)
		})
	}
}

func checkWrittenOfficialSidecars(t *testing.T, ctx context.Context, root *os.Root, p Plan, entries map[string]*zip.File) {
	t.Helper()
	if err := makeDirs(root, "sample/game"); err != nil {
		t.Fatal(err)
	}
	if err := writePatchSidecars(ctx, root, p, "sample", entries); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(ctx, root, "sample/target-game_files", p.ManifestBytes, p.ManifestMD5); err != nil {
		t.Fatal(err)
	}
	manifest, err := root.ReadFile("sample/target-game_files")
	if err != nil {
		t.Fatal(err)
	}
	files, err := decodeManifest(manifest)
	if err != nil || len(files) != len(p.Files) {
		t.Fatalf("complete external target manifest: %d files: %v", len(files), err)
	}
	packageFiles, err := root.ReadFile("sample/game/package_files")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decryptPatchMetadata(packageFiles)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(plain))
	found := false
	for scanner.Scan() {
		var f File
		if err := json.Unmarshal(scanner.Bytes(), &f); err != nil {
			t.Fatal(err)
		}
		if f.Path != "game_files" {
			continue
		}
		found = true
		if err := verifyFile(ctx, root, "sample/game/game_files", f.Size, f.MD5); err != nil {
			t.Fatal(err)
		}
		if p.Game == Arknights && f.Size != 16 {
			t.Fatalf("official Arknights game_files artifact changed size: %d", f.Size)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("official package_files has no game_files identity")
	}
	gameFiles, err := root.ReadFile("sample/game/game_files")
	if err != nil {
		t.Fatal(err)
	}
	if p.Game == Arknights {
		if sumMD5(gameFiles) == p.ManifestMD5 {
			t.Fatal("empty game artifact replaced complete target manifest")
		}
	} else {
		files, err := decodeManifest(gameFiles)
		if err != nil || len(files) != 1688 {
			t.Fatalf("official Endfield game_files artifact: %d files: %v", len(files), err)
		}
	}
	t.Logf("official sidecars retain archive game_files=%d bytes, complete target-game_files=%d bytes (%d target entries)", len(gameFiles), len(manifest), len(p.Files))
}
