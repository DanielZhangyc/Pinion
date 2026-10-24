// Package media downloads and validates public official launcher backgrounds.
// It does not read game installations, account credentials, or Wine containers.
package media

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
)

const (
	apiURL             = "https://launcher.hypergryph.com/api/proxy/web/batch_proxy"
	imageLimit   int64 = 16 << 20
	videoLimit   int64 = 64 << 20
	manifestName       = "manifest.json"
)

// Asset contains validated local files. VideoPath is empty when the API has no video.
type Asset struct {
	ImagePath string
	VideoPath string
}

// Snapshot is a complete pair of official game backgrounds.
type Snapshot struct {
	Arknights Asset
	Endfield  Asset
}

// Manager serializes cache access. Use one Manager for each cache directory.
type Manager struct {
	mu     sync.Mutex
	dir    string
	client *http.Client
}

// New creates a manager. Filesystem and network errors are returned by its methods.
func New(cacheDir string) *Manager {
	return &Manager{dir: cacheDir, client: &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many media redirects")
			}
			return validateURL(req.URL.String())
		},
	}}
}

type resource struct {
	URL          string    `json:"url"`
	MD5          string    `json:"md5,omitempty"`
	SHA256       string    `json:"sha256"`
	File         string    `json:"file"`
	Kind         string    `json:"kind"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	CacheControl string    `json:"cache_control,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type gameCache struct {
	Image resource  `json:"image"`
	Video *resource `json:"video,omitempty"`
}

type manifest struct {
	Version   int       `json:"version"`
	Arknights gameCache `json:"arknights"`
	Endfield  gameCache `json:"endfield"`
}

type background struct {
	URL      string `json:"url"`
	MD5      string `json:"md5"`
	VideoURL string `json:"video_url"`
}

// Cached reads and validates the last completely published snapshot without network access.
// A missing or damaged cache returns an error; callers may keep their embedded artwork.
func (m *Manager) Cached() (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.prepare(); err != nil {
		return Snapshot{}, err
	}
	meta, err := m.readManifest()
	if err != nil {
		return Snapshot{}, err
	}
	if err := m.validateManifest(meta); err != nil {
		return Snapshot{}, err
	}
	return m.snapshot(meta), nil
}

