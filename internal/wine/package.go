package wine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// PackageRuntime builds a distributable archive from a real, complete runtime.
// Only output is written. The archived manifest covers every regular file and
// file symlink, while preserving the build's source and patch metadata.
func PackageRuntime(ctx context.Context, source, output, downloadURL string) (descriptor PackageDescriptor, resultErr error) {
	if !filepath.IsAbs(source) || !filepath.IsAbs(output) {
		return descriptor, errors.New("FineWine 打包源和输出路径必须是绝对路径")
	}
	if downloadURL != "" {
		if err := validPackageURL(downloadURL); err != nil {
			return descriptor, err
		}
	}
	runtime, err := validatePackageRuntime(ctx, source)
	if err != nil {
		return descriptor, err
	}
	source = runtime.Root
	if rel, err := filepath.Rel(source, output); err != nil || filepath.IsLocal(rel) || rel == "." {
		return descriptor, errors.New("FineWine 打包输出不得位于源运行环境内")
	}
	files := make(map[string]string)
	err = filepath.WalkDir(source, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filename == source {
			return nil
		}
		relative, err := filepath.Rel(source, filename)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if !safePackagePath(name) {
			return fmt.Errorf("FineWine 源路径无效：%s", name)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(filename)
			if err != nil {
				return err
			}
			if !safePackageLink(name, target) {
				return fmt.Errorf("FineWine 源包含外部软链接：%s -> %s", name, target)
			}
			resolved, err := filepath.EvalSymlinks(filename)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, resolved)
			if err != nil || !filepath.IsLocal(rel) {
				return fmt.Errorf("FineWine 源软链接离开运行环境：%s", name)
			}
			info, err := os.Stat(filename)
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("FineWine 源软链接目标不是普通文件：%s", name)
			}
		} else if entry.IsDir() {
			return nil
		} else if !entry.Type().IsRegular() {
			return fmt.Errorf("FineWine 源包含特殊文件：%s", name)
		}
		if name != "runtime.json" {
			digest, err := packageFileDigest(ctx, filename)
			if err != nil {
				return err
			}
			files[name] = digest
		}
		return nil
	})
	if err != nil {
		return descriptor, err
	}
	data, err := os.ReadFile(filepath.Join(source, "runtime.json"))
	if err != nil {
		return descriptor, err
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(data, &metadata); err != nil {
		return descriptor, err
	}
	metadata["files"], err = json.Marshal(files)
	if err != nil {
		return descriptor, err
	}
	manifestData, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return descriptor, err
	}
	manifestData = append(manifestData, '\n')
	if _, err := os.Lstat(filepath.Join(output, PackageDescriptorName)); err == nil {
		existing, err := readPackageDescriptor(filepath.Join(output, PackageDescriptorName))
		if err != nil {
			return descriptor, err
		}
		if existing.ID != runtime.id || existing.URL != downloadURL {
			return descriptor, errors.New("FineWine 已有安装包的版本或下载地址不同，请显式移除该输出后重新打包")
		}
		archivePath := filepath.Join(output, existing.Archive)
		if err := verifyPackage(ctx, archivePath, existing, func(string, int64, int64) {}); err != nil {
			return descriptor, err
		}
		packagedManifest, err := readArchivedManifest(ctx, archivePath)
		if err != nil {
			return descriptor, err
		}
		if !bytes.Equal(packagedManifest, manifestData) {
			return descriptor, errors.New("FineWine 已有安装包与源运行环境不同，请显式移除该输出后重新打包")
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return descriptor, err
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return descriptor, err
	}
	stage, err := os.MkdirTemp(output, ".package-*")
	if err != nil {
		return descriptor, err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(stage)) }()
	archivePath := filepath.Join(stage, PackageArchiveName)
	file, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return descriptor, err
	}
	compressed, err := gzip.NewWriterLevel(file, gzip.BestSpeed)
	if err != nil {
		return descriptor, errors.Join(err, file.Close())
	}
	archive := tar.NewWriter(compressed)
	var size int64
	walkErr := filepath.WalkDir(source, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filename == source {
			return nil
		}
		relative, err := filepath.Rel(source, filename)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var target string
		if info.Mode()&os.ModeSymlink != 0 {
			target, err = os.Readlink(filename)
			if err != nil {
				return err
			}
			if !safePackageLink(filepath.ToSlash(relative), target) {
				return fmt.Errorf("FineWine 源软链接在打包时发生变化：%s", relative)
			}
		}
		header, err := tar.FileInfoHeader(info, target)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		header.Mode &= 0777
		if header.Name == "runtime.json" {
			header.Size = int64(len(manifestData))
		}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if header.Typeflag == tar.TypeReg {
			if header.Name == "runtime.json" {
				n, err := io.Copy(archive, bytes.NewReader(manifestData))
				size += n
				return err
			}
			input, err := os.Open(filename)
			if err != nil {
				return err
			}
			hash := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(archive, hash), &packageReader{ctx: ctx, reader: input})
			size += n
			if err := errors.Join(copyErr, input.Close()); err != nil {
				return err
			}
			if hex.EncodeToString(hash.Sum(nil)) != files[header.Name] {
				return fmt.Errorf("FineWine 源在打包时发生变化：%s", header.Name)
			}
		}
		return nil
	})
	if err := errors.Join(walkErr, archive.Close(), compressed.Close(), file.Sync(), file.Close()); err != nil {
		return descriptor, err
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return descriptor, err
	}
	digest, err := packageFileDigest(ctx, archivePath)
	if err != nil {
		return descriptor, err
	}
	manifestDigest := sha256.Sum256(manifestData)
	descriptor = PackageDescriptor{Schema: 1, ID: runtime.id, Archive: PackageArchiveName, Size: info.Size(), SHA256: digest, UnpackedSize: size, ManifestSHA256: hex.EncodeToString(manifestDigest[:]), URL: downloadURL}
	data, err = json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return descriptor, err
	}
	if err := os.WriteFile(filepath.Join(stage, PackageDescriptorName), append(data, '\n'), 0644); err != nil {
		return descriptor, err
	}
	if err := os.Rename(archivePath, filepath.Join(output, PackageArchiveName)); err != nil {
		return descriptor, err
	}
	if err := os.Rename(filepath.Join(stage, PackageDescriptorName), filepath.Join(output, PackageDescriptorName)); err != nil {
		return descriptor, err
	}
	return descriptor, nil
}

func readArchivedManifest(ctx context.Context, filename string) (data []byte, resultErr error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, compressed.Close()) }()
	archive := tar.NewReader(&packageReader{ctx: ctx, reader: compressed})
	for {
		header, err := archive.Next()
		if err != nil {
			return nil, fmt.Errorf("FineWine 已有安装包缺少清单：%w", err)
		}
		if header.Name == "runtime.json" {
			if header.Typeflag != tar.TypeReg || header.Size > 20*1024*1024 {
				return nil, errors.New("FineWine 已有安装包清单无效")
			}
			return io.ReadAll(archive)
		}
	}
}

func packageFileDigest(ctx context.Context, filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, &packageReader{ctx: ctx, reader: file})
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
