package directory

// oauth2client.go is the OAuth2 client registry for the native-client
// authorization-code + PKCE grant (issue #199, REQ-AND-AUTH-01/02).
//
// Design decision (flagged in the #199 analysis comment as an explicit
// open question, resolved here): clients are a compiled-in Go map, not a
// DB-backed CRUD surface. This deployment has exactly one first-party
// native client (the Android app) for v1; the grant does not support
// third-party OAuth2 clients (no consent screen, no per-client secret --
// every registered client here is "public" per RFC 8252, i.e. incapable
// of holding a secret, which is why PKCE is mandatory rather than
// optional). Promoting this to an admin-manageable oauth_clients table
// is a follow-up if a second native client or a third-party integration
// is ever wanted; it is not required to unblock Android Phase 0.
type OAuthClient struct {
	// ID is the client_id value the native client presents.
	ID string
	// RedirectURIs is the exact set of URIs this client may register at
	// /oauth2/authorize. Every entry MUST be one of:
	//   - an exact string (custom-scheme redirect, e.g.
	//     "net.netzhansa.herold:/oauth2redirect") -- matched byte-for-byte.
	//   - a loopback template "http://127.0.0.1/<path>" or
	//     "http://[::1]/<path>" -- matched on scheme+host+path, ignoring
	//     port (RFC 8252 §7.3: the OS assigns an ephemeral port to a
	//     loopback listener, so the AS cannot pin it in advance).
	RedirectURIs []string
}

// oauthClients is the compiled-in registry. herold-android is the
// Android client (docs/design/android/requirements/01-auth-and-token.md
// REQ-AND-AUTH-01): a custom-scheme redirect for the primary Custom Tab
// flow plus a loopback redirect for platforms/flows that prefer it
// (RFC 8252 §7.3 offers both; the client chooses at authorize time by
// which redirect_uri it presents, and ValidateRedirectURI accepts either
// registered form).
var oauthClients = map[string]OAuthClient{
	"herold-android": {
		ID: "herold-android",
		RedirectURIs: []string{
			"net.netzhansa.herold:/oauth2redirect",
			"http://127.0.0.1/oauth2redirect",
			"http://[::1]/oauth2redirect",
		},
	},
}

// LookupOAuthClient returns the registered client for clientID, or
// ok=false when the client is unknown.
func LookupOAuthClient(clientID string) (OAuthClient, bool) {
	c, ok := oauthClients[clientID]
	return c, ok
}

// ValidateRedirectURI reports whether redirectURI is an acceptable
// redirect target for client, per the exact-match / loopback rules
// documented on OAuthClient.RedirectURIs. Exact-match prevents an open
// redirect (REQ-AND-AUTH-02's security requirement); the loopback
// exception is the well-known, narrowly-scoped RFC 8252 §7.3 carve-out
// (scheme+host+path pinned, only the ephemeral port varies).
func ValidateRedirectURI(client OAuthClient, redirectURI string) bool {
	for _, registered := range client.RedirectURIs {
		if redirectURI == registered {
			return true
		}
		if host, path, ok := loopbackHostPath(registered); ok {
			if reqHost, reqPath, reqOK := loopbackHostPath(redirectURI); reqOK && reqHost == host && reqPath == path {
				return true
			}
		}
	}
	return false
}

// loopbackHostPath parses uri as "http://<host>[:<port>]<path>" and
// returns (host, path, true) when host is a loopback literal (127.0.0.1
// or [::1]) and the scheme is http. Any port present is intentionally
// discarded by the caller's comparison, not by this function -- it is
// simply never part of the returned tuple.
func loopbackHostPath(uri string) (host, path string, ok bool) {
	const httpPrefix = "http://"
	if len(uri) <= len(httpPrefix) || uri[:len(httpPrefix)] != httpPrefix {
		return "", "", false
	}
	rest := uri[len(httpPrefix):]
	for _, prefix := range []string{"127.0.0.1", "[::1]"} {
		if len(rest) < len(prefix) || rest[:len(prefix)] != prefix {
			continue
		}
		h := prefix
		tail := rest[len(prefix):]
		// tail is one of: "" | "/path..." | ":port" | ":port/path..."
		if len(tail) > 0 && tail[0] == ':' {
			i := 0
			for i < len(tail) && tail[i] != '/' {
				i++
			}
			tail = tail[i:]
		}
		if tail == "" {
			tail = "/"
		}
		return h, tail, true
	}
	return "", "", false
}
