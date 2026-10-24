package launch

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const officialLauncherAPI = "https://launcher.hypergryph.com/api/proxy/batch_proxy"

type officialLauncherPackage struct {
	Version string `json:"version"`
	URL     string `json:"zip_package_url"`
	MD5     string `json:"md5"`
	Size    string `json:"package_size"`
}

// Called only by an explicit launch, after Wine has initialized this prefix.
// Existing installations are reused without a network request or file writes.
func prepareEndfieldParent(ctx context.Context, root, prefix, directory string, output io.Writer) (string, error) {
	if err := prepareEndfieldMapping(prefix, directory); err != nil {
		return "", err
	}
	parent, err := installedEndfieldParent(prefix)
	if err != nil || parent != "" {
		return parent, err
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout: 90 * time.Second, ForceAttemptHTTP2: true,
		},
		Timeout: 15 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("官方启动组件下载重定向过多")
			}
			return checkOfficialLauncherURL(req.URL.String())
		},
	}
	defer client.CloseIdleConnections()
	pack, err := resolveOfficialLauncher(ctx, client)
	if err != nil {
		return "", fmt.Errorf("查询鹰角官方启动组件：%w", err)
	}
	if _, err := fmt.Fprintf(output, "Preparing official Games.exe: launcher=%s archive_bytes=%s md5=%s\n", pack.Version, pack.Size, pack.MD5); err != nil {
		return "", err
	}
	archive, err := cacheOfficialLauncher(ctx, client, filepath.Join(root, "Cache", "official-launcher"), pack)
	if err != nil {
		return "", fmt.Errorf("下载或校验鹰角官方启动组件：%w", err)
	}
	if err := deployOfficialParent(ctx, archive, prefix, pack.MD5, output); err != nil {
		return "", fmt.Errorf("部署鹰角官方 Games.exe：%w", err)
	}
	return endfieldParent(prefix, directory)
}

func prepareEndfieldMapping(prefix, directory string) error {
	if !filepath.IsAbs(directory) {
		return errors.New("终末地安装目录必须是绝对路径")
	}
	drive := filepath.Join(prefix, "drive_c")
	driveInfo, err := os.Lstat(drive)
	if err != nil {
		return err
	}
	mappedDriveInfo, err := os.Stat(filepath.Join(prefix, "dosdevices", "c:"))
	if err != nil {
		return err
	}
	if !driveInfo.IsDir() || !os.SameFile(driveInfo, mappedDriveInfo) {
		return errors.New("终末地容器 C: 未指向自己的 drive_c，无法安全准备游戏映射")
	}
	gameInfo, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if !gameInfo.IsDir() {
		return errors.New("终末地安装路径不是目录")
	}
	mapped := filepath.Join(drive, "GryphEndfield")
	if _, err := os.Lstat(mapped); errors.Is(err, os.ErrNotExist) {
		if err := os.Symlink(directory, mapped); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("创建终末地游戏映射：%w", err)
		}
	} else if err != nil {
		return err
	}
	// Preserve dangling links and user directories instead of replacing them.
	return checkEndfieldMapping(prefix, directory)
}

func checkOfficialLauncherURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") ||
		(u.Hostname() != "launcher.hypergryph.com" && !strings.HasSuffix(u.Hostname(), ".hycdn.cn")) {
		return fmt.Errorf("不支持的官方启动组件下载地址：%s", u.Hostname())
	}
	return nil
}

