//go:build nofrontend

package webspa

import (
	"io/fs"
	"testing/fstest"
)

// suiteEmbeddedFS returns a 1-file in-memory FS with a "frontend not
// built" placeholder. Used when herold is compiled with
// -tags nofrontend (Go-only CI lane / backend-only contributors;
// see docs/design/server/notes/plan-tabard-merge-and-admin-rewrite.md
// section 5).
func suiteEmbeddedFS() (fs.FS, error) {
	return stubFS("Suite SPA not built (binary compiled with -tags nofrontend). Run `make build` to produce a binary that includes the frontend."), nil
}

// adminEmbeddedFS returns the nofrontend stub for the admin SPA.
func adminEmbeddedFS() (fs.FS, error) {
	return stubFS("Admin SPA not built (binary compiled with -tags nofrontend). Run `make build` to produce a binary that includes the frontend."), nil
}

// manualEmbeddedFS returns the nofrontend stub for the standalone manual.
func manualEmbeddedFS() (fs.FS, error) {
	return stubFS("Manual not built -- run `make build-web`."), nil
}

func stubFS(msg string) fs.FS {
	body := []byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Herold</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
</head>
<body>
<h1>Herold</h1>
<p>` + msg + `</p>
</body>
</html>
`)
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: body},
	}
}
