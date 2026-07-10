package admin

// cmd_bugsink.go — `herold bug-sink`: a loopback-only HTTP sink for bug
// report bundles posted by the bug-reporter browser extension
// (see the sibling bug-reporter repo's README.md / PROTOCOL.md for the
// wire contract). This is local developer tooling: it accepts
// unauthenticated writes and receives confidential repro data (cookies,
// app-private state), so it refuses to bind anything but a loopback
// address.

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// bugSinkMaxBodyBytes caps the accepted request body (report.json +
// private.json + N screenshots). Requests over this size are rejected
// with 413 before any part is written to disk.
const bugSinkMaxBodyBytes = 50 << 20 // 50 MiB

// newBugSinkCmd returns the `herold bug-sink` command.
func newBugSinkCmd() *cobra.Command {
	var addr string
	var dir string
	c := &cobra.Command{
		Use:   "bug-sink",
		Short: "run a loopback-only HTTP sink for bug-report bundles from the browser extension",
		Long: "Serves POST /report on a loopback address and writes each report bundle " +
			"as a directory drop under --dir: public metadata (report.json, a rendered " +
			"report.md, logs.txt), the screenshots, and a private/ subdirectory holding " +
			"repro secrets (cookies, app-private state) segregated from everything a " +
			"ticket-filing step reads.\n\n" +
			"This is local developer tooling. It accepts unauthenticated writes and " +
			"confidential repro data, so it refuses to bind any address whose host is " +
			"not loopback (127.0.0.0/8, ::1, or localhost) — that check cannot be " +
			"overridden by a flag.\n\n" +
			"See the bug-reporter extension's README.md and PROTOCOL.md for the report " +
			"bundle format.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBugSink(cmd, addr, dir)
		},
	}
	c.Flags().StringVar(&addr, "addr", "127.0.0.1:7777",
		"loopback address to listen on (host must be 127.0.0.0/8, ::1, or localhost)")
	c.Flags().StringVar(&dir, "dir", defaultBugSinkDir(), "directory to write report drops into")
	return c
}

// defaultBugSinkDir resolves ~/herold-bugs, falling back to a relative
// path when the home directory cannot be determined.
func defaultBugSinkDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "./herold-bugs"
	}
	return filepath.Join(home, "herold-bugs")
}

