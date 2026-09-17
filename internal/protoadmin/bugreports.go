package protoadmin

// bugreports.go implements the bug-reports REST surface (issue #416,
// REQ-ADM-320..324): POST /api/v1/bug-reports lets an authenticated
// end-user principal (the Android in-app reporter, REQ-AND-SYS-51) upload
// a report bundle; GET (list, by id) and DELETE let a bug-reports-scoped
// or admin-scoped credential (the maintainer's `herold bug-fetch`)
// retrieve and clear the queue. Each report is a directory drop under
// Options.BugReportsDir named by its id, holding the bundle's files
// verbatim plus a server-written meta.json.

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/store"
)

const (
	// bugReportMaxScreenshotBytes caps one screenshot-N.png part. Phone
	// screenshots run from a few hundred KB to low single-digit MB; 8 MiB
	// leaves headroom without accepting an arbitrary upload.
	bugReportMaxScreenshotBytes = 8 << 20
	// bugReportMaxTextPartBytes caps report.json, report.md, logs.txt,
	// and private.json individually -- all are small text documents.
	bugReportMaxTextPartBytes = 4 << 20
	// bugReportMaxScreenshots caps the number of screenshot-N.png parts
	// accepted in one report.
	bugReportMaxScreenshots = 20
	// bugReportMaxTotalBytes caps the whole request body, whichever form
	// it takes (separate parts or one zip part).
	bugReportMaxTotalBytes = 40 << 20
)

// bugReportScreenshotPattern matches the screenshot-N.png drop filenames.
var bugReportScreenshotPattern = regexp.MustCompile(`^screenshot-([1-9][0-9]{0,2})\.png$`)

// bugReportFixedNames are the non-screenshot drop filenames a report may
// carry.
var bugReportFixedNames = map[string]bool{
	"report.json":  true,
	"report.md":    true,
	"logs.txt":     true,
	"private.json": true,
}

// bugReportIDPattern matches ids minted by newBugReportID: an ISO-basic
// UTC timestamp followed by an 8-hex-digit random suffix. GET/DELETE
// reject any id that doesn't match before it touches the filesystem, so a
// crafted id cannot traverse outside BugReportsDir.
var bugReportIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

// isBugReportPartName reports whether name is one of the fixed drop
// filenames or a screenshot-N.png within the accepted range.
func isBugReportPartName(name string) bool {
	if bugReportFixedNames[name] {
		return true
	}
	return bugReportScreenshotPattern.MatchString(name)
}

// bugReportPartLimit returns the per-part size cap for name.
func bugReportPartLimit(name string) int64 {
	if bugReportScreenshotPattern.MatchString(name) {
		return bugReportMaxScreenshotBytes
	}
	return bugReportMaxTextPartBytes
}

// bugReportStoredRelPath maps a received part name to the drop-relative
// path it is stored and served at. Every part is stored flat except
// private.json: the drop layout (.claude/commands/bug-inbox.md, "every
// drop has a private/ subdirectory holding repro-only secrets") requires
// repro secrets to live under a private/ subdirectory so /bug-inbox's
// hard rule -- never read private/ into a ticket -- can be enforced by
// path alone. The wire form stays flat (a "private.json" multipart part,
// or a "private.json" / "private/private.json" zip entry both collapse
// to the same key via unzipBugReportBundle's path.Base) so the sender
// doesn't need to know the server-side layout.
func bugReportStoredRelPath(name string) string {
	if name == "private.json" {
		return "private/private.json"
	}
	return name
}

// bugReportServerMeta is meta.json, written by the server (never by the
// caller): who submitted the report, when, and how large each part was.
type bugReportServerMeta struct {
	PrincipalID uint64           `json:"principal_id"`
	Email       string           `json:"email"`
	ReceivedAt  time.Time        `json:"received_at"`
	Sizes       map[string]int64 `json:"sizes"`
}

