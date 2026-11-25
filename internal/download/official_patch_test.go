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
	}{{Arknights, "78.0.0"}, {Endfield, "1.6.4"}} {
		t.Run(string(tc.game), func(t *testing.T) {
			p, err := m.resolveDifferential(ctx, tc.game, tc.old, "")
			if err != nil {
				t.Fatal(err)
			}
			if !p.Differential || p.patch == nil || p.RequiredBytes <= p.UnpackedBytes || p.DownloadBytes <= 0 {
				t.Fatalf("unexpected official differential plan: %+v", p)
			}
			t.Logf("%s %s→%s: target=%d files, operations=%d, download=%d, target bytes=%d, required=%d", tc.game, tc.old, p.Version, len(p.Files), len(p.patch.recipes), p.DownloadBytes, p.UnpackedBytes, p.RequiredBytes)
			if p.DownloadBytes > 4<<20 {
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
		if err != nil || len(files) != len(p.Files) {
			t.Fatalf("official Endfield game_files artifact: %d files (want %d): %v", len(files), len(p.Files), err)
		}
	}
	t.Logf("official sidecars retain archive game_files=%d bytes, complete target-game_files=%d bytes (%d target entries)", len(gameFiles), len(manifest), len(p.Files))
}

func TestParseV2PatchBasePaths(t *testing.T) {
	const (
		md5A = "0123456789abcdef0123456789abcdef"
		md5B = "fedcba9876543210fedcba9876543210"
		md5C = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		md5D = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	t.Run("empty_vfs_base_path", func(t *testing.T) {
		info := []byte(`{
			"version": "1.7.2",
			"vfs_base_path": "",
			"files": [
				{
					"name": "Endfield_Data/StreamingAssets/VFS/unchanged.chk",
					"name_path": "",
					"md5": "` + md5A + `",
					"size": 100,
					"diffType": 1
				},
				{
					"name": "Endfield_Data/StreamingAssets/VFS/replaced.chk",
					"name_path": "",
					"md5": "` + md5B + `",
					"size": 200,
					"diffType": 2,
					"local_path": "vfs_files/files/Endfield_Data/StreamingAssets/VFS/replaced.chk"
				},
				{
					"name": "Endfield_Data/StreamingAssets/VFS/patched.chk",
					"name_path": "",
					"md5": "` + md5C + `",
					"size": 300,
					"diffType": 1,
					"patch": [{
						"base_file": "Endfield_Data/StreamingAssets/VFS/old.chk",
						"base_file_path": "",
						"base_md5": "` + md5D + `",
						"base_size": 250,
						"patch": "diff_1.7.2_1.6.4/patched.chk_patch",
						"patch_path": "",
						"patch_size": 40
					}]
				}
			]
		}`)
		target := map[string]File{
			"Endfield_Data/StreamingAssets/VFS/unchanged.chk": {Path: "Endfield_Data/StreamingAssets/VFS/unchanged.chk", MD5: md5A, Size: 100},
			"Endfield_Data/StreamingAssets/VFS/replaced.chk":  {Path: "Endfield_Data/StreamingAssets/VFS/replaced.chk", MD5: md5B, Size: 200},
			"Endfield_Data/StreamingAssets/VFS/patched.chk":   {Path: "Endfield_Data/StreamingAssets/VFS/patched.chk", MD5: md5C, Size: 300},
		}
		archive := map[string]archiveItem{
			"vfs_files/files/Endfield_Data/StreamingAssets/VFS/replaced.chk": {name: "vfs_files/files/Endfield_Data/StreamingAssets/VFS/replaced.chk", size: 200},
			"vfs_files/vfs_patch/diff_1.7.2_1.6.4/patched.chk_patch":         {name: "vfs_files/vfs_patch/diff_1.7.2_1.6.4/patched.chk_patch", size: 40},
		}
		pp := &patchPlan{recipes: map[string]patchRecipe{}}
		if err := parseV2Patch(info, "1.7.2", target, archive, pp); err != nil {
			t.Fatalf("parseV2Patch with empty vfs_base_path: %v", err)
		}
		if len(pp.recipes) != 2 {
			t.Fatalf("expected 2 recipes, got %d: %+v", len(pp.recipes), pp.recipes)
		}
		if got := pp.recipes["Endfield_Data/StreamingAssets/VFS/replaced.chk"]; got != (patchRecipe{archive: "vfs_files/files/Endfield_Data/StreamingAssets/VFS/replaced.chk"}) {
			t.Fatalf("unexpected replacement recipe: %+v", got)
		}
		wantPatch := patchRecipe{
			archive:  "vfs_files/vfs_patch/diff_1.7.2_1.6.4/patched.chk_patch",
			source:   "Endfield_Data/StreamingAssets/VFS/old.chk",
			baseMD5:  md5D,
			baseSize: 250,
		}
		if got := pp.recipes["Endfield_Data/StreamingAssets/VFS/patched.chk"]; got != wantPatch {
			t.Fatalf("unexpected patch recipe: got %+v, want %+v", got, wantPatch)
		}
	})

	t.Run("nonempty_vfs_base_path", func(t *testing.T) {
		info := []byte(`{
			"version": "79.0.0",
			"vfs_base_path": "Arknights_Data/StreamingAssets/AB/Windows",
			"files": [{
				"name": "arts/item.ab",
				"name_path": "",
				"md5": "` + md5C + `",
				"size": 300,
				"diffType": 1,
				"patch": [{
					"base_file": "arts/item_old.ab",
					"base_file_path": "",
					"base_md5": "` + md5D + `",
					"base_size": 250,
					"patch": "arts/item.ab_patch",
					"patch_path": "",
					"patch_size": 40
				}]
			}]
		}`)
		target := map[string]File{
			"Arknights_Data/StreamingAssets/AB/Windows/arts/item.ab": {Path: "Arknights_Data/StreamingAssets/AB/Windows/arts/item.ab", MD5: md5C, Size: 300},
		}
		archive := map[string]archiveItem{
			"vfs_files/vfs_patch/arts/item.ab_patch": {name: "vfs_files/vfs_patch/arts/item.ab_patch", size: 40},
		}
		pp := &patchPlan{recipes: map[string]patchRecipe{}}
		if err := parseV2Patch(info, "79.0.0", target, archive, pp); err != nil {
			t.Fatalf("parseV2Patch with non-empty vfs_base_path: %v", err)
		}
		wantPatch := patchRecipe{
			archive:  "vfs_files/vfs_patch/arts/item.ab_patch",
			source:   "Arknights_Data/StreamingAssets/AB/Windows/arts/item_old.ab",
			baseMD5:  md5D,
			baseSize: 250,
		}
		if got := pp.recipes["Arknights_Data/StreamingAssets/AB/Windows/arts/item.ab"]; got != wantPatch {
			t.Fatalf("unexpected patch recipe: got %+v, want %+v", got, wantPatch)
		}
	})

	for _, badBase := range []string{".", "..", "../escape", "/abs/path"} {
		t.Run("reject_unsafe_base_"+badBase, func(t *testing.T) {
			info, err := json.Marshal(map[string]any{
				"version":       "1.7.2",
				"vfs_base_path": badBase,
				"files":         []any{},
			})
			if err != nil {
				t.Fatal(err)
			}
			pp := &patchPlan{recipes: map[string]patchRecipe{}}
			if err := parseV2Patch(info, "1.7.2", map[string]File{}, map[string]archiveItem{}, pp); err == nil {
				t.Fatalf("accepted unsafe vfs_base_path %q", badBase)
			}
		})
	}
}
