package storeblobfs_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storeblobfs"
)

func newStore(t *testing.T) *storeblobfs.Store {
	t.Helper()
	return storeblobfs.New(t.TempDir(), clock.NewFake(time.Unix(0, 0).UTC()))
}

func TestPutGetRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	body := []byte("From: a\r\nTo: b\r\n\r\nhello")
	ref, err := s.Put(ctx, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Size != int64(len(body)) {
		t.Fatalf("ref.Size = %d, want %d", ref.Size, len(body))
	}
	if len(ref.Hash) != 64 {
		t.Fatalf("ref.Hash len = %d, want 64", len(ref.Hash))
	}
	r, err := s.Get(ctx, ref.Hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body mismatch: got %q, want %q", got, body)
	}
}

func TestPutDedup(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	body := []byte("identical content")
	r1, err := s.Put(ctx, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put #1: %v", err)
	}
	r2, err := s.Put(ctx, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put #2: %v", err)
	}
	if r1 != r2 {
		t.Fatalf("Put is not dedup: %+v != %+v", r1, r2)
	}
}

func TestFanOutStructure(t *testing.T) {
	dir := t.TempDir()
	s := storeblobfs.New(dir, clock.NewFake(time.Unix(0, 0).UTC()))
	ctx := context.Background()
	ref, err := s.Put(ctx, strings.NewReader("x"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	want := filepath.Join(dir, ref.Hash[0:2], ref.Hash[2:4], ref.Hash)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("fan-out path missing: %v", err)
	}
}

func TestGetNotFound(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.Get(ctx, strings.Repeat("a", 64))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(absent) = %v, want ErrNotFound", err)
	}
}

func TestGetInvalidHashIsNotFound(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, h := range []string{"", "short", strings.Repeat("Z", 64), "../etc/passwd"} {
		if _, err := s.Get(ctx, h); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Get(%q) = %v, want ErrNotFound", h, err)
		}
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ref, err := s.Put(ctx, strings.NewReader("byebye"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(ctx, ref.Hash); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, ref.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("post-Delete Get: %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, ref.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second Delete: %v, want ErrNotFound", err)
	}
}

func TestStat(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ref, err := s.Put(ctx, strings.NewReader("abcde"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	size, refs, err := s.Stat(ctx, ref.Hash)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if size != 5 || refs != 1 {
		t.Fatalf("Stat = (size=%d, refs=%d), want (5, 1)", size, refs)
	}
	if _, _, err := s.Stat(ctx, strings.Repeat("0", 64)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Stat(absent) = %v, want ErrNotFound", err)
	}
}

func TestConcurrentPutAtomicity(t *testing.T) {
	s := newStore(t)
	const workers = 16
	body := []byte("racing writers produce the same hash")
	var wg sync.WaitGroup
	refs := make([]store.BlobRef, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			refs[i], errs[i] = s.Put(context.Background(), bytes.NewReader(body))
		}(i)
	}
	wg.Wait()
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if refs[i] != refs[0] {
			t.Fatalf("worker %d ref %v != %v", i, refs[i], refs[0])
		}
	}
}

func TestGC(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	keep, err := s.Put(ctx, strings.NewReader("keep"))
	if err != nil {
		t.Fatalf("Put keep: %v", err)
	}
	drop, err := s.Put(ctx, strings.NewReader("drop"))
	if err != nil {
		t.Fatalf("Put drop: %v", err)
	}
	removed, bytes, err := s.GC(ctx, func(h string) bool { return h == keep.Hash })
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed != 1 || bytes != 4 {
		t.Fatalf("GC = (removed=%d, bytes=%d), want (1, 4)", removed, bytes)
	}
	if _, err := s.Get(ctx, keep.Hash); err != nil {
		t.Fatalf("keep missing after GC: %v", err)
	}
	if _, err := s.Get(ctx, drop.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("drop present after GC: %v", err)
	}
}

func TestGCRejectsNilReferenced(t *testing.T) {
	s := newStore(t)
	if _, _, err := s.GC(context.Background(), nil); err == nil {
		t.Fatalf("GC(nil) returned no error")
	}
}

func TestGCOnEmptyDir(t *testing.T) {
	s := newStore(t)
	removed, bytes, err := s.GC(context.Background(), func(string) bool { return true })
	if err != nil {
		t.Fatalf("GC empty: %v", err)
	}
	if removed != 0 || bytes != 0 {
		t.Fatalf("GC empty = (removed=%d, bytes=%d), want (0, 0)", removed, bytes)
	}
}

func TestPutCancellation(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, strings.NewReader("cancelled")); err == nil {
		t.Fatalf("Put with cancelled ctx returned nil error")
	}
}
