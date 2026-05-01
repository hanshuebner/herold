package webspa

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ClientLogBootstrap is the bootstrap descriptor injected into the
// <meta name="herold-clientlog"> tag (REQ-CLOG-12). The wrapper reads
// it before any authentication so pre-auth code paths can decide
// whether to install handlers.
//
// Field names are camelCase to match the TypeScript schema in
// web/packages/clientlog/src/bootstrap.ts.
type ClientLogBootstrap struct {
	// Enabled is the master switch. When false the wrapper installs
	// no handlers and emits nothing (REQ-CLOG-12, REQ-OPS-219).
	Enabled bool `json:"enabled"`
	// BatchMaxEvents is the flush trigger event count (REQ-CLOG-02).
	BatchMaxEvents int `json:"batch_max_events"`
	// BatchMaxAgeMS is the flush trigger age in milliseconds (REQ-CLOG-02).
	BatchMaxAgeMS int `json:"batch_max_age_ms"`
	// QueueCap is the in-memory queue capacity (REQ-CLOG-09).
	QueueCap int `json:"queue_cap"`
	// TelemetryEnabledDefault is the deployment default for the
	// per-user telemetry opt-in (REQ-OPS-208). The SPA uses this
	// value before the authenticated session descriptor is available.
	TelemetryEnabledDefault bool `json:"telemetry_enabled_default"`
}

// InjectMetaTags rewrites the raw HTML content of index.html by
// inserting two <meta> tags immediately before the closing </head>
// tag:
//
//   - <meta name="herold-clientlog" content='{ bootstrap JSON }'>
//   - <meta name="herold-build" content="<sha>">
//
// When content does not contain </head> (malformed HTML) the tags are
// prepended to the document without any surgery on the existing
// content. The function never returns an error; injection failures
// degrade gracefully to returning the original content unmodified.
//
// The bootstrap JSON is compact (no indentation) and HTML-attribute
// safe: the content is single-quoted in the attribute so double-quotes
// in the JSON value are fine; angle brackets and ampersands are escaped
// so the tag is valid inside a raw HTML context.
func InjectMetaTags(content []byte, bootstrap ClientLogBootstrap, buildSHA string) []byte {
	sha := buildSHA
	if sha == "" {
		sha = "dev"
	}
	jsonBytes, err := json.Marshal(bootstrap)
	if err != nil {
		// Should never happen with a well-typed struct; return unchanged.
		return content
	}
	// Escape characters that could break the HTML attribute context.
	// The content attribute uses single quotes so only &, <, > need escaping.
	escaped := htmlEscapeJSON(jsonBytes)

	tags := "\n<meta name=\"herold-clientlog\" content='" + escaped + "'>\n" +
		"<meta name=\"herold-build\" content=\"" + htmlEscapeString(sha) + "\">\n"

	// Inject immediately before </head>. The search is case-insensitive
	// because some template engines emit </HEAD>; we only do the first
	// match (a well-formed document has exactly one </head>).
	lower := strings.ToLower(string(content))
	idx := strings.Index(lower, "</head>")
	if idx < 0 {
		// Malformed HTML: prepend tags at the start.
		return append([]byte(tags), content...)
	}
	var out bytes.Buffer
	out.Grow(len(content) + len(tags))
	out.Write(content[:idx])
	out.WriteString(tags)
	out.Write(content[idx:])
	return out.Bytes()
}

// htmlEscapeJSON escapes the minimal set of characters that need
// escaping when the JSON appears as an HTML attribute value inside
// single quotes: &, <, >.
func htmlEscapeJSON(b []byte) string {
	s := string(b)
	s = strings.ReplaceAll(s, "&", "\\u0026")
	s = strings.ReplaceAll(s, "<", "\\u003c")
	s = strings.ReplaceAll(s, ">", "\\u003e")
	return s
}

// htmlEscapeString escapes a plain string for use as an HTML attribute
// value (double-quoted context).
func htmlEscapeString(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
