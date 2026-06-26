package protojmap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// countingBlobs wraps a store.Blobs and tallies every byte read from the blob
// reader, so a test can assert that serving one part touches only that part's
// byte range rather than the whole message.
type countingBlobs struct {
	store.Blobs
	read *int64
}

func (c countingBlobs) Get(ctx context.Context, hash string) (store.BlobReader, error) {
	r, err := c.Blobs.Get(ctx, hash)
	if err != nil {
		return nil, err
	}
	return &countingReader{BlobReader: r, read: c.read}, nil
}

type countingReader struct {
	store.BlobReader
	read *int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.BlobReader.Read(p)
	atomic.AddInt64(c.read, int64(n))
	return n, err
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.BlobReader.ReadAt(p, off)
	atomic.AddInt64(c.read, int64(n))
	return n, err
}

// buildRelatedMessage assembles a multipart/related message with an HTML part
// and two base64 image parts of the given decoded sizes. Returns the raw bytes
// and the decoded payloads of the two image parts (DFS indices 3 and 4).
func buildRelatedMessage(img1, img2 int) (raw, payload1, payload2 []byte) {
	payload1 = make([]byte, img1)
	for i := range payload1 {
		payload1[i] = byte(i)
	}
	payload2 = make([]byte, img2)
	for i := range payload2 {
		payload2[i] = byte(i * 7)
	}
	var b bytes.Buffer
	b.WriteString("Content-Type: multipart/related; boundary=B\r\n\r\n")
	b.WriteString("--B\r\nContent-Type: text/html\r\n\r\n<html><body>hi</body></html>\r\n")
	b.WriteString("--B\r\nContent-Type: image/png\r\nContent-Transfer-Encoding: base64\r\nContent-ID: <img1>\r\n\r\n")
	b.WriteString(base64.StdEncoding.EncodeToString(payload1))
	b.WriteString("\r\n--B\r\nContent-Type: image/png\r\nContent-Transfer-Encoding: base64\r\nContent-ID: <img2>\r\n\r\n")
	b.WriteString(base64.StdEncoding.EncodeToString(payload2))
	b.WriteString("\r\n--B--\r\n")
	return b.Bytes(), payload1, payload2
}

func TestResolvePartBlobIndexedServesByRange(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(ctx, t.TempDir()+"/t.db", nil, clk)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	const img1, img2 = 512 * 1024, 512 * 1024
	raw, payload1, _ := buildRelatedMessage(img1, img2)
	ref, err := st.Blobs().Put(ctx, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	hash := ref.Hash

	// Persist the index exactly as the body-index worker would.
	parsed, perr := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions())
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	idxJSON, _ := json.Marshal(mailparse.BuildPartIndex(parsed))
	if err := st.Meta().PutBlobPartIndex(ctx, hash, mailparse.PartIndexVersion, idxJSON, clk.Now().UnixMicro()); err != nil {
		t.Fatalf("put index: %v", err)
	}

	// Part 3 is the first image. Serve it through the indexed path with a
	// byte-counting blob store.
	var read int64
	cb := countingBlobs{Blobs: st.Blobs(), read: &read}
	res, err := resolvePartBlob(ctx, st.Meta(), cb, hash, 3)
	if err != nil {
		t.Fatalf("resolvePartBlob (indexed): %v", err)
	}
	if !bytes.Equal(res.data, payload1) {
		t.Fatalf("indexed part 3 mismatch: got %d bytes, want %d", len(res.data), len(payload1))
	}

	// The indexed path must read only part 3's raw (base64) range, not the
	// whole message. Base64 of a 512 KiB payload is ~683 KiB; the full blob is
	// ~1.4 MiB (two such parts). Assert we read well under the whole blob.
	if read >= int64(len(raw)) {
		t.Fatalf("indexed serve read %d bytes; expected far less than the full blob %d", read, len(raw))
	}
	if read > int64(img1*4/3)+4096 {
		t.Fatalf("indexed serve read %d bytes; expected ~part-range (%d)", read, img1*4/3)
	}
	t.Logf("indexed serve of part 3 read %d of %d blob bytes", read, len(raw))

	// Correctness: the indexed result equals the legacy full-parse result.
	parseRes, err := resolvePartByParse(ctx, st.Blobs(), hash, 3)
	if err != nil {
		t.Fatalf("resolvePartByParse: %v", err)
	}
	if !bytes.Equal(res.data, parseRes.data) {
		t.Fatal("indexed vs parsed part bytes differ")
	}
}

