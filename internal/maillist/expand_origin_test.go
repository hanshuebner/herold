package maillist_test

import (
	"context"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/store"
)

// TestExpand_SubmissionsAreNotAuthenticatedLocal (re #559): a list post
// is externally received bytes, so every per-member Submission leaves
// AuthenticatedLocal false and a loopback copy to a local member is
// ingested as inbound mail, never as an authenticated local submission.
func TestExpand_SubmissionsAreNotAuthenticatedLocal(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, st store.Store) {
		ml := mustInsertList(t, st, "list@example.test", false)
		mustAddExternalMember(t, st, ml.ID, "member1@example.net", store.MailingListMemberActive)
		mustAddExternalMember(t, st, ml.ID, "member2@example.test", store.MailingListMemberActive)
		sub := &fakeSubmitter{}
		exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
		if _, err := exp.Expand(context.Background(), maillist.ExpandInput{
			List:   ml,
			Parsed: mustParse(t, testMessage),
			Raw:    []byte(testMessage),
		}); err != nil {
			t.Fatalf("Expand: %v", err)
		}
		calls := sub.Calls()
		if len(calls) != 2 {
			t.Fatalf("Submit calls = %d, want 2", len(calls))
		}
		for _, c := range calls {
			if c.AuthenticatedLocal {
				t.Errorf("Submission to %v has AuthenticatedLocal = true; list fan-out relays external bytes", c.Recipients)
			}
		}
	})
}
