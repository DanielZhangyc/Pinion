package wine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"pinion/internal/paths"
)

const PackageDescriptorName = "runtime-package.json"
const PackageArchiveName = "finewine-runtime.tar.gz"

var ErrProvisionBusy = errors.New("FineWine 安装正在进行，请稍后重试")
var ErrPackageUnavailable = errors.New("应用未包含 FineWine 安装描述符，请使用包含运行环境的发行版")

// PackageDescriptor pins one release. The application ships this descriptor;
// a downloaded archive can never choose its own digest, ID or download URL.
type PackageDescriptor struct {
	Schema         int    `json:"schema"`
	ID             string `json:"id"`
	Archive        string `json:"archive"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	UnpackedSize   int64  `json:"unpackedSize"`
	ManifestSHA256 string `json:"manifestSha256"`
	URL            string `json:"url,omitempty"`
}

type ProvisionOptions struct {
	// InstallRoot defaults to the launcher's independent Runtime directory.
	InstallRoot string
	// ResourcesDir defaults to <app>/Contents/Resources/runtime.
	ResourcesDir string
}

type ProvisionProgress struct {
	Stage     string
	Completed int64
	Total     int64
}

// DefaultResourcesDir locates the package next to a macOS app executable.
func DefaultResourcesDir() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "Resources", "runtime")), nil
}

// Provision reuses a valid installed runtime or installs the pinned package.
// It never runs Wine, prepares prefixes, upgrades an existing installation or
// falls back from a broken bundled package to a network download. Callbacks run
// synchronously on the caller's goroutine and must not block.
func Provision(ctx context.Context, options ProvisionOptions, progress func(ProvisionProgress)) (result Runtime, resultErr error) {
	if err := ctx.Err(); err != nil {
		return Runtime{}, err
	}
	root := options.InstallRoot
	if root == "" {
		layout, err := paths.Resolve()
		if err != nil {
			return Runtime{}, err
		}
		root = filepath.Join(layout.Data, "Runtime")
	}
	if !filepath.IsAbs(root) {
		return Runtime{}, errors.New("FineWine 安装路径必须是绝对路径")
	}
	report := func(stage string, done, total int64) {
		if progress != nil {
			progress(ProvisionProgress{Stage: stage, Completed: done, Total: total})
		}
	}
	report("checking", 0, 0)
	current := filepath.Join(root, "current")
	if _, err := os.Lstat(current); err == nil {
		runtime, err := Open(ctx, current)
		if err != nil {
			return Runtime{}, fmt.Errorf("现有 FineWine 损坏，保留原文件以便检查：%w", err)
		}
		report("ready", 1, 1)
		return runtime, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Runtime{}, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return Runtime{}, err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Runtime{}, err
	}
	current = filepath.Join(root, "current")
	lock, err := os.OpenFile(filepath.Join(root, ".install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Runtime{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return Runtime{}, ErrProvisionBusy
		}
		return Runtime{}, err
	}
	// Another process can finish between the initial read and acquiring the lock.
	if _, err := os.Lstat(current); err == nil {
		runtime, err := Open(ctx, current)
		if err == nil {
			report("ready", 1, 1)
		}
		return runtime, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return Runtime{}, err
	}
	resources := options.ResourcesDir
	if resources == "" {
		resources, err = DefaultResourcesDir()
		if err != nil {
			return Runtime{}, err
		}
	}
	if !filepath.IsAbs(resources) {
		return Runtime{}, errors.New("FineWine 资源路径必须是绝对路径")
	}
	descriptor, err := readPackageDescriptor(filepath.Join(resources, PackageDescriptorName))
	if err != nil {
		return Runtime{}, err
	}
	version := filepath.Join(root, descriptor.ID)
	if _, err := os.Lstat(version); err == nil {
		// A crash may leave a fully validated version before current is published.
		// Revalidate it against the descriptor's pinned manifest without replacing
		// any existing file, then finish activation under the same install lock.
		runtime, err := validateInstalledPackage(ctx, version, descriptor)
		if err != nil {
			return Runtime{}, fmt.Errorf("FineWine 现有版本目录与安装包不符，保留原文件：%w", err)
		}
		if err := ctx.Err(); err != nil {
			return Runtime{}, err
		}
		if err := activatePackage(root, descriptor.ID); err != nil {
			return Runtime{}, err
		}
		report("ready", 1, 1)
		return runtime, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Runtime{}, err
	}
	stage, err := os.MkdirTemp(root, ".install-*")
	if err != nil {
		return Runtime{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(stage)) }()
	archive := filepath.Join(resources, descriptor.Archive)
	if info, err := os.Lstat(archive); errors.Is(err, os.ErrNotExist) {
		if descriptor.URL == "" {
			return Runtime{}, errors.New("应用未包含 FineWine 安装包，且未配置固定版本下载地址")
		}
		archive = filepath.Join(stage, descriptor.Archive)
		if err := downloadPackage(ctx, descriptor, archive, report); err != nil {
			return Runtime{}, err
		}
	} else if err != nil {
		return Runtime{}, err
	} else if !info.Mode().IsRegular() {
		return Runtime{}, errors.New("FineWine 安装包必须是普通文件")
	}
	if err := verifyPackage(ctx, archive, descriptor, report); err != nil {
		return Runtime{}, err
	}
	payload := filepath.Join(stage, "payload")
	if err := os.Mkdir(payload, 0700); err != nil {
		return Runtime{}, err
	}
	if err := extractPackage(ctx, archive, payload, descriptor.UnpackedSize, report); err != nil {
		return Runtime{}, err
	}
	runtime, err := validateInstalledPackage(ctx, payload, descriptor)
	if err != nil {
		return Runtime{}, err
	}
	if err := ctx.Err(); err != nil {
		return Runtime{}, err
	}
	if err := os.Rename(payload, version); err != nil {
		return Runtime{}, err
	}
	published := false
	defer func() {
		if !published {
			resultErr = errors.Join(resultErr, os.RemoveAll(version))
		}
	}()
	if err := activatePackage(root, descriptor.ID); err != nil {
		return Runtime{}, err
	}
	published = true
	runtime.Root = version
	report("ready", 1, 1)
	return runtime, nil
}

func activatePackage(root, id string) (resultErr error) {
	stage, err := os.MkdirTemp(root, ".activate-*")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(stage)) }()
	link := filepath.Join(stage, "current")
	if err := os.Symlink(id, link); err != nil {
		return err
	}
	return os.Rename(link, filepath.Join(root, "current"))
}

func readPackageDescriptor(filename string) (PackageDescriptor, error) {
	file, err := os.Open(filename)
	if errors.Is(err, os.ErrNotExist) {
		return PackageDescriptor{}, fmt.Errorf("%w：%s", ErrPackageUnavailable, filename)
	}
	if err != nil {
		return PackageDescriptor{}, err
	}
	var descriptor PackageDescriptor
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&descriptor)
	var trailing any
	if decodeErr == nil {
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			decodeErr = errors.New("FineWine 发行描述符包含多余内容")
		}
	}
	if err := errors.Join(decodeErr, file.Close()); err != nil {
		return PackageDescriptor{}, err
	}
	if err := descriptor.validate(); err != nil {
		return PackageDescriptor{}, err
	}
	return descriptor, nil
}

func (d PackageDescriptor) validate() error {
	digest, err := hex.DecodeString(d.SHA256)
	if d.Schema != 1 || !validRuntimeID(d.ID) || d.Archive != PackageArchiveName || d.Size <= 0 || d.UnpackedSize <= 0 || err != nil || len(digest) != sha256.Size {
		return errors.New("FineWine 发行描述符的版本、文件名、大小或 SHA-256 无效")
	}
	manifestDigest, err := hex.DecodeString(d.ManifestSHA256)
	if err != nil || len(manifestDigest) != sha256.Size {
		return errors.New("FineWine 发行描述符缺少有效的完整文件清单 SHA-256")
	}
	if d.URL != "" {
		return validPackageURL(d.URL)
	}
	return nil
}

func validPackageURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("FineWine 下载地址必须是无用户凭证的 HTTPS 地址")
	}
	return nil
}

func downloadPackage(ctx context.Context, d PackageDescriptor, destination string, report func(string, int64, int64)) error {
	report("downloading", 0, d.Size)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{Timeout: 2 * time.Hour, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("FineWine 下载重定向过多")
		}
		return validPackageURL(req.URL.String())
	}}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("下载 FineWine：%w", err)
	}
	if response.StatusCode != http.StatusOK || response.ContentLength >= 0 && response.ContentLength != d.Size {
		return errors.Join(fmt.Errorf("FineWine 下载响应无效：HTTP %d，长度 %d，需要 %d", response.StatusCode, response.ContentLength, d.Size), response.Body.Close())
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.Join(err, response.Body.Close())
	}
	reader := &packageReader{ctx: ctx, reader: io.LimitReader(response.Body, d.Size+1), total: d.Size, report: func(done, total int64) { report("downloading", done, total) }}
	n, copyErr := io.Copy(file, reader)
	if copyErr == nil && n != d.Size {
		copyErr = errors.New("FineWine 下载文件大小与发行描述符不一致")
	}
	return errors.Join(copyErr, file.Close(), response.Body.Close())
}

func verifyPackage(ctx context.Context, filename string, d PackageDescriptor, report func(string, int64, int64)) error {
	report("verifying", 0, d.Size)
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	hash := sha256.New()
	reader := &packageReader{ctx: ctx, reader: io.LimitReader(file, d.Size+1), total: d.Size, report: func(done, total int64) { report("verifying", done, total) }}
	n, copyErr := io.Copy(hash, reader)
	if copyErr == nil && (n != d.Size || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), d.SHA256)) {
		copyErr = errors.New("FineWine 安装包大小或 SHA-256 与发行描述符不一致")
	}
	return errors.Join(copyErr, file.Close())
}

// packageReader applies cancellation within large file copies and bounds UI
// progress events to ten per second instead of one update per buffer.
type packageReader struct {
	ctx    context.Context
	reader io.Reader
	done   int64
	total  int64
	last   time.Time
	report func(int64, int64)
}

func (r *packageReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(buffer)
	r.done += int64(n)
	if r.report != nil && (time.Since(r.last) >= 100*time.Millisecond || err != nil || r.done == r.total) {
		r.report(r.done, r.total)
		r.last = time.Now()
	}
	return n, err
}

func safePackagePath(name string) bool {
	return name != "." && path.Clean(name) == name && filepath.IsLocal(name) && !strings.Contains(name, "\\")
}

func safePackageLink(name, target string) bool {
	return target != "" && !path.IsAbs(target) && !strings.Contains(target, "\\") && filepath.IsLocal(path.Join(path.Dir(name), target))
}

func extractPackage(ctx context.Context, filename, destination string, size int64, report func(string, int64, int64)) (resultErr error) {
	report("extracting", 0, size)
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, compressed.Close()) }()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	archive := tar.NewReader(&packageReader{ctx: ctx, reader: compressed})
	seen := make(map[string]bool)
	links := make(map[string]string)
	var done int64
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !safePackagePath(name) || seen[name] {
			return fmt.Errorf("FineWine 归档路径无效或重复：%q", header.Name)
		}
		seen[name] = true
		if err := root.MkdirAll(path.Dir(name), 0755); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size < 0 || header.Size > size-done {
				return errors.New("FineWine 归档展开大小超出发行描述符")
			}
			output, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0777)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(output, archive)
			if err := errors.Join(copyErr, output.Close()); err != nil {
				return err
			}
			done += n
			report("extracting", done, size)
		case tar.TypeSymlink:
			if !safePackageLink(name, header.Linkname) {
				return fmt.Errorf("FineWine 归档软链接越界：%s -> %s", name, header.Linkname)
			}
			links[name] = header.Linkname
		default:
			return fmt.Errorf("FineWine 归档包含不支持的条目：%s（类型 %d）", name, header.Typeflag)
		}
	}
	// Reach gzip EOF to verify its checksum, and reject extra tar payloads.
	if n, err := io.Copy(io.Discard, io.LimitReader(&packageReader{ctx: ctx, reader: compressed}, 1)); err != nil {
		return err
	} else if n != 0 {
		return errors.New("FineWine 归档包含额外的展开数据")
	}
	if done != size {
		return fmt.Errorf("FineWine 归档展开大小不符：%d，需要 %d", done, size)
	}
	// Links are created last: archive writes never traverse archive-created links.
	for name, target := range links {
		if err := root.Symlink(target, name); err != nil {
			return err
		}
	}
	for name := range links {
		if _, err := root.Stat(name); err != nil {
			return fmt.Errorf("FineWine 归档链接无效：%s：%w", name, err)
		}
	}
	return nil
}

func validatePackageRuntime(ctx context.Context, root string) (Runtime, error) {
	runtime, err := Open(ctx, root)
	if err != nil {
		return Runtime{}, err
	}
	data, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if err != nil {
		return Runtime{}, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Runtime{}, err
	}
	// Open hashes launch-critical files; installation additionally verifies every
	// file declared by the build, including future manifest additions.
	for name, digest := range m.Files {
		if err := ctx.Err(); err != nil {
			return Runtime{}, err
		}
		if err := verifyFile(filepath.Join(root, name), digest, false); err != nil {
			return Runtime{}, fmt.Errorf("FineWine 清单文件 %s：%w", name, err)
		}
	}
	for _, directory := range []string{"share/wine/mono", "share/wine/gecko", "lib/gstreamer-1.0", "lib/gstreamer-deps"} {
		entries, err := os.ReadDir(filepath.Join(root, directory))
		if err != nil {
			return Runtime{}, fmt.Errorf("FineWine 必需环境 %s：%w", directory, err)
		}
		if len(entries) == 0 {
			return Runtime{}, fmt.Errorf("FineWine 必需环境目录为空：%s", directory)
		}
	}
	return runtime, nil
}

func validateInstalledPackage(ctx context.Context, directory string, descriptor PackageDescriptor) (Runtime, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return Runtime{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Runtime{}, errors.New("FineWine 版本路径必须是普通目录")
	}
	if err := verifyFile(filepath.Join(directory, "runtime.json"), descriptor.ManifestSHA256, false); err != nil {
		return Runtime{}, fmt.Errorf("FineWine 完整文件清单与发行描述符不一致：%w", err)
	}
	runtime, err := validatePackageRuntime(ctx, directory)
	if err != nil {
		return Runtime{}, err
	}
	if runtime.id != descriptor.ID {
		return Runtime{}, errors.New("FineWine 安装包版本与发行描述符不一致")
	}
	data, err := os.ReadFile(filepath.Join(directory, "runtime.json"))
	if err != nil {
		return Runtime{}, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Runtime{}, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Runtime{}, err
	}
	var size int64
	walkErr := filepath.WalkDir(directory, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filename == directory {
			return nil
		}
		name, err := filepath.Rel(directory, filename)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := root.Readlink(name)
			if err != nil {
				return err
			}
			if !safePackageLink(filepath.ToSlash(name), target) {
				return fmt.Errorf("FineWine 安装目录包含外部软链接：%s", name)
			}
			info, err = root.Stat(name)
			if err != nil {
				return err
			}
		} else if info.Mode().IsRegular() {
			size += info.Size()
		}
		if info.Mode().IsRegular() && name != "runtime.json" {
			if _, exists := m.Files[filepath.ToSlash(name)]; !exists {
				return fmt.Errorf("FineWine 安装目录包含未列入清单的文件：%s", name)
			}
		} else if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("FineWine 安装目录包含特殊文件：%s", name)
		}
		return nil
	})
	if err := errors.Join(walkErr, root.Close()); err != nil {
		return Runtime{}, err
	}
	if size != descriptor.UnpackedSize {
		return Runtime{}, errors.New("FineWine 安装目录总大小与发行描述符不一致")
	}
	return runtime, nil
}