// bugReportPublicMeta is the subset of report.json the list view reads
// (REQ-ADM-322). Field names mirror the Android reporter's bundle shape
// (mobile/shared BugReport.kt reportJson) and the browser panel's public
// meta (internal/admin/cmd_bugsink.go bugReportMeta).
type bugReportPublicMeta struct {
	Sketch             string `json:"sketch"`
	DescriptionEntered bool   `json:"descriptionEntered"`
	ScreenshotCount    int    `json:"screenshotCount"`
	Context            struct {
		Route string `json:"route"`
	} `json:"context"`
}

// newBugReportID mints a lexicographically sortable id from now, so a
// directory listing sorted by name is sorted by receipt time.
func newBugReportID(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s", now.UTC().Format("20060102T150405Z"), hex.EncodeToString(b[:]))
}

// requireBugReportsScope returns true when the caller's credential
// carries ScopeBugReports or ScopeAdmin (REQ-ADM-322, REQ-AUTH-SCOPE-02).
// It writes the RFC 7807 403 response and returns false otherwise. Must
// be called behind requireAuth so an auth.AuthContext is attached.
func requireBugReportsScope(w http.ResponseWriter, r *http.Request) bool {
	ctx := r.Context()
	if auth.RequireScope(ctx, auth.ScopeBugReports) == nil {
		return true
	}
	if auth.RequireScope(ctx, auth.ScopeAdmin) == nil {
		return true
	}
	writeProblem(w, r, http.StatusForbidden, "insufficient_scope",
		"insufficient scope for this resource",
		"requires bug-reports or admin scope")
	return false
}

