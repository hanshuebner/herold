package webspa

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// parseMetaContent extracts the content attribute value from a
// <meta name="<name>" content='...'> or <meta name="<name>" content="...">
// tag in raw HTML. Returns ("", false) when not found.
func parseMetaContent(html, name string) (string, bool) {
	lower := strings.ToLower(html)
	// Look for name="herold-clientlog" or name='herold-clientlog'
	needle := `name="` + strings.ToLower(name) + `"`
	idx := strings.Index(lower, needle)
	if idx < 0 {
		// Try single-quote form.
		needle = `name='` + strings.ToLower(name) + `'`
		idx = strings.Index(lower, needle)
		if idx < 0 {
			return "", false
		}
	}
	// Find the content attribute after the name attribute.
	rest := html[idx:]
	// Look for content='...' (single quote) or content="..." (double quote)
	for _, q := range []string{`content='`, `content="`} {
		ci := strings.Index(strings.ToLower(rest), strings.ToLower(q))
		if ci < 0 {
			continue
		}
		quote := q[len(q)-1]
		start := ci + len(q)
		end := strings.IndexByte(rest[start:], quote)
		if end < 0 {
			continue
		}
		return rest[start : start+end], true
	}
	return "", false
}

// TestInjectMetaTags_Basic verifies that InjectMetaTags inserts both
// meta tags before </head> (REQ-CLOG-12, REQ-CLOG-03).
func TestInjectMetaTags_Basic(t *testing.T) {
	html := `<!doctype html><html><head><title>T</title></head><body></body></html>`
	bootstrap := ClientLogBootstrap{
		Enabled:                 true,
		BatchMaxEvents:          20,
		BatchMaxAgeMS:           5000,
		QueueCap:                200,
		TelemetryEnabledDefault: true,
	}
	result := InjectMetaTags([]byte(html), bootstrap, "abc123")

	resultStr := string(result)

	// Both meta tags must appear.
	if !strings.Contains(resultStr, `name="herold-clientlog"`) {
		t.Errorf("missing herold-clientlog meta tag; html=%q", resultStr)
	}
	if !strings.Contains(resultStr, `name="herold-build"`) {
		t.Errorf("missing herold-build meta tag; html=%q", resultStr)
	}

	// Tags must appear before </head>.
	headClose := strings.Index(strings.ToLower(resultStr), "</head>")
	clientlogMeta := strings.Index(resultStr, "herold-clientlog")
	buildMeta := strings.Index(resultStr, "herold-build")
	if clientlogMeta < 0 || clientlogMeta > headClose {
		t.Errorf("herold-clientlog meta not before </head>; html=%q", resultStr)
	}
	if buildMeta < 0 || buildMeta > headClose {
		t.Errorf("herold-build meta not before </head>; html=%q", resultStr)
	}

	// herold-build content must be the SHA.
	if !strings.Contains(resultStr, `content="abc123"`) {
		t.Errorf("herold-build content missing sha; html=%q", resultStr)
	}
}

// TestInjectMetaTags_ClientLogJSON verifies that the herold-clientlog
// content attribute carries valid JSON matching the bootstrap fields.
func TestInjectMetaTags_ClientLogJSON(t *testing.T) {
	html := `<!doctype html><html><head></head><body></body></html>`
	bootstrap := ClientLogBootstrap{
		Enabled:                 true,
		BatchMaxEvents:          20,
		BatchMaxAgeMS:           5000,
		QueueCap:                200,
		TelemetryEnabledDefault: true,
	}
	result := InjectMetaTags([]byte(html), bootstrap, "sha1")
	resultStr := string(result)

	content, ok := parseMetaContent(resultStr, "herold-clientlog")
	if !ok {
		t.Fatalf("could not find herold-clientlog meta content; html=%q", resultStr)
	}

	var got ClientLogBootstrap
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("clientlog content is not valid JSON: %v; content=%q", err, content)
	}
	if !got.Enabled {
		t.Errorf("enabled: got %v, want true", got.Enabled)
	}
	if got.BatchMaxEvents != 20 {
		t.Errorf("batch_max_events: got %d, want 20", got.BatchMaxEvents)
	}
	if got.BatchMaxAgeMS != 5000 {
		t.Errorf("batch_max_age_ms: got %d, want 5000", got.BatchMaxAgeMS)
	}
	if got.QueueCap != 200 {
		t.Errorf("queue_cap: got %d, want 200", got.QueueCap)
	}
	if !got.TelemetryEnabledDefault {
		t.Errorf("telemetry_enabled_default: got %v, want true", got.TelemetryEnabledDefault)
	}
}

