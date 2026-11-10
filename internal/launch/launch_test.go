package launch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"pinion/internal/download"
	"pinion/internal/paths"
)

func TestLaunchLockAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "arknights-launch.lock")
	lock, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestLaunchLockChild$")
	child.Env = append(os.Environ(), "GRYPH_TEST_LOCK_PATH="+path)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock check: %v\n%s", err, output)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	// Closing the owning session must release the existing lock file.
	lock, err = acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBilibiliProfile(t *testing.T) {
	official, err := profile(download.Arknights)
	if err != nil {
		t.Fatal(err)
	}
	bilibili, err := profile(download.ArknightsBilibili)
	if err != nil {
		t.Fatal(err)
	}
	if bilibili.prefix == official.prefix || bilibili.id == official.id {
		t.Fatal("Bilibili shares the official prefix or launch lock")
	}
	if bilibili.executable != official.executable || bilibili.backend != official.backend || bilibili.argument != official.argument {
		t.Fatalf("Bilibili launch settings %+v differ from official %+v", bilibili, official)
	}
	if _, err := profile("arknights-unknown"); err == nil {
		t.Fatal("accepted an unknown game")
	}
}

func TestUpdateAndLaunchShareLock(t *testing.T) {
	root := t.TempDir()
	t.Setenv(paths.DataRootEnvironment, root)
	for _, game := range []download.GameID{download.Arknights, download.ArknightsBilibili, download.Endfield} {
		t.Run(string(game), func(t *testing.T) {
			path := filepath.Join(root, string(game)+"-launch.lock")
			// Holding the ordinary launch lock prevents update acquisition before
			// either code path can inspect or change an installation.
			launchLock, err := acquireLock(path)
			if err != nil {
				t.Fatal(err)
			}
			updateLock, err := LockForUpdate(game)
			if updateLock != nil {
				_ = updateLock.Close()
				t.Fatal("update acquired an active launch lock")
			}
			if !errors.Is(err, ErrAlreadyRunning) {
				t.Fatalf("expected busy launch lock, got %v", err)
			}
			if err := launchLock.Close(); err != nil {
				t.Fatal(err)
			}
			// This checks the host's actual process list, without starting Wine.
			updateLock, err = LockForUpdate(game)
			if errors.Is(err, ErrAlreadyRunning) {
				t.Skip("the real game is already running outside this test")
			}
			if err != nil {
				t.Fatal(err)
			}
			defer updateLock.Close()
			child := exec.Command(os.Args[0], "-test.run=^TestUpdateLockChild$")
			child.Env = append(os.Environ(), "PINION_TEST_UPDATE_GAME="+string(game))
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("cross-process update exclusion: %v\n%s", err, output)
			}
			if err := updateLock.Close(); err != nil {
				t.Fatal(err)
			}
			launchLock, err = acquireLock(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := launchLock.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpdateLockChild(t *testing.T) {
	game := download.GameID(os.Getenv("PINION_TEST_UPDATE_GAME"))
	if game == "" {
		t.Skip("child of TestUpdateAndLaunchShareLock")
	}
	if lock, err := LockForUpdate(game); !errors.Is(err, ErrAlreadyRunning) || lock != nil {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("another process acquired the update lock: %v", err)
	}
	// Start fails at the same lock, before reading a record or running Wine.
	if session, err := Start(t.Context(), game, "", nil); !errors.Is(err, ErrAlreadyRunning) || session != nil {
		t.Fatalf("launch did not reject the update lock: session=%v error=%v", session, err)
	}
}

func TestIdleGameRejectsActualProcessAndReleasesLock(t *testing.T) {
	root := t.TempDir()
	t.Setenv(paths.DataRootEnvironment, root)
	// Select this genuine test process as the executable being checked. This
	// exercises the real kernel scan without impersonating a game process.
	p := gameProfile{id: download.Arknights, title: "current test process", executable: os.Args[0]}
	lock, err := lockIdleGame(p)
	if lock != nil || !errors.Is(err, ErrAlreadyRunning) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("actual running process was not rejected: %v", err)
	}
	lock, err = acquireLock(filepath.Join(root, string(p.id)+"-launch.lock"))
	if err != nil {
		t.Fatalf("process rejection leaked the launch lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchLockChild(t *testing.T) {
	path := os.Getenv("GRYPH_TEST_LOCK_PATH")
	if path == "" {
		t.Skip("child of TestLaunchLockAcrossProcesses")
	}
	lock, err := acquireLock(path)
	if lock != nil {
		_ = lock.Close()
		t.Fatal("another process acquired the active game's lock")
	}
	if !errors.Is(fmt.Errorf("明日方舟：%w", err), ErrAlreadyRunning) {
		t.Fatalf("busy lock must preserve ErrAlreadyRunning: %v", err)
	}
	// The other game's lock is independent, without launching either game.
	other, err := acquireLock(filepath.Join(filepath.Dir(path), "endfield-launch.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
}
