package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bugFetchFixtureMeta is the report.json the phone reporter attaches; it
// mirrors the browser panel's public meta (see cmd_bugsink_test.go).
const bugFetchFixtureMeta = `{
  "protocol": "webapp-diagnostics/1",
  "createdAt": "2026-09-16T09:00:00.000Z",
  "kind": "bug",
  "sketch": "Thread list jumps after sync\nSecond line.",
  "app": {"id": "herold-android", "name": "Herold Android", "version": "0.4.1+ab12cd"},
  "principal": {"id": "p1", "label": "alice@example.local"},
  "context": {"route": "thread/t42", "account": "a1"},
  "logs": [{"ts": 1758013200000, "level": "warn", "msg": "sync: outbox retry", "ctx": "sync"}],
  "screenshotCount": 1
}`

const bugFetchFixturePrivate = `{"included": true, "session": {"token": "PHONE-SECRET-TOKEN-DO-NOT-LEAK"}}`

var bugFetchFixturePNG = []byte("\x89PNG\r\n\x1a\nfake-png-bytes")

// zipBundle builds a zip in memory from name -> content.
func zipBundle(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestExpandBugMail_ZipBundle(t *testing.T) {
	archive := zipBundle(t, map[string][]byte{
		"bundle/report.json":          []byte(bugFetchFixtureMeta),
		"bundle/report.md":            []byte("# Bug: Thread list jumps after sync\n"),
		"bundle/logs.txt":             []byte("2026-09-16T09:00:00Z [sync] WARN: outbox retry\n"),
		"bundle/screenshot-1.png":     bugFetchFixturePNG,
		"bundle/private/private.json": []byte(bugFetchFixturePrivate),
		"bundle/private/state.json":   []byte(`{"draft": "x"}`),
		"../../etc/passwd":            []byte("root:x:0:0"),
		"bundle/notes.docx":           []byte("binary"),
	})
	mail := bugMail{
		ID:         "M1",
		Subject:    "herold bug: Thread list jumps after sync",
		ReceivedAt: "2026-09-16T09:00:05Z",
		TextBody:   "# Bug: Thread list jumps after sync\n",
		Attachments: []bugMailPart{
			{Name: "bug-report.zip", Type: "application/zip", Data: archive},
		},
	}

	drop := expandBugMail(mail)

	want := map[string]string{
		"report.json":          bugFetchFixtureMeta,
		"report.md":            "# Bug: Thread list jumps after sync\n",
		"logs.txt":             "2026-09-16T09:00:00Z [sync] WARN: outbox retry\n",
		"screenshot-1.png":     string(bugFetchFixturePNG),
		"private/private.json": bugFetchFixturePrivate,
		"private/state.json":   `{"draft": "x"}`,
	}
	if len(drop.Files) != len(want) {
		t.Fatalf("files = %v, want exactly %d entries", keysOf(drop.Files), len(want))
	}
	for p, content := range want {
		if got := string(drop.Files[p]); got != content {
			t.Errorf("%s = %q, want %q", p, got, content)
		}
	}
	for _, p := range keysOf(drop.Files) {
		if strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
			t.Errorf("unsafe drop path %q", p)
		}
	}
	if len(drop.Notes) != 2 {
		t.Fatalf("notes = %v, want one per ignored entry (passwd, docx)", drop.Notes)
	}
}

func TestExpandBugMail_SeparateParts(t *testing.T) {
	mail := bugMail{
		ID:         "M2",
		Subject:    "herold bug: Compose loses draft",
		ReceivedAt: "2026-09-16T09:10:00Z",
		TextBody:   "# Bug: Compose loses draft\n\nbody text\n",
		Attachments: []bugMailPart{
			{Name: "report.json", Type: "application/json", Data: []byte(bugFetchFixtureMeta)},
			{Name: "report.md", Type: "text/markdown", Data: []byte("# Bug: Compose loses draft\n")},
			{Name: "logs.txt", Type: "text/plain", Data: []byte("line\n")},
			{Name: "screenshot-1.png", Type: "image/png", Data: bugFetchFixturePNG},
			{Name: "capture-after.png", Type: "image/png", Data: []byte("second-png")},
			{Name: "private.json", Type: "application/json", Data: []byte(bugFetchFixturePrivate)},
			{Name: "trace.har", Type: "application/octet-stream", Data: []byte("ignored")},
		},
	}

	drop := expandBugMail(mail)

	want := map[string]string{
		"report.json":          bugFetchFixtureMeta,
		"report.md":            "# Bug: Compose loses draft\n",
		"logs.txt":             "line\n",
		"screenshot-1.png":     string(bugFetchFixturePNG),
		"screenshot-2.png":     "second-png",
		"private/private.json": bugFetchFixturePrivate,
	}
	if len(drop.Files) != len(want) {
		t.Fatalf("files = %v, want exactly %d entries", keysOf(drop.Files), len(want))
	}
	for p, content := range want {
		if got := string(drop.Files[p]); got != content {
			t.Errorf("%s = %q, want %q", p, got, content)
		}
	}
	if len(drop.Notes) != 1 || !strings.Contains(drop.Notes[0], "trace.har") {
		t.Fatalf("notes = %v, want one for trace.har", drop.Notes)
	}
}

