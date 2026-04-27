//go:build !nofrontend

package webspa

import (
	"embed"
	"io/fs"
)

// distEmbed holds the suite + admin SPA build artefacts copied into
// this module by scripts/build-web.sh. The placeholders shipped in
// source control are overwritten by the build script; the directories
// are kept alive in git via .gitkeep markers so the embed directive
// always resolves.
//
//go:embed dist
var distEmbed embed.FS

// suiteEmbeddedFS returns the embedded filesystem rooted at
// dist/suite/. Used when Options.SuiteAssetDir is empty.
func suiteEmbeddedFS() (fs.FS, error) {
	return fs.Sub(distEmbed, "dist/suite")
}

// adminEmbeddedFS returns the embedded filesystem rooted at
// dist/admin/. Used when Options.AdminAssetDir is empty. The
// dist/admin tree currently holds a placeholder index.html until
// Phase 2 of the merge plan ships the real Svelte admin SPA.
func adminEmbeddedFS() (fs.FS, error) {
	return fs.Sub(distEmbed, "dist/admin")
}
