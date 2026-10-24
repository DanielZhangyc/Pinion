package launch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
