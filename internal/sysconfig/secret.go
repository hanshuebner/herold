package sysconfig

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ResolveSecret expands a single raw secret reference (REQ-OPS-04).
//
// Supported forms:
//   - "$VAR"      — read the environment variable (must be set).
//   - "file:/p"   — read the file at /p, trim trailing newline.
//   - anything else — returned verbatim as an inline secret.
//
// Empty string yields an error (never treat "" as a valid secret; it is almost
// always a misconfiguration).
func ResolveSecret(raw string) (string, error) {
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
		return raw, nil
	}
}
