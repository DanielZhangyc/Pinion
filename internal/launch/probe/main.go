// The probe exercises the same installation record, runtime and lifecycle as the UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"pinion/internal/download"
	"pinion/internal/launch"
)

func run() error {
	game := flag.String("game", "", "game to launch: arknights, arknights-bilibili or endfield")
	root := flag.String("download-root", "", "absolute path to the configured game download directory")
	flag.Parse()
	if !filepath.IsAbs(*root) || flag.NArg() != 0 {
		return errors.New("provide -game and an absolute -download-root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	session, err := launch.Start(ctx, download.GameID(*game), *root, func(path string) {
		fmt.Printf("Game logs: %s\n", path)
	})
	if err != nil {
		return err
	}
	// The startup timeout must not limit an interactive game session.
	return session.Wait()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
