package launch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pinion/internal/download"
)

// These filesystem checks use temporary directories and empty files. No game
// files or substitute executables are manufactured and Wine is never executed.
func newEndfieldSetupDirectories(t *testing.T) (root, prefix, game string) {
	t.Helper()
	root = t.TempDir()
	prefix = filepath.Join(root, "prefix")
	game = filepath.Join(root, "downloaded-game")
	for _, directory := range []string{filepath.Join(prefix, "drive_c"), filepath.Join(prefix, "dosdevices"), game} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../drive_c", filepath.Join(prefix, "dosdevices", "c:")); err != nil {
		t.Fatal(err)
	}
	return root, prefix, game
}

func TestEndfieldMappingPreparation(t *testing.T) {
	_, prefix, game := newEndfieldSetupDirectories(t)
	if err := prepareEndfieldMapping(prefix, game); err != nil {
		t.Fatal(err)
	}
	mapped := filepath.Join(prefix, "drive_c", "GryphEndfield")
	before, err := os.Lstat(mapped)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareEndfieldMapping(prefix, game); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(mapped)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the existing correct mapping was replaced")
	}
}

func TestEndfieldMappingConflictsPreserved(t *testing.T) {
	for _, kind := range []string{"directory", "file", "different-link", "dangling-link"} {
		t.Run(kind, func(t *testing.T) {
			root, prefix, game := newEndfieldSetupDirectories(t)
			mapped := filepath.Join(prefix, "drive_c", "GryphEndfield")
			target := filepath.Join(root, "other")
			if kind != "dangling-link" {
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "directory" {
				if err := os.Mkdir(mapped, 0700); err != nil {
					t.Fatal(err)
				}
			} else if kind == "file" {
				file, err := os.Create(mapped)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Symlink(target, mapped); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(mapped)
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareEndfieldMapping(prefix, game); err == nil {
				t.Fatal("conflicting existing path was accepted")
			}
			after, err := os.Lstat(mapped)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("the conflicting user path was replaced")
			}
		})
	}
}

func TestEndfieldMappingSwitchPreservesDirectories(t *testing.T) {
	root, prefix, game := newEndfieldSetupDirectories(t)
	old := filepath.Join(root, "previous-directory")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	mapped := filepath.Join(prefix, "drive_c", "GryphEndfield")
	if err := os.Symlink(old, mapped); err != nil {
		t.Fatal(err)
	}
	original, err := os.Lstat(mapped)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceEndfieldMapping(mapped, game, original); err != nil {
		t.Fatal(err)
	}
	if err := checkEndfieldMapping(prefix, game); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("previous target was changed: %v", err)
	}
	current, err := os.Lstat(mapped)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(original, current) {
		t.Fatal("mapping did not switch")
	}
	// An inode changed by another writer must survive a stale switch attempt.
	if err := replaceEndfieldMapping(mapped, old, original); err == nil {
		t.Fatal("stale mapping switch was accepted")
	}
	if err := checkEndfieldMapping(prefix, game); err != nil {
		t.Fatal(err)
	}
	leftovers, err := filepath.Glob(filepath.Join(prefix, "drive_c", ".GryphEndfield-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("mapping staging paths remained: %v / %v", leftovers, err)
	}
}

// Prepare actual installer-owned metadata directories from a live official
// plan, canceling before any package download. No completion record, substitute
// executable, or game package is manufactured, and Wine is never executed.
func TestOwnedEndfieldDirectoryIntegration(t *testing.T) {
	if os.Getenv("PINION_GAME_UPDATE_INTEGRATION") != "1" {
		t.Skip("set PINION_GAME_UPDATE_INTEGRATION=1 for real official install metadata")
	}
	ctx, stop := context.WithTimeout(t.Context(), 2*time.Minute)
	defer stop()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := download.New(root)
	plan, err := m.Resolve(ctx, download.Endfield)
	if err != nil {
		t.Fatal(err)
	}
	installCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	_, err = m.Install(installCtx, plan, func(progress download.Progress) {
		if progress.Stage == download.StageDownloading {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel before package download: %v", err)
	}
	if _, err := m.LoadRecord(download.Endfield); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled preparation unexpectedly has a completion record: %v", err)
	}
	games, err := filepath.Glob(filepath.Join(root, "endfield", "versions", "*", "game"))
	if err != nil || len(games) != 1 {
		t.Fatalf("expected one installer-prepared version directory: %v / %v", games, err)
	}
	game := games[0]
	want := filepath.Join(root, "endfield")
	if got, err := ownedEndfieldDirectory(game); err != nil || got != want {
		t.Fatalf("genuine installer-owned directory: %s / %v", got, err)
	}
	versionRoot := filepath.Dir(game)
	suffixed := filepath.Join(filepath.Dir(versionRoot), plan.Version+"-"+plan.ManifestMD5)
	if versionRoot != suffixed {
		if err := os.Rename(versionRoot, suffixed); err != nil {
			t.Fatal(err)
		}
		game = filepath.Join(suffixed, "game")
	}
	if got, err := ownedEndfieldDirectory(game); err != nil || got != want {
		t.Fatalf("genuine manifest-addressed directory: %s / %v", got, err)
	}
	// Replacing an owned directory with a symlink must not confer ownership on
	// the link target, even when the version's genuine manifest remains intact.
	saved := game + "-saved"
	if err := os.Rename(game, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(saved, game); err != nil {
		t.Fatal(err)
	}
	if _, err := ownedEndfieldDirectory(game); err == nil {
		t.Fatal("symlinked version game directory was accepted")
	}
	t.Logf("verified genuine owned layout and manifest-suffixed layout: version=%s manifest=%s; no package downloaded or installation completed", plan.Version, plan.ManifestMD5)
}

func TestOfficialLauncherURLBoundary(t *testing.T) {
	for _, raw := range []string{
		"http://launcher-sign.hycdn.cn/a.zip",
		"https://launcher-sign.hycdn.cn.evil.example/a.zip",
		(&url.URL{Scheme: "https", Host: "launcher-sign.hycdn.cn", User: url.User("user"), Path: "/a.zip"}).String(),
		"https://launcher-sign.hycdn.cn:444/a.zip",
		"https://example.com/a.zip",
	} {
		if err := checkOfficialLauncherURL(raw); err == nil {
			t.Errorf("accepted unsupported official URL: %s", raw)
		}
	}
}

// Opt-in real HTTP check: downloads the actual public official launcher ZIP,
// deploys only its unchanged Games.exe into temporary directories, then checks
// reuse, cancellation, conflict handling and damaged-cache rejection.
func TestEndfieldOfficialSetupIntegration(t *testing.T) {
	if os.Getenv("GRYPH_OFFICIAL_LAUNCHER_INTEGRATION") != "1" {
		t.Skip("set GRYPH_OFFICIAL_LAUNCHER_INTEGRATION=1 to download the real official launcher ZIP")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	root, prefix, game := newEndfieldSetupDirectories(t)
	var output bytes.Buffer
	parent, err := prepareEndfieldParent(ctx, root, prefix, game, &output)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(output.String())
	parentPath := filepath.Join(prefix, "drive_c", filepath.FromSlash(strings.ReplaceAll(strings.TrimPrefix(parent, "C:\\"), "\\", "/")))
	before, err := os.Stat(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	image, err := pe.Open(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	header, ok := image.OptionalHeader.(*pe.OptionalHeader64)
	if !ok || image.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || header.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_SECURITY].Size == 0 {
		t.Fatal("downloaded official Games.exe is not an AMD64 PE with an embedded certificate table")
	}
	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
	// A canceled context makes accidental HTTP use fail immediately. Reusing an
	// installed parent requires no network, and must preserve the actual inode.
	canceled, stop := context.WithCancel(ctx)
	stop()
	again, err := prepareEndfieldParent(canceled, root, prefix, game, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	if parent != again || !os.SameFile(before, after) {
		t.Fatal("the installed official parent was replaced")
	}
	caches, err := filepath.Glob(filepath.Join(root, "Cache", "official-launcher", "*.zip"))
	if err != nil || len(caches) != 1 {
		t.Fatalf("expected exactly one official cache: %v / %v", caches, err)
	}
	// Use the real cached archive to test deployment collision handling. The
	// existing Games.exe must remain byte-for-byte unchanged.
	digest := func() [32]byte {
		t.Helper()
		data, err := os.ReadFile(parentPath)
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(data)
	}
	original := digest()
	identity := strings.TrimSuffix(filepath.Base(caches[0]), ".zip")
	if err := deployOfficialParent(ctx, caches[0], prefix, identity, io.Discard); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing deployment should be preserved: %v", err)
	}
	if digest() != original {
		t.Fatal("the original official executable changed")
	}
	info, err := os.Stat(caches[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyOfficialArchive(ctx, caches[0], info.Size(), identity); err != nil {
		t.Fatal(err)
	}
	if err := verifyOfficialArchive(canceled, caches[0], info.Size(), identity); !errors.Is(err, context.Canceled) {
		t.Fatalf("archive verification ignored cancellation: %v", err)
	}
	// Another fresh temporary prefix reuses the genuine cached package.
	_, otherPrefix, otherGame := newEndfieldSetupDirectories(t)
	if _, err := prepareEndfieldParent(ctx, root, otherPrefix, otherGame, io.Discard); err != nil {
		t.Fatal(err)
	}
	cacheAfter, err := os.Stat(caches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, cacheAfter) {
		t.Fatal("the valid cached official ZIP was replaced")
	}
	cache, err := os.OpenFile(caches[0], os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var last [1]byte
	if _, err := cache.ReadAt(last[:], info.Size()-1); err != nil {
		t.Fatal(err)
	}
	last[0] ^= 0xff
	if _, err := cache.WriteAt(last[:], info.Size()-1); err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if err := verifyOfficialArchive(ctx, caches[0], info.Size(), identity); err == nil {
		t.Fatal("same-size corruption of the real cached ZIP escaped MD5 verification")
	}
	if err := os.Truncate(caches[0], info.Size()-1); err != nil {
		t.Fatal(err)
	}
	if err := verifyOfficialArchive(ctx, caches[0], info.Size(), identity); err == nil {
		t.Fatal("damaged real official cache was accepted")
	}
	t.Logf("official Games.exe verified against archive MD5=%s; size=%d SHA-256=%x; certificate table present (Authenticode trust not executed)", identity, before.Size(), original)
}
