package admin

// public_routes_contract_test.go pins the contract between the Suite
// SPA's REST surface and the public listener's mux. A failure here
// means the SPA will see "404 page not found" (stdlib http.NotFound)
// on a path it expects to be served — like the regression in commit
// 095417e where /api/v1/identities/{id}/submission was registered
// inside RegisterSelfServiceRoutes but never Handle'd on publicMux,
// so the route table was correct but the public listener could not
// dispatch to it.
//
// What this test does:
//   - Walks web/apps/suite/src/ for every literal "/api/v1/..." URL
//     (single-quoted, double-quoted, or template-literal). Template
//     interpolations (${...}) are normalised to a placeholder so the
//     URL is a real path.
//   - Boots a real herold instance and probes each URL on the public
//     listener with an unauthenticated GET.
//   - Asserts the body is NOT exactly the stdlib "404 page not found"
//     marker. A registered handler returns either an auth-gate
//     response (401 with a structured JSON body), a CORS/preflight
//     response, a method-not-allowed (405 — route exists, wrong verb),
//     or a normal 2xx/4xx. The stdlib literal text fires only when
//     ServeMux had no match.
//
// What this test does NOT do:
//   - Verify correctness of any handler's response shape. That's the
//     responsibility of per-handler tests.
//   - Verify methods other than GET. Go's method-aware ServeMux
//     returns 405 (not 404) for an existing route with a wrong verb,
//     so the body marker fires only on a truly missing route. A
//     POST-only handler not mounted at all still surfaces here because
//     the GET against its prefix lands on the stdlib NotFound.
//   - Cover non-/api/v1 surfaces (/jmap, /chat/ws, /.well-known/jmap,
//     /proxy/image, /oidc/*). Those have their own dedicated tests;
//     this scope is deliberately tight to keep the failure signal
//     unambiguous ("an /api/v1 path the SPA calls is not mounted").

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// stdlibNotFoundMarker is the exact body Go's http.NotFound writes.
// Matching on the literal text rather than the 404 status is crucial:
// many handlers return 404 deliberately (e.g. "resource not found")
// with a structured JSON body, and those are not the failure we care
// about. Only the stdlib body indicates ServeMux dispatch failure.
const stdlibNotFoundMarker = "404 page not found"

// methodVerbCall matches the suite's typed helper invocations
//
//	get<T>('/api/v1/...')   -> GET
//	put<T>(`/api/v1/...`, ) -> PUT
//	del<T>("/api/v1/...")   -> DELETE
//	post<T>(`/api/v1/...`)  -> POST
//
// Capture groups: 1=verb-helper-name, 2=path. The helper's `<T>` is
// optional so calls like `get(...)` without a type parameter also match.
var methodVerbCall = regexp.MustCompile(
	`\b(get|put|del|post)\s*(?:<[^>]*>)?\s*\(\s*['"` + "`" + `](/api/v1/[^'"` + "`" + `?\s]+?)['"` + "`" + `]`)

// fetchCall matches direct fetch('/api/v1/...', { method: 'POST', ... })
// invocations. Capture groups: 1=path. Method is extracted in a
// second pass because the options object can have arbitrary key
// order and whitespace.
var fetchCall = regexp.MustCompile(
	`fetch\(\s*['"` + "`" + `](/api/v1/[^'"` + "`" + `?\s]+?)['"` + "`" + `]\s*,\s*\{([^}]*)\}`)

// fetchMethod extracts the method from a fetch options object body.
var fetchMethod = regexp.MustCompile(`method\s*:\s*['"]([A-Z]+)['"]`)

// templateInterp matches ${...} chunks inside template literals so we
// can substitute a placeholder and probe a real URL.
var templateInterp = regexp.MustCompile(`\$\{[^}]+\}`)

// verbHelperToMethod maps the suite's helper names to HTTP methods.
// `del` is the only one whose name does not match the verb verbatim
// (`delete` is a JS reserved word).
var verbHelperToMethod = map[string]string{
	"get":  http.MethodGet,
	"put":  http.MethodPut,
	"del":  http.MethodDelete,
	"post": http.MethodPost,
}

