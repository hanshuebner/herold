package autodns

import (
	"net/http"
	"strings"
)

// PolicyHandler returns an http.Handler that serves
// `/.well-known/mta-sts.txt` for every domain the publisher has cached
// an MTA-STS policy for. Operators wire the handler into the HTTPS
// listener serving `mta-sts.<domain>` (one cert per domain via ACME).
//
// The handler resolves the requesting host of the form `mta-sts.<domain>`
// to find the published policy. An unmatched host (or one without an
// `mta-sts.` prefix) returns 404.
func (p *Publisher) PolicyHandler() http.Handler {
	return http.HandlerFunc(p.servePolicy)
}

func (p *Publisher) servePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/.well-known/mta-sts.txt" {
		http.NotFound(w, r)
		return
	}
	host := r.Host
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	domain := strings.TrimPrefix(host, "mta-sts.")
	if domain == host || domain == "" {
		http.NotFound(w, r)
		return
	}
	p.mu.Lock()
	pp := p.policies[domain]
	p.mu.Unlock()
	if pp == nil || pp.mtastsBody == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(pp.mtastsBody))
}

// SetMTASTSBodyForTest seeds an MTA-STS policy body for domain. Test-only;
// production code goes through PublishDomain.
func (p *Publisher) SetMTASTSBodyForTest(domain, body, txt string) {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	p.mu.Lock()
	defer p.mu.Unlock()
	pp := p.policies[domain]
	if pp == nil {
		pp = &publishedPolicy{}
		p.policies[domain] = pp
	}
	pp.mtastsBody = body
	pp.mtastsTXT = txt
}
