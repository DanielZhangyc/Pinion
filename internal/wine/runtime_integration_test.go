package wine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This opt-in test executes independent Wine and its genuine Windows tools
// in disposable prefixes. GRYPH_WINE_RUNTIME selects an absolute runtime path;
// otherwise it uses the installed runtime. It never modifies a user prefix.
func TestRuntimeIntegration(t *testing.T) {
	if os.Getenv("GRYPH_WINE_INTEGRATION") != "1" {
		t.Skip("set GRYPH_WINE_INTEGRATION=1 to execute independent Wine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var runtime Runtime
	var err error
	if root := os.Getenv("GRYPH_WINE_RUNTIME"); root != "" {
		runtime, err = Open(ctx, root)
	} else {
		runtime, err = Discover(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("runtime: %s", runtime.Root)
	for _, backend := range []string{"dxmt", "vulkan"} {
		t.Run(backend, func(t *testing.T) {
			prefix := filepath.Join(t.TempDir(), "Wine test prefix")
			logDirectory := t.TempDir()
			newLog := func(name string) *os.File {
				file, err := os.Create(filepath.Join(logDirectory, name))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := file.Close(); err != nil {
						t.Errorf("close Wine log: %v", err)
					}
				})
				return file
			}
			readLog := func(file *os.File) []byte {
				data, err := os.ReadFile(file.Name())
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			output := newLog("prepare.log")
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cleanupCancel()
				if err := runtime.Wait(cleanupCtx, prefix, output); err != nil {
					t.Errorf("wait for disposable prefix: %v\n%s", err, readLog(output))
				}
			})
			if err := runtime.Prepare(ctx, prefix, backend, output); err != nil {
				t.Fatalf("%v\n%s", err, readLog(output))
			}
			marker := filepath.Join(prefix, "gryph-prefix.json")
			readProfile := func() prefixProfile {
				data, err := os.ReadFile(marker)
				if err != nil {
					t.Fatal(err)
				}
				var profile prefixProfile
				if err := json.Unmarshal(data, &profile); err != nil {
					t.Fatal(err)
				}
				return profile
			}
			if profile := readProfile(); profile.Schema != 2 || !profile.Ready {
				t.Fatalf("new prefix has not committed the Retina schema: %+v", profile)
			}
			// Give this check its own file: services from initialization may still
			// write to prepare.log after wineboot.exe has returned.
			readyOutput := newLog("ready.log")
			if err := runtime.Prepare(ctx, prefix, backend, readyOutput); err != nil {
				t.Fatalf("check ready prefix: %v\n%s", err, readLog(readyOutput))
			}
			if readyLog := readLog(readyOutput); len(readyLog) != 0 {
				t.Fatalf("checking a ready prefix unexpectedly executed Wine: %s", readyLog)
			}
			if err := runtime.verifyRegistry(ctx, prefix, output); err != nil {
				t.Fatalf("read actual registry: %v\n%s", err, readLog(output))
			}
			executable := filepath.Join(prefix, "drive_c", "windows", "system32", "cmd.exe")
			commandOutput := newLog("working-directory.log")
			cmd := runtime.Command(prefix, executable, []string{"/c", "cd"}, commandOutput)
			if err := cmd.Run(); err != nil {
				t.Fatalf("run cmd.exe: %v\n%s", err, readLog(commandOutput))
			}
			if data := readLog(commandOutput); !bytes.Contains(bytes.ToLower(data), []byte(`c:\windows\system32`)) {
				t.Fatalf("working directory was not applied: %s", data)
			}
			exitOutput := newLog("exit-status.log")
			cmd = runtime.Command(prefix, executable, []string{"/c", "exit 7"}, exitOutput)
			var exit *exec.ExitError
			if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 7 {
				t.Fatalf("exit status was not preserved: %v\n%s", err, readLog(exitOutput))
			}
			// Build the genuine old state with Windows tools: a fully initialized
			// prefix with the original settings and no RetinaMode value. Only its
			// metadata uses schema 1; no Wine executable or query is replaced.
			if err := runtime.Command(prefix, executable, []string{"/c", `echo Gryph Retina migration>C:\gryph-retina-user-data.txt`}, output).Run(); err != nil {
				t.Fatal(err)
			}
			regExe := filepath.Join(prefix, "drive_c/windows/system32/reg.exe")
			key := `HKCU\Software\GryphRetinaIntegration`
			for _, args := range [][]string{
				{"add", key, "/v", "Preserved", "/t", "REG_SZ", "/d", "before-retina", "/f"},
				{"delete", `HKCU\Software\Wine\Mac Driver`, "/v", "RetinaMode", "/f"},
			} {
				if err := runtime.Command(prefix, regExe, args, output).Run(); err != nil {
					t.Fatal(err)
				}
			}
			if err := runtime.Wait(ctx, prefix, output); err != nil {
				t.Fatal(err)
			}
			legacy := readProfile()
			legacy.Schema = 1
			if err := writeProfile(marker, legacy); err != nil {
				t.Fatal(err)
			}
			migrationOutput := newLog("schema-migration.log")
			if err := runtime.Prepare(ctx, prefix, backend, migrationOutput); err != nil {
				t.Fatalf("migrate genuine legacy prefix: %v\n%s", err, readLog(migrationOutput))
			}
			if profile := readProfile(); profile.Schema != 2 || !profile.Ready ||
				profile.RuntimeID != legacy.RuntimeID || profile.Backend != legacy.Backend || profile.PendingRuntimeID != "" {
				t.Fatalf("migrated prefix marker: %+v", profile)
			}
			if err := runtime.verifyRegistry(ctx, prefix, output); err != nil {
				t.Fatalf("read actual migrated Retina setting: %v\n%s", err, readLog(output))
			}
			data, err := os.ReadFile(filepath.Join(prefix, "drive_c/gryph-retina-user-data.txt"))
			if err != nil || !bytes.Contains(data, []byte("Gryph Retina migration")) {
				t.Fatalf("Windows-created file lost during schema migration: %v %q", err, data)
			}
			data, err = runtime.queryRegistry(ctx, prefix, key, "Preserved", output)
			if err != nil || !bytes.Contains(data, []byte("before-retina")) {
				t.Fatalf("Windows-created registry value lost during schema migration: %v %q", err, data)
			}
			migratedReadyOutput := newLog("migrated-ready.log")
			if err := runtime.Prepare(ctx, prefix, backend, migratedReadyOutput); err != nil {
				t.Fatal(err)
			}
			if data := readLog(migratedReadyOutput); len(data) != 0 {
				t.Fatalf("migrated Ready prefix unexpectedly executed Wine: %s", data)
			}
			t.Log("real Wine: Win11, builtin overrides, Retina schema migration, user data, Ready fast path, working directory and exit 7 verified")
		})
	}
}

func TestDiscoveryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runtime discovery ignored cancellation: %v", err)
	}
}

