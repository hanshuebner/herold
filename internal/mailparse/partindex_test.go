package mailparse

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"testing"
)

// relatedSample builds a multipart/related message: an HTML part and a base64
// image part carrying a Content-ID, plus a quoted-printable text alternative
// nested in a multipart/alternative. The structure exercises containers, cid
// extraction, multiple transfer encodings, and nested branches.
func relatedSample(t *testing.T) (raw []byte, imgPayload []byte) {
	t.Helper()
	imgPayload = make([]byte, 4096)
	for i := range imgPayload {
		imgPayload[i] = byte(i % 251)
	}
	var b bytes.Buffer
	b.WriteString("Content-Type: multipart/related; boundary=R\r\n\r\n")
	b.WriteString("--R\r\nContent-Type: multipart/alternative; boundary=A\r\n\r\n")
	b.WriteString("--A\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nhello plain\r\n")
	b.WriteString("--A\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>hi <img src=\"cid:img1\"></p>\r\n")
	b.WriteString("--A--\r\n")
	b.WriteString("--R\r\nContent-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-ID: <img1>\r\nContent-Disposition: inline; filename=\"pic.png\"\r\n\r\n")
	b.WriteString(base64.StdEncoding.EncodeToString(imgPayload))
	b.WriteString("\r\n--R--\r\n")
	return b.Bytes(), imgPayload
}

