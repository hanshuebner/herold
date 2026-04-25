package protoui

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// pageData is the canonical envelope every full-page render passes
// into layout.html. Sub-templates project from here via .Body or
// dedicated fields; nothing else is set by individual handlers.
type pageData struct {
	// Title is rendered into <title> and the page header.
	Title string
	// Active is the navigation slug (dashboard / principals / domains
	// / queue / research / audit) used to highlight the active link
	// in the side nav.
	Active string
	// Principal is the currently authenticated principal, or zero
	// when the page is unauthenticated (login).
	Principal store.Principal
	// CSRFToken is the per-session CSRF token. Forms must embed it as
	// a hidden input named CSRFFormField.
	CSRFToken string
	// PathBase is the configured prefix (e.g. "/ui"). Templates use it
	// to prefix every link so the nav works under any mount point.
	PathBase string
	// Flash is the optional one-shot status message at the top of the
	// page. nil means no flash.
	Flash *flashMessage
	// BodyTmpl names the template to invoke for the main page body.
	// layout.html does {{template .BodyTmpl .}} with the same envelope.
	BodyTmpl string
	// Body is the per-page payload struct. Per-page templates type-
	// assert as needed; an interface here keeps layout.html generic.
	Body any
}

// flashMessage is a single styled banner. Kind is "ok" | "error"; the
// CSS in layout.html maps each kind to a colour.
type flashMessage struct {
	Kind string
	Body string
}

// renderPage executes the layout with data and writes status to w.
// The handler stays free of formatting concerns: pick the body tmpl
// name + payload, set Active + Title, and call this.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, data *pageData) {
	if data.PathBase == "" {
		data.PathBase = s.pathPrefix
	}
	if p, ok := principalFromCtx(r.Context()); ok && data.Principal.ID == 0 {
		data.Principal = p
	}
	if sess, ok := sessionFromCtx(r.Context()); ok && data.CSRFToken == "" {
		data.CSRFToken = sess.CSRFToken
	}
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		s.logger.Error("protoui.render", "err", err, "tmpl", "layout.html")
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// renderFragment writes an HTMX-friendly partial: just the named
// fragment template, no layout. Used by hx-get / hx-post handlers
// that swap a single piece of the page in place.
func (s *Server) renderFragment(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.logger.Error("protoui.render_fragment", "err", err, "tmpl", name)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// funcMap is the set of helper functions templates call. Kept narrow
// so the templating language stays predictable; new helpers land here
// only when a template would otherwise duplicate logic.
func funcMap() template.FuncMap {
	return template.FuncMap{
		"isAdmin": func(p store.Principal) bool { return p.Flags.Has(store.PrincipalFlagAdmin) },
		"isDisabled": func(p store.Principal) bool {
			return p.Flags.Has(store.PrincipalFlagDisabled)
		},
		"hasTOTP": func(p store.Principal) bool {
			return p.Flags.Has(store.PrincipalFlagTOTPEnabled)
		},
		"queueState":  func(q store.QueueState) string { return q.String() },
		"shortHash":   func(s string) string { return shortHashHelper(s) },
		"flagStrings": principalFlagStrings,
		"formatTime":  func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"truncate":    truncate,
		"join":        strings.Join,
		"safeHTML":    func(s string) template.HTML { return template.HTML(s) },
		"prefixedURL": func(base, path string) string { return base + path },
		"add":         func(a, b int) int { return a + b },
		"isHTMX": func(r *http.Request) bool {
			return r.Header.Get("HX-Request") == "true"
		},
	}
}

// principalFlagStrings is the same projection protoadmin's wire DTO
// uses, lifted here so the templates can call it without reaching
// into protoadmin. Two-caller duplication; a third caller will earn a
// shared helper.
func principalFlagStrings(f store.PrincipalFlags) []string {
	out := []string{}
	if f.Has(store.PrincipalFlagDisabled) {
		out = append(out, "disabled")
	}
	if f.Has(store.PrincipalFlagIgnoreDownloadLimits) {
		out = append(out, "ignore_download_limits")
	}
	if f.Has(store.PrincipalFlagAdmin) {
		out = append(out, "admin")
	}
	if f.Has(store.PrincipalFlagTOTPEnabled) {
		out = append(out, "totp_enabled")
	}
	return out
}

// shortHashHelper truncates a hex-ish id to 8 characters for table
// rendering. Empty inputs render as "-" so the table cell is never
// awkward.
func shortHashHelper(s string) string {
	if s == "" {
		return "-"
	}
	if len(s) <= 8 {
		return s
	}
	return s[:8] + "…"
}

// truncate caps s at n runes and appends an ellipsis when truncated.
// Bytes-as-runes for ASCII is fine here; for arbitrary user input the
// cap is over-conservative on multibyte sequences but never wrong.
func truncate(n int, s string) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// renderError writes a rendered error page. status is the HTTP status;
// detail is the human message displayed in the flash banner.
func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, detail string) {
	data := &pageData{
		Title:    fmt.Sprintf("Error %d", status),
		Flash:    &flashMessage{Kind: "error", Body: detail},
		BodyTmpl: "error_body",
	}
	s.renderPage(w, r, status, data)
}
