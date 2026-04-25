package email

import (
	"context"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// currentState returns the JMAP state string for the principal's
// Email datatype.
func currentState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	st, err := meta.GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(st.Email, 10), nil
}

// parseState decodes a client state string. Returns ok=false on
// unparseable input; the caller emits cannotCalculateChanges.
func parseState(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

func stateFromCounter(v int64) string {
	return strconv.FormatInt(v, 10)
}