func TestBuildPartIndexEnumerationMatchesTree(t *testing.T) {
	raw, _ := relatedSample(t)
	msg, err := Parse(bytes.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	entries := BuildPartIndex(msg, bytes.NewReader(raw))

	// Collect the live parts in the same 1-based pre-order DFS.
	var parts []Part
	var collect func(p Part)
	collect = func(p Part) {
		parts = append(parts, p)
		for _, c := range p.Children {
			collect(c)
		}
	}
	collect(msg.Body)

	if len(entries) != len(parts) {
		t.Fatalf("entry/part count mismatch: %d vs %d", len(entries), len(parts))
	}
	for i, e := range entries {
		if e.Index != i+1 {
			t.Errorf("entry %d has Index %d, want %d (enumeration must be 1-based DFS)", i, e.Index, i+1)
		}
		if got, want := e.Container, len(parts[i].Children) > 0; got != want {
			t.Errorf("entry %d container=%v, want %v", e.Index, got, want)
		}
		if got, want := e.ContentType, parts[i].ContentType; got != want {
			t.Errorf("entry %d content-type=%q, want %q", e.Index, got, want)
		}
	}
}

func TestBuildPartIndexStructureAndMetadata(t *testing.T) {
	raw, imgPayload := relatedSample(t)
	msg, err := Parse(bytes.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	entries := BuildPartIndex(msg, bytes.NewReader(raw))

	// Expected pre-order DFS:
	//  1 multipart/related        (container, parent 0)
	//  2 multipart/alternative    (container, parent 1)
	//  3 text/plain               (leaf,      parent 2)
	//  4 text/html                (leaf,      parent 2)
	//  5 image/png                (leaf,      parent 1)
	if len(entries) != 5 {
		t.Fatalf("expected 5 parts, got %d", len(entries))
	}
	want := []struct {
		ct        string
		parent    int
		container bool
	}{
		{"multipart/related", 0, true},
		{"multipart/alternative", 1, true},
		{"text/plain", 2, false},
		{"text/html", 2, false},
		{"image/png", 1, false},
	}
	for i, w := range want {
		e := entries[i]
		if e.ContentType != w.ct || e.Parent != w.parent || e.Container != w.container {
			t.Errorf("part %d = {ct:%q parent:%d container:%v}, want {ct:%q parent:%d container:%v}",
				e.Index, e.ContentType, e.Parent, e.Container, w.ct, w.parent, w.container)
		}
	}

	img := entries[4]
	if img.CID != "img1" {
		t.Errorf("image CID = %q, want %q (angle brackets must be stripped)", img.CID, "img1")
	}
	if img.Disposition != "inline" {
		t.Errorf("image disposition = %q, want inline", img.Disposition)
	}
	if img.Filename != "pic.png" {
		t.Errorf("image filename = %q, want pic.png", img.Filename)
	}
	if img.CTE != "base64" {
		t.Errorf("image CTE = %q, want base64", img.CTE)
	}
	if img.DecodedSize != int64(len(imgPayload)) {
		t.Errorf("image decoded size = %d, want %d", img.DecodedSize, len(imgPayload))
	}
}

func TestPartIndexEntryOpenBodyMatchesPartOpenBody(t *testing.T) {
	raw, imgPayload := relatedSample(t)
	msg, err := Parse(bytes.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	entries := BuildPartIndex(msg, bytes.NewReader(raw))
	img := entries[4]

	// Decode via the persisted entry; it must equal the original payload.
	rc, err := img.OpenBody(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("entry.OpenBody: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, imgPayload) {
		t.Fatalf("entry.OpenBody decoded %d bytes, want %d", len(got), len(imgPayload))
	}

	// RawBody returns the encoded (base64) bytes, larger than the decoded form.
	rb, err := img.RawBody(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("entry.RawBody: %v", err)
	}
	rawBytes, err := io.ReadAll(rb)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if len(rawBytes) <= len(imgPayload) {
		t.Fatalf("raw body %d bytes should exceed decoded %d (base64 expansion)", len(rawBytes), len(imgPayload))
	}
}

func TestPartIndexEntryGuards(t *testing.T) {
	raw, _ := relatedSample(t)
	msg, err := Parse(bytes.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	entries := BuildPartIndex(msg, bytes.NewReader(raw))
	container := entries[0] // multipart/related
	leaf := entries[4]      // image/png

	if _, err := container.OpenBody(bytes.NewReader(raw)); err == nil {
		t.Error("OpenBody on a container must error")
	}
	if _, err := container.RawBody(bytes.NewReader(raw)); err == nil {
		t.Error("RawBody on a container must error")
	}
	if _, err := leaf.OpenBody(nil); err == nil {
		t.Error("OpenBody with nil src must error")
	}
	if _, err := leaf.RawBody(nil); err == nil {
		t.Error("RawBody with nil src must error")
	}
}

func TestNormalizeCID(t *testing.T) {
	cases := map[string]string{
		"<a@b>":     "a@b",
		"a@b":       "a@b",
		"  <x@y> ":  "x@y",
		"":          "",
		"<>":        "",
		"<only-id>": "only-id",
	}
	for in, want := range cases {
		if got := normalizeCID(in); got != want {
			t.Errorf("normalizeCID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPartIndexVersionPinned(t *testing.T) {
	// A change in serialized shape must come with a version bump so the worker
	// rebuilds and consumers reject stale indexes. Pin the current value.
	if PartIndexVersion != 2 {
		t.Fatalf("PartIndexVersion = %d; update this test and the migration/backfill story when bumping", PartIndexVersion)
	}
}

// encodePNG returns the bytes of a PNG image with the given dimensions, filled
// with a solid colour.
func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 149, B: 237, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// encodeJPEG returns the bytes of a JPEG image with the given dimensions.
func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 200, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

// buildImageMessage assembles a multipart/related message containing a
// text/plain body, a PNG attachment with the given pixel dimensions, and a JPEG
// attachment with the given pixel dimensions. Returns the raw bytes.
func buildImageMessage(t *testing.T, pngW, pngH, jpegW, jpegH int) []byte {
	t.Helper()
	pngBytes := encodePNG(t, pngW, pngH)
	jpegBytes := encodeJPEG(t, jpegW, jpegH)

	var b bytes.Buffer
	b.WriteString("Content-Type: multipart/related; boundary=IMG\r\n\r\n")
	b.WriteString("--IMG\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nhello\r\n")
	b.WriteString("--IMG\r\nContent-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: inline; filename=\"shot.png\"\r\n\r\n")
	b.WriteString(base64.StdEncoding.EncodeToString(pngBytes))
	b.WriteString("\r\n--IMG\r\nContent-Type: image/jpeg\r\nContent-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: inline; filename=\"shot.jpg\"\r\n\r\n")
	b.WriteString(base64.StdEncoding.EncodeToString(jpegBytes))
	b.WriteString("\r\n--IMG--\r\n")
	return b.Bytes()
}

// TestBuildPartIndexImageDimensions verifies that Width and Height are populated
// for PNG and JPEG leaves and are 0 for text and container parts.
func TestBuildPartIndexImageDimensions(t *testing.T) {
	const pngW, pngH = 7, 13
	const jpegW, jpegH = 20, 5

	raw := buildImageMessage(t, pngW, pngH, jpegW, jpegH)
	msg, err := Parse(bytes.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	entries := BuildPartIndex(msg, bytes.NewReader(raw))

	// Expected DFS order:
	//  1 multipart/related  (container)
	//  2 text/plain         (leaf, no dimensions)
	//  3 image/png          (leaf, pngW x pngH)
	//  4 image/jpeg         (leaf, jpegW x jpegH)
	if len(entries) != 4 {
		t.Fatalf("expected 4 parts, got %d", len(entries))
	}

	container := entries[0]
	if container.Width != 0 || container.Height != 0 {
		t.Errorf("container part has non-zero dimensions: %dx%d", container.Width, container.Height)
	}

	textPart := entries[1]
	if textPart.Width != 0 || textPart.Height != 0 {
		t.Errorf("text/plain part has non-zero dimensions: %dx%d", textPart.Width, textPart.Height)
	}

	pngPart := entries[2]
	if pngPart.Width != pngW || pngPart.Height != pngH {
		t.Errorf("image/png: got %dx%d, want %dx%d", pngPart.Width, pngPart.Height, pngW, pngH)
	}

	jpegPart := entries[3]
	if jpegPart.Width != jpegW || jpegPart.Height != jpegH {
		t.Errorf("image/jpeg: got %dx%d, want %dx%d", jpegPart.Width, jpegPart.Height, jpegW, jpegH)
	}
}

// TestBuildPartIndexImageDimensionsGarbage verifies that a leaf with
// Content-Type image/png but a body that is not a valid PNG produces Width=0
// and Height=0 with no error, and that the rest of the index is unaffected.
func TestBuildPartIndexImageDimensionsGarbage(t *testing.T) {
	// Build a message where one part claims to be image/png but its body is
	// garbage binary data.
	garbage := make([]byte, 512)
	for i := range garbage {
		garbage[i] = byte(i)
	}
	var b bytes.Buffer
	b.WriteString("Content-Type: multipart/mixed; boundary=G\r\n\r\n")
	b.WriteString("--G\r\nContent-Type: text/plain\r\n\r\nhello\r\n")
	b.WriteString("--G\r\nContent-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(base64.StdEncoding.EncodeToString(garbage))
	b.WriteString("\r\n--G--\r\n")
	raw := b.Bytes()

	msg, err := Parse(bytes.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	entries := BuildPartIndex(msg, bytes.NewReader(raw))

	if len(entries) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(entries))
	}
	garbagePart := entries[2]
	if garbagePart.ContentType != "image/png" {
		t.Fatalf("expected image/png, got %q", garbagePart.ContentType)
	}
	if garbagePart.Width != 0 || garbagePart.Height != 0 {
		t.Errorf("garbage image/png has non-zero dimensions: %dx%d", garbagePart.Width, garbagePart.Height)
	}
	// The text part before the garbage image must still be correctly indexed.
	textPart := entries[1]
	if textPart.ContentType != "text/plain" {
		t.Errorf("expected text/plain at index 2, got %q", textPart.ContentType)
	}
}

// TestBuildPartIndexNilSrc verifies that BuildPartIndex with a nil src returns
// a valid index with Width=Height=0 for all parts (including image leaves).
func TestBuildPartIndexNilSrc(t *testing.T) {
	const pngW, pngH = 4, 4
	raw := buildImageMessage(t, pngW, pngH, 4, 4)
	msg, err := Parse(bytes.NewReader(raw), NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	entries := BuildPartIndex(msg, nil)
	for _, e := range entries {
		if e.Width != 0 || e.Height != 0 {
			t.Errorf("part %d (%s): expected 0x0 with nil src, got %dx%d", e.Index, e.ContentType, e.Width, e.Height)
		}
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 parts, got %d", len(entries))
	}
}
