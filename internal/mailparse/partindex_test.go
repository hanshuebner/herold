package mailparse

import (
	"bytes"
	"encoding/base64"
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
	entries := BuildPartIndex(msg)

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
	entries := BuildPartIndex(msg)

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
	entries := BuildPartIndex(msg)
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
	entries := BuildPartIndex(msg)
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
	if PartIndexVersion != 1 {
		t.Fatalf("PartIndexVersion = %d; update this test and the migration/backfill story when bumping", PartIndexVersion)
	}
}
