// Command packagecmd creates the pinned FineWine application resource package.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"pinion/internal/wine"
)

func main() {
	source := flag.String("source", "", "absolute path to a complete FineWine runtime")
	output := flag.String("output", "resources/runtime", "package output directory")
	url := flag.String("url", "", "optional pinned HTTPS archive URL for online distribution")
	flag.Parse()
	if *source == "" {
		fmt.Fprintln(os.Stderr, "-source is required; no existing runtime is selected implicitly")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	outputPath, err := filepath.Abs(*output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	descriptor, err := wine.PackageRuntime(ctx, *source, outputPath, *url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(descriptor); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
