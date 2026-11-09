// Package download installs official CN Windows game packages into an isolated
// caller-selected directory. It never discovers or modifies Wine installations.
package download

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type GameID string

const (
	Arknights        GameID = "arknights"
	Endfield         GameID = "endfield"
	StagePreparing          = "preparing"
	StageDownloading        = "downloading"
	StageExtracting         = "extracting"
	StageVerifying          = "verifying"
	StageCompleted          = "completed"
	apiURL                  = "https://launcher.hypergryph.com/api/proxy/batch_proxy"
	metadataLimit    int64  = 16 << 20
)

type Pack struct {
	URL  string `json:"url"`
	MD5  string `json:"md5"`
	Size int64  `json:"size"`
}

type File struct {
	Path string `json:"path"`
	MD5  string `json:"md5"`
	Size int64  `json:"size"`
}

type Plan struct {
	Differential  bool   `json:"differential"`
	RequiredBytes int64  `json:"required_bytes"`
	Game          GameID `json:"game"`
	Version       string `json:"version"`
	DownloadBytes int64  `json:"download_bytes"`
	UnpackedBytes int64  `json:"unpacked_bytes"`
	Packs         []Pack `json:"packs"`
	Files         []File `json:"files"`
	ManifestMD5   string `json:"manifest_md5"`
	ManifestURL   string `json:"manifest_url"`
	ManifestBytes int64  `json:"manifest_bytes"`
	archiveFiles  []archiveItem
	patch         *patchPlan
}

type archiveItem struct {
	name string
	size int64
	crc  uint32
	dir  bool
}

var ErrChecksum = errors.New("file checksum mismatch")

// Progress counters describe the current stage. Callbacks run on the caller's
// goroutine and should return promptly; UI callers marshal updates themselves.
type Progress struct {
	Game       GameID
	Stage      string
	File       string
	DoneBytes  int64
	TotalBytes int64
}

type Record struct {
	Game        GameID    `json:"game"`
	Version     string    `json:"version"`
	Directory   string    `json:"directory"`
	CompletedAt time.Time `json:"completed_at"`
	ManifestMD5 string    `json:"manifest_md5"`
}

type Manager struct {
	root   string
	client *http.Client
	mu     sync.Mutex
}

func New(root string) *Manager {
	return &Manager{root: root, client: &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 30 * time.Second,
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second, ForceAttemptHTTP2: true,
		IdleConnTimeout: 90 * time.Second,
	}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many download redirects")
		}
		return checkURL(req.URL.String())
	}}}
}

func appcode(game GameID) (string, error) {
	switch game {
	case Arknights:
		return "GzD1CpaWgmSq1wew", nil
	case Endfield:
		return "6LL0KJuqHBVz33WK", nil
	default:
		return "", fmt.Errorf("unknown game ID %q", game)
	}
}

func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := u.Hostname()
	if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") ||
		(host != "launcher.hypergryph.com" && !strings.HasSuffix(host, ".hycdn.cn")) {
		return fmt.Errorf("unsupported official download URL host %q", host)
	}
	return nil
}

