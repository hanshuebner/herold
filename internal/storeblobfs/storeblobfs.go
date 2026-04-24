package storeblobfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/zeebo/blake3"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// Store is a content-addressed blob store on the local filesystem. Blobs
// are keyed by lowercase-hex BLAKE3 of their canonical bytes. The layout
// uses a 2-level hex fan-out: blob abcd...ef is stored at
// <dir>/ab/cd/abcd...ef. Writes go through <dir>/tmp/ and atomically
// rename into place; second Put of identical content is a no-op.
//
// Refcounting is the Metadata layer's concern; this package treats every
// present blob as refcount 1 and returns 0 for absent blobs. GC exposes
// a callback so the caller (Metadata) can decide what is still live.
type Store struct {
	dir    string
	clock  clock.Clock
	tmpDir string
	mu     sync.Mutex // guards tmp-file name collisions; not on disk access
}

// New returns a Store rooted at dir. The directory and its tmp subdir
// are created lazily on first write; New itself does not touch disk.
// clock is used only for naming (not currently, but kept for parity with
// sibling backends that need deterministic time in tests).
func New(dir string, c clock.Clock) *Store {
	if c == nil {
		c = clock.NewReal()
	}
	return &Store{
		dir:    dir,
		clock:  c,
		tmpDir: filepath.Join(dir, "tmp"),
	}
}

// Put streams r to a new blob, computing its BLAKE3 hash in flight and
// moving the completed file to its content-addressed location with an
// atomic rename. Canonicalization (CRLF normalization required by
// REQ-STORE-10) is the caller's responsibility: Put hashes exactly the
// bytes delivered by r. If the destination already exists, the temp file
// is removed and the existing BlobRef is returned (dedup).
func (s *Store) Put(ctx context.Context, r io.Reader) (store.BlobRef, error) {
	if err := ctx.Err(); err != nil {
		return store.BlobRef{}, err
	}
	if err := s.ensureDirs(); err != nil {
		return store.BlobRef{}, err
	}
	tmpPath, err := s.newTempFile()
	if err != nil {
		return store.BlobRef{}, fmt.Errorf("storeblobfs: create temp: %w", err)
	}
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return store.BlobRef{}, fmt.Errorf("storeblobfs: open temp: %w", err)
	}
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	h := blake3.New()
	mw := io.MultiWriter(f, h)
	n, copyErr := copyWithCtx(ctx, mw, r)
	if copyErr != nil {
		_ = f.Close()
		return store.BlobRef{}, fmt.Errorf("storeblobfs: write: %w", copyErr)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return store.BlobRef{}, fmt.Errorf("storeblobfs: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return store.BlobRef{}, fmt.Errorf("storeblobfs: close: %w", err)
	}

	hash := hex.EncodeToString(h.Sum(nil))
	finalPath := s.pathFor(hash)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return store.BlobRef{}, fmt.Errorf("storeblobfs: mkdir fanout: %w", err)
	}
	// If the target exists already, the content is identical by hash and
	// we keep the existing file (dedup). Remove the temp and return.
	if fi, statErr := os.Stat(finalPath); statErr == nil {
		return store.BlobRef{Hash: hash, Size: fi.Size()}, nil
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// On some filesystems a concurrent Put of identical content may
		// have beaten us to the punch; treat EEXIST as dedup success.
		if errors.Is(err, os.ErrExist) {
			if fi, statErr := os.Stat(finalPath); statErr == nil {
				return store.BlobRef{Hash: hash, Size: fi.Size()}, nil
			}
		}
		return store.BlobRef{}, fmt.Errorf("storeblobfs: rename: %w", err)
	}
	cleanupTemp = false
	return store.BlobRef{Hash: hash, Size: n}, nil
}

// Get opens the blob at hash for streaming read. The caller must Close
// the returned reader. Returns store.ErrNotFound if the file is absent.
func (s *Store) Get(ctx context.Context, hash string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validHash(hash) {
		return nil, store.ErrNotFound
	}
	f, err := os.Open(s.pathFor(hash))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("storeblobfs: open: %w", err)
	}
	return f, nil
}

// Stat returns the blob's size and a synthetic refs value: 1 when the
// file is present, 0 when absent (and an ErrNotFound in the error slot
// in that case). Refcounting proper lives in Metadata.
func (s *Store) Stat(ctx context.Context, hash string) (int64, int, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	if !validHash(hash) {
		return 0, 0, store.ErrNotFound
	}
	fi, err := os.Stat(s.pathFor(hash))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, store.ErrNotFound
		}
		return 0, 0, fmt.Errorf("storeblobfs: stat: %w", err)
	}
	return fi.Size(), 1, nil
}

