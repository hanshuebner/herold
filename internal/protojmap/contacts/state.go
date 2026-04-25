package contacts

import (
	"context"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// currentAddressBookState returns the JMAP state string for the
// principal's AddressBook datatype: the decimal of
// JMAPStates.AddressBookState. The store creates a zero-valued row on
// first access, so an unmutated principal returns "0".
func currentAddressBookState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	st, err := meta.GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(st.AddressBook, 10), nil
}

// currentContactState returns the JMAP state string for the principal's
// Contact datatype.
func currentContactState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	st, err := meta.GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(st.Contact, 10), nil
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