// TestInjectMetaTags_EnabledFalse verifies that when enabled=false the
// meta tag is still present but carries enabled:false (REQ-CLOG-12):
// the kill switch propagates to the SPA via the meta tag.
func TestInjectMetaTags_EnabledFalse(t *testing.T) {
	html := `<!doctype html><html><head></head><body></body></html>`
	bootstrap := ClientLogBootstrap{Enabled: false}
	result := InjectMetaTags([]byte(html), bootstrap, "sha2")
	resultStr := string(result)

	content, ok := parseMetaContent(resultStr, "herold-clientlog")
	if !ok {
		t.Fatalf("herold-clientlog meta missing even when enabled=false; html=%q", resultStr)
	}

	var got ClientLogBootstrap
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("clientlog content invalid JSON: %v; content=%q", err, content)
	}
	if got.Enabled {
		t.Error("enabled: got true, want false in meta tag")
	}
}

// TestInjectMetaTags_DevSHA verifies that an empty BuildSHA produces
// the sentinel "dev" value in the meta tag (REQ-CLOG-03).
func TestInjectMetaTags_DevSHA(t *testing.T) {
	html := `<!doctype html><html><head></head><body></body></html>`
	result := InjectMetaTags([]byte(html), ClientLogBootstrap{Enabled: true}, "")
	resultStr := string(result)

	if !strings.Contains(resultStr, `content="dev"`) {
		t.Errorf("expected build SHA \"dev\" when empty; html=%q", resultStr)
	}
}

// TestInjectMetaTags_MalformedHTML verifies that when the input has no
// </head> the tags are still injected (graceful degradation).
func TestInjectMetaTags_MalformedHTML(t *testing.T) {
	html := `<html><body>no head tag at all</body></html>`
	result := InjectMetaTags([]byte(html), ClientLogBootstrap{Enabled: true}, "sha3")
	resultStr := string(result)

	if !strings.Contains(resultStr, "herold-clientlog") {
		t.Errorf("meta tags missing from malformed HTML; html=%q", resultStr)
	}
	if !strings.Contains(resultStr, "herold-build") {
		t.Errorf("herold-build meta missing from malformed HTML; html=%q", resultStr)
	}
}

// TestInjectMetaTags_CaseInsensitiveHead verifies that </HEAD> (upper
// case) is handled the same as </head>.
func TestInjectMetaTags_CaseInsensitiveHead(t *testing.T) {
	html := `<!doctype html><html><head><title>T</title></HEAD><body></body></html>`
	result := InjectMetaTags([]byte(html), ClientLogBootstrap{Enabled: true}, "sha4")
	resultStr := string(result)

	if !strings.Contains(resultStr, "herold-clientlog") {
		t.Errorf("meta tag not injected before </HEAD>; html=%q", resultStr)
	}
}

// TestSpaServesMetaTags_Enabled tests the full round-trip through the
// HTTP handler: serve index.html, parse the response, assert both meta
// tags are present with the correct content (REQ-CLOG-12, REQ-CLOG-03).
func TestSpaServesMetaTags_Enabled(t *testing.T) {
	bootstrap := ClientLogBootstrap{
		Enabled:                 true,
		BatchMaxEvents:          20,
		BatchMaxAgeMS:           5000,
		QueueCap:                200,
		TelemetryEnabledDefault: true,
	}
	s, _ := newMetaServer(t, map[string]string{
		"index.html": `<!doctype html><html><head><title>T</title></head><body></body></html>`,
	}, bootstrap, "deadbeef")

	resp := do(t, s, "/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	// Both meta tags present.
	if !strings.Contains(html, `name="herold-clientlog"`) {
		t.Errorf("herold-clientlog meta missing; html=%q", html)
	}
	if !strings.Contains(html, `name="herold-build"`) {
		t.Errorf("herold-build meta missing; html=%q", html)
	}

	// Build SHA embedded.
	if !strings.Contains(html, `content="deadbeef"`) {
		t.Errorf("build SHA not found; html=%q", html)
	}

	// Parse clientlog JSON.
	content, ok := parseMetaContent(html, "herold-clientlog")
	if !ok {
		t.Fatalf("could not extract herold-clientlog content; html=%q", html)
	}
	var got ClientLogBootstrap
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("clientlog JSON invalid: %v; content=%q", err, content)
	}
	if !got.Enabled {
		t.Error("enabled: got false, want true")
	}
}