// Delete removes the blob file. Returns ErrNotFound when the file is
// already absent. Refcounting is Metadata's concern; this layer does
// not inspect refs.
func (s *Store) Delete(ctx context.Context, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validHash(hash) {
		return store.ErrNotFound
	}
	err := os.Remove(s.pathFor(hash))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store.ErrNotFound
		}
		return fmt.Errorf("storeblobfs: remove: %w", err)
	}
	return nil
}

// GC walks the blob tree and deletes files whose hash the referenced
// callback reports as unreferenced. Returns the number of deleted blobs
// and the number of bytes reclaimed. The tmp/ directory is skipped.
// referenced may be called concurrently with new Put requests; callers
// MUST be tolerant of TOCTOU: the grace window (REQ-STORE-12) lives in
// the calling Metadata layer, not here.
func (s *Store) GC(ctx context.Context, referenced func(hash string) bool) (int, int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	if referenced == nil {
		return 0, 0, errors.New("storeblobfs: GC requires a referenced callback")
	}
	var removed int
	var bytes int64
	// Walk dir/*/*/<file>. Skip tmp/ and anything that does not match the
	// fan-out layout to avoid removing operator-placed files.
	topEntries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("storeblobfs: readdir root: %w", err)
	}
	for _, top := range topEntries {
		if err := ctx.Err(); err != nil {
			return removed, bytes, err
		}
		if !top.IsDir() || top.Name() == "tmp" || len(top.Name()) != 2 {
			continue
		}
		midPath := filepath.Join(s.dir, top.Name())
		midEntries, err := os.ReadDir(midPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return removed, bytes, fmt.Errorf("storeblobfs: readdir mid: %w", err)
		}
		for _, mid := range midEntries {
			if err := ctx.Err(); err != nil {
				return removed, bytes, err
			}
			if !mid.IsDir() || len(mid.Name()) != 2 {
				continue
			}
			leafPath := filepath.Join(midPath, mid.Name())
			leafEntries, err := os.ReadDir(leafPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return removed, bytes, fmt.Errorf("storeblobfs: readdir leaf: %w", err)
			}
			for _, f := range leafEntries {
				if err := ctx.Err(); err != nil {
					return removed, bytes, err
				}
				if f.IsDir() {
					continue
				}
				name := f.Name()
				if !validHash(name) {
					continue
				}
				// Quick prefix sanity: the two top + two mid nibbles must
				// match the hash prefix.
				if name[0:2] != top.Name() || name[2:4] != mid.Name() {
					continue
				}
				if referenced(name) {
					continue
				}
				info, statErr := f.Info()
				if statErr != nil {
					if errors.Is(statErr, os.ErrNotExist) {
						continue
					}
					return removed, bytes, fmt.Errorf("storeblobfs: stat during GC: %w", statErr)
				}
				if err := os.Remove(filepath.Join(leafPath, name)); err != nil {
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					return removed, bytes, fmt.Errorf("storeblobfs: remove during GC: %w", err)
				}
				removed++
				bytes += info.Size()
			}
		}
	}
	return removed, bytes, nil
}

func (s *Store) ensureDirs() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("storeblobfs: mkdir root: %w", err)
	}
	if err := os.MkdirAll(s.tmpDir, 0o700); err != nil {
		return fmt.Errorf("storeblobfs: mkdir tmp: %w", err)
	}
	return nil
}

func (s *Store) newTempFile() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return filepath.Join(s.tmpDir, hex.EncodeToString(raw[:])), nil
}

func (s *Store) pathFor(hash string) string {
	return filepath.Join(s.dir, hash[0:2], hash[2:4], hash)
}

// validHash returns true for lowercase-hex strings of BLAKE3's 32-byte
// output length (64 hex chars). Guards against traversal and malformed
// input before any path operations.
func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// copyWithCtx copies r into w until EOF or ctx cancellation. It behaves
// like io.Copy but polls ctx at each chunk boundary so a caller can
// abort a slow reader.
func copyWithCtx(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	buf := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, rerr := r.Read(buf)
		if n > 0 {
			wn, werr := w.Write(buf[:n])
			total += int64(wn)
			if werr != nil {
				return total, werr
			}
			if wn != n {
				return total, io.ErrShortWrite
			}
		}
		if rerr == io.EOF {
			return total, nil
		}
		if rerr != nil {
			return total, rerr
		}
	}
}
