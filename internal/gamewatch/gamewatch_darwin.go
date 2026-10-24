//go:build darwin

// Package gamewatch detects local game processes without starting Wine or
// spawning a command. Callers choose the polling rate and can share a Snapshot.
package gamewatch

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

var native struct {
	once  sync.Once
	err   error
	list  func(uint32, uint32, *int32, int32) int32
	name  func(int32, *byte, uint32) int32
	path  func(int32, *byte, uint32) int32
	args  func(*int32, uint32, *byte, *uintptr, unsafe.Pointer, uintptr) int32
	errno func() *int32
}

func loadNative() error {
	native.once.Do(func() {
		type symbol struct {
			name   string
			target any
		}
		for _, library := range []struct {
			path      string
			functions []symbol
		}{
			{"/usr/lib/libproc.dylib", []symbol{
				{"proc_listpids", &native.list},
				{"proc_name", &native.name},
				{"proc_pidpath", &native.path},
			}},
			{"/usr/lib/libSystem.B.dylib", []symbol{
				{"sysctl", &native.args},
				{"__error", &native.errno},
			}},
		} {
			// Keep these system libraries loaded for the lifetime of their functions.
			handle, err := purego.Dlopen(library.path, purego.RTLD_NOW|purego.RTLD_LOCAL)
			if err != nil {
				native.err = fmt.Errorf("gamewatch: load %s: %w", library.path, err)
				return
			}
			for _, function := range library.functions {
				address, err := purego.Dlsym(handle, function.name)
				if err != nil {
					native.err = fmt.Errorf("gamewatch: resolve %s: %w", function.name, err)
					return
				}
				purego.RegisterFunc(function.target, address)
			}
		}
	})
	return native.err
}

// Snapshot holds the results of one process scan. Its queries do not inspect
// the system again, and its state remains unchanged as processes come and go.
type Snapshot struct {
	running map[string]bool
	err     error
}

// Capture scans the current UID once for all specified executable names, or
// both supported games when no names are specified. No arguments are retained.
func Capture(executables ...string) Snapshot {
	if len(executables) == 0 {
		executables = []string{"Endfield.exe", "Arknights.exe"}
	}
	snapshot := Snapshot{running: make(map[string]bool, len(executables))}
	for _, executable := range executables {
		snapshot.running[strings.ToLower(executableName(executable))] = false
	}
	if err := loadNative(); err != nil {
		snapshot.err = err
		return snapshot
	}
	// Darwin errno is thread local. Resolve its address before any native call.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	errno := native.errno()
	pids, err := processIDs(errno)
	if err != nil {
		snapshot.err = err
		return snapshot
	}
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		name, err := executablePID(pid, errno, executables...)
		if name != "" {
			snapshot.running[strings.ToLower(name)] = true
		}
		if err != nil && !processExited(pid, err) && snapshot.err == nil {
			snapshot.err = err
		}
	}
	return snapshot
}

// Running queries captured names. A positive result confirms presence even if
// another candidate could not be inspected. Absence with an error is unknown;
// callers must retain the prior game state. No names queries the entire scan.
func (s Snapshot) Running(executables ...string) (bool, error) {
	if s.running == nil {
		return false, errors.New("gamewatch: snapshot has not been captured")
	}
	if len(executables) == 0 {
		for _, running := range s.running {
			if running {
				return true, nil
			}
		}
	} else {
		for _, executable := range executables {
			running, captured := s.running[strings.ToLower(executableName(executable))]
			if !captured {
				return false, fmt.Errorf("gamewatch: executable was not included in snapshot: %s", executableName(executable))
			}
			if running {
				return true, nil
			}
		}
	}
	return false, s.err
}

// Running always captures fresh process state, including immediately before
// launch. Periodic consumers can instead share Capture's result.
func Running(executables ...string) (bool, error) {
	return Capture(executables...).Running()
}

func nativeError(operation string, pid int32, errno *int32) error {
	if *errno == 0 {
		return fmt.Errorf("gamewatch: %s pid %d returned no data", operation, pid)
	}
	return fmt.Errorf("gamewatch: %s pid %d: %w", operation, pid, syscall.Errno(*errno))
}

func processExited(pid int32, err error) bool {
	if errors.Is(err, syscall.ESRCH) {
		return true
	}
	// KERN_PROCARGS2 returns EINVAL when a PID disappears. Do not hide an
	// EINVAL for a process that still exists or any permission failure.
	return errors.Is(err, syscall.EINVAL) && errors.Is(syscall.Kill(int(pid), 0), syscall.ESRCH)
}

