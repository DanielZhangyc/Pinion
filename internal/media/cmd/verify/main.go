// Command verify exercises the real official API and local cache. It downloads
// current backgrounds and any official video; it does not launch a game.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"pinion/internal/media"
)

func main() {
	cache := flag.String("cache", "", "dedicated persistent media cache directory (required)")
	flag.Parse()
	if *cache == "" {
		fmt.Fprintln(os.Stderr, "-cache is required")
		os.Exit(2)
	}
	if err := verify(*cache); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func verify(dir string) error {
	m := media.New(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	first, err := m.Refresh(ctx)
	if err != nil {
		return fmt.Errorf("first refresh: %w", err)
	}
	before, err := fileStates(first)
	if err != nil {
		return err
	}
	second, err := m.Refresh(ctx)
	if err != nil {
		return fmt.Errorf("second refresh: %w", err)
	}
	if first != second {
		return fmt.Errorf("official media changed between verification refreshes")
	}
	after, err := fileStates(second)
	if err != nil {
		return err
	}
	for name, state := range before {
		if state != after[name] {
			return fmt.Errorf("second refresh replaced a fresh cached resource: %s", name)
		}
	}
	cached, err := media.New(dir).Cached()
	if err != nil {
		return fmt.Errorf("read offline cache: %w", err)
	}
	if cached != second {
		return fmt.Errorf("offline snapshot differs from refreshed snapshot")
	}
	data, err := json.MarshalIndent(struct {
		Snapshot              media.Snapshot       `json:"snapshot"`
		Files                 map[string]fileState `json:"files"`
		FreshFilesReused      bool                 `json:"fresh_files_reused"`
		OfflineCacheValidated bool                 `json:"offline_cache_validated"`
	}{cached, after, true, true}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

type fileState struct {
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

func fileStates(snapshot media.Snapshot) (map[string]fileState, error) {
	states := make(map[string]fileState)
	for _, path := range []string{snapshot.Arknights.ImagePath, snapshot.Arknights.VideoPath, snapshot.Endfield.ImagePath, snapshot.Endfield.VideoPath} {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		states[filepath.Base(path)] = fileState{Size: info.Size(), Modified: info.ModTime()}
	}
	return states, nil
}
