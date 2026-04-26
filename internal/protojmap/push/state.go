package push

import (
	"context"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// currentState returns the JMAP state string for the principal's
// PushSubscription datatype: the decimal of
// JMAPStates.PushSubscription. The store creates a zero-valued row
// on first access, so an unmutated principal returns "0".
func currentState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	st, err := meta.GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(st.PushSubscription, 10), nil
}