// handleCreateBugReport implements POST /api/v1/bug-reports (REQ-ADM-320).
// Authenticated like the Suite's self-service endpoints: session cookie
// or bearer device token carrying ScopeEndUser. The body is either
// multipart/form-data with one part per drop file (report.json,
// report.md, logs.txt, screenshot-N.png, optional private.json), or a
// single "zip" part holding the same entries. private.json is stored at
// private/private.json (bugReportStoredRelPath), matching the drop
// layout every other producer uses for repro-only secrets.
func (s *Server) handleCreateBugReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := auth.RequireScope(ctx, auth.ScopeEndUser); err != nil {
		writeProblem(w, r, http.StatusForbidden, "insufficient_scope",
			"insufficient scope for this resource", err.Error())
		return
	}
	if s.opts.BugReportsDir == "" {
		writeProblem(w, r, http.StatusNotImplemented, "bugreports/not_implemented",
			"bug report storage is not configured on this server", "")
		return
	}
	caller, _ := principalFrom(ctx)

	r.Body = http.MaxBytesReader(w, r.Body, bugReportMaxTotalBytes)
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_body",
			"expected multipart/form-data", "")
		return
	}
	if err := r.ParseMultipartForm(bugReportMaxTotalBytes); err != nil {
		writeProblem(w, r, http.StatusRequestEntityTooLarge, "payload_too_large",
			"request body too large or malformed multipart", err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	files, ok := collectBugReportFiles(w, r)
	if !ok {
		return
	}
	if _, ok := files["report.json"]; !ok {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"report.json is required", "")
		return
	}

	id := newBugReportID(s.clk.Now())
	dropDir := filepath.Join(s.opts.BugReportsDir, id)
	if err := os.MkdirAll(dropDir, 0o700); err != nil {
		s.loggerFrom(ctx).Error("protoadmin.bugreports.mkdir_failed", "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error",
			"failed to store report", "")
		return
	}
	sizes := make(map[string]int64, len(files))
	for name, data := range files {
		relPath := bugReportStoredRelPath(name)
		target := filepath.Join(dropDir, filepath.FromSlash(relPath))
		if dir := filepath.Dir(target); dir != dropDir {
			// private/ (0700, matching the drop layout every other
			// producer of a drop uses for the repro-secrets subdirectory).
			if err := os.MkdirAll(dir, 0o700); err != nil {
				_ = os.RemoveAll(dropDir)
				s.loggerFrom(ctx).Error("protoadmin.bugreports.mkdir_failed", "err", err, "name", name)
				writeProblem(w, r, http.StatusInternalServerError, "internal_error",
					"failed to store report", "")
				return
			}
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			_ = os.RemoveAll(dropDir)
			s.loggerFrom(ctx).Error("protoadmin.bugreports.write_failed", "err", err, "name", name)
			writeProblem(w, r, http.StatusInternalServerError, "internal_error",
				"failed to store report", "")
			return
		}
		sizes[relPath] = int64(len(data))
	}
	meta := bugReportServerMeta{
		PrincipalID: uint64(caller.ID),
		Email:       caller.CanonicalEmail,
		ReceivedAt:  s.clk.Now().UTC(),
		Sizes:       sizes,
	}
	metaRaw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		_ = os.RemoveAll(dropDir)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error",
			"failed to store report", "")
		return
	}
	if err := os.WriteFile(filepath.Join(dropDir, "meta.json"), metaRaw, 0o600); err != nil {
		_ = os.RemoveAll(dropDir)
		s.loggerFrom(ctx).Error("protoadmin.bugreports.write_meta_failed", "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error",
			"failed to store report", "")
		return
	}

	s.appendAudit(ctx, "bugreport.create", "bugreport:"+id, store.OutcomeSuccess, "",
		map[string]string{
			"principal_id": fmt.Sprintf("%d", caller.ID),
		})
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// collectBugReportFiles extracts the drop's files from the parsed
// multipart form, either from a single "zip" part or from one part per
// drop file. On success it returns the file contents keyed by their
// final drop filename ("report.json", "screenshot-1.png", ...). On
// failure it writes the RFC 7807 response itself and returns false.
func collectBugReportFiles(w http.ResponseWriter, r *http.Request) (map[string][]byte, bool) {
	if r.MultipartForm == nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid_body", "empty multipart body", "")
		return nil, false
	}
	if zipParts := r.MultipartForm.File["zip"]; len(zipParts) == 1 {
		data, err := readMultipartFile(zipParts[0], bugReportMaxTotalBytes)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_body", "could not read zip part", err.Error())
			return nil, false
		}
		files, err := unzipBugReportBundle(data)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_body", "malformed zip bundle", err.Error())
			return nil, false
		}
		return files, true
	}

	files := make(map[string][]byte)
	for name, fhs := range r.MultipartForm.File {
		if !isBugReportPartName(name) {
			writeProblem(w, r, http.StatusBadRequest, "invalid_body",
				fmt.Sprintf("unexpected part %q", name), "")
			return nil, false
		}
		if len(fhs) != 1 {
			writeProblem(w, r, http.StatusBadRequest, "invalid_body",
				fmt.Sprintf("part %q must appear exactly once", name), "")
			return nil, false
		}
		limit := bugReportPartLimit(name)
		data, err := readMultipartFile(fhs[0], limit)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_body",
				fmt.Sprintf("could not read part %q", name), err.Error())
			return nil, false
		}
		if int64(len(data)) > limit {
			writeProblem(w, r, http.StatusRequestEntityTooLarge, "payload_too_large",
				fmt.Sprintf("part %q exceeds the %d byte limit", name, limit), "")
			return nil, false
		}
		files[name] = data
	}
	if n := bugReportScreenshotCount(files); n > bugReportMaxScreenshots {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			fmt.Sprintf("too many screenshots (%d), limit %d", n, bugReportMaxScreenshots), "")
		return nil, false
	}
	return files, true
}

// bugReportScreenshotCount counts the screenshot-N.png entries in files.
func bugReportScreenshotCount(files map[string][]byte) int {
	n := 0
	for name := range files {
		if bugReportScreenshotPattern.MatchString(name) {
			n++
		}
	}
	return n
}

// readMultipartFile reads fh's content, capped at limit+1 bytes so an
// oversized part is detected without buffering it in full.
func readMultipartFile(fh *multipart.FileHeader, limit int64) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit+1))
}

