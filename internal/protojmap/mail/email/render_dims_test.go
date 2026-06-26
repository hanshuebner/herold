package email_test

// render_dims_test.go — integration tests for intrinsic image dimensions on
// EmailBodyPart (re #47). These tests exercise the full Email/get path: real
// PNG and JPEG images with known pixel dimensions are base64-encoded into a
// multipart/related message, stored in the blob store, a part index is
// persisted via PutBlobPartIndex, and then Email/get is invoked. The tests
// assert that width/height are returned on image body parts when the index is
// present and absent (graceful degradation) when it is not.

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
	"strings"
	"testing"

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

// TestEmailGet_InlineImageDimensions_NoIndex verifies graceful degradation:
// when no part index is stored for the blob, Email/get omits width/height
// entirely from image body parts so the client falls back to its previous
// behaviour (allocating space based on the image after it loads).
func TestEmailGet_InlineImageDimensions_NoIndex(t *testing.T) {
	f := setupFixture(t)

	pngData := buildPNGImageBytes(t, 100, 60)
	jpegData := buildJPEGImageBytes(t, 50, 30)
	raw := buildInlineImagesMIME(pngData, jpegData)

	// Insert without persisting a part index.
	m := f.insertMessage(t, string(raw), "no index", "a@example.test", "b@example.test", nil, "")

	_, rawResp := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{fmt.Sprintf("%d", m.ID)},
		"properties": []string{"attachments"},
	})

	var resp struct {
		List []struct {
			Attachments []map[string]json.RawMessage `json:"attachments"`
		} `json:"list"`
	}
	if err := json.Unmarshal(rawResp, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, rawResp)
	}
	if len(resp.List) != 1 {
		t.Fatalf("want 1 message, got %d: %s", len(resp.List), rawResp)
	}
	for _, att := range resp.List[0].Attachments {
		if _, ok := att["width"]; ok {
			t.Errorf("attachment has 'width' key but no index was stored: %v", att)
		}
		if _, ok := att["height"]; ok {
			t.Errorf("attachment has 'height' key but no index was stored: %v", att)
		}
	}
}
