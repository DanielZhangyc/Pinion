//go:build darwin

package gamewatch

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

func TestCurrentUIDProcessIDs(t *testing.T) {
	if err := loadNative(); err != nil {
		t.Fatal(err)
	}
	handle, err := purego.Dlopen("/usr/lib/libproc.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := purego.Dlclose(handle); err != nil {
			t.Error(err)
		}
	}()
	address, err := purego.Dlsym(handle, "proc_pidinfo")
	if err != nil {
		t.Fatal(err)
	}
	// struct proc_bsdinfo from the Darwin SDK's sys/proc_info.h. Verify
	// ownership with proc_pidinfo, independently of the PID-list filter.
	type bsdInfo struct {
		Flags, Status, XStatus, PID, PPID, UID, GID, RUID, RGID, SVUID, SVGID, Reserved uint32
		Command                                                                         [16]byte
		Name                                                                            [32]byte
		NFiles, PGID, PJobC, TDev, TPGID                                                uint32
		Nice                                                                            int32
		StartSec, StartUsec                                                             uint64
	}
	var pidInfo func(int32, int32, uint64, *bsdInfo, int32) int32
	purego.RegisterFunc(&pidInfo, address)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	errno := native.errno()
	pids, err := processIDs(errno)
	if err != nil {
		t.Fatal(err)
	}
	currentFound, checked := false, 0
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		var info bsdInfo
		*errno = 0
		const procPIDTBSDInfo = 3
		if n := pidInfo(pid, procPIDTBSDInfo, 0, &info, int32(unsafe.Sizeof(info))); n != int32(unsafe.Sizeof(info)) {
			err := nativeError("proc_pidinfo", pid, errno)
			if processExited(pid, err) {
				continue
			}
			t.Fatal(err)
		}
		if info.PID != uint32(pid) || info.UID != uint32(os.Getuid()) {
			t.Fatalf("PID %d returned PID %d UID %d; expected current UID %d", pid, info.PID, info.UID, os.Getuid())
		}
		currentFound = currentFound || pid == int32(os.Getpid())
		checked++
	}
	if !currentFound {
		t.Fatal("current test process was missing from current-UID enumeration")
	}
	t.Logf("verified %d live processes belong to current UID %d", checked, os.Getuid())
}

// Exercise the real Darwin ABI and this test process's actual kernel argv.
// No synthetic process names or command lines are injected.
func TestCurrentProcessArguments(t *testing.T) {
	if err := loadNative(); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	args, err := processArguments(int32(os.Getpid()), native.errno())
	if err != nil {
		t.Fatal(err)
	}
	if len(args) == 0 || args[0] != os.Args[0] {
		t.Fatal("kernel argv[0] did not match the current test executable")
	}
}

func TestLiveSnapshot(t *testing.T) {
	for _, executable := range []string{"", "Arknights.exe"} {
		t.Run(executable, func(t *testing.T) {
			start := time.Now()
			var names []string
			if executable != "" {
				names = []string{executable}
			}
			running, err := Running(names...)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("live game running=%t; snapshot duration=%s", running, time.Since(start))
		})
	}
}

func TestCurrentProcessDetection(t *testing.T) {
	// Select the actual test executable to exercise caller-selected detection
	// through the real process name and path APIs, without starting a game.
	running, err := Running(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Fatal("caller-selected executable did not detect the current test process")
	}
}

func TestSharedSnapshotTracksRealProcesses(t *testing.T) {
	// Run this genuine test binary under a distinct executable name. Its
	// inherited stdin keeps the helper alive until the test closes it.
	program, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snap-child")
	if err := os.WriteFile(path, program, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(path, "-test.run=^TestSnapshotChild$")
	child.Env = append(os.Environ(), "GRYPH_SNAPSHOT_CHILD=1")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			if err := input.Close(); err != nil {
				t.Error(err)
			}
			if err := child.Wait(); err != nil {
				t.Error(err)
			}
		}
	})
	snapshot := Capture(os.Args[0], path)
	for _, names := range [][]string{nil, {os.Args[0]}, {path}, {os.Args[0], path}} {
		running, err := snapshot.Running(names...)
		if err != nil || !running {
			t.Fatalf("real captured process %v: running=%t error=%v", names, running, err)
		}
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	err = child.Wait()
	waited = true
	if err != nil {
		t.Fatal(err)
	}
	if running, err := snapshot.Running(path); err != nil || !running {
		t.Fatalf("captured state changed after process exit: running=%t error=%v", running, err)
	}
	fresh := Capture(os.Args[0], path)
	if running, err := fresh.Running(path); err != nil || running {
		t.Fatalf("new snapshot retained exited child: running=%t error=%v", running, err)
	}
	if running, err := fresh.Running(); err != nil || !running {
		t.Fatalf("overall query lost the remaining real process: running=%t error=%v", running, err)
	}
	if running, err := Running(path); err != nil || running {
		t.Fatalf("immediate check reused stale state: running=%t error=%v", running, err)
	}
}

func TestSnapshotChild(t *testing.T) {
	if os.Getenv("GRYPH_SNAPSHOT_CHILD") != "1" {
		t.Skip("helper of TestSharedSnapshotTracksRealProcesses")
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func TestLiveArknightsProcess(t *testing.T) {
	testLiveGameProcess(t, "GAMEWATCH_TEST_ARKNIGHTS_PID", "Arknights.exe")
}

func TestLiveEndfieldProcess(t *testing.T) {
	testLiveGameProcess(t, "GAMEWATCH_TEST_ENDFIELD_PID", "Endfield.exe")
}

func TestLiveOfficialLauncherProcess(t *testing.T) {
	testLiveGameProcess(t, "GAMEWATCH_TEST_LAUNCHER_PID", "Games.exe")
}

func testLiveGameProcess(t *testing.T, variable, executable string) {
	t.Helper()
	value := os.Getenv(variable)
	if value == "" {
		t.Skipf("set %s to an already-running %s process", variable, executable)
	}
	pid, err := strconv.ParseInt(value, 10, 32)
	if err != nil || pid <= 0 {
		t.Fatalf("%s must be a positive process ID", variable)
	}
	if err := loadNative(); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	running, err := runningPID(int32(pid), native.errno(), executable)
	runtime.UnlockOSThread()
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Fatalf("the specified live process was not detected as %s", executable)
	}
	running, err = Running(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Fatalf("the full process snapshot did not detect the live %s process", executable)
	}
}

func TestExitedProcess(t *testing.T) {
	// Use a real, already-reaped process to verify libproc's exit race.
	command := exec.Command("/usr/bin/true")
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	if err := loadNative(); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pid := int32(command.Process.Pid)
	running, err := runningPID(pid, native.errno())
	if running || err == nil || !processExited(pid, err) {
		t.Fatalf("reaped PID: running=%t error=%v; expected a recognized exit race", running, err)
	}
}