// unzipBugReportBundle reads every regular file out of a zip archive,
// keeping only entries whose basename matches the drop layout and
// capping each decompressed entry at its size class's limit -- the same
// caps the separate-parts path enforces. Only the basename is ever used
// as the stored filename, so an archive entry cannot escape the drop
// directory.
func unzipBugReportBundle(data []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := path.Base(f.Name)
		if !isBugReportPartName(name) {
			return nil, fmt.Errorf("unexpected entry %q", f.Name)
		}
		if _, dup := files[name]; dup {
			return nil, fmt.Errorf("duplicate entry %q", name)
		}
		limit := bugReportPartLimit(name)
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", f.Name, err)
		}
		content, err := io.ReadAll(io.LimitReader(rc, limit+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		if int64(len(content)) > limit {
			return nil, fmt.Errorf("entry %s exceeds %d bytes", f.Name, limit)
		}
		files[name] = content
	}
	if n := bugReportScreenshotCount(files); n > bugReportMaxScreenshots {
		return nil, fmt.Errorf("too many screenshots (%d), limit %d", n, bugReportMaxScreenshots)
	}
	return files, nil
}

// bugReportListItem is one row of GET /api/v1/bug-reports (REQ-ADM-322).
type bugReportListItem struct {
	ID                 string `json:"id"`
	ReceivedAt         string `json:"received_at"`
	PrincipalID        uint64 `json:"principal_id"`
	Email              string `json:"email"`
	Title              string `json:"title"`
	Route              string `json:"route"`
	DescriptionEntered bool   `json:"description_entered"`
	ScreenshotCount    int    `json:"screenshot_count"`
}

// handleListBugReports implements GET /api/v1/bug-reports (REQ-ADM-322):
// every stored report, newest first, with the fields `herold bug-fetch`
// and /bug-inbox need to triage without downloading the full drop.
func (s *Server) handleListBugReports(w http.ResponseWriter, r *http.Request) {
	if !requireBugReportsScope(w, r) {
		return
	}
	if s.opts.BugReportsDir == "" {
		writeProblem(w, r, http.StatusNotImplemented, "bugreports/not_implemented",
			"bug report storage is not configured on this server", "")
		return
	}
	entries, err := os.ReadDir(s.opts.BugReportsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusOK, map[string]any{"items": []bugReportListItem{}})
			return
		}
		s.loggerFrom(r.Context()).Error("protoadmin.bugreports.list_failed", "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to list reports", "")
		return
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && bugReportIDPattern.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	// Ids are timestamp-prefixed, so a reverse lexical sort is newest
	// first.
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	items := make([]bugReportListItem, 0, len(ids))
	for _, id := range ids {
		item, ok := s.readBugReportListItem(r, id)
		if !ok {
			continue
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// readBugReportListItem loads id's meta.json and report.json and builds
// its list row. A missing or unreadable meta.json skips the entry (a
// report mid-write, or a foreign directory an operator dropped in by
// hand) rather than failing the whole list.
func (s *Server) readBugReportListItem(r *http.Request, id string) (bugReportListItem, bool) {
	dir := filepath.Join(s.opts.BugReportsDir, id)
	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		s.loggerFrom(r.Context()).Warn("protoadmin.bugreports.meta_missing", "id", id, "err", err)
		return bugReportListItem{}, false
	}
	var meta bugReportServerMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		s.loggerFrom(r.Context()).Warn("protoadmin.bugreports.meta_invalid", "id", id, "err", err)
		return bugReportListItem{}, false
	}
	item := bugReportListItem{
		ID:          id,
		ReceivedAt:  meta.ReceivedAt.UTC().Format(time.RFC3339),
		PrincipalID: meta.PrincipalID,
		Email:       meta.Email,
	}
	if reportRaw, err := os.ReadFile(filepath.Join(dir, "report.json")); err == nil {
		var pub bugReportPublicMeta
		if err := json.Unmarshal(reportRaw, &pub); err == nil {
			item.Title = firstNonEmptyLine(pub.Sketch)
			item.Route = pub.Context.Route
			item.DescriptionEntered = pub.DescriptionEntered
			item.ScreenshotCount = pub.ScreenshotCount
		}
	}
	return item, true
}

