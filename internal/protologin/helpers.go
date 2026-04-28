package protologin

// helpers.go contains thin internal utilities shared by login.go and
// logout.go. These are intentionally NOT exported; they mirror analogous
// helpers in internal/protoadmin without creating an import dependency.

import (
	"encoding/json"
	"net/http"

	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/clock"
)

// problemTypeBase is the URI-reference namespace for protologin problem types.
// Matches protoadmin's namespace so the same SIEM rule hits both listeners.
const problemTypeBase = "https://netzhansa.com/problems/"

// problemDoc is the RFC 7807 "application/problem+json" body.
type problemDoc struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// writeProblem serialises an RFC 7807 problem document.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, typeSlug, title, detail string) {
	doc := problemDoc{
		Type:   problemTypeBase + typeSlug,
		Title:  title,
		Status: status,
		Detail: detail,
	}
	if r != nil {
		doc.Instance = r.URL.Path
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(doc)
}

// remoteHost strips the port from a RemoteAddr (host:port or [ipv6]:port).
// Mirrors internal/protoadmin/server_endpoints.go remoteHost.
func remoteHost(addr string) string {
	if len(addr) > 0 && addr[0] == '[' {
		for i := 1; i < len(addr); i++ {
			if addr[i] == ']' {
				return addr[1:i]
			}
		}
	}
	for i := 0; i < len(addr); i++ {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

// resolveSessionCookie reads and verifies the session cookie from r using cfg
// and clk. Returns the decoded Session or an error.
func resolveSessionCookie(r *http.Request, cfg authsession.SessionConfig, clk clock.Clock) (authsession.Session, error) {
	c, err := r.Cookie(cfg.CookieName)
	if err != nil {
		return authsession.Session{}, err
	}
	return authsession.DecodeSession(c.Value, cfg.SigningKey, clk.Now())
}
