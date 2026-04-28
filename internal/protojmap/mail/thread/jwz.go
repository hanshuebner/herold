package thread

import (
	"strings"
)

// ThreadKey is the derived stable identifier for a thread. The current
// methods.go derives it directly from store.Message.ThreadID; this type
// remains the wire-side handle.
type ThreadKey uint64

// normalizeSubject implements RFC 5256 §2.1 base-subject extraction:
// strip leading "Re:" / "Fwd:" / "Fw:" runs (case-insensitive) and
// optional list-id "[tag]" prefixes. Returns the trimmed lower-cased
// subject and a boolean indicating whether anything was stripped (i.e.
// the message looks like a reply).
func normalizeSubject(subject string) (string, bool) {
	s := strings.TrimSpace(subject)
	stripped := false
	for {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "[") {
			if i := strings.Index(s, "]"); i > 0 {
				s = s[i+1:]
				continue
			}
		}
		lower := strings.ToLower(s)
		switch {
		case strings.HasPrefix(lower, "re:"):
			s = s[3:]
			stripped = true
		case strings.HasPrefix(lower, "fwd:"):
			s = s[4:]
			stripped = true
		case strings.HasPrefix(lower, "fw:"):
			s = s[3:]
			stripped = true
		default:
			return strings.ToLower(strings.TrimSpace(s)), stripped
		}
	}
}