func (m *Manager) request(ctx context.Context, method, raw string, body io.Reader) (*http.Response, error) {
	if err := checkURL(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept-Encoding", "identity")
	return m.client.Do(req)
}

func readBounded(resp *http.Response, limit int64) ([]byte, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("official server returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("metadata exceeds %d bytes", limit)
	}
	return b, nil
}

// latestPackage reads official version metadata without fetching game files.
func (m *Manager) latestPackage(ctx context.Context, game GameID) (Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	plan := Plan{Game: game}
	code, err := appcode(game)
	if err != nil {
		return plan, err
	}
	body, err := json.Marshal(map[string]any{"seq": "5", "proxy_reqs": []any{map[string]any{
		"kind": "get_latest_game", "get_latest_game_req": map[string]string{
			"appcode": code, "launcher_appcode": "abYeZZ16BPluCFyT", "channel": "1", "sub_channel": "1", "version": "",
		},
	}}})
	if err != nil {
		return plan, err
	}
	resp, err := m.request(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return plan, err
	}
	data, err := readBounded(resp, metadataLimit)
	if err != nil {
		return plan, err
	}
	var result struct {
		Responses []struct {
			Kind string `json:"kind"`
			Game struct {
				Version string `json:"version"`
				Package struct {
					Packs []struct {
						URL  string `json:"url"`
						MD5  string `json:"md5"`
						Size string `json:"package_size"`
					} `json:"packs"`
					FilePath string `json:"file_path"`
					MD5      string `json:"game_files_md5"`
				} `json:"pkg"`
			} `json:"get_latest_game_rsp"`
		} `json:"proxy_rsps"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return plan, err
	}
	if len(result.Responses) != 1 || result.Responses[0].Kind != "get_latest_game" {
		return plan, errors.New("unexpected game version response")
	}
	gameResult := result.Responses[0].Game
	plan.Version = gameResult.Version
	plan.ManifestMD5 = gameResult.Package.MD5
	plan.ManifestURL = strings.TrimRight(gameResult.Package.FilePath, "/") + "/game_files"
	for _, p := range gameResult.Package.Packs {
		size, err := strconv.ParseInt(p.Size, 10, 64)
		if err != nil {
			return plan, fmt.Errorf("package size: %w", err)
		}
		plan.Packs = append(plan.Packs, Pack{p.URL, p.MD5, size})
		plan.DownloadBytes += size
	}
	if err := plan.validate(false); err != nil {
		return plan, err
	}
	return plan, nil
}

// Resolve refreshes signed URLs and validates the encrypted file manifest and
// ZIP64 directory. Only bounded metadata ranges are read, never whole packages.
func (m *Manager) Resolve(ctx context.Context, game GameID) (Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	plan, err := m.latestPackage(ctx, game)
	if err != nil {
		return plan, err
	}
	resp, err := m.request(ctx, http.MethodGet, plan.ManifestURL, nil)
	if err != nil {
		return plan, err
	}
	data, err := readBounded(resp, metadataLimit)
	if err != nil {
		return plan, err
	}
	if sumMD5(data) != plan.ManifestMD5 {
		return plan, errors.New("game_files MD5 mismatch")
	}
	plan.ManifestBytes = int64(len(data))
	plan.Files, err = decodeManifest(data)
	if err != nil {
		return plan, err
	}
	remote := &remoteArchive{ctx: ctx, manager: m, packs: plan.Packs, budget: 4 << 20}
	zr, err := zip.NewReader(remote, plan.DownloadBytes)
	if err != nil {
		return plan, fmt.Errorf("official ZIP directory: %w", err)
	}
	plan.UnpackedBytes, err = inspectArchive(zr)
	if err != nil {
		return plan, err
	}
	for _, f := range zr.File {
		name, err := safeName(f.Name)
		if err != nil {
			return plan, err
		}
		plan.archiveFiles = append(plan.archiveFiles, archiveItem{name, int64(f.UncompressedSize64), f.CRC32, f.FileInfo().IsDir()})
	}
	plan.RequiredBytes = plan.DownloadBytes + plan.UnpackedBytes
	return plan, plan.validate(true)
}

func (p Plan) validate(full bool) error {
	if _, err := appcode(p.Game); err != nil {
		return err
	}
	if !validVersion(p.Version) || !validMD5(p.ManifestMD5) || len(p.Packs) == 0 {
		return errors.New("invalid official game package metadata")
	}
	var total int64
	for _, pack := range p.Packs {
		if pack.Size <= 0 || pack.Size > 2<<30 || !validMD5(pack.MD5) {
			return errors.New("invalid package size or MD5")
		}
		if err := checkURL(pack.URL); err != nil {
			return err
		}
		total += pack.Size
	}
	if total != p.DownloadBytes || total > 1<<40 {
		return errors.New("invalid total download size")
	}
	if full && (len(p.Files) == 0 || p.UnpackedBytes <= 0 || len(p.archiveFiles) == 0 || p.Differential != (p.patch != nil)) {
		return errors.New("missing file manifest or ZIP directory")
	}
	for _, f := range p.Files {
		if _, err := safeName(f.Path); err != nil {
			return err
		}
		if f.Size < 0 || !validMD5(f.MD5) {
			return errors.New("invalid file manifest entry")
		}
	}
	return nil
}

func validMD5(s string) bool { b, err := hex.DecodeString(s); return err == nil && len(b) == md5.Size }
func sumMD5(b []byte) string { h := md5.Sum(b); return hex.EncodeToString(h[:]) }

func validVersion(s string) bool {
	return s != "" && strings.Trim(s, "0123456789.") == "" && !strings.Contains(s, "..") && s[0] != '.' && s[len(s)-1] != '.'
}

func decodeManifest(data []byte) ([]File, error) {
	key, err := hex.DecodeString("c0f30e1ce763bbc21cc355a34303ac50399444bff68c4a22af398c0a166ee143")
	if err != nil {
		return nil, err
	}
	iv, err := hex.DecodeString("33467861192750649501937264608400")
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%block.BlockSize() != 0 {
		return nil, errors.New("invalid encrypted manifest length")
	}
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, data)
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > block.BlockSize() || !bytes.Equal(plain[len(plain)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) {
		return nil, errors.New("invalid manifest PKCS7 padding")
	}
	scanner := bufio.NewScanner(bytes.NewReader(plain[:len(plain)-padding]))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var files []File
	seen := map[string]bool{}
	for scanner.Scan() {
		var f File
		if err := json.Unmarshal(scanner.Bytes(), &f); err != nil {
			return nil, err
		}
		name, err := safeName(f.Path)
		if err != nil {
			return nil, err
		}
		f.Path = name
		if seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("duplicate manifest path %q", name)
		}
		seen[strings.ToLower(name)] = true
		files = append(files, f)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("empty game file manifest")
	}
	return files, nil
}

func safeName(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	clean := path.Clean(name)
	if clean == "." || clean != strings.TrimSuffix(name, "/") || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsAny(clean, ":\x00") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return clean, nil
}

func inspectArchive(zr *zip.Reader) (int64, error) {
	var total int64
	seen := map[string]bool{}
	for _, f := range zr.File {
		name, err := safeName(f.Name)
		if err != nil {
			return 0, err
		}
		if seen[strings.ToLower(name)] {
			return 0, fmt.Errorf("duplicate ZIP path %q", name)
		}
		seen[strings.ToLower(name)] = true
		if f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return 0, fmt.Errorf("unsupported ZIP entry %q", name)
		}
		if f.Flags&1 != 0 || (f.Method != zip.Store && f.Method != zip.Deflate) {
			return 0, fmt.Errorf("unsupported ZIP encryption or compression for %q", name)
		}
		if f.UncompressedSize64 > 1<<40 {
			return 0, errors.New("ZIP entry too large")
		}
		total += int64(f.UncompressedSize64)
		if total > 1<<40 {
			return 0, errors.New("ZIP expanded size exceeds limit")
		}
	}
	if len(zr.File) == 0 {
		return 0, errors.New("empty ZIP archive")
	}
	return total, nil
}

// openGame creates only a dedicated launcher-owned game directory. Existing
// nonempty directories without its ownership marker are never adopted.
func (m *Manager) openGame(game GameID, create bool) (*os.Root, error) {
	if _, err := appcode(game); err != nil {
		return nil, err
	}
	if m.root == "" {
		return nil, errors.New("download root is empty")
	}
	abs, err := filepath.Abs(m.root)
	if err != nil {
		return nil, err
	}
	// Resolve the existing ancestor before making any directory: a root chosen
	// through a symlink must not create files inside a container either.
	candidate := abs
	var missing []string
	var real string
	for {
		real, err = filepath.EvalSymlinks(candidate)
		if err == nil {
			break
		}
		if !create || !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, filepath.Base(candidate))
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return nil, err
		}
		candidate = parent
	}
	for i := len(missing) - 1; i >= 0; i-- {
		real = filepath.Join(real, missing[i])
	}
	lower := strings.ToLower(real) + "/"
	if strings.Contains(lower, "/crossover/") || strings.Contains(lower, "/bottles/") {
		return nil, errors.New("download root must be outside Wine/CrossOver containers")
	}
	if create {
		if err := os.MkdirAll(real, 0755); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(real)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := string(game)
	if create {
		if err := root.MkdirAll(name, 0755); err != nil {
			return nil, err
		}
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("game download directory is not a real directory")
	}
	gr, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	marker, err := gr.ReadFile(".gryph-download.json")
	if errors.Is(err, os.ErrNotExist) && create {
		entries, openErr := gr.Open(".")
		if openErr != nil {
			gr.Close()
			return nil, openErr
		}
		children, readErr := entries.Readdirnames(1)
		closeErr := entries.Close()
		if readErr != nil && readErr != io.EOF {
			gr.Close()
			return nil, readErr
		}
		if closeErr != nil {
			gr.Close()
			return nil, closeErr
		}
		if len(children) != 0 {
			gr.Close()
			return nil, errors.New("refusing to adopt existing nonempty game directory")
		}
		err = gr.WriteFile(".gryph-download.json", []byte("gryph-launcher:"+name+"\n"), 0644)
	} else if err == nil && string(marker) != "gryph-launcher:"+name+"\n" {
		err = errors.New("game directory ownership marker mismatch")
	}
	if err != nil {
		gr.Close()
		return nil, err
	}
	return gr, nil
}

func (m *Manager) LoadRecord(game GameID) (Record, error) {
	var record Record
	gr, err := m.openGame(game, false)
	if err != nil {
		return record, err
	}
	defer gr.Close()
	return loadRecord(gr, game)
}

func loadRecord(gr *os.Root, game GameID) (Record, error) {
	var record Record
	if err := regular(gr, "completed.json"); err != nil {
		return record, err
	}
	data, err := gr.ReadFile("completed.json")
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	if record.Game != game || !validVersion(record.Version) || !validMD5(record.ManifestMD5) || record.CompletedAt.IsZero() {
		return record, errors.New("invalid completion record")
	}
	base := path.Join("versions", record.Version)
	if record.Directory != filepath.Join(gr.Name(), base, "game") {
		base += "-" + record.ManifestMD5
	}
	if record.Directory != filepath.Join(gr.Name(), base, "game") {
		return record, errors.New("completion directory mismatch")
	}
	if err := checkDirs(gr, path.Join(base, "game")); err != nil {
		return record, err
	}
	if err := regular(gr, path.Join(base, "manifest.md5")); err != nil {
		return record, err
	}
	identity, err := gr.ReadFile(path.Join(base, "manifest.md5"))
	if err != nil {
		return record, err
	}
	if !bytes.Equal(identity, []byte(record.ManifestMD5+"\n")) {
		return record, errors.New("completion manifest mismatch")
	}
	return record, nil
}

// Install blocks until a fully checked installation completes. Cancellation or
// errors retain downloaded bytes; call Resolve and Install again to resume.
func (m *Manager) Install(ctx context.Context, plan Plan, report func(Progress)) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var record Record
	if err := plan.validate(true); err != nil {
		return record, err
	}
	if err := ctx.Err(); err != nil {
		return record, err
	}
	emit := func(stage, file string, done, total int64) {
		if report != nil {
			report(Progress{plan.Game, stage, file, done, total})
		}
	}
	emit(StagePreparing, "", 0, plan.UnpackedBytes)
	gr, err := m.openGame(plan.Game, true)
	if err != nil {
		return record, err
	}
	defer gr.Close()
	if err := regular(gr, ".lock"); err != nil {
		return record, err
	}
	lock, err := gr.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return record, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return record, fmt.Errorf("another installation is active: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	installed, err := loadRecord(gr, plan.Game)
	if err != nil {
		// Missing game files or metadata in a recorded installation are damage,
		// not permission to overwrite the directory referenced by that record.
		if _, statErr := gr.Lstat("completed.json"); !errors.Is(statErr, os.ErrNotExist) {
			return record, err
		}
	}
	if err == nil && installed.Version != plan.Version && !(VersionInfo{Version: plan.Version, ManifestMD5: plan.ManifestMD5}).UpdateAvailable(installed) {
		return record, errors.New("refusing to replace a newer installed version; resolve the update again")
	}
	if err == nil && installed.Version == plan.Version && installed.ManifestMD5 == plan.ManifestMD5 {
		// Never repair in place while completed.json points at this directory.
		// An already-installed identity must still pass the full manifest check.
		if err := verifyPlan(ctx, gr, plan, recordBase(installed), emit); err != nil {
			return record, err
		}
		emit(StageCompleted, "", plan.DownloadBytes, plan.DownloadBytes)
		return installed, nil
	}
	if err == nil {
		// Remember that this directory was completed before moving the current
		// pointer. A future publisher rollback must not repair it in place.
		data, err := json.MarshalIndent(installed, "", "  ")
		if err != nil {
			return record, err
		}
		if err := atomicWrite(gr, path.Join(recordBase(installed), "completed.json"), data); err != nil {
			return record, err
		}
	}
	base, err := prepareVersion(gr, plan)
	if err != nil {
		return record, err
	}
	completed, err := versionCompleted(gr, base, plan)
	if err != nil {
		return record, err
	}
	if completed {
		if err := verifyPlan(ctx, gr, plan, base, emit); err != nil {
			return record, err
		}
		return commitRecord(gr, plan, base, emit)
	}
	if plan.patch != nil {
		if installed.Version != plan.patch.oldVersion || installed.ManifestMD5 != plan.patch.oldManifest {
			return record, errors.New("installed differential base changed; resolve the update again")
		}
		return m.installPatch(ctx, gr, plan, base, installed, emit)
	}
	for _, dir := range []string{"cache", "game", "staging"} {
		if err := makeDirs(gr, path.Join(base, dir)); err != nil {
			return record, err
		}
	}
	// Interrupted decompression restarts only its unfinished file. Remove its
	// owned temporary output before measuring free space, so it is not counted
	// twice. Completed files are retained and checked by CRC below.
	for i := range plan.archiveFiles {
		tmp := path.Join(base, "staging", fmt.Sprintf("%d.tmp", i))
		if err := regular(gr, tmp); err != nil {
			return record, err
		}
		if err := gr.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
			return record, err
		}
	}
	reusable, err := checkSpace(ctx, gr, plan, base)
	if err != nil {
		return record, err
	}
	emit(StageDownloading, "", 0, plan.DownloadBytes)
	var downloaded int64
	for i, p := range plan.Packs {
		name := packName(base, i)
		if err := m.fetchPack(ctx, gr, p, name, func(n int64) { emit(StageDownloading, path.Base(name), downloaded+n, plan.DownloadBytes) }); err != nil {
			return record, fmt.Errorf("package %d: %w", i+1, err)
		}
		downloaded += p.Size
		emit(StageDownloading, path.Base(name), downloaded, plan.DownloadBytes)
	}
	local := &localArchive{root: gr, packs: plan.Packs, base: base}
	defer local.Close()
	zr, err := zip.NewReader(local, plan.DownloadBytes)
	if err != nil {
		return record, err
	}
	unpacked, err := inspectArchive(zr)
	if err != nil {
		return record, err
	}
	if unpacked != plan.UnpackedBytes {
		return record, errors.New("ZIP expanded size changed")
	}
	var extracted int64
	emit(StageExtracting, "", 0, unpacked)
	for i, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return record, err
		}
		name, err := safeName(f.Name)
		if err != nil {
			return record, err
		}
		dest := path.Join(base, "game", name)
		if reusable[name] {
			extracted += int64(f.UncompressedSize64)
			emit(StageExtracting, name, extracted, unpacked)
			continue
		}
		if f.FileInfo().IsDir() {
			if err := makeDirs(gr, dest); err != nil {
				return record, err
			}
			continue
		}
		if err := makeDirs(gr, path.Dir(dest)); err != nil {
			return record, err
		}
		tmp := path.Join(base, "staging", fmt.Sprintf("%d.tmp", i))
		if err := extractOne(ctx, gr, f, dest, tmp, func(n int64) { emit(StageExtracting, name, extracted+n, unpacked) }); err != nil {
			return record, err
		}
		extracted += int64(f.UncompressedSize64)
	}
	if err := local.Close(); err != nil {
		return record, err
	}
	if err := verifyPlan(ctx, gr, plan, base, emit); err != nil {
		return record, err
	}
	if err := ctx.Err(); err != nil {
		return record, err
	}
	return commitRecord(gr, plan, base, emit)
}

func recordBase(record Record) string {
	return path.Join("versions", filepath.Base(filepath.Dir(record.Directory)))
}

func commitRecord(root *os.Root, plan Plan, base string, emit func(string, string, int64, int64)) (Record, error) {
	record := Record{plan.Game, plan.Version, filepath.Join(root.Name(), base, "game"), time.Now().UTC(), plan.ManifestMD5}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return record, err
	}
	if err := atomicWrite(root, path.Join(base, "completed.json"), data); err != nil {
		return record, err
	}
	if err := atomicWrite(root, "completed.json", data); err != nil {
		return record, err
	}
	emit(StageCompleted, "", plan.DownloadBytes, plan.DownloadBytes)
	return record, nil
}

func versionCompleted(root *os.Root, base string, plan Plan) (bool, error) {
	name := path.Join(base, "completed.json")
	if err := regular(root, name); err != nil {
		return false, err
	}
	data, err := root.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return false, err
	}
	if record.Game != plan.Game || record.Version != plan.Version || record.ManifestMD5 != plan.ManifestMD5 ||
		record.CompletedAt.IsZero() || record.Directory != filepath.Join(root.Name(), base, "game") {
		return false, errors.New("version completion record mismatch")
	}
	return true, nil
}

func verifyPlan(ctx context.Context, root *os.Root, plan Plan, base string, emit func(string, string, int64, int64)) error {
	var verified, total int64
	for _, f := range plan.Files {
		total += f.Size
	}
	emit(StageVerifying, "", 0, total)
	for _, f := range plan.Files {
		if err := verifyFile(ctx, root, path.Join(base, "game", f.Path), f.Size, f.MD5); err != nil {
			return err
		}
		verified += f.Size
		emit(StageVerifying, f.Path, verified, total)
	}
	return ctx.Err()
}

// Keep the original version path for existing downloads; publisher changes to
// that version get their own manifest-qualified directory, never the old files.
func prepareVersion(root *os.Root, plan Plan) (string, error) {
	base := path.Join("versions", plan.Version)
	identity := []byte(plan.ManifestMD5 + "\n")
	if err := checkDirs(root, base); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := regular(root, path.Join(base, "manifest.md5")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	old, err := root.ReadFile(path.Join(base, "manifest.md5"))
	if err == nil && !bytes.Equal(old, identity) {
		base += "-" + plan.ManifestMD5
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := makeDirs(root, base); err != nil {
		return "", err
	}
	marker := path.Join(base, "manifest.md5")
	if err := regular(root, marker); err != nil {
		return "", err
	}
	old, err = root.ReadFile(marker)
	if err == nil {
		if !bytes.Equal(old, identity) {
			return "", errors.New("version directory manifest mismatch")
		}
		return base, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	dir, err := root.Open(base)
	if err != nil {
		return "", err
	}
	children, readErr := dir.Readdirnames(1)
	closeErr := dir.Close()
	if readErr != nil && readErr != io.EOF {
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if len(children) != 0 {
		return "", errors.New("refusing to overwrite an unrecorded version directory")
	}
	if err := atomicWrite(root, marker, identity); err != nil {
		return "", err
	}
	return base, nil
}

func packName(base string, i int) string {
	return path.Join(base, "cache", fmt.Sprintf("package.zip.%03d", i+1))
}

func checkSpace(ctx context.Context, root *os.Root, p Plan, base string) (map[string]bool, error) {
	reusable := map[string]bool{}
	required := p.UnpackedBytes
	for _, item := range p.archiveFiles {
		if item.dir {
			continue
		}
		name := path.Join(base, "game", item.name)
		ok, err := matchesArchiveFile(ctx, root, name, item)
		if err != nil {
			return nil, err
		}
		if ok {
			reusable[item.name] = true
			required -= item.size
		}
	}
	for i, pack := range p.Packs {
		var have int64
		for _, suffix := range []string{"", ".part"} {
			info, err := root.Lstat(packName(base, i) + suffix)
			if err == nil {
				if !info.Mode().IsRegular() {
					return nil, errors.New("package cache is not a regular file")
				}
				if info.Size() > have {
					have = info.Size()
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		if have > pack.Size {
			return nil, errors.New("package cache exceeds declared size")
		}
		required += pack.Size - have
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(root.Name(), &stat); err != nil {
		return nil, err
	}
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	if uint64(required) > free {
		return nil, fmt.Errorf("insufficient disk space: need %d bytes, available %d bytes", required, free)
	}
	return reusable, nil
}

func matchesArchiveFile(ctx context.Context, root *os.Root, name string, item archiveItem) (bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := regular(root, name); err != nil {
		return false, err
	}
	if info.Size() != item.size {
		return false, nil
	}
	f, err := root.Open(name)
	if err != nil {
		return false, err
	}
	h := crc32.NewIEEE()
	_, readErr := io.Copy(h, &contextReader{ctx: ctx, r: f})
	closeErr := f.Close()
	if readErr != nil {
		return false, readErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	return h.Sum32() == item.crc, nil
}

func regular(root *os.Root, name string) error {
	if err := checkDirs(root, path.Dir(name)); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", name)
	}
	return nil
}

func checkDirs(root *os.Root, name string) error { return walkDirs(root, name, false) }
func makeDirs(root *os.Root, name string) error  { return walkDirs(root, name, true) }
func walkDirs(root *os.Root, name string, create bool) error {
	if name == "." {
		return nil
	}
	if _, err := safeName(name); err != nil {
		return err
	}
	current := ""
	for _, part := range strings.Split(name, "/") {
		current = path.Join(current, part)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := root.Mkdir(current, 0755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is not a real directory", current)
		}
	}
	return nil
}

func atomicWrite(root *os.Root, name string, b []byte) error {
	if err := regular(root, name); err != nil {
		return err
	}
	if err := regular(root, name+".tmp"); err != nil {
		return err
	}
	f, err := root.OpenFile(name+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Rename(name+".tmp", name)
}

func verifyFile(ctx context.Context, root *os.Root, name string, size int64, want string) error {
	if err := regular(root, name); err != nil {
		return err
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != size {
		return fmt.Errorf("%s: %w (size)", name, ErrChecksum)
	}
	h := md5.New()
	_, err = io.Copy(h, &contextReader{ctx: ctx, r: f})
	if err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return fmt.Errorf("%s: %w (MD5)", name, ErrChecksum)
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

type progressWriter struct {
	w      io.Writer
	done   int64
	report func(int64)
}

func (w *progressWriter) Write(b []byte) (int, error) {
	n, err := w.w.Write(b)
	w.done += int64(n)
	if w.report != nil {
		w.report(w.done)
	}
	return n, err
}

func (m *Manager) fetchPack(ctx context.Context, root *os.Root, p Pack, name string, report func(int64)) error {
	info, err := root.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("cached package is not a regular file")
		}
		if err := verifyFile(ctx, root, name, p.Size, p.MD5); err != nil {
			if errors.Is(err, ErrChecksum) {
				if removeErr := root.Remove(name); removeErr != nil {
					return errors.Join(err, removeErr)
				}
			}
			return err
		}
		if report != nil {
			report(p.Size)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	part := name + ".part"
	if err := regular(root, part); err != nil {
		return err
	}
	f, err := root.OpenFile(part, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return err
	}
	offset := info.Size()
	if offset > p.Size {
		return errors.New("partial package exceeds declared size")
	}
	if offset < p.Size {
		requestCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
		defer cancel()
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, p.URL, nil)
		if err != nil {
			return err
		}
		if err := checkURL(p.URL); err != nil {
			return err
		}
		req.Header.Set("Accept-Encoding", "identity")
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, p.Size-1))
		}
		resp, err := m.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if offset > 0 {
			if err := checkRange(resp, offset, p.Size-offset, p.Size); err != nil {
				return err
			}
		} else if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("package server returned HTTP %d", resp.StatusCode)
		}
		if resp.ContentLength >= 0 && resp.ContentLength != p.Size-offset {
			return errors.New("package Content-Length mismatch")
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		writer := &progressWriter{w: f, done: offset, report: report}
		if _, err := io.Copy(writer, &contextReader{ctx: ctx, r: io.LimitReader(resp.Body, p.Size-offset)}); err != nil {
			return err
		}
		if writer.done != p.Size {
			return io.ErrUnexpectedEOF
		}
		var extra [1]byte
		if n, err := resp.Body.Read(extra[:]); n != 0 || err != io.EOF {
			if err != nil && err != io.EOF {
				return err
			}
			return errors.New("package response exceeds declared size")
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := verifyFile(ctx, root, part, p.Size, p.MD5); err != nil {
		if errors.Is(err, ErrChecksum) {
			if removeErr := root.Remove(part); removeErr != nil {
				return errors.Join(err, removeErr)
			}
		}
		return err
	}
	return root.Rename(part, name)
}

func extractOne(ctx context.Context, root *os.Root, zf *zip.File, dest, temporary string, report func(int64)) error {
	if err := regular(root, dest); err != nil {
		return err
	}
	if err := regular(root, temporary); err != nil {
		return err
	}
	in, err := zf.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := root.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	writer := &progressWriter{w: out, report: report}
	_, copyErr := io.Copy(writer, &contextReader{ctx: ctx, r: in})
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if uint64(writer.done) != zf.UncompressedSize64 {
		return errors.New("extracted file size mismatch")
	}
	return root.Rename(temporary, dest)
}

type remoteArchive struct {
	ctx     context.Context
	manager *Manager
	packs   []Pack
	budget  int64
	blocks  map[string][]byte
}

func checkRange(resp *http.Response, start, count, total int64) error {
	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("server ignored Range: HTTP %d", resp.StatusCode)
	}
	want := fmt.Sprintf("bytes %d-%d/%d", start, start+count-1, total)
	if resp.Header.Get("Content-Range") != want {
		return fmt.Errorf("unexpected Content-Range %q", resp.Header.Get("Content-Range"))
	}
	return nil
}

func (r *remoteArchive) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative ZIP metadata offset")
	}
	if r.blocks == nil {
		r.blocks = map[string][]byte{}
	}
	var n int
	var base int64
	for i, p := range r.packs {
		for off >= base && off < base+p.Size && n < len(b) {
			start := (off - base) / (64 << 10) * (64 << 10)
			count := min(int64(64<<10), p.Size-start)
			key := fmt.Sprintf("%d:%d", i, start)
			data, ok := r.blocks[key]
			if !ok {
				if count > r.budget {
					return n, errors.New("ZIP metadata range budget exceeded")
				}
				req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, p.URL, nil)
				if err != nil {
					return n, err
				}
				if err := checkURL(p.URL); err != nil {
					return n, err
				}
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+count-1))
				req.Header.Set("Accept-Encoding", "identity")
				resp, err := r.manager.client.Do(req)
				if err != nil {
					return n, err
				}
				if err := checkRange(resp, start, count, p.Size); err != nil {
					resp.Body.Close()
					return n, err
				}
				var readErr error
				data, readErr = io.ReadAll(io.LimitReader(resp.Body, count+1))
				closeErr := resp.Body.Close()
				if readErr != nil {
					return n, readErr
				}
				if closeErr != nil {
					return n, closeErr
				}
				if int64(len(data)) != count {
					return n, errors.New("ZIP metadata range length mismatch")
				}
				r.budget -= count
				r.blocks[key] = data
			}
			copied := copy(b[n:], data[off-base-start:])
			n += copied
			off += int64(copied)
		}
		base += p.Size
	}
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

type localArchive struct {
	root  *os.Root
	packs []Pack
	base  string
	files map[int]*os.File
}

func (r *localArchive) Close() error {
	var result error
	for i, f := range r.files {
		result = errors.Join(result, f.Close())
		delete(r.files, i)
	}
	return result
}

func (r *localArchive) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative archive offset")
	}
	var n int
	var base int64
	if r.files == nil {
		r.files = map[int]*os.File{}
	}
	for i, p := range r.packs {
		if off >= base && off < base+p.Size && n < len(b) {
			count := min(int64(len(b)-n), p.Size-(off-base))
			f, ok := r.files[i]
			if !ok {
				var err error
				f, err = r.root.Open(packName(r.base, i))
				if err != nil {
					return n, err
				}
				r.files[i] = f
			}
			read, readErr := f.ReadAt(b[n:n+int(count)], off-base)
			n += read
			off += int64(read)
			if readErr != nil {
				return n, readErr
			}
		}
		base += p.Size
	}
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}
