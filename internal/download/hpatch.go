package download

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"pinion/internal/paths"
)

//go:embed assets/pinion-hpatch
var hpatchHelper []byte

// applyHDiff creates out exclusively; the caller owns its staging directory and
// must discard partial output on error, including cancellation. The manifest MD5
// must still be checked by the caller before publishing the new version.
func applyHDiff(ctx context.Context, old, diff, out string, expectedSize int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if expectedSize < 0 {
		return errors.New("HDiff 目标大小不能为负数")
	}
	layout, err := paths.Resolve()
	if err != nil {
		return err
	}
	helper, err := deployHPatchHelper(layout.Data)
	if err != nil {
		return fmt.Errorf("准备 HDiff 合成组件：%w", err)
	}
	command := exec.CommandContext(ctx, helper, old, diff, out, strconv.FormatInt(expectedSize, 10))
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("HDiff 合成失败：%w：%s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Link publishes each content-addressed executable atomically without replacing
// any deployed version, following the launch bootstrap's deployment convention.
func deployHPatchHelper(root string) (_ string, resultErr error) {
	identity := sha256.Sum256(hpatchHelper)
	directory := filepath.Join(root, "Helpers", "pinion-hpatch", fmt.Sprintf("%x", identity))
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "pinion-hpatch")
	if _, err := os.Lstat(path); err == nil {
		return path, verifyHPatchHelper(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.CreateTemp(directory, ".hpatch-*")
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, os.Remove(file.Name())) }()
	if _, err := file.Write(hpatchHelper); err != nil {
		return "", errors.Join(err, file.Close())
	}
	if err := file.Chmod(0700); err != nil {
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
	return path, verifyHPatchHelper(path)
}

func verifyHPatchHelper(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(hpatchHelper)) || info.Mode().Perm()&0100 == 0 {
		return fmt.Errorf("HDiff 合成组件类型、大小或执行权限与内嵌版本不一致：%s", path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if sha256.Sum256(contents) != sha256.Sum256(hpatchHelper) {
		return fmt.Errorf("HDiff 合成组件 SHA-256 与内嵌版本不一致：%s", path)
	}
	return nil
}
