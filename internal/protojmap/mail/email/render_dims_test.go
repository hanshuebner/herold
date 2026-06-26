package email_test

// render_dims_test.go — integration tests for intrinsic image dimensions on
// EmailBodyPart (re #47). These tests exercise the full Email/get path: real
// PNG and JPEG images with known pixel dimensions are base64-encoded into a
// multipart/related message, stored in the blob store, and then Email/get is
// invoked. The tests assert that width/height are returned on image body parts
// both when a pre-stored part index exists and when it does not (the inline-
// compute path that eliminates the race window between message delivery and the
// bodymeta worker's first sweep).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/protojmap"
)

// buildPNGImageBytes encodes a w×h solid-color image as PNG.
func buildPNGImageBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill := color.RGBA{R: 0x42, G: 0x84, B: 0xC6, A: 0xFF}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// buildJPEGImageBytes encodes a w×h solid-color image as JPEG.
func buildJPEGImageBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill := color.RGBA{R: 0x80, G: 0x40, B: 0x20, A: 0xFF}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

// buildInlineImagesMIME assembles a multipart/related message containing an
// HTML part plus two inline image parts (one PNG, one JPEG) base64-encoded.
// The DFS part index will be: 1=multipart/related, 2=text/html, 3=image/png,
// 4=image/jpeg — matching the PartIndexEntry.Index values that BuildPartIndex
// assigns.
func buildInlineImagesMIME(pngBytes, jpegBytes []byte) []byte {
	const bnd = "TESTBND"
	var b strings.Builder
	b.WriteString("Content-Type: multipart/related; boundary=\"" + bnd + "\"\r\n\r\n")

	b.WriteString("--" + bnd + "\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
	b.WriteString("<html><body><img src=\"cid:img1@t\"/><img src=\"cid:img2@t\"/></body></html>\r\n")

	b.WriteString("--" + bnd + "\r\n")
	b.WriteString("Content-Type: image/png\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-ID: <img1@t>\r\n")
	b.WriteString("Content-Disposition: inline\r\n\r\n")
	b.WriteString(base64.StdEncoding.EncodeToString(pngBytes))
	b.WriteString("\r\n")

	b.WriteString("--" + bnd + "\r\n")
	b.WriteString("Content-Type: image/jpeg\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-ID: <img2@t>\r\n")
	b.WriteString("Content-Disposition: inline\r\n\r\n")
	b.WriteString(base64.StdEncoding.EncodeToString(jpegBytes))
	b.WriteString("\r\n")

	b.WriteString("--" + bnd + "--\r\n")
	return []byte(b.String())
}

// TestEmailGet_InlineImageDimensions_WithIndex verifies that when a persisted
// part index (version == mailparse.PartIndexVersion) is present, Email/get
// returns intrinsic width/height on image body parts so the web client can
// reserve layout space before the images load.
func TestEmailGet_InlineImageDimensions_WithIndex(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()

	const (
		pngW, pngH   = 120, 80
		jpegW, jpegH = 200, 150
	)

	pngData := buildPNGImageBytes(t, pngW, pngH)
	jpegData := buildJPEGImageBytes(t, jpegW, jpegH)
	raw := buildInlineImagesMIME(pngData, jpegData)

	// Parse and build the part index from the same bytes the store will hold
	// so DFS indices and byte offsets are consistent.
	parsed, err := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}
	entries := mailparse.BuildPartIndex(parsed, bytes.NewReader(raw))
	idxJSON, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("json.Marshal(entries): %v", err)
	}

	// Verify the index carries the expected dimensions before inserting.
	var checkDims struct{ PNG, JPEG *mailparse.PartIndexEntry }
	for i := range entries {
		switch entries[i].ContentType {
		case "image/png":
			e := entries[i]
			checkDims.PNG = &e
		case "image/jpeg":
			e := entries[i]
			checkDims.JPEG = &e
		}
	}
	if checkDims.PNG == nil || checkDims.PNG.Width != pngW || checkDims.PNG.Height != pngH {
		t.Fatalf("BuildPartIndex PNG dims: got %v, want %dx%d", checkDims.PNG, pngW, pngH)
	}
	if checkDims.JPEG == nil || checkDims.JPEG.Width != jpegW || checkDims.JPEG.Height != jpegH {
		t.Fatalf("BuildPartIndex JPEG dims: got %v, want %dx%d", checkDims.JPEG, jpegW, jpegH)
	}

	// Insert the message (which stores the blob), then persist the index.
	m := f.insertMessage(t, string(raw), "inline imgs", "a@example.test", "b@example.test", nil, "")
	if err := f.srv.Store.Meta().PutBlobPartIndex(
		ctx, m.Blob.Hash, mailparse.PartIndexVersion, idxJSON,
		f.srv.Clock.Now().UnixMicro(),
	); err != nil {
		t.Fatalf("PutBlobPartIndex: %v", err)
	}

	// Email/get requesting attachments: image parts land there because their
	// content type is not text/plain or text/html.
	_, rawResp := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{fmt.Sprintf("%d", m.ID)},
		"properties": []string{"attachments"},
	})

	var resp struct {
		List []struct {
			Attachments []struct {
				Type   string `json:"type"`
				Width  *int   `json:"width"`
				Height *int   `json:"height"`
			} `json:"attachments"`
		} `json:"list"`
	}
	if err := json.Unmarshal(rawResp, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, rawResp)
	}
	if len(resp.List) != 1 {
		t.Fatalf("want 1 message, got %d: %s", len(resp.List), rawResp)
	}
	atts := resp.List[0].Attachments
	if len(atts) != 2 {
		t.Fatalf("want 2 image attachments, got %d: %s", len(atts), rawResp)
	}

	// Locate parts by content type.
	var pngPart, jpegPart *struct {
		Type   string `json:"type"`
		Width  *int   `json:"width"`
		Height *int   `json:"height"`
	}
	for i := range atts {
		switch atts[i].Type {
		case "image/png":
			pngPart = &atts[i]
		case "image/jpeg":
			jpegPart = &atts[i]
		}
	}
	if pngPart == nil {
		t.Fatal("no image/png attachment in response")
	}
	if jpegPart == nil {
		t.Fatal("no image/jpeg attachment in response")
	}

	if pngPart.Width == nil || *pngPart.Width != pngW {
		t.Errorf("png Width = %v, want %d", pngPart.Width, pngW)
	}
	if pngPart.Height == nil || *pngPart.Height != pngH {
		t.Errorf("png Height = %v, want %d", pngPart.Height, pngH)
	}
	if jpegPart.Width == nil || *jpegPart.Width != jpegW {
		t.Errorf("jpeg Width = %v, want %d", jpegPart.Width, jpegW)
	}
	if jpegPart.Height == nil || *jpegPart.Height != jpegH {
		t.Errorf("jpeg Height = %v, want %d", jpegPart.Height, jpegH)
	}
}