// Refresh fetches current configuration and reuses fresh validated resources.
// An expired resource is conditionally revalidated using its HTTP validators.
// Only after every resource succeeds is a new manifest atomically published.
func (m *Manager) Refresh(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := m.prepare(); err != nil {
		return Snapshot{}, err
	}
	previous, err := m.readManifest()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	ark, err := m.fetchBackground(ctx, "GzD1CpaWgmSq1wew")
	if err != nil {
		return Snapshot{}, fmt.Errorf("Arknights configuration: %w", err)
	}
	end, err := m.fetchBackground(ctx, "6LL0KJuqHBVz33WK")
	if err != nil {
		return Snapshot{}, fmt.Errorf("Endfield configuration: %w", err)
	}
	next := manifest{Version: 1}
	next.Arknights, err = m.refreshGame(ctx, ark, previous.Arknights)
	if err != nil {
		return Snapshot{}, fmt.Errorf("Arknights media: %w", err)
	}
	next.Endfield, err = m.refreshGame(ctx, end, previous.Endfield)
	if err != nil {
		return Snapshot{}, fmt.Errorf("Endfield media: %w", err)
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode media manifest: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := writeAtomic(m.dir, manifestName, data); err != nil {
		return Snapshot{}, err
	}
	return m.snapshot(next), nil
}

func (m *Manager) prepare() error {
	if m.dir == "" {
		return errors.New("media cache directory is empty")
	}
	absolute, err := filepath.Abs(m.dir)
	if err != nil {
		return fmt.Errorf("resolve media cache directory: %w", err)
	}
	m.dir = absolute
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return fmt.Errorf("create media cache: %w", err)
	}
	info, err := os.Lstat(m.dir)
	if err != nil {
		return fmt.Errorf("inspect media cache: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("media cache must be a directory, not a symlink")
	}
	return nil
}

func (m *Manager) fetchBackground(ctx context.Context, appcode string) (background, error) {
	// One request per game: responses do not echo appcode, and both use the same kind.
	body, err := json.Marshal(map[string]any{"proxy_reqs": []any{map[string]any{
		"kind":                  "get_main_bg_image",
		"get_main_bg_image_req": map[string]string{"appcode": appcode, "language": "zh-cn", "channel": "1", "sub_channel": "1", "platform": "Windows", "source": "launcher"},
	}}})
	if err != nil {
		return background{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return background{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return background{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return background{}, fmt.Errorf("official API HTTP %d", resp.StatusCode)
	}
	data, err := readLimited(resp.Body, 1<<20)
	if err != nil {
		return background{}, fmt.Errorf("read official API: %w", err)
	}
	var payload struct {
		Responses []struct {
			Kind       string `json:"kind"`
			Background struct {
				Image background `json:"main_bg_image"`
			} `json:"get_main_bg_image_rsp"`
		} `json:"proxy_rsps"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return background{}, fmt.Errorf("decode official API: %w", err)
	}
	var result background
	matches := 0
	for _, response := range payload.Responses {
		if response.Kind == "get_main_bg_image" {
			result = response.Background.Image
			matches++
		}
	}
	if matches != 1 {
		return background{}, fmt.Errorf("official API returned %d backgrounds, expected one", matches)
	}
	if err := validateURL(result.URL); err != nil {
		return background{}, err
	}
	result.MD5 = strings.ToLower(result.MD5)
	checksum, err := hex.DecodeString(result.MD5)
	if err != nil || len(checksum) != md5.Size {
		return background{}, errors.New("official API returned an invalid image MD5")
	}
	if result.VideoURL != "" {
		if err := validateURL(result.VideoURL); err != nil {
			return background{}, err
		}
	}
	return result, nil
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid media URL: %w", err)
	}
	if u.Scheme != "https" || u.Host != "hg-utils-public.hycdn.cn" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/hg-utils/") {
		return errors.New("media URL must use the official HTTPS CDN and hg-utils path")
	}
	return nil
}

func (m *Manager) refreshGame(ctx context.Context, bg background, old gameCache) (gameCache, error) {
	img, err := m.refreshResource(ctx, bg.URL, bg.MD5, "image", old.Image)
	if err != nil {
		return gameCache{}, err
	}
	game := gameCache{Image: img}
	if bg.VideoURL != "" {
		var previous resource
		if old.Video != nil {
			previous = *old.Video
		}
		vid, err := m.refreshResource(ctx, bg.VideoURL, "", "video", previous)
		if err != nil {
			return gameCache{}, err
		}
		game.Video = &vid
	}
	return game, nil
}

func resourceName(raw, checksum, kind, contentHash string) string {
	key := sha256.Sum256([]byte(raw + "\n" + checksum + "\n" + kind + "\n" + contentHash))
	extension := ".image"
	if kind == "video" {
		extension = ".mp4"
	}
	return hex.EncodeToString(key[:]) + extension
}

func (m *Manager) refreshResource(ctx context.Context, raw, checksum, kind string, old resource) (resource, error) {
	current := resource{URL: raw, MD5: checksum, Kind: kind}
	validOld := old.URL == raw && old.MD5 == checksum && old.Kind == kind
	if validOld {
		if err := m.validateResource(old); err != nil {
			return resource{}, fmt.Errorf("validate existing %s cache: %w", kind, err)
		}
		if time.Now().Before(old.ExpiresAt) {
			return old, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return resource{}, err
	}
	if validOld {
		if old.ETag != "" {
			req.Header.Set("If-None-Match", old.ETag)
		}
		if old.LastModified != "" {
			req.Header.Set("If-Modified-Since", old.LastModified)
		}
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return resource{}, fmt.Errorf("download %s: %w", kind, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		if !validOld {
			return resource{}, errors.New("CDN returned 304 without a validated cached file")
		}
		if err := applyCacheHeaders(&old, resp.Header, time.Now()); err != nil {
			return resource{}, err
		}
		return old, nil
	}
	if resp.StatusCode != http.StatusOK {
		return resource{}, fmt.Errorf("download %s HTTP %d", kind, resp.StatusCode)
	}
	limit := imageLimit
	if kind == "video" {
		limit = videoLimit
	}
	if resp.ContentLength > limit {
		return resource{}, fmt.Errorf("%s exceeds %d byte limit", kind, limit)
	}
	if kind == "video" {
		contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if err != nil || contentType != "video/mp4" {
			return resource{}, errors.New("video response must have Content-Type video/mp4")
		}
	}
	if err := applyCacheHeaders(&current, resp.Header, time.Now()); err != nil {
		return resource{}, err
	}
	f, err := os.CreateTemp(m.dir, ".media-download-*")
	if err != nil {
		return resource{}, fmt.Errorf("create media download: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resource{}, fmt.Errorf("write %s download: %w", kind, err)
	}
	if n == 0 || n > limit {
		return resource{}, fmt.Errorf("%s download has invalid size %d", kind, n)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return resource{}, fmt.Errorf("%s download length differs from Content-Length", kind)
	}
	current.Size = n
	if err := f.Sync(); err != nil {
		return resource{}, fmt.Errorf("sync media download: %w", err)
	}
	if err := f.Close(); err != nil {
		return resource{}, fmt.Errorf("close media download: %w", err)
	}
	if err := validateFile(f.Name(), &current, true); err != nil {
		return resource{}, err
	}
	current.File = resourceName(raw, checksum, kind, current.SHA256)
	if err := ctx.Err(); err != nil {
		return resource{}, err
	}
	if err := os.Rename(f.Name(), filepath.Join(m.dir, current.File)); err != nil {
		return resource{}, fmt.Errorf("publish media download: %w", err)
	}
	return current, nil
}

func applyCacheHeaders(r *resource, headers http.Header, now time.Time) error {
	// Missing freshness means revalidate next time; never invent a CDN TTL.
	r.ExpiresAt = now
	if value := headers.Get("Cache-Control"); value != "" {
		r.CacheControl = value
	}
	forceRevalidate := false
	for _, directive := range strings.Split(r.CacheControl, ",") {
		parts := strings.SplitN(strings.TrimSpace(directive), "=", 2)
		switch strings.ToLower(parts[0]) {
		case "no-store":
			return errors.New("official CDN forbids storing this media (Cache-Control: no-store)")
		case "no-cache":
			forceRevalidate = true
		case "max-age":
			if len(parts) != 2 {
				return errors.New("invalid CDN max-age")
			}
			seconds, err := strconv.ParseInt(strings.Trim(parts[1], "\""), 10, 64)
			if err != nil || seconds < 0 || seconds > int64((365*24*time.Hour)/time.Second) {
				return errors.New("invalid CDN max-age")
			}
			age := int64(0)
			if raw := headers.Get("Age"); raw != "" {
				age, err = strconv.ParseInt(raw, 10, 64)
				if err != nil || age < 0 {
					return errors.New("invalid CDN Age")
				}
			}
			// Also account for apparent response age when Age is absent or smaller.
			if date, err := http.ParseTime(headers.Get("Date")); err == nil && now.After(date) {
				if apparent := int64(now.Sub(date) / time.Second); apparent > age {
					age = apparent
				}
			}
			remaining := seconds - age
			if remaining < 0 {
				remaining = 0
			}
			r.ExpiresAt = now.Add(time.Duration(remaining) * time.Second)
		}
	}
	if forceRevalidate {
		r.ExpiresAt = now
	}
	if value := headers.Get("ETag"); value != "" {
		r.ETag = value
	}
	if value := headers.Get("Last-Modified"); value != "" {
		r.LastModified = value
	}
	return nil
}

func (m *Manager) readManifest() (manifest, error) {
	path := filepath.Join(m.dir, manifestName)
	info, err := os.Lstat(path)
	if err != nil {
		return manifest{}, fmt.Errorf("read media manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return manifest{}, errors.New("media manifest must be a regular file smaller than 1 MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, fmt.Errorf("read media manifest: %w", err)
	}
	var meta manifest
	if err := json.Unmarshal(data, &meta); err != nil {
		return manifest{}, fmt.Errorf("decode media manifest: %w", err)
	}
	if meta.Version != 1 {
		return manifest{}, fmt.Errorf("unsupported media manifest version %d", meta.Version)
	}
	return meta, nil
}

func (m *Manager) validateManifest(meta manifest) error {
	for _, game := range []gameCache{meta.Arknights, meta.Endfield} {
		if game.Image.Kind != "image" {
			return errors.New("media manifest is missing an image")
		}
		if err := m.validateResource(game.Image); err != nil {
			return err
		}
		if game.Video != nil {
			if game.Video.Kind != "video" {
				return errors.New("media manifest has invalid video kind")
			}
			if err := m.validateResource(*game.Video); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) validateResource(r resource) error {
	if err := validateURL(r.URL); err != nil {
		return err
	}
	if r.Kind != "image" && r.Kind != "video" {
		return errors.New("invalid media resource kind")
	}
	if r.File != resourceName(r.URL, r.MD5, r.Kind, r.SHA256) {
		return errors.New("media cache filename does not match resource identity")
	}
	return validateFile(filepath.Join(m.dir, r.File), &r, false)
}

func validateFile(path string, r *resource, downloaded bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect cached %s: %w", r.Kind, err)
	}
	limit := imageLimit
	if r.Kind == "video" {
		limit = videoLimit
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit || info.Size() != r.Size {
		return fmt.Errorf("invalid cached %s file size or type", r.Kind)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open cached %s: %w", r.Kind, err)
	}
	defer f.Close()
	sha := sha256.New()
	md := md5.New()
	if _, err := io.Copy(io.MultiWriter(sha, md), f); err != nil {
		return fmt.Errorf("hash cached %s: %w", r.Kind, err)
	}
	actual := hex.EncodeToString(sha.Sum(nil))
	if downloaded {
		r.SHA256 = actual
	} else if actual != r.SHA256 {
		return fmt.Errorf("cached %s SHA256 mismatch", r.Kind)
	}
	if r.Kind == "image" && hex.EncodeToString(md.Sum(nil)) != r.MD5 {
		return errors.New("background image MD5 differs from official API")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if r.Kind == "video" {
		var header [12]byte
		if _, err := io.ReadFull(f, header[:]); err != nil {
			return fmt.Errorf("read video container: %w", err)
		}
		if string(header[4:8]) != "ftyp" {
			return errors.New("video is missing its MP4 ftyp signature")
		}
		return nil
	}
	config, format, err := image.DecodeConfig(f)
	if err != nil {
		return fmt.Errorf("decode background image dimensions: %w", err)
	}
	if format != "png" && format != "jpeg" && format != "webp" {
		return fmt.Errorf("unsupported background image format %q", format)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 40_000_000 {
		return errors.New("background image dimensions exceed supported bounds")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, _, err := image.Decode(f); err != nil {
		return fmt.Errorf("decode complete background image: %w", err)
	}
	return nil
}

func (m *Manager) snapshot(meta manifest) Snapshot {
	asset := func(game gameCache) Asset {
		result := Asset{ImagePath: filepath.Join(m.dir, game.Image.File)}
		if game.Video != nil {
			result.VideoPath = filepath.Join(m.dir, game.Video.File)
		}
		return result
	}
	return Snapshot{Arknights: asset(meta.Arknights), Endfield: asset(meta.Endfield)}
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response exceeds size limit")
	}
	return data, nil
}

func writeAtomic(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, ".media-manifest-*")
	if err != nil {
		return fmt.Errorf("create media manifest: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write media manifest: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync media manifest: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close media manifest: %w", err)
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("publish media manifest: %w", err)
	}
	return nil
}
