//go:build integration

package media

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// recordingTransport delegates every request to the real network transport.
// It records actual CDN responses, without substituting data or status codes.
type recordingTransport struct {
	base           http.RoundTripper
	notModified    int
	mediaDownloads int
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.URL.Host == "hg-utils-public.hycdn.cn" {
		if resp.StatusCode == http.StatusNotModified {
			r.notModified++
		}
		if resp.StatusCode == http.StatusOK {
			r.mediaDownloads++
		}
	}
	return resp, nil
}

func TestOfficialMediaCache(t *testing.T) {
	m := New(t.TempDir())
	recorder := &recordingTransport{base: http.DefaultTransport}
	m.client.Transport = recorder
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	first, err := m.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Arknights.ImagePath == "" || first.Endfield.ImagePath == "" || first.Endfield.VideoPath == "" {
		t.Fatal("current official API did not provide both images and Endfield video")
	}
	downloads := recorder.mediaDownloads
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.mediaDownloads != downloads {
		t.Fatal("fresh resources were downloaded again")
	}

	// Expire only the stored freshness; all media and HTTP validators are real.
	meta, err := m.readManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, game := range []*gameCache{&meta.Arknights, &meta.Endfield} {
		game.Image.ExpiresAt = time.Now().Add(-time.Hour)
		if game.Video != nil {
			game.Video.ExpiresAt = time.Now().Add(-time.Hour)
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(m.dir, manifestName, data); err != nil {
		t.Fatal(err)
	}
	revalidated, err := m.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if revalidated != first {
		t.Fatal("conditional revalidation changed media paths")
	}
	if recorder.notModified != downloads {
		t.Fatalf("expected %d real CDN 304 responses, got %d", downloads, recorder.notModified)
	}
	if recorder.mediaDownloads != downloads {
		t.Fatal("conditional revalidation downloaded media bodies")
	}
	t.Logf("verified %d actual downloads, fresh reuse, and %d actual HTTP 304 responses", downloads, recorder.notModified)

	manifestPath := filepath.Join(m.dir, manifestName)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if snapshot, err := m.Refresh(cancelled); !errors.Is(err, context.Canceled) || snapshot != (Snapshot{}) {
		t.Fatalf("cancelled refresh returned %#v, %v", snapshot, err)
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("cancelled refresh modified the published manifest")
	}
	if cached, err := New(m.dir).Cached(); err != nil || cached != first {
		t.Fatalf("offline cache: %#v, %v", cached, err)
	}

	// Deliberately damage an actually downloaded resource to test explicit failure.
	f, err := os.OpenFile(first.Endfield.VideoPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0}, 4); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := m.Cached(); err == nil || snapshot != (Snapshot{}) {
		t.Fatalf("damaged cache returned %#v, %v", snapshot, err)
	}
	if snapshot, err := m.Refresh(ctx); err == nil || snapshot != (Snapshot{}) {
		t.Fatalf("damaged refresh returned %#v, %v", snapshot, err)
	}
	after, err = os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed refresh modified the published manifest")
	}
}