// isLoopbackHost reports whether host (the host part of an already-split
// address) is a loopback address or the literal "localhost".
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// runBugSink validates --addr, binds the listener, and serves until the
// command context is cancelled (SIGTERM/SIGINT — see main.go's
// signal.NotifyContext).
func runBugSink(cmd *cobra.Command, addr, dir string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("bug-sink: invalid --addr %q: %w", addr, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("bug-sink: refusing to bind non-loopback address %q; "+
			"--addr host must be 127.0.0.0/8, ::1, or localhost (this endpoint accepts "+
			"unauthenticated writes and confidential repro data)", addr)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("bug-sink: create --dir %s: %w", dir, err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}

	logger := slog.New(slog.NewJSONHandler(cmd.ErrOrStderr(), nil)).With("component", "bug-sink")
	sink := &bugSink{dir: absDir, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("/report", sink.handleReport)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("bug-sink: listen on %s: %w", addr, err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: mux}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	fmt.Fprintf(cmd.OutOrStdout(), "herold bug-sink: listening on %s, writing drops to %s\n", ln.Addr().String(), absDir)
	logger.Info("bug-sink listening", "addr", ln.Addr().String(), "dir", absDir)

	ctx := cmd.Context()
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("bug-sink: serve: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("bug-sink: shutdown: %w", err)
	}
	return nil
}

// bugSink holds the state needed to service /report requests: the drop
// root directory and a logger. It has no dependency on the store, admin
// REST client, or system config — it is intentionally standalone.
type bugSink struct {
	dir    string
	logger *slog.Logger
}

// bugReportMeta mirrors the public "meta" JSON part of a report bundle
// (see bug-reporter/PROTOCOL.md Descriptor and panel.js submit()). Only
// the fields report.md rendering needs are modelled; report.json itself
// is always written verbatim from the raw bytes, never re-marshalled.
type bugReportMeta struct {
	Protocol        string              `json:"protocol"`
	CreatedAt       string              `json:"createdAt"`
	Kind            string              `json:"kind"`
	Sketch          string              `json:"sketch"`
	Page            *bugReportPage      `json:"page"`
	App             *bugReportApp       `json:"app"`
	Principal       *bugReportPrincipal `json:"principal"`
	Context         json.RawMessage     `json:"context"`
	Logs            []bugReportLogEntry `json:"logs"`
	Breadcrumbs     []json.RawMessage   `json:"breadcrumbs"`
	ScreenshotCount int                 `json:"screenshotCount"`
}

type bugReportPage struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type bugReportApp struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type bugReportPrincipal struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type bugReportLogEntry struct {
	TS      float64         `json:"ts"`
	Level   string          `json:"level"`
	Msg     string          `json:"msg"`
	Ctx     string          `json:"ctx"`
	Payload json.RawMessage `json:"payload"`
}

// handleReport is the POST /report handler. It accepts only
// multipart/form-data, caps the body at bugSinkMaxBodyBytes, and writes
// one drop directory per request.
func (s *bugSink) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Allow", "POST, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		http.Error(w, "expected multipart/form-data", http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, bugSinkMaxBodyBytes)
	if err := r.ParseMultipartForm(bugSinkMaxBodyBytes); err != nil {
		s.logger.Warn("bug-sink: reject oversized or malformed body", "err", err)
		http.Error(w, "body too large or malformed multipart", http.StatusRequestEntityTooLarge)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	id, err := s.writeDrop(r)
	if err != nil {
		s.logger.Error("bug-sink: write drop failed", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.logger.Info("bug-sink: report received", "id", id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
}

// writeDrop creates the drop directory and its contents from the parsed
// multipart form, returning the drop's basename (its id).
func (s *bugSink) writeDrop(r *http.Request) (string, error) {
	metaRaw, err := readFilePart(r, "meta")
	if err != nil {
		return "", fmt.Errorf("meta part: %w", err)
	}
	privateRaw, err := readFilePart(r, "private")
	if err != nil {
		return "", fmt.Errorf("private part: %w", err)
	}
	var meta bugReportMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return "", fmt.Errorf("meta part: invalid JSON: %w", err)
	}
	screenshots, err := readFileParts(r, "screenshot")
	if err != nil {
		return "", fmt.Errorf("screenshot part: %w", err)
	}

	id := newDropID(time.Now().UTC())
	dropDir := filepath.Join(s.dir, id)
	if err := os.MkdirAll(dropDir, 0o700); err != nil {
		return "", fmt.Errorf("create drop dir: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dropDir, "report.json"), metaRaw, 0o600); err != nil {
		return "", fmt.Errorf("write report.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dropDir, "report.md"), []byte(renderReportMarkdown(meta)), 0o600); err != nil {
		return "", fmt.Errorf("write report.md: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dropDir, "logs.txt"), []byte(renderLogsTxt(meta)), 0o600); err != nil {
		return "", fmt.Errorf("write logs.txt: %w", err)
	}
	for i, data := range screenshots {
		name := fmt.Sprintf("screenshot-%d.png", i+1)
		if err := os.WriteFile(filepath.Join(dropDir, name), data, 0o600); err != nil {
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}

	// The private zone is segregated in its own 0700 subdirectory and
	// holds only the verbatim private.json — repro secrets never flow
	// into report.md, logs.txt, or any other file the ticket-filing step
	// reads (bug-reporter/PROTOCOL.md "Zones").
	privateDir := filepath.Join(dropDir, "private")
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		return "", fmt.Errorf("create private dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(privateDir, "private.json"), privateRaw, 0o600); err != nil {
		return "", fmt.Errorf("write private.json: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dropDir, "STATUS"), []byte("new"), 0o600); err != nil {
		return "", fmt.Errorf("write STATUS: %w", err)
	}

	return id, nil
}

// readFilePart reads the single named file part's contents. The
// bug-reporter extension sends "meta" and "private" as Blob parts with a
// filename (report.json / private.json respectively), so Go's
// multipart parser files them under MultipartForm.File, not .Value.
func readFilePart(r *http.Request, field string) ([]byte, error) {
	if r.MultipartForm == nil {
		return nil, fmt.Errorf("missing required part %q", field)
	}
	fhs := r.MultipartForm.File[field]
	if len(fhs) == 0 {
		return nil, fmt.Errorf("missing required part %q", field)
	}
	f, err := fhs[0].Open()
	if err != nil {
		return nil, fmt.Errorf("open part %q: %w", field, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read part %q: %w", field, err)
	}
	return data, nil
}

// readFileParts reads every part filed under field, in submission order
// (used for the repeated "screenshot" field).
func readFileParts(r *http.Request, field string) ([][]byte, error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	fhs := r.MultipartForm.File[field]
	out := make([][]byte, 0, len(fhs))
	for _, fh := range fhs {
		f, err := fh.Open()
		if err != nil {
			return nil, fmt.Errorf("open part %q: %w", field, err)
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("read part %q: %w", field, err)
		}
		out = append(out, data)
	}
	return out, nil
}

// newDropID builds the "<UTC-timestamp>-<shortrand>" drop directory name.
func newDropID(now time.Time) string {
	return fmt.Sprintf("%s-%s", now.Format("20060102T150405Z"), shortRandHex(4))
}

// shortRandHex returns n random bytes hex-encoded. A time-based fallback
// covers the vanishingly unlikely case crypto/rand is unavailable; this
// is local dev tooling, not a security boundary for the id itself.
func shortRandHex(n int) string {
	b := make([]byte, n)
	if _, err := cryptorand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// renderReportMarkdown builds a human/ticket-ready report.md from the
// PUBLIC meta fields only. It must never be handed private.json.
func renderReportMarkdown(meta bugReportMeta) string {
	var b strings.Builder

	kind := meta.Kind
	if kind == "" {
		kind = "report"
	}
	title := firstNonEmptyLine(meta.Sketch)
	if title == "" {
		title = "(no description)"
	}
	fmt.Fprintf(&b, "# %s: %s\n\n", capitalize(kind), title)

	b.WriteString("## Description\n\n")
	if meta.Sketch != "" {
		b.WriteString(meta.Sketch)
		b.WriteString("\n\n")
	} else {
		b.WriteString("(none)\n\n")
	}

	b.WriteString("## Page\n\n")
	if meta.Page != nil {
		fmt.Fprintf(&b, "- URL: %s\n- Title: %s\n\n", meta.Page.URL, meta.Page.Title)
	} else {
		b.WriteString("(not reported)\n\n")
	}

	b.WriteString("## App\n\n")
	if meta.App != nil {
		fmt.Fprintf(&b, "%s %s\n\n", meta.App.Name, meta.App.Version)
	} else {
		b.WriteString("(not reported)\n\n")
	}

	b.WriteString("## Principal\n\n")
	if meta.Principal != nil && meta.Principal.Label != "" {
		fmt.Fprintf(&b, "%s\n\n", meta.Principal.Label)
	} else {
		b.WriteString("(not reported)\n\n")
	}

	b.WriteString("## Context\n\n")
	if len(meta.Context) > 0 && string(meta.Context) != "null" {
		pretty, err := prettyJSON(meta.Context)
		if err != nil {
			pretty = string(meta.Context)
		}
		b.WriteString("```json\n")
		b.WriteString(pretty)
		b.WriteString("\n```\n\n")
	} else {
		b.WriteString("(none)\n\n")
	}

	b.WriteString("## Logs (tail)\n\n")
	tail := logLines(meta)
	if len(tail) > 50 {
		tail = tail[len(tail)-50:]
	}
	if len(tail) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, line := range tail {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}

	return b.String()
}

// renderLogsTxt formats every log + breadcrumb entry one per line.
func renderLogsTxt(meta bugReportMeta) string {
	lines := logLines(meta)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// logLines formats meta.Logs followed by meta.Breadcrumbs, one string
// per entry, in submission order.
func logLines(meta bugReportMeta) []string {
	lines := make([]string, 0, len(meta.Logs)+len(meta.Breadcrumbs))
	for _, e := range meta.Logs {
		lines = append(lines, formatLogEntry(e))
	}
	for _, raw := range meta.Breadcrumbs {
		lines = append(lines, "breadcrumb: "+compactJSON(raw))
	}
	return lines
}

func formatLogEntry(e bugReportLogEntry) string {
	var ts string
	if e.TS > 0 {
		ts = time.UnixMilli(int64(e.TS)).UTC().Format(time.RFC3339)
	}
	ctx := ""
	if e.Ctx != "" {
		ctx = "[" + e.Ctx + "] "
	}
	level := strings.ToUpper(e.Level)
	if level == "" {
		level = "LOG"
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s%s: %s", ts, ctx, level, e.Msg))
}

func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func prettyJSON(raw json.RawMessage) (string, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}
