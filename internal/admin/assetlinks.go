package admin

import (
	"encoding/json"
	"net/http"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// assetLinksPath is the well-known path Android's Digital Asset Links
// verifier fetches to confirm a package is authorised to handle App
// Links for the responding origin (REQ-AND-SYS-11, issue #365).
const assetLinksPath = "/.well-known/assetlinks.json"

// assetLinksStatement is one entry of a Digital Asset Links document
// (https://digitalassetlinks.org/), scoped to the
// "delegate_permission/common.handle_all_urls" relation Android App
// Links verification checks.
type assetLinksStatement struct {
	Relation []string                `json:"relation"`
	Target   assetLinksAndroidTarget `json:"target"`
}

type assetLinksAndroidTarget struct {
	Namespace              string   `json:"namespace"`
	PackageName            string   `json:"package_name"`
	SHA256CertFingerprints []string `json:"sha256_cert_fingerprints"`
}

// newAssetLinksHandler serves the Digital Asset Links document built
// from [server.ui] android_app_links. Returns nil when the config list
// is empty so the caller mounts nothing and the path 404s via the SPA
// catch-all/stdlib default, matching the "absent when unconfigured"
// requirement from issue #365 -- an operator running no Android client
// need not see this route at all.
func newAssetLinksHandler(links []sysconfig.AndroidAppLinkConfig) http.Handler {
	if len(links) == 0 {
		return nil
	}
	statements := make([]assetLinksStatement, 0, len(links))
	for _, link := range links {
		statements = append(statements, assetLinksStatement{
			Relation: []string{"delegate_permission/common.handle_all_urls"},
			Target: assetLinksAndroidTarget{
				Namespace:              "android_app",
				PackageName:            link.Package,
				SHA256CertFingerprints: link.SHA256CertFingerprints,
			},
		})
	}
	body, err := json.Marshal(statements)
	if err != nil {
		// Unreachable: statements is built from plain strings/slices with
		// no cyclic types or unsupported kinds.
		panic("admin: marshal assetlinks.json: " + err.Error())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// One day: long enough that Android's periodic re-verification
		// and a curl-ing operator are not re-fetching on every request,
		// short enough that a rotated signing certificate propagates
		// without an operator having to think about cache purges.
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	})
}
