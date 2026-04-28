package webspa

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Options configures a Server. Zero values apply documented defaults.
type Options struct {
	// Logger overrides the default slog.Default.
	Logger *slog.Logger
	// SuiteAssetDir, when non-empty, makes the suite handler read
	// every asset from disk rather than the embedded FS. The
	// directory MUST contain index.html at the root; absence is a
	// configuration error. Relative paths are accepted and resolved
	// against the current working directory at construction time;
	// the resolved absolute path is then used for every request, so
	// changing cwd later does not relocate the asset root. Used in
	// development to hot-reload suite builds without rebuilding
	// herold.
	SuiteAssetDir string
	// PublicHost is the externally-visible hostname (without scheme
	// or port) used to populate the connect-src wss:// directive in
	// the Content-Security-Policy header (REQ-DEPLOY-COLOC-04). When
	// empty the directive collapses to 'self' which still matches a
	// relative WebSocket URL on the same origin.
	PublicHost string
}

// Server is the suite SPA HTTP handler. Construct via New; serve via
// Handler. The server is safe for concurrent use.
type Server struct {
	logger   *slog.Logger
	assetDir string
	root     fs.FS
	csp      string
}

// reservedAPIPrefixes is the defensive 404 list (REQ-DEPLOY-COLOC-02).
// Every prefix here is mounted on the public mux ahead of the SPA
// catch-all in production; the SPA handler refuses to serve index.html
// for any of them so a future mux misconfiguration does not silently
// shadow an API route.
var reservedAPIPrefixes = []string{
	"/api/",
	"/.well-known/",
	"/jmap",
	"/chat/",
	"/proxy/",
	"/hooks/",
	"/login",
	"/logout",
	"/auth/",
	"/oidc/",
}

// New constructs a Server for the suite SPA.
//
// When opts.SuiteAssetDir is non-empty New verifies the directory
// exists and contains index.html; the constructor refuses to start
// the server otherwise so the operator sees the misconfiguration at
// boot rather than at the first 404. Relative SuiteAssetDir paths
// are resolved to absolute via filepath.Abs at construction time so
// a later cwd change does not relocate the asset root.
//
// When opts.SuiteAssetDir is empty New serves from the embedded
// dist/suite/ FS (the release-build artefact, or the placeholder
// index in a fresh checkout). Under -tags nofrontend the embedded
// FS is a tiny in-memory placeholder; see embed_stub.go.
func New(opts Options) (*Server, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		logger: logger,
	}
	if opts.SuiteAssetDir != "" {
		dir, err := filepath.Abs(opts.SuiteAssetDir)
		if err != nil {
			return nil, fmt.Errorf("webspa: resolve suite_asset_dir %q: %w", opts.SuiteAssetDir, err)
		}
		s.assetDir = dir
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("webspa: stat suite_asset_dir %q: %w", dir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("webspa: suite_asset_dir %q is not a directory", dir)
		}
		idx := filepath.Join(dir, "index.html")
		if _, err := os.Stat(idx); err != nil {
			return nil, fmt.Errorf("webspa: suite_asset_dir %q missing index.html: %w", dir, err)
		}
		s.root = os.DirFS(dir)
	} else {
		sub, err := suiteEmbeddedFS()
		if err != nil {
			return nil, fmt.Errorf("webspa: open embedded suite dist: %w", err)
		}
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			return nil, fmt.Errorf("webspa: embedded suite dist missing index.html: %w", err)
		}
		s.root = sub
	}
	s.csp = buildCSP(opts.PublicHost)
	return s, nil
}

// Handler returns the SPA HTTP handler. The returned handler is the
// same instance every call; the caller mounts it on the public mux at
// the catch-all "/" path.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

// serveHTTP routes the request per REQ-DEPLOY-COLOC-02:
//
//   - GET / or /index.html -> index.html with no-cache.
//   - GET /<existing-asset> -> file with cache class derived from the
//     filename (immutable for hashed Vite outputs, 1h for stable
//     non-hashed assets).
//   - GET /<reserved-API-prefix>... -> 404 (defensive guard against
//     a future mux misconfiguration).
//   - GET /<unknown-with-extension> -> 404 (browser asked for a
//     missing asset; do NOT serve index.html with the wrong content
//     type).
//   - GET /<unknown-without-extension> -> index.html with no-cache
//     (SPA-router fallback per REQ-DEPLOY-COLOC-02).
func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.writeSecurityHeaders(w)

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	urlPath := r.URL.Path
	// Defensive 404 on reserved API prefixes. Production never sees
	// this branch -- the public mux's longest-prefix routing keeps
	// API paths off the catch-all -- but mount-order regressions stay
	// visible as 404s instead of silently serving the SPA shell.
	if isReservedAPIPath(urlPath) {
		http.NotFound(w, r)
		return
	}

	switch urlPath {
	case "/", "/index.html":
		s.serveIndex(w, r, http.StatusOK)
		return
	}

	// Map URL path -> FS path. Strip leading slash; reject any path
	// that escapes the dist root (defence against ../ traversal even
	// though fs.FS forbids it).
	clean := strings.TrimPrefix(path.Clean(urlPath), "/")
	if clean == "" || clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		http.NotFound(w, r)
		return
	}
	if !fs.ValidPath(clean) {
		http.NotFound(w, r)
		return
	}

	f, err := s.root.Open(clean)
	if err != nil {
		s.handleMiss(w, r, urlPath, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.handleMiss(w, r, urlPath, err)
		return
	}
	if info.IsDir() {
		// Directory hits fall through to the SPA-router fallback for
		// extension-free paths; with-extension paths cannot be
		// directories on a sane dist tree.
		s.handleMiss(w, r, urlPath, errors.New("path is a directory"))
		return
	}

	ct := mimeFor(clean)
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", cacheControlFor(clean))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		s.logger.LogAttrs(r.Context(), slog.LevelDebug, "webspa.suite: copy",
			slog.String("path", urlPath), slog.String("err", err.Error()))
	}
}