func TestExpandBugMail_SynthesisesMissingFiles(t *testing.T) {
	mail := bugMail{
		ID:         "M3",
		Subject:    "herold bug: Crash on open",
		ReceivedAt: "2026-09-16T09:20:00Z",
		TextBody:   "It crashed when I opened the thread.",
		Attachments: []bugMailPart{
			{Name: "Screenshot_20260916.png", Type: "image/png", Data: bugFetchFixturePNG},
		},
	}

	drop := expandBugMail(mail)

	if got := string(drop.Files["report.md"]); got != "It crashed when I opened the thread.\n" {
		t.Errorf("report.md = %q", got)
	}
	if got, ok := drop.Files["logs.txt"]; !ok || len(got) != 0 {
		t.Errorf("logs.txt = %q, want present and empty", got)
	}
	if got := string(drop.Files["screenshot-1.png"]); got != string(bugFetchFixturePNG) {
		t.Errorf("screenshot-1.png = %q", got)
	}
	var meta struct {
		Kind            string `json:"kind"`
		Sketch          string `json:"sketch"`
		CreatedAt       string `json:"createdAt"`
		ScreenshotCount int    `json:"screenshotCount"`
	}
	if err := json.Unmarshal(drop.Files["report.json"], &meta); err != nil {
		t.Fatalf("report.json: %v: %s", err, drop.Files["report.json"])
	}
	if meta.Kind != "bug" || meta.CreatedAt != "2026-09-16T09:20:00Z" || meta.ScreenshotCount != 1 {
		t.Errorf("synthesised meta = %+v", meta)
	}
	if !strings.HasPrefix(meta.Sketch, "Crash on open\n\nIt crashed") {
		t.Errorf("sketch = %q", meta.Sketch)
	}
	if len(drop.Notes) != 1 || !strings.Contains(drop.Notes[0], "synthesised") {
		t.Errorf("notes = %v", drop.Notes)
	}
}