// firstNonEmptyLine returns the first non-blank line of s, trimmed.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// bugReportDir resolves id to its drop directory, rejecting any id that
// does not match bugReportIDPattern before it touches the filesystem.
func (s *Server) bugReportDir(id string) (string, bool) {
	if !bugReportIDPattern.MatchString(id) {
		return "", false
	}
	return filepath.Join(s.opts.BugReportsDir, id), true
}

// handleGetBugReport implements GET /api/v1/bug-reports/{id}
// (REQ-ADM-323): the drop as a zip archive in the drop layout, ready for
// `herold bug-fetch` to extract verbatim.
func (s *Server) handleGetBugReport(w http.ResponseWriter, r *http.Request) {
	if !requireBugReportsScope(w, r) {
		return
	}
	if s.opts.BugReportsDir == "" {
		writeProblem(w, r, http.StatusNotImplemented, "bugreports/not_implemented",
			"bug report storage is not configured on this server", "")
		return
	}
	id := r.PathValue("id")
	dir, ok := s.bugReportDir(id)
	if !ok {
		writeProblem(w, r, http.StatusNotFound, "not_found", "bug report not found", "")
		return
	}
	if _, err := os.Stat(dir); err != nil {
		writeProblem(w, r, http.StatusNotFound, "not_found", "bug report not found", "")
		return
	}

	s.appendAudit(r.Context(), "bugreport.download", "bugreport:"+id, store.OutcomeSuccess, "", nil)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, id))
	w.WriteHeader(http.StatusOK)
	zw := zip.NewWriter(w)
	// Walk recursively (not a flat os.ReadDir) so private/private.json is
	// carried in the zip at its stored path, preserving the drop layout
	// end to end: `herold bug-fetch` extracts this zip verbatim.
	walkErr := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			s.loggerFrom(r.Context()).Warn("protoadmin.bugreports.zip_read_failed",
				"id", id, "name", rel, "err", readErr)
			return nil
		}
		fw, createErr := zw.Create(filepath.ToSlash(rel))
		if createErr != nil {
			s.loggerFrom(r.Context()).Warn("protoadmin.bugreports.zip_create_failed",
				"id", id, "name", rel, "err", createErr)
			return nil
		}
		if _, writeErr := fw.Write(data); writeErr != nil {
			s.loggerFrom(r.Context()).Warn("protoadmin.bugreports.zip_write_failed",
				"id", id, "name", rel, "err", writeErr)
		}
		return nil
	})
	if walkErr != nil {
		s.loggerFrom(r.Context()).Warn("protoadmin.bugreports.zip_walk_failed", "id", id, "err", walkErr)
	}
	if err := zw.Close(); err != nil {
		s.loggerFrom(r.Context()).Warn("protoadmin.bugreports.zip_close_failed", "id", id, "err", err)
	}
}

// handleDeleteBugReport implements DELETE /api/v1/bug-reports/{id}
// (REQ-ADM-324): removes the drop directory. `herold bug-fetch` calls
// this once the drop is durably written to the maintainer's disk, unless
// --keep was given.
func (s *Server) handleDeleteBugReport(w http.ResponseWriter, r *http.Request) {
	if !requireBugReportsScope(w, r) {
		return
	}
	if s.opts.BugReportsDir == "" {
		writeProblem(w, r, http.StatusNotImplemented, "bugreports/not_implemented",
			"bug report storage is not configured on this server", "")
		return
	}
	id := r.PathValue("id")
	dir, ok := s.bugReportDir(id)
	if !ok {
		writeProblem(w, r, http.StatusNotFound, "not_found", "bug report not found", "")
		return
	}
	if _, err := os.Stat(dir); err != nil {
		writeProblem(w, r, http.StatusNotFound, "not_found", "bug report not found", "")
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.bugreports.delete_failed", "id", id, "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to delete report", "")
		return
	}
	s.appendAudit(r.Context(), "bugreport.delete", "bugreport:"+id, store.OutcomeSuccess, "", nil)
	w.WriteHeader(http.StatusNoContent)
}