func TestResolvePartBlobFallsBackWithoutIndex(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(ctx, t.TempDir()+"/t.db", nil, clk)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	raw, _, payload2 := buildRelatedMessage(64*1024, 96*1024)
	ref, err := st.Blobs().Put(ctx, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	// No PutBlobPartIndex: resolvePartBlob must fall back to a full parse and
	// still return the right bytes.
	res, err := resolvePartBlob(ctx, st.Meta(), st.Blobs(), ref.Hash, 4)
	if err != nil {
		t.Fatalf("resolvePartBlob (fallback): %v", err)
	}
	if !bytes.Equal(res.data, payload2) {
		t.Fatalf("fallback part 4 mismatch: got %d bytes, want %d", len(res.data), len(payload2))
	}
}

// TestResolvePartBlobTextPartNotCappedAt1MiB verifies that downloading a text
// part returns the full decoded body, not the 1 MiB-capped Part.Text. This is
// the server guarantee the reader relies on to render an oversized HTML body
// (issue #48): both the indexed path and the parse fallback must return it all.
func TestResolvePartBlobTextPartNotCappedAt1MiB(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(ctx, t.TempDir()+"/t.db", nil, clk)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// A text/html part well over the 1 MiB DefaultMaxTextPartBytes cap.
	const htmlLen = 1<<20 + 500_000 // ~1.5 MiB
	var body bytes.Buffer
	body.WriteString("Content-Type: multipart/alternative; boundary=A\r\n\r\n")
	body.WriteString("--A\r\nContent-Type: text/plain\r\n\r\nplain\r\n")
	body.WriteString("--A\r\nContent-Type: text/html; charset=utf-8\r\n\r\n")
	body.WriteString("<html><body>")
	body.Write(bytes.Repeat([]byte("x"), htmlLen))
	body.WriteString("</body></html>\r\n--A--\r\n")
	raw := body.Bytes()

	ref, err := st.Blobs().Put(ctx, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Part 3 is the text/html leaf (1=alternative, 2=text/plain, 3=text/html).
	// Without an index (parse fallback):
	res, err := resolvePartBlob(ctx, st.Meta(), st.Blobs(), ref.Hash, 3)
	if err != nil {
		t.Fatalf("resolvePartBlob (fallback): %v", err)
	}
	if len(res.data) <= 1<<20 {
		t.Fatalf("fallback returned %d bytes; expected the full part (>1 MiB), not the capped Text", len(res.data))
	}

	// And with the index persisted:
	parsed, _ := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions())
	idxJSON, _ := json.Marshal(mailparse.BuildPartIndex(parsed))
	if err := st.Meta().PutBlobPartIndex(ctx, ref.Hash, mailparse.PartIndexVersion, idxJSON, clk.Now().UnixMicro()); err != nil {
		t.Fatalf("put index: %v", err)
	}
	res2, err := resolvePartBlob(ctx, st.Meta(), st.Blobs(), ref.Hash, 3)
	if err != nil {
		t.Fatalf("resolvePartBlob (indexed): %v", err)
	}
	if len(res2.data) != len(res.data) {
		t.Fatalf("indexed (%d) and fallback (%d) text lengths differ", len(res2.data), len(res.data))
	}
}

// TestResolvePartBlobConcurrentFanoutBounded reproduces the inline-image fanout
// from issue #46: many concurrent part downloads of the same large message. With
// the index, total bytes read across the burst stays proportional to the parts
// actually served, not to fan-out times the whole blob.
func TestResolvePartBlobConcurrentFanoutBounded(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(ctx, t.TempDir()+"/t.db", nil, clk)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	raw, _, _ := buildRelatedMessage(512*1024, 512*1024)
	ref, err := st.Blobs().Put(ctx, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	parsed, _ := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions())
	idxJSON, _ := json.Marshal(mailparse.BuildPartIndex(parsed))
	if err := st.Meta().PutBlobPartIndex(ctx, ref.Hash, mailparse.PartIndexVersion, idxJSON, clk.Now().UnixMicro()); err != nil {
		t.Fatalf("put index: %v", err)
	}

	const fanout = 32
	var read int64
	cb := countingBlobs{Blobs: st.Blobs(), read: &read}
	var wg sync.WaitGroup
	errs := make([]error, fanout)
	for i := 0; i < fanout; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Alternate between the two image parts (DFS 3 and 4).
			part := 3 + (i % 2)
			r, e := resolvePartBlob(ctx, st.Meta(), cb, ref.Hash, part)
			if e != nil {
				errs[i] = e
				return
			}
			if len(r.data) != 512*1024 {
				errs[i] = fmt.Errorf("part %d: got %d bytes", part, len(r.data))
			}
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("fanout request %d: %v", i, e)
		}
	}
	// Each request reads ~one part's base64 range (~683 KiB). A whole-message
	// re-read per request (the OOM behaviour) would read fanout * len(raw).
	wholeMessagePerRequest := int64(fanout) * int64(len(raw))
	if read >= wholeMessagePerRequest {
		t.Fatalf("fanout read %d bytes; whole-message-per-request would be %d", read, wholeMessagePerRequest)
	}
	_ = io.Discard
	t.Logf("fanout x%d read %d bytes total (whole-message-per-request would be %d)", fanout, read, wholeMessagePerRequest)
}
