// Package settings persists the user's launcher preferences.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pinion/internal/paths"
)

type Settings struct {
	DownloadDirectory string `json:"download_directory"`
	ArknightsChannel  string `json:"arknights_channel,omitempty"`
}

type Store struct {
	path string
}

func New(path string) *Store {
	return &Store{path: path}
}

func DefaultPath() (string, error) {
	layout, err := paths.Resolve()
	if err != nil {
		return "", fmt.Errorf("locate settings directory: %w", err)
	}
	return filepath.Join(layout.Data, "settings.json"), nil
}

// ValidateDirectory returns a clean absolute path after verifying that the
// existing directory accepts a temporary file. It does not create game folders.
func ValidateDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("download directory is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve download directory: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect download directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("download path is not a directory")
	}
	probe, err := os.CreateTemp(absolute, ".gryph-write-check-*")
	if err != nil {
		return "", fmt.Errorf("download directory is not writable: %w", err)
	}
	closeErr := probe.Close()
	removeErr := os.Remove(probe.Name())
	if err := errors.Join(closeErr, removeErr); err != nil {
		return "", fmt.Errorf("clean up download directory check: %w", err)
	}
	return absolute, nil
}

// Load does not create a configuration file or validate saved directories.
// A previously selected disk may be disconnected when the launcher starts.
func (s *Store) Load() (Settings, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	var settings *Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	if settings == nil {
		return Settings{}, errors.New("decode settings: expected an object")
	}
	return *settings, nil
}

// Save atomically replaces the settings file after validating the selected
// directory. An empty directory clears the user's selection.
func (s *Store) Save(settings Settings) error {
	if settings.DownloadDirectory != "" {
		directory, err := ValidateDirectory(settings.DownloadDirectory)
		if err != nil {
			return err
		}
		settings.DownloadDirectory = directory
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	file, err := os.CreateTemp(directory, ".settings-*")
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err := file.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write settings: %w", err), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync settings: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close settings: %w", err)
	}
	if err := os.Rename(temporary, s.path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	return nil
}