// Both inputs must be genuine, validated runtimes with distinct IDs. All writes
// and Wine processes belong to a disposable prefix, including the live server.
func TestRuntimeUpgradeIntegration(t *testing.T) {
	if os.Getenv("GRYPH_WINE_INTEGRATION") != "1" {
		t.Skip("set GRYPH_WINE_INTEGRATION=1 to execute independent Wine")
	}
	newRoot, oldRoot := os.Getenv("GRYPH_WINE_RUNTIME"), os.Getenv("GRYPH_WINE_PREVIOUS_RUNTIME")
	if newRoot == "" || oldRoot == "" {
		t.Skip("set GRYPH_WINE_RUNTIME and GRYPH_WINE_PREVIOUS_RUNTIME to distinct real runtimes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	oldRuntime, err := Open(ctx, oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	newRuntime, err := Open(ctx, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if oldRuntime.id == newRuntime.id {
		t.Fatal("runtime upgrade requires distinct genuine manifest IDs")
	}
	for _, backend := range []string{"dxmt", "vulkan"} {
		t.Run(backend, func(t *testing.T) {
			prefix := filepath.Join(t.TempDir(), "Upgrade test prefix")
			output, err := os.Create(filepath.Join(t.TempDir(), "upgrade.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := output.Close(); err != nil {
					t.Error(err)
				}
			}()
			defer func() {
				waitCtx, waitCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer waitCancel()
				if err := newRuntime.Wait(waitCtx, prefix, output); err != nil {
					t.Errorf("wait for disposable prefix: %v", err)
				}
				data, err := os.ReadFile(output.Name())
				if err != nil {
					t.Error(err)
				} else if t.Failed() {
					t.Logf("real Wine output:\n%s", data)
				}
			}()
			if err := oldRuntime.Prepare(ctx, prefix, backend, output); err != nil {
				t.Fatal(err)
			}
			cmdExe := filepath.Join(prefix, "drive_c/windows/system32/cmd.exe")
			if err := oldRuntime.Command(prefix, cmdExe, []string{"/c", `echo Gryph runtime upgrade>C:\gryph-upgrade-user-data.txt`}, output).Run(); err != nil {
				t.Fatal(err)
			}
			regExe := filepath.Join(prefix, "drive_c/windows/system32/reg.exe")
			key := `HKCU\Software\GryphRuntimeUpgradeIntegration`
			if err := oldRuntime.Command(prefix, regExe, []string{"add", key, "/v", "Preserved", "/t", "REG_SZ", "/d", "old-runtime", "/f"}, output).Run(); err != nil {
				t.Fatal(err)
			}
			if err := oldRuntime.Wait(ctx, prefix, output); err != nil {
				t.Fatal(err)
			}
			readMarker := func() ([]byte, prefixProfile) {
				data, err := os.ReadFile(filepath.Join(prefix, "gryph-prefix.json"))
				if err != nil {
					t.Fatal(err)
				}
				var profile prefixProfile
				if err := json.Unmarshal(data, &profile); err != nil {
					t.Fatal(err)
				}
				return data, profile
			}
			// Also exercise the same-runtime schema migration's live-server
			// refusal, then upgrade this actual schema-1 prefix to the new runtime.
			if err := oldRuntime.Command(prefix, regExe, []string{"delete", `HKCU\Software\Wine\Mac Driver`, "/v", "RetinaMode", "/f"}, output).Run(); err != nil {
				t.Fatal(err)
			}
			if err := oldRuntime.Wait(ctx, prefix, output); err != nil {
				t.Fatal(err)
			}
			_, legacy := readMarker()
			legacy.Schema = 1
			if err := writeProfile(filepath.Join(prefix, "gryph-prefix.json"), legacy); err != nil {
				t.Fatal(err)
			}
			before, _ := readMarker()
			if err := idlePrefix(prefix); err != nil {
				t.Fatalf("idle server check: %v", err)
			}
			// Query a genuine server lock, then run a real client so its final
			// disconnect starts the server's two-second idle exit timer.
			server := exec.CommandContext(ctx, filepath.Join(oldRuntime.Root, "bin/wineserver"), "-f", "-p2")
			server.Env, server.Stdout, server.Stderr = oldRuntime.environment(prefix), output, output
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			serverDone := make(chan error, 1)
			go func() { serverDone <- server.Wait() }()
			waited := false
			defer func() {
				if !waited {
					if err := oldRuntime.Command(prefix, cmdExe, []string{"/c", "exit 0"}, output).Run(); err != nil {
						t.Errorf("disposable server client: %v", err)
					}
					if err := <-serverDone; err != nil {
						t.Errorf("disposable server: %v", err)
					}
				}
			}()
			deadline := time.Now().Add(time.Second)
			for {
				err := idlePrefix(prefix)
				if errors.Is(err, ErrPrefixBusy) {
					break
				}
				if err != nil || time.Now().After(deadline) {
					t.Fatalf("real server did not acquire its protocol lock: %v", err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := newRuntime.Prepare(ctx, prefix, backend, output); !errors.Is(err, ErrPrefixBusy) {
				t.Fatalf("upgrade did not reject live server: %v", err)
			}
			if err := oldRuntime.Prepare(ctx, prefix, backend, output); !errors.Is(err, ErrPrefixBusy) {
				t.Fatalf("schema migration did not reject live server: %v", err)
			}
			after, _ := readMarker()
			if !bytes.Equal(before, after) {
				t.Fatal("live-server rejection changed the prefix marker")
			}
			if err := oldRuntime.Command(prefix, cmdExe, []string{"/c", "exit 0"}, output).Run(); err != nil {
				t.Fatal(err)
			}
			err = <-serverDone
			waited = true
			if err != nil {
				t.Fatal(err)
			}
			shortCtx, shortCancel := context.WithTimeout(ctx, 100*time.Millisecond)
			err = newRuntime.Prepare(shortCtx, prefix, backend, output)
			shortCancel()
			if err == nil {
				t.Fatal("real upgrade unexpectedly completed before the cancellation deadline")
			}
			_, pending := readMarker()
			if pending.RuntimeID != oldRuntime.id || pending.Ready || pending.PendingRuntimeID != newRuntime.id {
				t.Fatalf("failed upgrade published an invalid marker: %+v", pending)
			}
			if err := oldRuntime.Prepare(ctx, prefix, backend, output); err == nil {
				t.Fatal("unfinished upgrade silently accepted its previous runtime")
			}
			// Cancellation may occur between spawning a server and connecting
			// wineboot. Supply a real client so that server can exit naturally.
			if err := newRuntime.Command(prefix, cmdExe, []string{"/c", "exit 0"}, output).Run(); err != nil {
				t.Fatal(err)
			}
			if err := newRuntime.Wait(ctx, prefix, output); err != nil {
				t.Fatal(err)
			}
			if err := newRuntime.Prepare(ctx, prefix, backend, output); err != nil {
				t.Fatalf("retry real wineboot update: %v", err)
			}
			_, ready := readMarker()
			if ready.Schema != 2 || ready.RuntimeID != newRuntime.id || !ready.Ready || ready.PendingRuntimeID != "" {
				t.Fatalf("completed upgrade marker: %+v", ready)
			}
			data, err := os.ReadFile(filepath.Join(prefix, "drive_c/gryph-upgrade-user-data.txt"))
			if err != nil || !bytes.Contains(data, []byte("Gryph runtime upgrade")) {
				t.Fatalf("Windows-created user file not preserved: %v %q", err, data)
			}
			data, err = newRuntime.queryRegistry(ctx, prefix, key, "Preserved", output)
			if err != nil || !bytes.Contains(data, []byte("old-runtime")) {
				t.Fatalf("Windows-created user registry value not preserved: %v %q", err, data)
			}
			if err := newRuntime.verifyRegistry(ctx, prefix, output); err != nil {
				t.Fatal(err)
			}
			readyOutput, err := os.Create(filepath.Join(t.TempDir(), "ready.log"))
			if err != nil {
				t.Fatal(err)
			}
			if err := newRuntime.Prepare(ctx, prefix, backend, readyOutput); err != nil {
				t.Error(err)
			}
			if err := readyOutput.Close(); err != nil {
				t.Error(err)
			}
			data, err = os.ReadFile(readyOutput.Name())
			if err != nil || len(data) != 0 {
				t.Fatalf("upgraded Ready prefix executed Wine again: %v %s", err, data)
			}
			t.Logf("real %s -> %s: active/idle protocol locks, failed-upgrade marker, same-target retry, wineboot update, user data and Ready verified", oldRuntime.id, newRuntime.id)
		})
	}
}
