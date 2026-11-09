package download

import (
	"archive/zip"
	"bufio"
	"compress/flate"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
)

type patchAES struct {
	version uint16
	method  uint16
}

// patchEncryption accepts the official AES-256 format and its AE-1 variant.
// Layout and derivation: https://www.winzip.com/en/support/aes-encryption/
func patchEncryption(f *zip.File) (*patchAES, error) {
	var encryption *patchAES
	for extra := f.Extra; len(extra) > 0; {
		if len(extra) < 4 {
			return nil, errors.New("truncated ZIP extra field")
		}
		tag, size := binary.LittleEndian.Uint16(extra), int(binary.LittleEndian.Uint16(extra[2:]))
		extra = extra[4:]
		if size > len(extra) {
			return nil, errors.New("truncated ZIP extra field data")
		}
		data := extra[:size]
		extra = extra[size:]
		if tag != 0x9901 {
			continue
		}
		if encryption != nil || len(data) < 7 {
			return nil, errors.New("invalid ZIP AES extra field")
		}
		version := binary.LittleEndian.Uint16(data)
		if (version != 1 && version != 2) || string(data[2:4]) != "AE" || data[4] != 3 {
			return nil, errors.New("unsupported ZIP AES encryption")
		}
		encryption = &patchAES{version: version, method: binary.LittleEndian.Uint16(data[5:])}
	}
	if f.Flags&1 == 0 {
		if f.Method == 99 || encryption != nil {
			return nil, errors.New("ZIP AES encryption flag missing")
		}
		return nil, nil
	}
	if f.Method != 99 || encryption == nil || f.Flags&0x2040 != 0 {
		return nil, errors.New("unsupported ZIP encryption")
	}
	if encryption.version == 2 && f.CRC32 != 0 {
		return nil, errors.New("AE-2 ZIP entry has a nonzero CRC")
	}
	if f.CompressedSize64 < 28 {
		return nil, errors.New("truncated ZIP AES payload")
	}
	return encryption, nil
}

func validatePatchFile(f *zip.File) error {
	if f.UncompressedSize64 > 1<<40 || f.CompressedSize64 > 1<<40 {
		return errors.New("ZIP patch entry exceeds size limit")
	}
	encryption, err := patchEncryption(f)
	if err != nil {
		return err
	}
	method := f.Method
	if encryption != nil {
		method = encryption.method
	}
	if method != zip.Store && method != zip.Deflate {
		return fmt.Errorf("unsupported ZIP patch compression %d", method)
	}
	return nil
}

// openPatchFile streams into a caller-owned temporary file. The caller must
// reach EOF and successfully Close before committing that file. Close rejects
// an incomplete read instead of continuing a possibly cancelled extraction.
func openPatchFile(f *zip.File, password string) (io.ReadCloser, error) {
	if err := validatePatchFile(f); err != nil {
		return nil, err
	}
	encryption, err := patchEncryption(f)
	if err != nil {
		return nil, err
	}
	if encryption == nil {
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		return &patchFileReader{source: r, remaining: f.UncompressedSize64}, nil
	}
	raw, err := f.OpenRaw()
	if err != nil {
		return nil, err
	}
	var header [18]byte
	if _, err := io.ReadFull(raw, header[:]); err != nil {
		return nil, fmt.Errorf("ZIP AES salt: %w", err)
	}
	key, err := pbkdf2.Key(sha1.New, password, header[:16], 1000, 66)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(key[64:], header[16:]) {
		return nil, errors.New("ZIP AES password verification failed")
	}
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha1.New, key[32:64])
	ciphertext := &io.LimitedReader{R: raw, N: int64(f.CompressedSize64) - 28}
	decrypted := bufio.NewReader(&cipher.StreamReader{
		S: &patchCTR{block: block, used: aes.BlockSize},
		R: io.TeeReader(ciphertext, mac),
	})
	var source io.ReadCloser = io.NopCloser(decrypted)
	if encryption.method == zip.Deflate {
		source = flate.NewReader(decrypted)
	}
	r := &patchFileReader{source: source, remaining: f.UncompressedSize64}
	if encryption.version == 1 {
		r.crc = crc32.NewIEEE()
	}
	r.finish = func() error {
		// A flate stream must consume its entire declared compressed payload.
		if _, err := decrypted.ReadByte(); err != io.EOF {
			if err == nil {
				return errors.New("ZIP AES payload has trailing compressed data")
			}
			return err
		}
		if ciphertext.N != 0 {
			return io.ErrUnexpectedEOF
		}
		var authentication [10]byte
		if _, err := io.ReadFull(raw, authentication[:]); err != nil {
			return fmt.Errorf("ZIP AES authentication: %w", err)
		}
		if !hmac.Equal(mac.Sum(nil)[:10], authentication[:]) {
			return fmt.Errorf("ZIP AES authentication: %w", ErrChecksum)
		}
		if r.crc != nil && r.crc.Sum32() != f.CRC32 {
			return zip.ErrChecksum
		}
		return nil
	}
	return r, nil
}

type patchFileReader struct {
	source    io.ReadCloser
	remaining uint64
	crc       hash.Hash32
	finish    func() error
	err       error
	closed    bool
}

func (r *patchFileReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, errors.New("ZIP patch reader closed")
	}
	if r.err != nil {
		return 0, r.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	// Never expose bytes beyond the declared uncompressed size, including
	// when a forged header understates the output of a deflate stream.
	var probe [1]byte
	if r.remaining == 0 {
		p = probe[:]
	} else if uint64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.source.Read(p)
	if uint64(n) > r.remaining {
		r.err = errors.New("ZIP patch exceeds declared uncompressed size")
		return 0, r.err
	}
	r.remaining -= uint64(n)
	if r.crc != nil {
		r.crc.Write(p[:n])
	}
	if err == io.EOF {
		if r.remaining != 0 {
			err = io.ErrUnexpectedEOF
		} else if r.finish != nil {
			err = r.finish()
			if err == nil {
				err = io.EOF
			}
		}
	}
	if err != nil {
		r.err = err
	}
	return n, err
}

func (r *patchFileReader) Close() error {
	if !r.closed && r.err == nil && r.remaining == 0 {
		var probe [1]byte
		r.Read(probe[:])
	}
	if !r.closed {
		r.closed = true
		closeErr := r.source.Close()
		if r.err == nil {
			r.err = errors.New("ZIP patch entry not fully read")
		}
		if closeErr != nil {
			if r.err == io.EOF {
				r.err = closeErr
			} else {
				r.err = errors.Join(r.err, closeErr)
			}
		}
	}
	if r.err == io.EOF {
		return nil
	}
	return r.err
}

// WinZip CTR starts at 1 and increments the counter in little-endian order;
// crypto/cipher.NewCTR uses the opposite byte order.
type patchCTR struct {
	block   cipher.Block
	counter [aes.BlockSize]byte
	stream  [aes.BlockSize]byte
	used    int
}

func (s *patchCTR) XORKeyStream(dst, src []byte) {
	for len(src) > 0 {
		if s.used == aes.BlockSize {
			for i := range s.counter {
				s.counter[i]++
				if s.counter[i] != 0 {
					break
				}
			}
			s.block.Encrypt(s.stream[:], s.counter[:])
			s.used = 0
		}
		n := min(len(src), aes.BlockSize-s.used)
		for i := range n {
			dst[i] = src[i] ^ s.stream[s.used+i]
		}
		s.used += n
		dst, src = dst[n:], src[n:]
	}
}
