package webspa

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// AdminOptions configures the admin SPA Server. Mirrors Options for
// the suite SPA but scopes the asset_dir field to the admin tree and
// drops PublicHost (the admin SPA never opens a chat WebSocket so the
// CSP collapses to connect-src 'self' which already covers same-origin
// REST + EventSource).
type AdminOptions struct {
	// Logger overrides the default slog.Default.
	Logger *slog.Logger
	// AdminAssetDir, when non-empty, makes the admin handler read
	// every asset from disk rather than the embedded FS. The
	// directory MUST contain index.html at the root; absence is a
	// configuration error. Used in development to hot-reload admin
	// builds without rebuilding herold (typical loop:
	// `pnpm --filter @herold/admin build` -> hit refresh).
	AdminAssetDir string
}

// NewAdmin constructs a Server for the admin SPA.
//
// When opts.AdminAssetDir is non-empty NewAdmin verifies the directory
// exists and contains index.html; it refuses to construct otherwise so
// the operator sees the misconfiguration at boot rather than at the
// first 404. Relative AdminAssetDir paths are resolved to absolute via
// filepath.Abs at construction time so a later cwd change does not
// relocate the asset root.
//
// When opts.AdminAssetDir is empty NewAdmin serves from the embedded
// dist/admin/ FS (the release-build artefact, or the placeholder index
// in a fresh checkout). Under -tags nofrontend the embedded FS is a
// tiny in-memory placeholder; see embed_stub.go.
//
// The returned Server reuses the suite handler logic in serveHTTP --
// the request handling is identical (longest-prefix routing on the
// admin mux delivers only catch-all paths here). The difference is the
// asset root and the CSP, both wired below.
func NewAdmin(opts AdminOptions) (*Server, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		logger: logger,
	}
	if opts.AdminAssetDir != "" {
		dir, err := filepath.Abs(opts.AdminAssetDir)
		if err != nil {
			return nil, fmt.Errorf("webspa: resolve admin_asset_dir %q: %w", opts.AdminAssetDir, err)
		}
		s.assetDir = dir
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("webspa: stat admin_asset_dir %q: %w", dir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("webspa: admin_asset_dir %q is not a directory", dir)
		}
		idx := filepath.Join(dir, "index.html")
		if _, err := os.Stat(idx); err != nil {
			return nil, fmt.Errorf("webspa: admin_asset_dir %q missing index.html: %w", dir, err)
		}
		s.root = os.DirFS(dir)
	} else {
		sub, err := adminEmbeddedFS()
		if err != nil {
			return nil, fmt.Errorf("webspa: open embedded admin dist: %w", err)
		}
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			return nil, fmt.Errorf("webspa: embedded admin dist missing index.html: %w", err)
		}
		s.root = sub
	}
	s.csp = buildAdminCSP()
	return s, nil
}

// buildAdminCSP returns the CSP for the admin SPA. The admin app talks
// only to its same-origin REST surface (/api/v1/*) and -- in a future
// commit -- an EventSource for live updates (also same-origin). No
// wss://, no third-party origins, no inline scripts.
func buildAdminCSP() string {
	return "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		"font-src 'self' data:; " +
		"frame-ancestors 'none'; " +
		"base-uri 'self';"
}
