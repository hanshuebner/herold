package email_test

// preview_binary_leaf_store_test.go -- store-level acceptance test for
// issue #325: a text/plain-labelled leaf whose decoded content is actually
// binary must never surface as the message's persisted preview, on either
// store backend.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
)

// pngLeafRawMessage builds a multipart/related message shaped like issue
// #325's store message 3400: a multipart/alternative whose genuine
// text/plain part decodes to plainAlt (empty, or whitespace-only) alongside
// a text/html part, plus a sibling leaf carrying a base64-encoded PNG
// signature that is mislabelled text/plain (mirroring the #324 extimg
// defect, which invalidates the Content-Type of an inline image so
// mailparse defaults it to "text/plain; charset=us-ascii" per RFC 2045).
// The PNG-labelled leaf precedes the alternative so that, pre-fix, it is
// the first text/plain candidate the preview computation would select.
func pngLeafRawMessage(plainAlt string) string {
	return strings.Join([]string{
		"From: Carol <carol@example.test>",
		"To: Dave <dave@example.test>",
		"Subject: Reduce tus costos de embalaje desde hoy",
		"Message-ID: <png-leaf-325@example.test>",
		"MIME-Version: 1.0",
		"Content-Type: multipart/related; boundary=\"rel\"",
		"",
		"--rel",
		"Content-Type: text/plain; charset=us-ascii",
		"Content-Transfer-Encoding: base64",
		"Content-ID: <img1>",
		"Content-Disposition: inline",
		"",
		"iVBORw0KGgoAAAANSUhEUg==", // base64 of the PNG signature + start of an IHDR chunk
		"--rel",
		"Content-Type: multipart/alternative; boundary=\"alt\"",
		"",
		"--alt",
		"Content-Type: text/plain; charset=utf-8",
		"",
		plainAlt,
		"--alt",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>Reduce tus costos de embalaje desde hoy</p>",
		"--alt--",
		"--rel--",
	}, "\r\n")
}

// TestEmail_StoredPreview_SkipsMislabelledBinaryTextPlain verifies that the
// PERSISTED preview column for a message shaped like #325's report never
// holds the raw bytes of the mislabelled text/plain-PNG leaf: a message is
// inserted directly via the store (BodyMetaComputed starts false, exactly
// like a freshly delivered message), Email/get is invoked to trigger the
// opportunistic body-meta persist (internal/protojmap/mail/email/get.go),
// and the STORED row -- not just the one JSON response -- is asserted to
// carry the clean HTML-extracted preview.
func TestEmail_StoredPreview_SkipsMislabelledBinaryTextPlain(t *testing.T) {
	testStoredPreviewSkipsMislabelledBinaryTextPlain(t, setupFixture(t), "")
}

// TestEmail_StoredPreview_SkipsMislabelledBinaryTextPlain_WhitespaceOnlyAlt
// is the same fixture with a whitespace-only (rather than zero-byte)
// genuine text/plain alternative, covering the issue's "empty or
// whitespace-only" wording.
func TestEmail_StoredPreview_SkipsMislabelledBinaryTextPlain_WhitespaceOnlyAlt(t *testing.T) {
	testStoredPreviewSkipsMislabelledBinaryTextPlain(t, setupFixture(t), "   \t  ")
}

// TestEmail_StoredPreview_SkipsMislabelledBinaryTextPlain_Postgres is the
// Postgres leg of TestEmail_StoredPreview_SkipsMislabelledBinaryTextPlain.
// Skips when HEROLD_PG_DSN is unset.
func TestEmail_StoredPreview_SkipsMislabelledBinaryTextPlain_Postgres(t *testing.T) {
	testStoredPreviewSkipsMislabelledBinaryTextPlain(t, setupFixturePostgres(t), "")
}

// testStoredPreviewSkipsMislabelledBinaryTextPlain is the backend-agnostic
// test body shared by the SQLite and Postgres variants above.
func testStoredPreviewSkipsMislabelledBinaryTextPlain(t *testing.T, f *fixture, plainAlt string) {
	rawBody := pngLeafRawMessage(plainAlt)
	want := "Reduce tus costos de embalaje desde hoy"

	m := f.insertMessage(t, rawBody, "" /* subject */, "" /* from */, "dave@example.test", nil, "")
	if m.BodyMetaComputed {
		t.Fatalf("test setup: expected BodyMetaComputed=false on a freshly inserted row")
	}

	emailID := fmt.Sprintf("%d", m.ID)
	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{emailID},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("got %d entries, want 1: %s", len(getResp.List), raw)
	}
	gotPreview, _ := getResp.List[0]["preview"].(string)
	if gotPreview != want {
		t.Errorf("Email/get response preview = %q, want %q", gotPreview, want)
	}
	if strings.Contains(gotPreview, "\x89PNG") {
		t.Errorf("Email/get response preview leaked the PNG signature: %q (bytes % x)", gotPreview, []byte(gotPreview))
	}

	// The critical assertion: the STORED row must carry the clean preview
	// too, so every later list-view read serves it straight from the store
	// without re-parsing the blob.
	stored, err := f.srv.Store.Meta().GetMessage(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("GetMessage after persist: %v", err)
	}
	if !stored.BodyMetaComputed {
		t.Fatalf("stored BodyMetaComputed = false after Email/get; want true")
	}
	if stored.Preview != want {
		t.Errorf("stored Preview = %q, want %q", stored.Preview, want)
	}
	if strings.Contains(stored.Preview, "\x89PNG") {
		t.Errorf("stored Preview leaked the PNG signature: %q (bytes % x)", stored.Preview, []byte(stored.Preview))
	}
}
