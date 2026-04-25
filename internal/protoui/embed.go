package protoui

import "embed"

// templatesFS holds the html/template files compiled into the binary.
// One pattern per top-level directory keeps the embed directives close
// to the directories they cover so future edits do not silently miss a
// new file.
//
//go:embed templates/*.html templates/fragments/*.html
var templatesFS embed.FS

// staticFS holds the vendored HTMX + Alpine assets and any small UI
// stylesheet. Served verbatim under /ui/static/. Per docs/notes/
// open-questions.md R35 we pin specific versions of HTMX and Alpine and
// document them in static/VERSIONS.md; no CDN.
//
//go:embed static/*
var staticFS embed.FS
