// The probe runs real Windows programs through the launcher's runtime API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"pinion/internal/wine"
)

func run() error {
	prefix := flag.String("prefix", "", "absolute path to a new or Pinion-owned prefix")
	backend := flag.String("backend", "dxmt", "prefix backend: dxmt or vulkan")
	runtimeRoot := flag.String("runtime", "", "absolute path to an explicitly selected staged runtime; defaults to the installed runtime")
	gstDebug := flag.String("gst-debug", "", "GStreamer diagnostic categories for this probe only")
	wineDebug := flag.String("wine-debug", "", "Wine diagnostic channels for this probe only")
	mvkSynchronous := flag.String("mvk-synchronous-queue-submits", "default", "probe-only Vulkan queue submissions: default, 0 (asynchronous), or 1 (synchronous)")
	mvkPrefill := flag.String("mvk-prefill-command-buffers", "default", "probe-only Metal encoding: default, 0 (on submit), or 1 (on end recording; requires a validated experimental runtime)")
	mvkPrefillDiagnostics := flag.Bool("mvk-prefill-diagnostics", false, "enable bounded prefill diagnostics in a compatible experimental runtime")
	mvkResourceDiagnostics := flag.Bool("mvk-resource-diagnostics", false, "enable bounded resource-map diagnostics in a compatible experimental runtime")
	mvkLogFrames := flag.Uint("mvk-log-frames", 0, "probe-only MoltenVK performance statistics interval in frames; 0 disables statistics")
	flag.Parse()
	if !filepath.IsAbs(*prefix) {
		return errors.New("-prefix must specify an absolute path")
	}
	switch *mvkSynchronous {
	case "default":
	case "0", "1":
		if *backend != "vulkan" {
			return errors.New("-mvk-synchronous-queue-submits requires -backend vulkan when set to 0 or 1")
		}
	default:
		return errors.New("-mvk-synchronous-queue-submits must be default, 0, or 1")
	}
	switch *mvkPrefill {
	case "default":
	case "0", "1":
		if *backend != "vulkan" {
			return errors.New("-mvk-prefill-command-buffers requires -backend vulkan when set to 0 or 1")
		}
	default:
		return errors.New("-mvk-prefill-command-buffers must be default, 0, or 1")
	}
	if *mvkPrefill == "1" && *runtimeRoot == "" {
		return errors.New("-mvk-prefill-command-buffers 1 requires an explicit -runtime with the prefill eligibility fix")
	}
	if *mvkPrefillDiagnostics && *backend != "vulkan" {
		return errors.New("-mvk-prefill-diagnostics requires -backend vulkan")
	}
	if *mvkResourceDiagnostics && *backend != "vulkan" {
		return errors.New("-mvk-resource-diagnostics requires -backend vulkan")
	}
	if *mvkLogFrames > 0 && *backend != "vulkan" {
		return errors.New("-mvk-log-frames requires -backend vulkan")
	}
	if uint64(*mvkLogFrames) > 1<<32-1 {
		return errors.New("-mvk-log-frames exceeds the MoltenVK uint32 limit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var runtime wine.Runtime
	var err error
	if *runtimeRoot == "" {
		runtime, err = wine.Discover(ctx)
	} else {
		runtime, err = wine.Open(ctx, *runtimeRoot)
	}
	if err != nil {
		return err
	}
	started := time.Now()
	if err := runtime.Prepare(ctx, *prefix, *backend, os.Stdout); err != nil {
		return err
	}
	cancel()
	fmt.Printf("Prefix prepared in %s\n", time.Since(started))
	var commandErr error
	if flag.NArg() > 0 {
		if !filepath.IsAbs(flag.Arg(0)) {
			return errors.New("executable path must be absolute")
		}
		cmd := runtime.Command(*prefix, flag.Arg(0), flag.Args()[1:], os.Stdout)
		if *gstDebug != "" {
			cmd.Env = append(cmd.Env, "GST_DEBUG="+*gstDebug, "GST_DEBUG_NO_COLOR=1")
		}
		if *wineDebug != "" {
			cmd.Env = append(cmd.Env, "WINEDEBUG="+*wineDebug)
		}
		if *mvkSynchronous != "default" {
			cmd.Env = append(cmd.Env, "MVK_CONFIG_SYNCHRONOUS_QUEUE_SUBMITS="+*mvkSynchronous)
			fmt.Printf("MVK_CONFIG_SYNCHRONOUS_QUEUE_SUBMITS=%s (probe override)\n", *mvkSynchronous)
		} else {
			fmt.Println("MVK_CONFIG_SYNCHRONOUS_QUEUE_SUBMITS=default (no probe override)")
		}
		if *mvkPrefill != "default" {
			cmd.Env = append(cmd.Env, "MVK_CONFIG_PREFILL_METAL_COMMAND_BUFFERS="+*mvkPrefill)
			fmt.Printf("MVK_CONFIG_PREFILL_METAL_COMMAND_BUFFERS=%s (probe override)\n", *mvkPrefill)
		}
		if *mvkPrefillDiagnostics {
			cmd.Env = append(cmd.Env, "FINEWINE_PREFILL_DIAGNOSTICS=1")
			fmt.Println("FINEWINE_PREFILL_DIAGNOSTICS=1 (probe override)")
		}
		if *mvkResourceDiagnostics {
			cmd.Env = append(cmd.Env, "FINEWINE_RESOURCE_DIAGNOSTICS=1")
			fmt.Println("FINEWINE_RESOURCE_DIAGNOSTICS=1 (probe override)")
		}
		if *mvkLogFrames > 0 {
			frames := strconv.FormatUint(uint64(*mvkLogFrames), 10)
			cmd.Env = append(cmd.Env, "MVK_CONFIG_PERFORMANCE_TRACKING=1", "MVK_CONFIG_PERFORMANCE_LOGGING_FRAME_COUNT="+frames)
			fmt.Printf("MoltenVK performance statistics every %s frames (probe override)\n", frames)
		}
		commandErr = cmd.Run()
	}
	// Match the launch probe: the startup timeout does not limit the session.
	return errors.Join(commandErr, runtime.Wait(context.Background(), *prefix, os.Stdout))
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