func TestBugDropID(t *testing.T) {
	cases := map[string]string{
		"M8f3a":         "mail-M8f3a",
		"a/b..c":        "mail-a_b..c",
		"..":            "mail-mail",
		"x y\x00z":      "mail-x_y_z",
		"id-1_2.3":      "mail-id-1_2.3",
		"":              "mail-mail",
		"\u00e9t\u00e9": "mail-_t_",
	}
	for in, want := range cases {
		if got := bugDropID(in); got != want {
			t.Errorf("bugDropID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteBugDrop_LayoutAndIdempotence(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "mail-M1")
	drop := bugDrop{Files: map[string][]byte{
		"report.json":          []byte("{}"),
		"report.md":            []byte("# x\n"),
		"logs.txt":             {},
		"screenshot-1.png":     bugFetchFixturePNG,
		"private/private.json": []byte(bugFetchFixturePrivate),
	}}

	written, err := writeBugDrop(dir, drop)
	if err != nil || !written {
		t.Fatalf("writeBugDrop: written=%v err=%v", written, err)
	}
	for _, p := range []string{"report.json", "report.md", "logs.txt", "screenshot-1.png", "private/private.json", "STATUS"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "private")); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("private/ mode = %v err=%v, want 0700", st.Mode(), err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "STATUS")); string(got) != "new" {
		t.Errorf("STATUS = %q", got)
	}

	// A second write onto an existing drop leaves it alone.
	if err := os.WriteFile(filepath.Join(dir, "STATUS"), []byte("filed:#1"), 0o600); err != nil {
		t.Fatal(err)
	}
	written, err = writeBugDrop(dir, drop)
	if err != nil || written {
		t.Fatalf("second writeBugDrop: written=%v err=%v, want false/nil", written, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "STATUS")); string(got) != "filed:#1" {
		t.Errorf("STATUS after re-run = %q, want untouched", got)
	}
}

// buildBugReportMail assembles an RFC 5322 multipart/mixed message with a
// plain-text body and the given attachments (Content-Disposition:
// attachment; base64 transfer encoding).
func buildBugReportMail(t *testing.T, subject, body string, attachments []bugMailPart) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	buf.WriteString("From: alice@example.local\r\n")
	buf.WriteString("To: alice@example.local\r\n")
	buf.WriteString("Subject: " + subject + "\r\n")
	buf.WriteString("Date: Wed, 16 Sep 2026 09:00:00 +0000\r\n")
	buf.WriteString("Message-ID: <" + strings.ReplaceAll(subject, " ", "-") + "@phone.example>\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: multipart/mixed; boundary=" + mw.Boundary() + "\r\n\r\n")

	textHdr := textproto.MIMEHeader{}
	textHdr.Set("Content-Type", "text/plain; charset=utf-8")
	tw, err := mw.CreatePart(textHdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	for _, a := range attachments {
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Type", a.Type+`; name="`+a.Name+`"`)
		hdr.Set("Content-Disposition", `attachment; filename="`+a.Name+`"`)
		hdr.Set("Content-Transfer-Encoding", "base64")
		pw, err := mw.CreatePart(hdr)
		if err != nil {
			t.Fatal(err)
		}
		enc := base64.StdEncoding.EncodeToString(a.Data)
		for len(enc) > 76 {
			pw.Write([]byte(enc[:76] + "\r\n"))
			enc = enc[76:]
		}
		pw.Write([]byte(enc + "\r\n"))
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// jmapImportMail uploads raw and imports it into mailboxID, unread.
func jmapImportMail(t *testing.T, publicAddr, apiKey, accountID, mailboxID string, raw []byte) string {
	t.Helper()
	blobID := jmapUploadBlob(t, publicAddr, apiKey, accountID, raw)
	resp := jmapCall(t, publicAddr, apiKey, "Email/import", map[string]any{
		"accountId": accountID,
		"emails": map[string]any{
			"i1": map[string]any{
				"blobId":     blobID,
				"mailboxIds": map[string]bool{mailboxID: true},
				"keywords":   map[string]bool{},
			},
		},
	})
	var out struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
		NotCreated map[string]json.RawMessage `json:"notCreated"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("decode Email/import: %v body=%s", err, resp)
	}
	id := out.Created["i1"].ID
	if id == "" {
		t.Fatalf("Email/import did not create: %s", resp)
	}
	return id
}

func jmapEmailKeywords(t *testing.T, publicAddr, apiKey, accountID, emailID string) map[string]bool {
	t.Helper()
	resp := jmapCall(t, publicAddr, apiKey, "Email/get", map[string]any{
		"accountId":  accountID,
		"ids":        []string{emailID},
		"properties": []string{"keywords"},
	})
	var out struct {
		List []struct {
			Keywords map[string]bool `json:"keywords"`
		} `json:"list"`
	}
	if err := json.Unmarshal(resp, &out); err != nil || len(out.List) != 1 {
		t.Fatalf("Email/get keywords: err=%v body=%s", err, resp)
	}
	return out.List[0].Keywords
}

// TestBugFetch_EndToEnd drives `herold bug-fetch` against the real server:
// a "Bug reports" mailbox holding one zipped bundle and one separate-parts
// bundle, both unread. A dry run lists them and changes nothing; the real
// run writes both drops and marks both read; a further run finds nothing.
func TestBugFetch_EndToEnd(t *testing.T) {
	publicAddr, apiKey, accountID := jmapBootstrapFixture(t)

	// Create the label the phone reporter files under.
	created := jmapCall(t, publicAddr, apiKey, "Mailbox/set", map[string]any{
		"accountId": accountID,
		"create":    map[string]any{"m1": map[string]any{"name": "Bug reports"}},
	})
	var mbOut struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	if err := json.Unmarshal(created, &mbOut); err != nil || mbOut.Created["m1"].ID == "" {
		t.Fatalf("Mailbox/set create: err=%v body=%s", err, created)
	}
	mailboxID := mbOut.Created["m1"].ID

	archive := zipBundle(t, map[string][]byte{
		"report.json":          []byte(bugFetchFixtureMeta),
		"report.md":            []byte("# Bug: zipped\n"),
		"logs.txt":             []byte("zipped log\n"),
		"screenshot-1.png":     bugFetchFixturePNG,
		"private/private.json": []byte(bugFetchFixturePrivate),
	})
	zippedID := jmapImportMail(t, publicAddr, apiKey, accountID, mailboxID,
		buildBugReportMail(t, "herold bug: zipped", "# Bug: zipped\n", []bugMailPart{
			{Name: "bug-report.zip", Type: "application/zip", Data: archive},
		}))
	partsID := jmapImportMail(t, publicAddr, apiKey, accountID, mailboxID,
		buildBugReportMail(t, "herold bug: parts", "# Bug: parts\n", []bugMailPart{
			{Name: "report.json", Type: "application/json", Data: []byte(bugFetchFixtureMeta)},
			{Name: "report.md", Type: "text/markdown", Data: []byte("# Bug: parts\n")},
			{Name: "logs.txt", Type: "text/plain", Data: []byte("parts log\n")},
			{Name: "screenshot-1.png", Type: "image/png", Data: bugFetchFixturePNG},
			{Name: "private.json", Type: "application/json", Data: []byte(bugFetchFixturePrivate)},
		}))

	out := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		root := NewRootCmd()
		stdout := &bytes.Buffer{}
		root.SetOut(stdout)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{
			"bug-fetch",
			"--server-url", "http://" + publicAddr,
			"--api-key", apiKey,
			"--out", out,
		}, args...))
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := root.ExecuteContext(ctx); err != nil {
			t.Fatalf("bug-fetch %v: %v\n%s", args, err, stdout.String())
		}
		return stdout.String()
	}

	// Dry run: lists both, writes nothing, marks nothing.
	dry := run("--dry-run")
	if !strings.Contains(dry, `"herold bug: zipped"`) || !strings.Contains(dry, `"herold bug: parts"`) {
		t.Fatalf("dry run did not list both messages:\n%s", dry)
	}
	if !strings.Contains(dry, "bug-report.zip") || !strings.Contains(dry, "screenshot-1.png") {
		t.Fatalf("dry run did not list attachments:\n%s", dry)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("dry run wrote %d entries into --out", len(entries))
	}
	for _, id := range []string{zippedID, partsID} {
		if kw := jmapEmailKeywords(t, publicAddr, apiKey, accountID, id); kw["$seen"] {
			t.Fatalf("dry run marked %s read", id)
		}
	}

	// Real run: both drops on disk, both messages read.
	got := run()
	for _, id := range []string{zippedID, partsID} {
		dir := filepath.Join(out, bugDropID(id))
		if !strings.Contains(got, "bug-fetch: wrote "+dir) {
			t.Errorf("output does not report %s:\n%s", dir, got)
		}
		for _, p := range []string{"report.json", "report.md", "logs.txt", "screenshot-1.png", "private/private.json", "STATUS"} {
			if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
				t.Errorf("%s: missing %s: %v", id, p, err)
			}
		}
		if meta, _ := os.ReadFile(filepath.Join(dir, "report.json")); string(meta) != bugFetchFixtureMeta {
			t.Errorf("%s: report.json not verbatim:\n%s", id, meta)
		}
		if png, _ := os.ReadFile(filepath.Join(dir, "screenshot-1.png")); !bytes.Equal(png, bugFetchFixturePNG) {
			t.Errorf("%s: screenshot-1.png content mismatch", id)
		}
		if md, _ := os.ReadFile(filepath.Join(dir, "report.md")); strings.Contains(string(md), "PHONE-SECRET") {
			t.Errorf("%s: report.md leaked private content", id)
		}
		if priv, _ := os.ReadFile(filepath.Join(dir, "private", "private.json")); string(priv) != bugFetchFixturePrivate {
			t.Errorf("%s: private/private.json not verbatim: %s", id, priv)
		}
		if kw := jmapEmailKeywords(t, publicAddr, apiKey, accountID, id); !kw["$seen"] {
			t.Errorf("%s: not marked read after fetch: %v", id, kw)
		}
	}
	if md, _ := os.ReadFile(filepath.Join(out, bugDropID(zippedID), "logs.txt")); string(md) != "zipped log\n" {
		t.Errorf("zipped logs.txt = %q", md)
	}
	if md, _ := os.ReadFile(filepath.Join(out, bugDropID(partsID), "logs.txt")); string(md) != "parts log\n" {
		t.Errorf("parts logs.txt = %q", md)
	}

	// Nothing left unread: the next run is a no-op.
	again := run()
	if !strings.Contains(again, `no unread messages in "Bug reports"`) {
		t.Fatalf("second run should find nothing:\n%s", again)
	}

	// An absent label is "nothing to fetch", not an error.
	none := run("--label", "No such label")
	if !strings.Contains(none, `no mailbox named "No such label"`) {
		t.Fatalf("absent label:\n%s", none)
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
