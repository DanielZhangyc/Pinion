package launch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"pinion/internal/download"
	"pinion/internal/gamewatch"
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
			updateLock, err := LockForUpdate(game, "")
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
			updateLock, err = LockForUpdate(game, "")
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
	if lock, err := LockForUpdate(game, ""); !errors.Is(err, ErrAlreadyRunning) || lock != nil {
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
	lock, err := lockIdleGame(p, "")
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

// Genuine processes run under game and helper names from installation layouts,
// as Wine processes do. No game, Wine, or prefix is started.
func TestDistributionProcessAttribution(t *testing.T) {
	t.Setenv(paths.DataRootEnvironment, t.TempDir())
	if running, err := gamewatch.Running("Arknights.exe"); err != nil || running {
		t.Skipf("a real Arknights.exe is running or undetectable: %v", err)
	}
	program, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, program, 0700); err != nil {
			t.Fatal(err)
		}
		child := exec.Command(path, "-test.run=^TestProcessChild$")
		child.Env = append(os.Environ(), "PINION_TEST_PROCESS_CHILD=1")
		input, err := child.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			input.Close()
			child.Wait()
		})
	}
	expect := func(t *testing.T, want map[download.GameID]bool) {
		t.Helper()
		snapshot := gamewatch.Capture()
		for game, wantRunning := range want {
			if running, err := Running(snapshot, game, root); err != nil || running != wantRunning {
				t.Fatalf("%s running=%t error=%v, want %t", game, running, err, wantRunning)
			}
		}
	}
	bilibili := filepath.Join(root, string(download.ArknightsBilibili), "versions", "79.0.0", "game")
	t.Run("game", func(t *testing.T) {
		start(t, filepath.Join(bilibili, "Arknights.exe"))
		expect(t, map[download.GameID]bool{download.Arknights: false, download.ArknightsBilibili: true, download.Endfield: false})
		if lock, err := LockForUpdate(download.ArknightsBilibili, root); !errors.Is(err, ErrAlreadyRunning) || lock != nil {
			if lock != nil {
				lock.Close()
			}
			t.Fatalf("Bilibili update while it runs: %v", err)
		}
		lock, err := LockForUpdate(download.Arknights, root)
		if err != nil {
			t.Fatalf("official update blocked by Bilibili: %v", err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		if lock, err := LockForUpdate(download.Arknights, ""); !errors.Is(err, ErrAlreadyRunning) || lock != nil {
			if lock != nil {
				lock.Close()
			}
			t.Fatalf("unattributed update check ignored a running Arknights.exe: %v", err)
		}
	})
	t.Run("helper", func(t *testing.T) {
		start(t, filepath.Join(bilibili, "BLPlatform64", "PCGamePlatform.exe"))
		expect(t, map[download.GameID]bool{download.Arknights: false, download.ArknightsBilibili: true})
	})
	t.Run("unknown_location", func(t *testing.T) {
		start(t, filepath.Join(t.TempDir(), "Arknights.exe"))
		expect(t, map[download.GameID]bool{download.Arknights: true, download.ArknightsBilibili: true, download.Endfield: false})
	})
}

func TestProcessChild(t *testing.T) {
	if os.Getenv("PINION_TEST_PROCESS_CHILD") != "1" {
		t.Skip("helper of TestDistributionProcessAttribution")
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

// Run while a game launched by Pinion is open. Only process state is read.
func TestLiveDistributionProcess(t *testing.T) {
	game := download.GameID(os.Getenv("PINION_TEST_RUNNING_GAME"))
	root := os.Getenv("GRYPH_DOWNLOAD_ROOT")
	if game == "" {
		t.Skip("set PINION_TEST_RUNNING_GAME and GRYPH_DOWNLOAD_ROOT while that game is running")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("GRYPH_DOWNLOAD_ROOT must identify the absolute download directory")
	}
	running, err := profile(game)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := gamewatch.Capture()
	for _, other := range games {
		p, err := profile(other)
		if err != nil || p.executable != running.executable {
			continue
		}
		got, err := Running(snapshot, other, root)
		if err != nil || got != (other == game) {
			t.Fatalf("%s running=%t error=%v while %s runs", other, got, err, game)
		}
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
