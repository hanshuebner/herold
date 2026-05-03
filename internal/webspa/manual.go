package webspa

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// ManualOptions configures the manual Server. Mirrors AdminOptions for
// the admin SPA but scopes the asset_dir field to the manual tree.
// The manual is intentionally public -- no session check is applied by
// the handler. It is statically generated HTML served as a read-only
// directory.
type ManualOptions struct {
	// Logger overrides the default slog.Default.
	Logger *slog.Logger
	// AssetDir, when non-empty, makes the manual handler read
	// every asset from disk rather than the embedded FS. The
	// directory MUST contain index.html at the root; absence is a
	// configuration error. Relative AssetDir paths are resolved to
	// absolute via filepath.Abs at construction time so a later cwd
	// change does not relocate the asset root. Used in development to
	// serve a freshly bundled manual without rebuilding herold.
	AssetDir string
}

// NewManual constructs a Server for the standalone manual.
//
// When opts.AssetDir is non-empty NewManual verifies the directory
// exists and contains index.html; it refuses to construct otherwise so
// the operator sees the misconfiguration at boot rather than at the
// first 404. Relative AssetDir paths are resolved to absolute via
// filepath.Abs at construction time so a later cwd change does not
// relocate the asset root.
//
// When opts.AssetDir is empty NewManual serves from the embedded
// dist/manual/ FS (the release-build artefact, or the placeholder index
// in a fresh checkout). Under -tags nofrontend the embedded FS is a
// tiny in-memory placeholder; see embed_stub.go.
//
// The returned Server reuses the suite handler logic in serveHTTP --
// the request handling is identical (longest-prefix routing on the
// mux delivers only catch-all paths here). The difference is the
// asset root and the CSP, both wired below.
func NewManual(opts ManualOptions) (*Server, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		logger: logger,
	}
	if opts.AssetDir != "" {
		dir, err := filepath.Abs(opts.AssetDir)
		if err != nil {
			return nil, fmt.Errorf("webspa: resolve manual asset_dir %q: %w", opts.AssetDir, err)
		}
		s.assetDir = dir
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("webspa: stat manual asset_dir %q: %w", dir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("webspa: manual asset_dir %q is not a directory", dir)
		}
		idx := filepath.Join(dir, "index.html")
		if _, err := os.Stat(idx); err != nil {
			return nil, fmt.Errorf("webspa: manual asset_dir %q missing index.html: %w", dir, err)
		}
		s.root = os.DirFS(dir)
	} else {
		sub, err := manualEmbeddedFS()
		if err != nil {
			return nil, fmt.Errorf("webspa: open embedded manual dist: %w", err)
		}
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			return nil, fmt.Errorf("webspa: embedded manual dist missing index.html: %w", err)
		}
		s.root = sub
	}
	s.csp = buildManualCSP()
	// The manual dist tree uses <audience>/<slug>/index.html layout;
	// directory hits must try to serve index.html within the directory.
	s.serveDirectoryIndex = true
	// Manual is public, no client-log bootstrap or build SHA injection needed.
	return s, nil
}

// buildManualCSP returns the CSP for the standalone manual. The manual
// is purely static HTML with no JMAP, no WebSocket, no third-party
// origins. script-src includes 'self' for the inline manual.js module.
func buildManualCSP() string {
	return "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		"font-src 'self'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'self';"
}
