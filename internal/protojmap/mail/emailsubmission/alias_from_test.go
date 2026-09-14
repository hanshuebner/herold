package emailsubmission

// EmailSubmission must never accept a JMAP Identity alias address as
// the actual From address that goes out on the wire (REQ-IDENT-01, re
// #387): aliases are match-only, selecting an identity as the reply
// sender, and are never themselves a send-from address. The identityId
// property always resolves to the identity's own primary email
// (methods.go's IdentityEmail resolver), so the only vector by which an
// alias could reach the wire as mailFrom is an explicit
// envelope.mailFrom override -- this test proves sendpolicy.CheckFrom
// still refuses that.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

func TestEmailSubmission_Set_EnvelopeMailFromAlias_Rejected(t *testing.T) {
	h, st, p, _, mid, sub := newSetup(t)
	ctx := context.Background()
	const idID = "id-with-alias"
	const alias = "alice-alias@example.test"
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:           idID,
		PrincipalID:  p.ID,
		Name:         "Alice",
		Email:        "alice@example.test",
		MayDelete:    true,
		VerifiedAtUs: 1,
		Aliases:      []string{alias},
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	h.identity = mapResolver{m: map[string]string{idID: "alice@example.test"}}

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"k1": map[string]any{
				"identityId": idID,
				"emailId":    renderEmailID(mid),
				"envelope": map[string]any{
					"mailFrom": map[string]any{"email": alias},
					"rcptTo":   []map[string]any{{"email": "bob@example.test"}},
				},
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("EmailSubmission/set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"forbiddenFrom"`) {
		t.Fatalf("expected forbiddenFrom for an alias used as envelope mailFrom: %s", js)
	}
	if strings.Contains(string(js), `"created"`) {
		t.Fatalf("alias must never be accepted as a From address: %s", js)
	}
	if len(sub.calls) != 0 {
		t.Fatalf("expected 0 queue submits, got %d", len(sub.calls))
	}
}