func processIDs(errno *int32) ([]int32, error) {
	const procUIDOnly, pidBytes = 4, 4 // PROC_UID_ONLY and sizeof(pid_t), from the Darwin SDK.
	uid := uint32(os.Getuid())
	*errno = 0
	size := native.list(procUIDOnly, uid, nil, 0)
	if size <= 0 {
		return nil, nativeError("proc_listpids", 0, errno)
	}
	if size%pidBytes != 0 {
		return nil, errors.New("gamewatch: proc_listpids returned a partial PID")
	}
	for range 3 {
		pids := make([]int32, int(size)/pidBytes+128)
		*errno = 0
		size = native.list(procUIDOnly, uid, &pids[0], int32(len(pids)*pidBytes))
		if size <= 0 {
			return nil, nativeError("proc_listpids", 0, errno)
		}
		if size%pidBytes != 0 {
			return nil, errors.New("gamewatch: proc_listpids returned a partial PID")
		}
		if int(size)/pidBytes < len(pids) {
			return pids[:int(size)/pidBytes], nil
		}
	}
	return nil, errors.New("gamewatch: process list kept growing during snapshot")
}

func runningPID(pid int32, errno *int32, executables ...string) (bool, error) {
	name, err := executablePID(pid, errno, executables...)
	return name != "", err
}

func executablePID(pid int32, errno *int32, executables ...string) (string, error) {
	var name [256]byte
	*errno = 0
	if native.name(pid, &name[0], uint32(len(name))) <= 0 {
		return "", nativeError("proc_name", pid, errno)
	}
	processName := cString(name[:])
	if !isGame(processName, executables...) && !isWine(processName) && processName != "wine64-preloade" {
		return "", nil
	}
	var path [4096]byte // PROC_PIDPATHINFO_MAXSIZE
	*errno = 0
	if native.path(pid, &path[0], uint32(len(path))) <= 0 {
		return "", nativeError("proc_pidpath", pid, errno)
	}
	executable := cString(path[:])
	if isGame(executable, executables...) {
		return executableName(executable), nil
	}
	// Wine may give the process its PE name and argv while proc_pidpath still
	// points to another PE image (observed as explorer.exe in Wine's winetemp
	// directory). Confirm that candidate's actual program argv below as well.
	if !isWine(executable) && !isGame(processName, executables...) {
		return "", nil
	}
	args, err := processArguments(pid, errno)
	if err != nil {
		return "", err
	}
	// Match the PE program, not its arguments. A preloader may retain both
	// its own argv[0] and the Wine loader before the actual PE executable.
	for i, arg := range args {
		if isGame(arg, executables...) {
			return executableName(arg), nil
		}
		if i == 2 || !isWine(arg) {
			return "", nil
		}
	}
	return "", nil
}

func processArguments(pid int32, errno *int32) ([]string, error) {
	const ctlKern, kernProcargs2 = 1, 49
	mib := [3]int32{ctlKern, kernProcargs2, pid}
	var size uintptr
	*errno = 0
	if native.args(&mib[0], 3, nil, &size, nil, 0) != 0 {
		return nil, nativeError("kern.procargs2 size", pid, errno)
	}
	if size < 4 || size > 16<<20 {
		return nil, fmt.Errorf("gamewatch: invalid kern.procargs2 size for pid %d", pid)
	}
	buffer := make([]byte, size)
	defer clear(buffer)
	*errno = 0
	if native.args(&mib[0], 3, &buffer[0], &size, nil, 0) != 0 {
		return nil, nativeError("kern.procargs2", pid, errno)
	}
	if size < 4 || size > uintptr(len(buffer)) {
		return nil, fmt.Errorf("gamewatch: truncated kern.procargs2 for pid %d", pid)
	}
	// Native argc, followed by exec_path, padding NULs, then argc NUL-delimited
	// argv entries. Only inspect the first three entries; never inspect envp.
	argc := binary.NativeEndian.Uint32(buffer[:4])
	_, remaining, ok := bytes.Cut(buffer[4:size], []byte{0})
	if !ok || argc == 0 {
		return nil, fmt.Errorf("gamewatch: missing argv for pid %d", pid)
	}
	remaining = bytes.TrimLeft(remaining, "\x00")
	args := make([]string, 0, 3)
	for range min(argc, 3) {
		arg, rest, ok := bytes.Cut(remaining, []byte{0})
		if !ok {
			return nil, fmt.Errorf("gamewatch: incomplete argv for pid %d", pid)
		}
		args = append(args, string(arg))
		remaining = rest
	}
	return args, nil
}

func cString(value []byte) string {
	if end := bytes.IndexByte(value, 0); end >= 0 {
		value = value[:end]
	}
	return string(value)
}

func executableName(path string) string {
	// Wine accepts Unix paths and Windows drive/backslash paths. Do not trim
	// quotes or split on spaces: argv already supplies real argument boundaries.
	return path[strings.LastIndexAny(path, "/\\")+1:]
}

func isGame(path string, executables ...string) bool {
	if len(executables) == 0 {
		executables = []string{"Endfield.exe", "Arknights.exe"}
	}
	name := executableName(path)
	for _, executable := range executables {
		if executable != "" && strings.EqualFold(name, executableName(executable)) {
			return true
		}
	}
	return false
}

func isWine(path string) bool {
	switch strings.ToLower(executableName(path)) {
	case "wine", "wine64", "wine-preloader", "wine64-preloader":
		return true
	}
	return false
}
