package protoimg

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// validateImageProxyURL parses raw and applies the SSRF-adjacent
// pre-flight checks every fetch must clear before we touch the
// network. Returns the lowercased origin (host[:port]) on success.
//
// Rejected:
//   - non-https schemes (REQ-SEND-72; redirect path also enforces this).
//   - userinfo segments ("https://user:pass@host/...") which leak credentials
//     to upstream and can be used to confuse origin allowlisting.
//   - empty host.
//   - URLs longer than MaxURLLength (callers can enforce this earlier
//     too, but we re-check defensively).
func validateImageProxyURL(raw string) (origin string, err error) {
	if raw == "" {
		return "", errors.New("protoimg: empty url")
	}
	if len(raw) > MaxURLLength {
		return "", fmt.Errorf("protoimg: url exceeds %d bytes", MaxURLLength)
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", fmt.Errorf("protoimg: parse url: %w", perr)
	}
	// Scheme is case-insensitive per RFC 3986; normalise before checking.
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" {
		return "", fmt.Errorf("protoimg: scheme %q not allowed (https only)", u.Scheme)
	}
	u.Scheme = scheme
	if u.User != nil {
		return "", errors.New("protoimg: url must not embed credentials")
	}
	if u.Host == "" {
		return "", errors.New("protoimg: url missing host")
	}
	// Hosts may be encoded with non-ASCII; normalise to lowercase so the
	// rate-limiter's per-origin key is stable regardless of casing.
	return strings.ToLower(u.Host), nil
}
