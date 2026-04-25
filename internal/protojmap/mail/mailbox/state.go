package mailbox

import (
	"context"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// currentState returns the JMAP state string for the principal's
// Mailbox datatype: the decimal of JMAPStates.Mailbox per
// docs/architecture/05-sync-and-state.md. The store creates a
// zero-valued row on first access, so an unmutated principal returns
// "0".
func currentState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	st, err := meta.GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(st.Mailbox, 10), nil
}

// parseState decodes a client-supplied state string into the integer
// counter form. Returns ok=false on unparseable input; callers map that
// to RFC 8620 §5.2 "cannotCalculateChanges".
func parseState(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// stateFromCounter renders a state counter into the wire form.
func stateFromCounter(v int64) string {
	return strconv.FormatInt(v, 10)
}
