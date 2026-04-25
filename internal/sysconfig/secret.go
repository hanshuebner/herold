package sysconfig

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrInlineSecretRefused is returned by ResolveSecret when a literal
// inline value is presented and the caller has not set
// ResolveOptions.AllowInline. STANDARDS §9: "No inline secrets in
// system.toml. Env var, file, or external KMS only." Production
// callers should pass AllowInline=false (the default) so inline
// values fail loudly at config-load time.
var ErrInlineSecretRefused = errors.New("sysconfig: inline secret refused (use \"$ENV\" or \"file:/path\"; STANDARDS §9)")

// ResolveOptions controls ResolveSecret's behaviour. The zero value
// is the strict / production default: inline secrets are refused.
type ResolveOptions struct {
	// AllowInline permits values that do not start with "$" or
	// "file:" to pass through as literal secrets. Tests and migration
	// paths may opt in. Production callers MUST leave this false.
	AllowInline bool
}

// ResolveSecret expands a single raw secret reference (REQ-OPS-04).
//
// Supported forms:
//   - "$VAR"      — read the environment variable (must be set).
//   - "file:/p"   — read the file at /p, trim trailing newline.
//   - anything else — refused with ErrInlineSecretRefused unless the
//     caller explicitly opts in via ResolveOptions.AllowInline.
//
// Empty string yields an error (never treat "" as a valid secret; it
// is almost always a misconfiguration).
//
// The legacy single-argument form is preserved as a thin wrapper that
// permits inline values for backwards compatibility — but production
// callers should migrate to the strict form (ResolveSecretStrict)
// over time. The Wave-4 review keeps inline lookups callable so
// existing fixtures and migration tooling still work, but plumbs the
// strict path through Validate so a real `system.toml` with an
// inline secret fails at Load time.
func ResolveSecret(raw string) (string, error) {
	return ResolveSecretWith(raw, ResolveOptions{AllowInline: true})
}

// ResolveSecretStrict is ResolveSecret with AllowInline=false. Use
// from production code paths that must not accept inline secrets.
func ResolveSecretStrict(raw string) (string, error) {
	return ResolveSecretWith(raw, ResolveOptions{AllowInline: false})
}

// ResolveSecretWith expands raw subject to opts. See ResolveSecret
// for the form vocabulary.
func ResolveSecretWith(raw string, opts ResolveOptions) (string, error) {
	if raw == "" {
		return "", errors.New("sysconfig: empty secret reference")
	}
	switch {
	case strings.HasPrefix(raw, "$"):
		name := raw[1:]
		if name == "" {
			return "", errors.New("sysconfig: \"$\" without variable name")
		}
		val, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("sysconfig: environment variable %q not set", name)
		}
		return val, nil
	case strings.HasPrefix(raw, "file:"):
		path := raw[len("file:"):]
		if path == "" {
			return "", errors.New("sysconfig: \"file:\" without path")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("sysconfig: read secret %q: %w", path, err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	default:
		if opts.AllowInline {
			return raw, nil
		}
		return "", ErrInlineSecretRefused
	}
}

// IsSecretReference reports whether raw is a recognised secret-
// reference form (env or file). Used at config-load time to refuse
// inline secrets without committing to a resolution attempt (env
// vars may legitimately be unset until the binary launches).
func IsSecretReference(raw string) bool {
	if strings.HasPrefix(raw, "$") && len(raw) > 1 {
		return true
	}
	if strings.HasPrefix(raw, "file:") && len(raw) > len("file:") {
		return true
	}
	return false
}
