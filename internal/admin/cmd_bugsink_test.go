package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildBugReportBody constructs a multipart/form-data body matching the
// bug-reporter extension's panel.js submit(): a "meta" part (report.json),
// a "private" part (private.json), and one or more "screenshot" parts.
func buildBugReportBody(t *testing.T, meta, private []byte, screenshots [][]byte) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)

	metaPart, err := w.CreateFormFile("meta", "report.json")
	if err != nil {
		t.Fatalf("create meta part: %v", err)
	}
	if _, err := metaPart.Write(meta); err != nil {
		t.Fatalf("write meta part: %v", err)
	}

	privPart, err := w.CreateFormFile("private", "private.json")
	if err != nil {
		t.Fatalf("create private part: %v", err)
	}
	if _, err := privPart.Write(private); err != nil {
		t.Fatalf("write private part: %v", err)
	}

	for i, data := range screenshots {
		fname := "screenshot-" + string(rune('1'+i)) + ".png"
		shotPart, err := w.CreateFormFile("screenshot", fname)
		if err != nil {
			t.Fatalf("create screenshot part %d: %v", i, err)
		}
		if _, err := shotPart.Write(data); err != nil {
			t.Fatalf("write screenshot part %d: %v", i, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return buf, w.FormDataContentType()
}

func TestBugSink_HandleReport_WritesExpectedDropLayout(t *testing.T) {
	dir := t.TempDir()
	sink := &bugSink{dir: dir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	metaJSON := []byte(`{
		"protocol": "webapp-diagnostics/1",
		"createdAt": "2026-07-10T12:00:00.000Z",
		"kind": "bug",
		"sketch": "Compose window loses focus after paste\nSecond line of detail.",
		"page": {"url": "https://suite.example/#/mail", "title": "Herold Suite"},
		"app": {"id": "herold-suite", "name": "Herold Suite", "version": "1.2.3"},
		"principal": {"id": "p1", "label": "alice@example.local"},
		"context": {"route": "/mail", "selectedThread": "t42"},
		"logs": [
			{"ts": 1752000000000, "level": "warn", "msg": "compose focus lost", "ctx": "compose"}
		],
		"breadcrumbs": [
			{"type": "console.error", "text": "boom"}
		],
		"screenshotCount": 2
	}`)
	privateJSON := []byte(`{
		"included": true,
		"cookies": {"session": "SUPER-SECRET-COOKIE-VALUE-DO-NOT-LEAK"},
		"appPrivate": {"draftId": "d1"}
	}`)
	screenshots := [][]byte{
		[]byte("fake-png-bytes-one"),
		[]byte("fake-png-bytes-two"),
	}

	body, contentType := buildBugReportBody(t, metaJSON, privateJSON, screenshots)

	req := httptest.NewRequest(http.MethodPost, "/report", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	sink.handleReport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleReport: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	if resp.ID == "" {
		t.Fatalf("response carried no id: %s", rec.Body.String())
	}

	// Exactly one drop directory should exist; don't assert its exact name
	// (it is timestamp + random derived), only that it was created with the
	// expected files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read sink dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 drop directory, got %d: %v", len(entries), entries)
	}
	if !entries[0].IsDir() {
		t.Fatalf("expected drop to be a directory, got %v", entries[0])
	}
	if entries[0].Name() != resp.ID {
		t.Fatalf("drop directory name %q does not match response id %q", entries[0].Name(), resp.ID)
	}

	dropDir := filepath.Join(dir, entries[0].Name())

	// report.json present, verbatim.
	gotMeta, err := os.ReadFile(filepath.Join(dropDir, "report.json"))
	if err != nil {
		t.Fatalf("read report.json: %v", err)
	}
	if !bytes.Equal(gotMeta, metaJSON) {
		t.Fatalf("report.json not written verbatim:\ngot:  %s\nwant: %s", gotMeta, metaJSON)
	}

	// report.md present, contains the sketch, does NOT contain the cookie
	// value or any other private.json content.
	reportMD, err := os.ReadFile(filepath.Join(dropDir, "report.md"))
	if err != nil {
		t.Fatalf("read report.md: %v", err)
	}
	md := string(reportMD)
	if !strings.Contains(md, "Compose window loses focus after paste") {
		t.Fatalf("report.md missing sketch text:\n%s", md)
	}
	if strings.Contains(md, "SUPER-SECRET-COOKIE-VALUE-DO-NOT-LEAK") {
		t.Fatalf("report.md leaked private cookie value:\n%s", md)
	}
	if strings.Contains(md, "draftId") {
		t.Fatalf("report.md leaked private appPrivate content:\n%s", md)
	}

	// logs.txt present and non-empty.
	logsTxt, err := os.ReadFile(filepath.Join(dropDir, "logs.txt"))
	if err != nil {
		t.Fatalf("read logs.txt: %v", err)
	}
	if len(strings.TrimSpace(string(logsTxt))) == 0 {
		t.Fatalf("logs.txt is empty")
	}
	if strings.Contains(string(logsTxt), "SUPER-SECRET-COOKIE-VALUE-DO-NOT-LEAK") {
		t.Fatalf("logs.txt leaked private cookie value:\n%s", logsTxt)
	}

	// screenshot-1.png and screenshot-2.png present with expected content.
	shot1, err := os.ReadFile(filepath.Join(dropDir, "screenshot-1.png"))
	if err != nil {
		t.Fatalf("read screenshot-1.png: %v", err)
	}
	if !bytes.Equal(shot1, screenshots[0]) {
		t.Fatalf("screenshot-1.png content mismatch")
	}
	shot2, err := os.ReadFile(filepath.Join(dropDir, "screenshot-2.png"))
	if err != nil {
		t.Fatalf("read screenshot-2.png: %v", err)
	}
	if !bytes.Equal(shot2, screenshots[1]) {
		t.Fatalf("screenshot-2.png content mismatch")
	}
	if _, err := os.Stat(filepath.Join(dropDir, "screenshot-3.png")); !os.IsNotExist(err) {
		t.Fatalf("unexpected screenshot-3.png (only 2 screenshots were sent)")
	}

	// private/private.json present, verbatim, and NOT referenced from
	// report.md (checked above) or logs.txt.
	privDir := filepath.Join(dropDir, "private")
	privSt, err := os.Stat(privDir)
	if err != nil {
		t.Fatalf("stat private dir: %v", err)
	}
	if !privSt.IsDir() {
		t.Fatalf("private is not a directory")
	}
	gotPriv, err := os.ReadFile(filepath.Join(privDir, "private.json"))
	if err != nil {
		t.Fatalf("read private/private.json: %v", err)
	}
	if !bytes.Equal(gotPriv, privateJSON) {
		t.Fatalf("private/private.json not written verbatim:\ngot:  %s\nwant: %s", gotPriv, privateJSON)
	}

	// STATUS == "new".
	status, err := os.ReadFile(filepath.Join(dropDir, "STATUS"))
	if err != nil {
		t.Fatalf("read STATUS: %v", err)
	}
	if string(status) != "new" {
		t.Fatalf("STATUS = %q, want %q", status, "new")
	}
}

func TestBugSink_HandleReport_RejectsNonMultipart(t *testing.T) {
	dir := t.TempDir()
	sink := &bugSink{dir: dir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	req := httptest.NewRequest(http.MethodPost, "/report", strings.NewReader(`{"not":"multipart"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	sink.handleReport(rec, req)

	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("expected 4xx for non-multipart body, got %d", rec.Code)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read sink dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no drop directory for a rejected request, got %v", entries)
	}
}

func TestBugSink_HandleReport_RejectsOversizedBody(t *testing.T) {
	dir := t.TempDir()
	sink := &bugSink{dir: dir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	oversized := bytes.Repeat([]byte("x"), bugSinkMaxBodyBytes+1024)
	body, contentType := buildBugReportBody(t, []byte(`{"kind":"bug","sketch":"x"}`), []byte(`{}`), [][]byte{oversized})

	req := httptest.NewRequest(http.MethodPost, "/report", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	sink.handleReport(rec, req)

	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("expected 4xx for oversized body, got %d", rec.Code)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read sink dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no drop directory for a rejected oversized request, got %v", entries)
	}
}

func TestBugSink_HandleReport_HandlesOptionsPreflight(t *testing.T) {
	dir := t.TempDir()
	sink := &bugSink{dir: dir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	req := httptest.NewRequest(http.MethodOptions, "/report", nil)
	rec := httptest.NewRecorder()

	sink.handleReport(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS: status = %d, want 204", rec.Code)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.5.5.5", true},
		{"::1", true},
		{"localhost", true},
		{"LOCALHOST", true},
		{"0.0.0.0", false},
		{"192.168.1.1", false},
		{"", false},
		{"example.com", false},
	}
	for _, c := range cases {
		if got := isLoopbackHost(c.host); got != c.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestNewBugSinkCmd_RefusesNonLoopbackAddr(t *testing.T) {
	root := NewRootCmd()
	root.SetArgs([]string{"bug-sink", "--addr", "0.0.0.0:7777", "--dir", t.TempDir()})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.ExecuteContext(t.Context())
	if err == nil {
		t.Fatalf("expected an error for a non-loopback --addr, got none")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected the error to mention loopback, got: %v", err)
	}
}
