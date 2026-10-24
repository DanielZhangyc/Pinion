// Package paths resolves the launcher's own persistent data directories.
package paths

import (
	"errors"
	"os"
	"path/filepath"
)

const DataRootEnvironment = "PINION_DATA_ROOT"

type Layout struct {
	Data  string
	Cache string
	Logs  string
	// UserData is empty when MyGo should retain its default path.
	UserData string
}

// Resolve does not create directories. An explicit data root isolates all
// launcher state without changing HOME or the user's selected game directory.
func Resolve() (Layout, error) {
	if root, set := os.LookupEnv(DataRootEnvironment); set {
		if !filepath.IsAbs(root) {
			return Layout{}, errors.New("PINION_DATA_ROOT must be an absolute path")
		}
		root = filepath.Clean(root)
		return Layout{
			Data:     root,
			Cache:    filepath.Join(root, "Cache"),
			Logs:     filepath.Join(root, "Logs"),
			UserData: filepath.Join(root, "UserData"),
		}, nil
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return Layout{}, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return Layout{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, err
	}
	return Layout{
		Data:  filepath.Join(config, "Gryph Launcher"),
		Cache: filepath.Join(cache, "Gryph Launcher"),
		Logs:  filepath.Join(home, "Library", "Logs", "Gryph Launcher"),
	}, nil
}