// handleMiss handles a path that did not resolve in the FS. Asset
// requests (with extension) return 404; SPA-router paths (no extension)
// fall back to index.html.
func (s *Server) handleMiss(w http.ResponseWriter, r *http.Request, urlPath string, _ error) {
	if hasFileExtension(urlPath) {
		http.NotFound(w, r)
		return
	}
	s.serveIndex(w, r, http.StatusOK)
}

// serveIndex writes index.html with no-cache headers.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, status int) {
	body, err := fs.ReadFile(s.root, "index.html")
	if err != nil {
		s.logger.LogAttrs(r.Context(), slog.LevelError, "webspa.suite: read index.html",
			slog.String("err", err.Error()))
		http.Error(w, "index.html unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	w.WriteHeader(status)
	_, _ = io.Copy(w, bytes.NewReader(body))
}

// writeSecurityHeaders sets the strict CSP plus the conventional
// nosniff / referrer-policy / x-frame-options headers on every
// response (REQ-DEPLOY-COLOC-04). frame-ancestors covers modern
// browsers; X-Frame-Options stays for older clients.
func (s *Server) writeSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", s.csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("X-Frame-Options", "DENY")
}

// buildCSP constructs the Content-Security-Policy header value. The
// connect-src directive includes wss://<publicHost> when publicHost is
// non-empty so the SPA's WebSocket client can dial /chat/ws on the
// same origin without falling foul of CSP; an empty publicHost emits
// connect-src 'self' which already covers same-origin relative WS
// URLs in every contemporary browser.
func buildCSP(publicHost string) string {
	connect := "connect-src 'self'"
	if h := strings.TrimSpace(publicHost); h != "" {
		connect = fmt.Sprintf("connect-src 'self' wss://%s", h)
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' https: data: blob:",
		connect,
		"font-src 'self' data:",
		"frame-ancestors 'none'",
		"base-uri 'self'",
	}, "; ") + ";"
}

// cacheControlFor returns the Cache-Control directive for a non-index
// asset. Vite emits content-addressed filenames such as
// "app-Df8z2.js" or "vendor.4f1c2.css" for hashed assets; those get
// the immutable max-age. Stable filenames (favicon.ico, robots.txt)
// get a 1h max-age so updates land within the hour without operator
// intervention.
func cacheControlFor(fsPath string) string {
	if isContentAddressed(fsPath) {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=3600"
}

// isContentAddressed reports whether the basename carries a Vite-style
// content-hash segment. Vite's default scheme produces names like
// "app-Df8z2.js", "index-9f3b1.css", or "asset-abcDEF1.png": the hash
// is at least 6 [A-Za-z0-9_-] characters separated from the human
// stem by a single '-' or '.', followed by the extension. Conservative
// heuristic -- any miss falls into the 1h class which is still safe.
func isContentAddressed(fsPath string) bool {
	base := path.Base(fsPath)
	ext := path.Ext(base)
	if ext == "" {
		return false
	}
	stem := strings.TrimSuffix(base, ext)
	// Find the last '-' or '.' separator.
	sep := strings.LastIndexAny(stem, "-.")
	if sep < 1 {
		return false
	}
	tail := stem[sep+1:]
	if len(tail) < 6 {
		return false
	}
	for _, r := range tail {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	// At least one alpha and one digit -> content hash, not a
	// human-readable suffix like "favicon-32x32".
	hasAlpha, hasDigit := false, false
	for _, r := range tail {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasAlpha = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	return hasAlpha && hasDigit
}

// hasFileExtension reports whether the URL path's last segment looks
// like an asset request. Used to decide whether a missed path returns
// 404 (asset typo) or falls back to index.html (SPA route).
func hasFileExtension(urlPath string) bool {
	base := path.Base(urlPath)
	if base == "/" || base == "." || base == "" {
		return false
	}
	ext := path.Ext(base)
	if ext == "" || ext == "." {
		return false
	}
	// Reject "ext == base" (a leading-dot file like ".env" looks
	// like an extension to path.Ext). The SPA's router rarely uses
	// dotfile-style paths; a 404 here is the safer choice anyway.
	if ext == base {
		return false
	}
	return true
}

// isReservedAPIPath reports whether urlPath matches any of the
// reserved API prefixes that must never fall through to the SPA
// fallback (REQ-DEPLOY-COLOC-02 defensive guard).
func isReservedAPIPath(urlPath string) bool {
	for _, p := range reservedAPIPrefixes {
		if urlPath == p {
			return true
		}
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(urlPath, p) {
				return true
			}
		} else {
			if urlPath == p || strings.HasPrefix(urlPath, p+"/") {
				return true
			}
		}
	}
	return false
}

// mimeFor resolves the Content-Type for an asset path. mime.TypeByExtension
// covers the common cases; for the asset shapes Vite emits we fall back
// to application/octet-stream rather than letting the writer guess.
func mimeFor(fsPath string) string {
	ext := path.Ext(fsPath)
	if ext == "" {
		return "application/octet-stream"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	switch strings.ToLower(ext) {
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	}
	return "application/octet-stream"
}
