package coach

import (
	"context"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// currentState returns the JMAP state string for the principal's
// ShortcutCoachStat datatype: the decimal of
// JMAPStates.ShortcutCoach. The store creates a zero-valued row on
// first access, so an unmutated principal returns "0".
func currentState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	st, err := meta.GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(st.ShortcutCoach, 10), nil
}

// utcDate formats t as a JMAP UTCDate string (RFC 3339 with second
// precision, UTC offset "Z").
func utcDate(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
