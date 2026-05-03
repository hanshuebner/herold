package sieve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"path/filepath"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// newHandlers builds the handlerSet directly so tests can drive Execute
// without going through the full CapabilityRegistry round-trip. The
// fixture creates one principal and seeds the test ctx with it.
func newHandlers(t *testing.T) (*handlerSet, store.Store, store.Principal, context.Context) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
		DisplayName:    "Alice",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	h := &handlerSet{store: st, clk: clk}
	authCtx := contextWithTestPrincipal(ctx, p)
	return h, st, p, authCtx
}

// uploadBlob stores body in the blob store and returns its hash. The
// JMAP /upload path on a real server does this via HTTP; tests bypass
// it.
func uploadBlob(t *testing.T, st store.Store, body string) string {
	t.Helper()
	ref, err := st.Blobs().Put(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	return ref.Hash
}

// validScript exercises a feature-rich Sieve script the parser +
// validator both accept; used as the happy path body in /set + /validate.
const validScript = `require ["fileinto"];
if header :contains "Subject" "spam" {
  fileinto "Junk";
}
`

// invalidScript fails the parser (missing trailing semicolon → unknown
// state). The exact error doesn't matter; the test only asserts the
// /set surface routes parse/validate failures into sieveValidationError.
const invalidScript = `if header "Subject" "x" { fileinto "Folder"`

// normaliseLF collapses CRLF and CR to LF so callers compare scripts
// without coupling to whether the blob path canonicalised line endings.
func normaliseLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

func TestJMAPSieve_Get_Set_RoundTrip(t *testing.T) {
	h, st, p, ctx := newHandlers(t)
	blob := uploadBlob(t, st, validScript)

	// Initially empty: /get returns an empty list and state "0".
	getArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, mErr := getHandler{h: h}.Execute(ctx, getArgs)
	if mErr != nil {
		t.Fatalf("Sieve/get(initial): %v", mErr)
	}
	gr := resp.(getResponse)
	if len(gr.List) != 0 {
		t.Fatalf("initial list = %d, want 0", len(gr.List))
	}
	if gr.State != "0" {
		t.Fatalf("initial state = %q, want %q", gr.State, "0")
	}

	// Sieve/set create with the uploaded blob.
	setArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"new": map[string]any{"name": "main", "blobId": blob},
		},
	})
	sresp, mErr := setHandler{h: h}.Execute(ctx, setArgs)
	if mErr != nil {
		t.Fatalf("Sieve/set: %v", mErr)
	}
	sr := sresp.(setResponse)
	if len(sr.Created) != 1 {
		t.Fatalf("created = %v", sr.Created)
	}
	if len(sr.NotCreated) != 0 {
		t.Fatalf("notCreated unexpected: %v", sr.NotCreated)
	}
	if sr.NewState == sr.OldState {
		t.Fatalf("state not bumped: old=%s new=%s", sr.OldState, sr.NewState)
	}
	created := sr.Created["new"]
	if !created.IsActive {
		t.Fatalf("created script isActive = false")
	}

	// Persistence: GetSieveScript reflects the new body. The blob
	// store canonicalises line endings to CRLF on Put; we compare on
	// the LF-normalised form so the test does not couple to that
	// detail.
	got, err := st.Meta().GetSieveScript(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("GetSieveScript: %v", err)
	}
	if normaliseLF(got) != normaliseLF(validScript) {
		t.Fatalf("persisted script mismatch:\ngot=%q\nwant=%q", got, validScript)
	}

	// /get now returns the singleton.
	resp2, mErr := getHandler{h: h}.Execute(ctx, getArgs) // getArgs already has accountId
	if mErr != nil {
		t.Fatalf("Sieve/get(post): %v", mErr)
	}
	gr2 := resp2.(getResponse)
	if len(gr2.List) != 1 {
		t.Fatalf("post list = %d, want 1", len(gr2.List))
	}
	if gr2.List[0].ID != created.ID {
		t.Fatalf("ID round-trip: got %q, created %q", gr2.List[0].ID, created.ID)
	}
}

func TestJMAPSieve_Set_InvalidScript_Returns_sieveValidationError(t *testing.T) {
	h, st, p, ctx := newHandlers(t)
	blob := uploadBlob(t, st, invalidScript)
	setArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"bad": map[string]any{"blobId": blob},
		},
	})
	sresp, mErr := setHandler{h: h}.Execute(ctx, setArgs)
	if mErr != nil {
		t.Fatalf("Sieve/set: %v", mErr)
	}
	sr := sresp.(setResponse)
	if len(sr.Created) != 0 {
		t.Fatalf("invalid script unexpectedly created: %v", sr.Created)
	}
	se, ok := sr.NotCreated["bad"]
	if !ok {
		t.Fatalf("notCreated[bad] missing: %v", sr.NotCreated)
	}
	if se.Type != "sieveValidationError" {
		t.Fatalf("notCreated[bad].Type = %q, want sieveValidationError", se.Type)
	}
	if len(se.Errors) == 0 {
		t.Fatalf("expected non-empty errors list, got %v", se)
	}
	// State must NOT have been bumped on a no-op /set.
	if sr.NewState != sr.OldState {
		t.Fatalf("state bumped on failed-only /set: old=%s new=%s",
			sr.OldState, sr.NewState)
	}
}

func TestJMAPSieve_Validate_NoPersist(t *testing.T) {
	h, st, p, ctx := newHandlers(t)
	goodBlob := uploadBlob(t, st, validScript)
	badBlob := uploadBlob(t, st, invalidScript)

	// Good script → isValid true, errors empty.
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID), "blobId": goodBlob})
	resp, mErr := validateHandler{h: h}.Execute(ctx, args)
	if mErr != nil {
		t.Fatalf("Sieve/validate(good): %v", mErr)
	}
	vr := resp.(validateResponse)
	if !vr.IsValid {
		t.Fatalf("good script reported invalid: %+v", vr)
	}
	if len(vr.Errors) != 0 {
		t.Fatalf("good script errors: %v", vr.Errors)
	}

	// Bad script → isValid false, errors populated.
	args, _ = json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID), "blobId": badBlob})
	resp, mErr = validateHandler{h: h}.Execute(ctx, args)
	if mErr != nil {
		t.Fatalf("Sieve/validate(bad): %v", mErr)
	}
	vr = resp.(validateResponse)
	if vr.IsValid {
		t.Fatalf("bad script reported valid: %+v", vr)
	}
	if len(vr.Errors) == 0 {
		t.Fatalf("bad script no errors")
	}

	// Critically: nothing was persisted.
	got, err := st.Meta().GetSieveScript(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("GetSieveScript: %v", err)
	}
	if got != "" {
		t.Fatalf("validate persisted: %q", got)
	}
}
