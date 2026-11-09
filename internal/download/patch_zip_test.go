package download

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This opt-in test downloads a genuine, bounded official patch. Mutations below
// deliberately damage that same archive; no stand-in package or server is used.
func TestOfficialPatchZIP(t *testing.T) {
	if os.Getenv("PINION_PATCH_ZIP_INTEGRATION") != "1" {
		t.Skip("set PINION_PATCH_ZIP_INTEGRATION=1 for the bounded official patch ZIP check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m := New(t.TempDir())
	defer m.client.CloseIdleConnections()
	_, patch, err := m.officialPatch(ctx, Endfield, "1.6.3")
	if err != nil {
		t.Fatal(err)
	}
	if patch == nil || len(patch.Packs) != 1 {
		t.Fatal("official 1.6.3 patch is no longer available as a single package")
	}
	pack := patch.Packs[0]
	size, err := strconv.ParseInt(pack.Size, 10, 64)
	if err != nil || size <= 0 || size > 4<<20 {
		t.Fatalf("official patch outside 4 MiB test limit: %q", pack.Size)
	}
	resp, err := m.request(ctx, http.MethodGet, pack.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := readBounded(resp, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	digest := md5.Sum(raw)
	if int64(len(raw)) != size || !strings.EqualFold(hex.EncodeToString(digest[:]), pack.MD5) {
		t.Fatal("official patch size or MD5 mismatch")
	}
	archive := func(raw []byte) *zip.Reader {
		t.Helper()
		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		return zr
	}
	zr := archive(raw)
	selected := -1
	for index, f := range zr.File {
		if err := validatePatchFile(f); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		r, err := openPatchFile(f, patch.Password)
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, r)
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || uint64(n) != f.UncompressedSize64 {
			t.Fatalf("%s: bytes=%d read=%v close=%v", f.Name, n, readErr, closeErr)
		}
		t.Logf("entry=%s bytes=%d sha256=%x", f.Name, n, hash.Sum(nil))
		if selected == -1 && f.Flags&1 != 0 && f.UncompressedSize64 > 1 {
			selected = index
		}
	}
	if selected == -1 {
		t.Fatal("no encrypted official patch entry available")
	}
	f := zr.File[selected]
	t.Run("wrong_password", func(t *testing.T) {
		r, err := openPatchFile(f, "incorrect:"+patch.Password)
		if err != nil {
			return
		}
		_, readErr := io.Copy(io.Discard, r)
		if closeErr := r.Close(); readErr == nil || closeErr == nil {
			t.Fatalf("wrong password accepted: read=%v close=%v", readErr, closeErr)
		}
	})
	t.Run("close_before_eof", func(t *testing.T) {
		r, err := openPatchFile(f, patch.Password)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(io.Discard, r, 1); err != nil {
			t.Fatal(err)
		}
		if err := r.Close(); err == nil {
			t.Fatal("partially read entry closed successfully")
		}
	})
	t.Run("close_at_declared_size", func(t *testing.T) {
		r, err := openPatchFile(f, patch.Password)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(io.Discard, r, int64(f.UncompressedSize64)); err != nil {
			t.Fatal(err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	})
	dataOffset, err := f.DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("authentication_corruption", func(t *testing.T) {
		damaged := bytes.Clone(raw)
		damaged[dataOffset+int64(f.CompressedSize64)-1] ^= 1
		r, err := openPatchFile(archive(damaged).File[selected], patch.Password)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, r)
		closeErr := r.Close()
		if !errors.Is(readErr, ErrChecksum) || !errors.Is(closeErr, ErrChecksum) {
			t.Fatalf("damaged authentication: read=%v close=%v", readErr, closeErr)
		}
	})
	t.Run("authentication_on_close", func(t *testing.T) {
		damaged := bytes.Clone(raw)
		damaged[dataOffset+int64(f.CompressedSize64)-1] ^= 1
		r, err := openPatchFile(archive(damaged).File[selected], patch.Password)
		if err != nil {
			t.Fatal(err)
		}
		// CopyN can return success when the last Read returns (n, error).
		// A caller checking Close must still observe the failed authentication.
		if _, err := io.CopyN(io.Discard, r, int64(f.UncompressedSize64)); err != nil && !errors.Is(err, ErrChecksum) {
			t.Fatal(err)
		}
		if err := r.Close(); !errors.Is(err, ErrChecksum) {
			t.Fatalf("authentication not checked on Close: %v", err)
		}
	})
	t.Run("truncated_payload", func(t *testing.T) {
		// Parse the genuine central directory, then make its backing archive
		// shorter. The entry's SectionReader must expose the actual truncation.
		backing := bytes.NewReader(raw)
		truncated, err := zip.NewReader(backing, int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		backing.Reset(raw[:dataOffset+int64(f.CompressedSize64)-1])
		r, err := openPatchFile(truncated.File[selected], patch.Password)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, r)
		closeErr := r.Close()
		if !errors.Is(readErr, io.ErrUnexpectedEOF) || !errors.Is(closeErr, io.ErrUnexpectedEOF) {
			t.Fatalf("truncated authentication: read=%v close=%v", readErr, closeErr)
		}
	})
	t.Run("understated_size", func(t *testing.T) {
		// Corrupt only the real entry's central uncompressed-size field.
		damaged := bytes.Clone(raw)
		end := bytes.LastIndex(damaged, []byte{'P', 'K', 5, 6})
		if end < 0 || end+22 > len(damaged) {
			t.Fatal("central directory end missing")
		}
		position := int(binary.LittleEndian.Uint32(damaged[end+16:]))
		for index := 0; index <= selected; index++ {
			if position+46 > len(damaged) || !bytes.Equal(damaged[position:position+4], []byte{'P', 'K', 1, 2}) {
				t.Fatal("central directory entry missing")
			}
			if index == selected {
				binary.LittleEndian.PutUint32(damaged[position+24:], uint32(f.UncompressedSize64-1))
				break
			}
			position += 46 + int(binary.LittleEndian.Uint16(damaged[position+28:])) + int(binary.LittleEndian.Uint16(damaged[position+30:])) + int(binary.LittleEndian.Uint16(damaged[position+32:]))
		}
		r, err := openPatchFile(archive(damaged).File[selected], patch.Password)
		if err != nil {
			t.Fatal(err)
		}
		n, readErr := io.Copy(io.Discard, r)
		if closeErr := r.Close(); readErr == nil || closeErr == nil || uint64(n) > f.UncompressedSize64-1 {
			t.Fatalf("understated output size: bytes=%d read=%v close=%v", n, readErr, closeErr)
		}
	})
	t.Logf("official ZIP package bytes=%d md5=%x entries=%d; all output authenticated", len(raw), digest, len(zr.File))
}

func TestOfficialPatchZIPRange(t *testing.T) {
	if os.Getenv("PINION_PATCH_ZIP_INTEGRATION") != "1" {
		t.Skip("set PINION_PATCH_ZIP_INTEGRATION=1 for bounded official split patch ZIP reads")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m := New(t.TempDir())
	defer m.client.CloseIdleConnections()
	_, patch, err := m.officialPatch(ctx, Arknights, "78.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if patch == nil || len(patch.Packs) == 0 {
		t.Fatal("official 78.0.0 patch no longer available")
	}
	remote := &remoteArchive{ctx: ctx, manager: m, budget: 4 << 20}
	var total int64
	for _, pack := range patch.Packs {
		size, err := strconv.ParseInt(pack.Size, 10, 64)
		if err != nil || size <= 0 {
			t.Fatalf("invalid official patch size %q", pack.Size)
		}
		remote.packs = append(remote.packs, Pack{URL: pack.URL, Size: size, MD5: pack.MD5})
		total += size
	}
	zr, err := zip.NewReader(remote, total)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, "char_1027_greyy2.ab_patch") {
			continue
		}
		if f.UncompressedSize64 > 64<<10 || f.CompressedSize64 > 64<<10 {
			t.Fatal("selected official diff exceeds 64 KiB sample limit")
		}
		r, err := openPatchFile(f, patch.Password)
		if err != nil {
			t.Fatal(err)
		}
		plain, readErr := io.ReadAll(r)
		closeErr := r.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("split patch read=%v close=%v", readErr, closeErr)
		}
		if !bytes.HasPrefix(plain, []byte("HDIFFSF20&\x00")) {
			t.Fatalf("official patch no longer contains the observed HDIFFSF20 header")
		}
		t.Logf("official split ZIP size=%d entries=%d sample=%s bytes=%d sha256=%x Range bytes=%d", total, len(zr.File), f.Name, len(plain), sha256.Sum256(plain), (4<<20)-remote.budget)
		return
	}
	t.Fatal("official split patch sample missing")
}

func TestOfficialPatchZIPPlainRange(t *testing.T) {
	if os.Getenv("PINION_PATCH_ZIP_INTEGRATION") != "1" {
		t.Skip("set PINION_PATCH_ZIP_INTEGRATION=1 for bounded official plain ZIP CRC reads")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m := New(t.TempDir())
	defer m.client.CloseIdleConnections()
	plan, err := m.latestPackage(ctx, Endfield)
	if err != nil {
		t.Fatal(err)
	}
	remote := &remoteArchive{ctx: ctx, manager: m, packs: plan.Packs, budget: 4 << 20}
	zr, err := zip.NewReader(remote, plan.DownloadBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Flags&1 != 0 || f.UncompressedSize64 == 0 || f.UncompressedSize64 > 4<<10 || f.CompressedSize64 > 64<<10 {
			continue
		}
		r, err := openPatchFile(f, "")
		if err != nil {
			t.Fatal(err)
		}
		n, readErr := io.Copy(io.Discard, r)
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || uint64(n) != f.UncompressedSize64 {
			t.Fatalf("plain official ZIP: bytes=%d read=%v close=%v", n, readErr, closeErr)
		}
		// Damage only the genuine central directory's parsed CRC, preserving
		// the original remote payload. Standard ZIP verification must fail.
		f.CRC32 ^= 1
		r, err = openPatchFile(f, "")
		if err != nil {
			t.Fatal(err)
		}
		_, readErr = io.Copy(io.Discard, r)
		closeErr = r.Close()
		if !errors.Is(readErr, zip.ErrChecksum) || !errors.Is(closeErr, zip.ErrChecksum) {
			t.Fatalf("damaged plain CRC: read=%v close=%v", readErr, closeErr)
		}
		t.Logf("official plain ZIP version=%s entry=%s bytes=%d valid CRC accepted and corrupted CRC rejected; Range bytes=%d", plan.Version, f.Name, n, (4<<20)-remote.budget)
		return
	}
	t.Fatal("no bounded plain official ZIP entry found")
}