func resolveOfficialLauncher(ctx context.Context, client *http.Client) (officialLauncherPackage, error) {
	body := []byte(`{"seq":"5","proxy_reqs":[{"kind":"get_latest_launcher","get_latest_launcher_req":{"appcode":"abYeZZ16BPluCFyT","launcher_appcode":"6LL0KJuqHBVz33WK","channel":"1","sub_channel":"1","version":""}}]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, officialLauncherAPI, bytes.NewReader(body))
	if err != nil {
		return officialLauncherPackage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return officialLauncherPackage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return officialLauncherPackage{}, fmt.Errorf("官方版本接口返回 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return officialLauncherPackage{}, err
	}
	if len(data) > 1<<20 {
		return officialLauncherPackage{}, errors.New("官方启动组件元数据超过 1 MiB")
	}
	var result struct {
		Responses []struct {
			Kind string                  `json:"kind"`
			Pack officialLauncherPackage `json:"get_latest_launcher_rsp"`
		} `json:"proxy_rsps"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return officialLauncherPackage{}, err
	}
	if len(result.Responses) != 1 || result.Responses[0].Kind != "get_latest_launcher" {
		return officialLauncherPackage{}, errors.New("官方启动组件版本响应无效")
	}
	pack := result.Responses[0].Pack
	size, err := strconv.ParseInt(pack.Size, 10, 64)
	digest, digestErr := hex.DecodeString(pack.MD5)
	if err != nil || size <= 0 || size > 512<<20 || digestErr != nil || len(digest) != md5.Size || pack.Version == "" {
		return officialLauncherPackage{}, errors.New("官方启动组件大小、版本或 MD5 无效")
	}
	pack.MD5 = strings.ToLower(pack.MD5)
	return pack, checkOfficialLauncherURL(pack.URL)
}

func cacheOfficialLauncher(ctx context.Context, client *http.Client, directory string, pack officialLauncherPackage) (_ string, resultErr error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	name := filepath.Join(directory, pack.MD5+".zip")
	size, err := strconv.ParseInt(pack.Size, 10, 64)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(name); err == nil {
		return name, verifyOfficialArchive(ctx, name, size, pack.MD5)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pack.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || (resp.ContentLength >= 0 && resp.ContentLength != size) {
		return "", fmt.Errorf("官方启动组件响应大小无效：HTTP %d / %d", resp.StatusCode, resp.ContentLength)
	}
	file, err := os.CreateTemp(directory, ".launcher-*")
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, os.Remove(file.Name())) }()
	hash := md5.New()
	count, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, size+1))
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	if count != size || hex.EncodeToString(hash.Sum(nil)) != pack.MD5 {
		return "", errors.New("官方启动组件压缩包大小或 MD5 校验失败")
	}
	if err := os.Link(file.Name(), name); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return name, verifyOfficialArchive(ctx, name, size, pack.MD5)
}

func verifyOfficialArchive(ctx context.Context, name string, size int64, digest string) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return fmt.Errorf("官方启动组件缓存文件类型或大小无效：%s", name)
	}
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := md5.New()
	if _, err := io.Copy(hash, &launchContextReader{ctx, file}); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("官方启动组件缓存 MD5 校验失败：%s", name)
	}
	return nil
}

func deployOfficialParent(ctx context.Context, archive, prefix, identity string, output io.Writer) (resultErr error) {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	var game *zip.File
	for _, file := range reader.File {
		if strings.EqualFold(file.Name, "Games.exe") {
			if game != nil || !file.Mode().IsRegular() || file.UncompressedSize64 == 0 || file.UncompressedSize64 > 64<<20 {
				return errors.New("官方启动组件 ZIP 中的 Games.exe 不唯一或文件类型、大小无效")
			}
			game = file
		}
	}
	if game == nil {
		return errors.New("官方启动组件 ZIP 中没有 Games.exe")
	}
	directory := filepath.Join(prefix, "drive_c")
	for _, component := range []string{"Program Files", "Hypergryph Launcher", "gryph-" + identity} {
		directory = filepath.Join(directory, component)
		if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(directory)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("官方启动组件目录不是普通目录：%s", directory)
		}
	}
	in, err := game.Open()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, in.Close()) }()
	file, err := os.CreateTemp(directory, ".Games-*")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.Remove(file.Name())) }()
	hash := sha256.New()
	count, copyErr := io.Copy(io.MultiWriter(file, hash), &launchContextReader{ctx, io.LimitReader(in, int64(game.UncompressedSize64)+1)})
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if uint64(count) != game.UncompressedSize64 {
		return errors.New("官方 Games.exe 解压大小不一致")
	}
	dest := filepath.Join(directory, "Games.exe")
	if err := os.Link(file.Name(), dest); err != nil {
		return fmt.Errorf("保留已有文件，未替换 Games.exe：%w", err)
	}
	_, err = fmt.Fprintf(output, "Official Games.exe deployed: %s\nOfficial Games.exe SHA-256: %x\nAuthenticode verification remains with the game SDK.\n", dest, hash.Sum(nil))
	return err
}

type launchContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *launchContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