// methodPath bundles an HTTP method and a path. Probe input.
type methodPath struct {
	method string
	path   string
}

func TestPublicListener_ServesAllSuiteSPAEndpoints(t *testing.T) {
	suiteSrc := suiteSrcDir(t)
	probes := extractAPIV1MethodPathsFromTree(t, suiteSrc)
	if len(probes) == 0 {
		t.Fatalf("no /api/v1 paths discovered under %s; regex broken?", suiteSrc)
	}

	addrs, doneCh, cancel := startTestServerWithCookies(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-doneCh:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	publicAddr := addrs["public"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	for _, p := range probes {
		p := p
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			req, err := http.NewRequest(p.method, "http://"+publicAddr+p.path, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode == http.StatusNotFound &&
				strings.Contains(string(body), stdlibNotFoundMarker) {
				t.Errorf("public listener returned the stdlib 404 body for %s %s — "+
					"the suite SPA hits this path but no handler is mounted. "+
					"Either register the route (with the correct method) and "+
					"mount the prefix on publicMux in admin/server.go, or remove "+
					"the SPA call. Body: %q", p.method, p.path, body)
			}
		})
	}
}

// suiteSrcDir locates web/apps/suite/src relative to this package.
// Tests live at internal/admin/, so the relative path is "../../web/...".
func suiteSrcDir(t *testing.T) string {
	t.Helper()
	candidate := filepath.Join("..", "..", "web", "apps", "suite", "src")
	if _, err := os.Stat(candidate); err != nil {
		t.Skipf("suite SPA source not present at %s (err=%v); skipping contract test", candidate, err)
	}
	return candidate
}

// extractAPIV1MethodPathsFromTree walks root for *.ts / *.svelte
// files and returns the unique set of (method, path) pairs the suite
// SPA invokes against /api/v1/*. Each pair is the smallest unit a
// route registration must cover: a route may exist for one verb but
// not another, and probing with the wrong verb hides the real gap.
//
// Two recognised call shapes:
//
//	get<T>('/api/v1/...')           // -> GET
//	put<T>(`/api/v1/${id}`, body)   // -> PUT
//	del('/api/v1/...')              // -> DELETE
//	post(`/api/v1/...`, body)       // -> POST
//	fetch('/api/v1/...', { method: 'POST', ... })
//
// Template-literal interpolations (${...}) are normalised to "1" so
// the probe URL is concrete.
func extractAPIV1MethodPathsFromTree(t *testing.T, root string) []methodPath {
	t.Helper()
	seen := map[methodPath]struct{}{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip generated / vendored trees that may contain stale or
			// fixture URLs unrelated to runtime behaviour.
			name := d.Name()
			if name == "node_modules" || name == "dist" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		if !(strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".svelte")) {
			return nil
		}
		// Skip test fixtures: they often contain stub URLs that the SPA
		// would never actually hit at runtime.
		if strings.HasSuffix(p, ".test.ts") || strings.HasSuffix(p, ".spec.ts") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		src := string(b)
		// Typed-helper calls (get/put/del/post).
		for _, m := range methodVerbCall.FindAllStringSubmatch(src, -1) {
			method, ok := verbHelperToMethod[m[1]]
			if !ok {
				continue
			}
			seen[methodPath{method: method, path: templateInterp.ReplaceAllString(m[2], "1")}] = struct{}{}
		}
		// Raw fetch() calls with an options object.
		for _, m := range fetchCall.FindAllStringSubmatch(src, -1) {
			path := templateInterp.ReplaceAllString(m[1], "1")
			methodMatch := fetchMethod.FindStringSubmatch(m[2])
			method := http.MethodGet
			if methodMatch != nil {
				method = methodMatch[1]
			}
			seen[methodPath{method: method, path: path}] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s): %v", root, err)
	}
	out := make([]methodPath, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return out[i].method < out[j].method
	})
	return out
}
