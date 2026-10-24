package launch

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	bootstrapVersion          = "1"
	endfieldWindowsDirectory  = `C:\GryphEndfield`
	endfieldWindowsExecutable = endfieldWindowsDirectory + `\Endfield.exe`
)

//go:embed assets/endfield-bootstrap.exe
var endfieldBootstrap []byte

func bootstrapIdentity() string {
	return fmt.Sprintf("%x", sha256.Sum256(endfieldBootstrap))
}

// Each embedded version has its own immutable content-addressed path. Publishing
// with Link is atomic and never replaces an existing, possibly running helper.
func deployEndfieldBootstrap(root string) (_ string, resultErr error) {
	identity := bootstrapIdentity()
	directory := filepath.Join(root, "Helpers", "endfield-bootstrap", "v"+bootstrapVersion+"-"+identity)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "gryph-endfield-bootstrap.exe")
	if _, err := os.Lstat(path); err == nil {
		return path, verifyEndfieldBootstrap(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.CreateTemp(directory, ".bootstrap-*")
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, os.Remove(file.Name())) }()
	if _, err := file.Write(endfieldBootstrap); err != nil {
		return "", errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return "", errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Link(file.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return path, verifyEndfieldBootstrap(path)
}

func verifyEndfieldBootstrap(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(endfieldBootstrap)) {
		return fmt.Errorf("启动组件文件类型或大小与内嵌版本不一致：%s", path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if sha256.Sum256(contents) != sha256.Sum256(endfieldBootstrap) {
		return fmt.Errorf("启动组件 SHA-256 与内嵌版本不一致：%s", path)
	}
	return nil
}

// The read-only check is also used by installation diagnostics.
func endfieldParent(prefix, directory string) (string, error) {
	if err := checkEndfieldMapping(prefix, directory); err != nil {
		return "", err
	}
	parent, err := installedEndfieldParent(prefix)
	if err != nil {
		return "", err
	}
	if parent == "" {
		return "", errors.New("终末地容器中没有官方 Games.exe，请启动游戏以准备官方启动组件")
	}
	return parent, nil
}

func checkEndfieldMapping(prefix, directory string) error {
	mapped := filepath.Join(prefix, "dosdevices", "c:", "GryphEndfield")
	mappedInfo, err := os.Stat(mapped)
	if err != nil {
		return fmt.Errorf("读取 C:\\GryphEndfield 游戏目录映射：%w", err)
	}
	installedInfo, err := os.Stat(directory)
	if err != nil {
		return fmt.Errorf("读取终末地已完成安装目录：%w", err)
	}
	if !mappedInfo.IsDir() || !installedInfo.IsDir() || !os.SameFile(mappedInfo, installedInfo) {
		return fmt.Errorf("C:\\GryphEndfield 映射与已完成安装目录不一致：%s", directory)
	}
	return nil
}

// Never guess among multiple installed versions or change an existing parent.
func installedEndfieldParent(prefix string) (string, error) {
	root := filepath.Join(prefix, "drive_c", "Program Files", "Hypergryph Launcher")
	versions, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取鹰角官方启动器安装目录：%w", err)
	}
	var matches []string
	for _, version := range versions {
		if !version.IsDir() {
			continue
		}
		path := filepath.Join(root, version.Name(), "Games.exe")
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("读取鹰角官方 Games.exe：%w", err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return "", fmt.Errorf("鹰角官方 Games.exe 文件无效：%s", path)
		}
		matches = append(matches, path)
	}
	if len(matches) == 0 {
		return "", nil
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("终末地容器中应有唯一的官方 Games.exe，实际找到 %d 个；请检查官方启动器安装", len(matches))
	}
	relative, err := filepath.Rel(filepath.Join(prefix, "drive_c"), matches[0])
	if err != nil {
		return "", err
	}
	return `C:\` + strings.ReplaceAll(relative, string(filepath.Separator), `\`), nil
}

func logEndfieldPlayerLogs(prefix string, output io.Writer) error {
	paths, err := filepath.Glob(filepath.Join(prefix, "drive_c", "users", "*", "AppData", "LocalLow", "Hypergryph", "Endfield", "Player.log"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		_, err := fmt.Fprintln(output, "Unity Player.log: not yet found in the prefix's Windows user profiles")
		return err
	}
	for _, path := range paths {
		if _, err := fmt.Fprintf(output, "Unity Player.log: %s\n", path); err != nil {
			return err
		}
	}
	return nil
}