// TestSpaServesMetaTags_DisabledClientLog tests that when
// clientlog.enabled=false the meta tag still appears but carries
// enabled:false (REQ-CLOG-12 kill switch).
func TestSpaServesMetaTags_DisabledClientLog(t *testing.T) {
	bootstrap := ClientLogBootstrap{Enabled: false}
	s, _ := newMetaServer(t, map[string]string{
		"index.html": `<!doctype html><html><head><title>T</title></head><body></body></html>`,
	}, bootstrap, "sha5")

	resp := do(t, s, "/")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	content, ok := parseMetaContent(html, "herold-clientlog")
	if !ok {
		t.Fatalf("herold-clientlog meta missing; html=%q", html)
	}
	var got ClientLogBootstrap
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("clientlog JSON invalid: %v; content=%q", err, content)
	}
	if got.Enabled {
		t.Error("enabled: got true, want false (kill switch)")
	}
}

// TestSpaServesMetaTags_SPAFallback verifies that the meta tags are
// also present on index.html served as the SPA-router fallback (not
// just on "/").
func TestSpaServesMetaTags_SPAFallback(t *testing.T) {
	bootstrap := ClientLogBootstrap{Enabled: true, TelemetryEnabledDefault: true}
	s, _ := newMetaServer(t, map[string]string{
		"index.html": `<!doctype html><html><head></head><body></body></html>`,
	}, bootstrap, "sha6")

	resp := do(t, s, "/some/app/route")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "herold-clientlog") {
		t.Errorf("herold-clientlog meta missing on SPA fallback; html=%q", html)
	}
}

// newMetaServer is a test helper that creates a Server with explicit
// ClientLogBootstrap and BuildSHA.
func newMetaServer(t *testing.T, files map[string]string, bootstrap ClientLogBootstrap, sha string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		writeFile(t, dir, name, body)
	}
	s, err := New(Options{
		SuiteAssetDir: dir,
		ClientLog:     bootstrap,
		BuildSHA:      sha,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, dir
}

// TestHtmlEscapeJSON_NoAngularBrackets verifies that angle brackets in
// the JSON are escaped to unicode escapes so the meta attribute is safe.
func TestHtmlEscapeJSON_NoAngularBrackets(t *testing.T) {
	// Embed a string that would contain < > & if unescaped.
	type payload struct {
		Msg string `json:"msg"`
	}
	raw, _ := json.Marshal(payload{Msg: `<script>alert("xss")</script>`})
	escaped := htmlEscapeJSON(raw)
	if strings.Contains(escaped, "<") || strings.Contains(escaped, ">") || strings.Contains(escaped, "&") {
		t.Errorf("escaped JSON still contains raw HTML chars: %s", escaped)
	}
}

// TestAdminSpaServesMetaTags verifies that the admin SPA handler also
// injects the meta tags (both SPAs share the same serveIndex logic).
func TestAdminSpaServesMetaTags(t *testing.T) {
	bootstrap := ClientLogBootstrap{
		Enabled:  true,
		QueueCap: 100,
	}
	dir := t.TempDir()
	writeFile(t, dir, "index.html",
		`<!doctype html><html><head><title>Admin</title></head><body></body></html>`)

	s, err := NewAdmin(AdminOptions{
		AdminAssetDir: dir,
		ClientLog:     bootstrap,
		BuildSHA:      "adminsha",
	})
	if err != nil {
		t.Fatalf("NewAdmin: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	resp := rr.Result()
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, `name="herold-clientlog"`) {
		t.Errorf("admin SPA missing herold-clientlog meta; html=%q", html)
	}
	if !strings.Contains(html, `content="adminsha"`) {
		t.Errorf("admin SPA missing build SHA; html=%q", html)
	}
}