// TestEmailGet_InlineImageDimensions_NoIndex verifies that when no part index
// is stored for the blob, Email/get still returns correct width/height by
// computing image dimensions inline from the already-parsed body (re #47).
// This eliminates the race window between message delivery and the bodymeta
// worker's first sweep, so clients can inject aspect-ratio reservations on
// first paint regardless of when the worker runs.
//
// The test also verifies that the inline computation triggers a background
// write of the part index to the DB, so subsequent Email/get calls hit the
// fast DB path instead of recomputing.
func TestEmailGet_InlineImageDimensions_NoIndex(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()

	const (
		pngW, pngH   = 100, 60
		jpegW, jpegH = 50, 30
	)

	pngData := buildPNGImageBytes(t, pngW, pngH)
	jpegData := buildJPEGImageBytes(t, jpegW, jpegH)
	raw := buildInlineImagesMIME(pngData, jpegData)

	// Insert without persisting a part index — simulates the window between
	// message delivery and the bodymeta worker's first sweep.
	m := f.insertMessage(t, string(raw), "no index", "a@example.test", "b@example.test", nil, "")

	_, rawResp := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{fmt.Sprintf("%d", m.ID)},
		"properties": []string{"attachments"},
	})

	var resp struct {
		List []struct {
			Attachments []struct {
				Type   string `json:"type"`
				Width  *int   `json:"width"`
				Height *int   `json:"height"`
			} `json:"attachments"`
		} `json:"list"`
	}
	if err := json.Unmarshal(rawResp, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, rawResp)
	}
	if len(resp.List) != 1 {
		t.Fatalf("want 1 message, got %d: %s", len(resp.List), rawResp)
	}
	atts := resp.List[0].Attachments
	if len(atts) != 2 {
		t.Fatalf("want 2 image attachments, got %d: %s", len(atts), rawResp)
	}

	// Dims must be returned via the inline-compute path even with no pre-stored
	// index — this is the fix for the first-paint truncation (re #47).
	var pngPart, jpegPart *struct {
		Type   string `json:"type"`
		Width  *int   `json:"width"`
		Height *int   `json:"height"`
	}
	for i := range atts {
		switch atts[i].Type {
		case "image/png":
			pngPart = &atts[i]
		case "image/jpeg":
			jpegPart = &atts[i]
		}
	}
	if pngPart == nil {
		t.Fatal("no image/png attachment in response")
	}
	if jpegPart == nil {
		t.Fatal("no image/jpeg attachment in response")
	}
	if pngPart.Width == nil || *pngPart.Width != pngW {
		t.Errorf("png Width = %v, want %d", pngPart.Width, pngW)
	}
	if pngPart.Height == nil || *pngPart.Height != pngH {
		t.Errorf("png Height = %v, want %d", pngPart.Height, pngH)
	}
	if jpegPart.Width == nil || *jpegPart.Width != jpegW {
		t.Errorf("jpeg Width = %v, want %d", jpegPart.Width, jpegW)
	}
	if jpegPart.Height == nil || *jpegPart.Height != jpegH {
		t.Errorf("jpeg Height = %v, want %d", jpegPart.Height, jpegH)
	}

	// The inline computation must also trigger a background write of the correct
	// part index so future calls hit the DB path. Poll with a short timeout.
	deadline := time.Now().Add(2 * time.Second)
	var dbVersion int
	for time.Now().Before(deadline) {
		ver, _, err := f.srv.Store.Meta().GetBlobPartIndex(ctx, m.Blob.Hash)
		if err == nil && ver >= mailparse.PartIndexVersion {
			dbVersion = ver
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if dbVersion < mailparse.PartIndexVersion {
		t.Errorf("background write: part index not written to DB within 2s (got version %d, want >= %d)", dbVersion, mailparse.PartIndexVersion)
	}

	// Verify the DB-written index also carries correct dims (not just
	// the placeholder empty index written on parse failure).
	ver, partsJSON, err := f.srv.Store.Meta().GetBlobPartIndex(ctx, m.Blob.Hash)
	if err != nil {
		t.Fatalf("GetBlobPartIndex after background write: %v", err)
	}
	if ver != mailparse.PartIndexVersion {
		t.Fatalf("DB index version = %d, want %d", ver, mailparse.PartIndexVersion)
	}
	var dbEntries []mailparse.PartIndexEntry
	if err := json.Unmarshal(partsJSON, &dbEntries); err != nil {
		t.Fatalf("unmarshal DB entries: %v", err)
	}
	var dbPNG, dbJPEG *mailparse.PartIndexEntry
	for i := range dbEntries {
		switch dbEntries[i].ContentType {
		case "image/png":
			e := dbEntries[i]
			dbPNG = &e
		case "image/jpeg":
			e := dbEntries[i]
			dbJPEG = &e
		}
	}
	if dbPNG == nil || dbPNG.Width != pngW || dbPNG.Height != pngH {
		t.Errorf("DB PNG dims: got %v, want %dx%d", dbPNG, pngW, pngH)
	}
	if dbJPEG == nil || dbJPEG.Width != jpegW || dbJPEG.Height != jpegH {
		t.Errorf("DB JPEG dims: got %v, want %dx%d", dbJPEG, jpegW, jpegH)
	}
}

// TestEmailGet_InlineImageDimensions_BackgroundWriteCorrectOffsets verifies
// that the part index written by writePartIndexBackground carries byte-range
// offsets valid for the stored blob (not for the injected rawBody). It checks
// that store.GetBlobPartIndex after the background write returns entries whose
// RawOffset values can be used to open each part's body.
func TestEmailGet_InlineImageDimensions_BackgroundWriteCorrectOffsets(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()

	const (
		pngW, pngH = 80, 40
	)
	pngData := buildPNGImageBytes(t, pngW, pngH)
	const bnd = "BNDOFF"
	var b strings.Builder
	b.WriteString("Content-Type: multipart/related; boundary=\"" + bnd + "\"\r\n\r\n")
	b.WriteString("--" + bnd + "\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
	b.WriteString("<img src=\"cid:img@t\"/>\r\n")
	b.WriteString("--" + bnd + "\r\n")
	b.WriteString("Content-Type: image/png\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-ID: <img@t>\r\n")
	b.WriteString("Content-Disposition: inline\r\n\r\n")
	b.WriteString(base64.StdEncoding.EncodeToString(pngData))
	b.WriteString("\r\n--" + bnd + "--\r\n")
	raw := []byte(b.String())

	// Insert without pre-stored index.
	m := f.insertMessage(t, string(raw), "offsets", "a@t", "b@t", nil, "")

	// Trigger Email/get to kick off the background write.
	f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{fmt.Sprintf("%d", m.ID)},
		"properties": []string{"attachments"},
	})

	// Wait for background write.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ver, _, err := f.srv.Store.Meta().GetBlobPartIndex(ctx, m.Blob.Hash)
		if err == nil && ver >= mailparse.PartIndexVersion {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify the DB index entries can open the PNG body from the stored blob.
	_, partsJSON, err := f.srv.Store.Meta().GetBlobPartIndex(ctx, m.Blob.Hash)
	if err != nil {
		t.Fatalf("GetBlobPartIndex: %v", err)
	}
	var entries []mailparse.PartIndexEntry
	if err := json.Unmarshal(partsJSON, &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var pngEntry *mailparse.PartIndexEntry
	for i := range entries {
		if entries[i].ContentType == "image/png" {
			e := entries[i]
			pngEntry = &e
			break
		}
	}
	if pngEntry == nil {
		t.Fatal("no image/png entry in DB index")
	}

	// Fetch the blob from the store and verify the PNG entry's OpenBody reads
	// the correct bytes (decodable as a PNG with the expected dimensions).
	rc, err := f.srv.Store.Blobs().Get(ctx, m.Blob.Hash)
	if err != nil {
		t.Fatalf("blobs.Get: %v", err)
	}
	blobBytes, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}

	body, err := pngEntry.OpenBody(bytes.NewReader(blobBytes))
	if err != nil {
		t.Fatalf("OpenBody: %v", err)
	}
	defer body.Close()
	cfg, _, err := image.DecodeConfig(body)
	if err != nil {
		t.Fatalf("image.DecodeConfig from stored blob offsets: %v", err)
	}
	if cfg.Width != pngW || cfg.Height != pngH {
		t.Errorf("decoded dims from stored blob = %dx%d, want %dx%d", cfg.Width, cfg.Height, pngW, pngH)
	}
}
