package imapimport

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// testDataKey is a 32-byte key used across tests. It must not be used
// outside of tests.
var testDataKey = []byte("test-key-must-be-exactly-32bytes")

// fakeNow is the reference instant used by the test fake clock.
var fakeNow = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := storesqlite.Open(context.Background(),
		filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(fakeNow))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newHandlers creates a handlerSet plus one seeded principal.
func newHandlers(t *testing.T) (*handlerSet, store.Store, store.Principal) {
	t.Helper()
	st := newTestStore(t)
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
	h := &handlerSet{
		store:   st,
		clk:     clock.NewFake(fakeNow),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		dataKey: testDataKey,
	}
	return h, st, p
}

func accountID(p store.Principal) string {
	return string(protojmap.AccountIDForPrincipal(p.ID))
}

// newSecondPrincipal inserts a second principal into an existing store.
func newSecondPrincipal(t *testing.T, st store.Store) store.Principal {
	t.Helper()
	p, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bob@example.test",
		DisplayName:    "Bob",
	})
	if err != nil {
		t.Fatalf("insert second principal: %v", err)
	}
	return p
}

// minimalCreate returns a valid create payload map for one account.
func minimalCreate(clientID string) map[string]any {
	return map[string]any{
		clientID: map[string]any{
			"accountName":     "My Gmail",
			"host":            "imap.gmail.com",
			"port":            993,
			"tlsMode":         "implicit",
			"username":        "user@gmail.com",
			"authMethod":      "password",
			"backfillHorizon": "30d",
			"credential":      "s3cr3t-p4ssw0rd",
		},
	}
}

// doCreate calls IMAPImport/set create and returns the setResponse.
func doCreate(t *testing.T, h *handlerSet, p store.Principal, creates map[string]any) setResponse {
	t.Helper()
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create":    creates,
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set create: %v", merr)
	}
	return r.(setResponse)
}

// -- IMAPImport/get ---------------------------------------------------

func TestGet_EmptyList(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	r, merr := getHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("get: %v", merr)
	}
	resp := r.(getResponse)
	if len(resp.List) != 0 {
		t.Fatalf("expected empty list, got %+v", resp.List)
	}
	if resp.State == "" {
		t.Fatalf("state must be non-empty")
	}
	if resp.AccountID != accountID(p) {
		t.Fatalf("accountId mismatch: got %q, want %q", resp.AccountID, accountID(p))
	}
}

// TestGet_CredentialNeverReturned asserts that after a create the
// wire-form list items never contain a credential field.
func TestGet_CredentialNeverReturned(t *testing.T) {
	h, _, p := newHandlers(t)

	// Create one account.
	resp := doCreate(t, h, p, minimalCreate("c1"))
	if len(resp.NotCreated) != 0 {
		t.Fatalf("unexpected notCreated: %v", resp.NotCreated)
	}

	// Fetch via get.
	args, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	r, merr := getHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("get: %v", merr)
	}
	gotResp := r.(getResponse)
	if len(gotResp.List) != 1 {
		t.Fatalf("expected 1 account, got %d", len(gotResp.List))
	}
	wire, _ := json.Marshal(gotResp.List[0])
	wireStr := string(wire)
	// No credential or sealed ciphertext must appear in the wire output.
	for _, banned := range []string{"credential", "credentialCt", "credential_ct"} {
		if strings.Contains(wireStr, banned) {
			t.Fatalf("credential must never appear in wire output; found %q in %s", banned, wireStr)
		}
	}
	// HasCredential must be true because we stored a credential at create.
	if !gotResp.List[0].HasCredential {
		t.Fatalf("hasCredential must be true after create with credential")
	}
}

func TestGet_RejectsForeignAccount(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{"accountId": "not-mine"})
	_, merr := getHandler{h: h}.executeAs(p, args)
	if merr == nil || merr.Type != "accountNotFound" {
		t.Fatalf("expected accountNotFound, got %v", merr)
	}
}

func TestGet_ByIDs_NotFound(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"ids":       []string{"does-not-exist"},
	})
	r, merr := getHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("get: %v", merr)
	}
	resp := r.(getResponse)
	if len(resp.NotFound) != 1 || resp.NotFound[0] != "does-not-exist" {
		t.Fatalf("expected NotFound[does-not-exist], got %+v", resp.NotFound)
	}
}

// -- IMAPImport/set create -------------------------------------------

func TestCreate_BasicFlow(t *testing.T) {
	h, _, p := newHandlers(t)
	resp := doCreate(t, h, p, minimalCreate("c1"))
	if len(resp.NotCreated) != 0 {
		t.Fatalf("unexpected notCreated: %v", resp.NotCreated)
	}
	created, ok := resp.Created["c1"]
	if !ok {
		t.Fatalf("c1 not in Created")
	}
	if created.ID == "" {
		t.Fatalf("created.ID must not be empty")
	}
	if created.AccountName != "My Gmail" {
		t.Fatalf("accountName mismatch: %q", created.AccountName)
	}
	if created.Host != "imap.gmail.com" {
		t.Fatalf("host mismatch: %q", created.Host)
	}
	if created.Port != 993 {
		t.Fatalf("port mismatch: %d", created.Port)
	}
	if created.TLSMode != "implicit" {
		t.Fatalf("tlsMode mismatch: %q", created.TLSMode)
	}
	if created.AuthMethod != "password" {
		t.Fatalf("authMethod mismatch: %q", created.AuthMethod)
	}
}

// TestCreate_CredentialSealed asserts that the stored credential_ct is
// v1:-prefixed and that decryption with the test key yields the original
// plaintext.
func TestCreate_CredentialSealed(t *testing.T) {
	h, st, p := newHandlers(t)

	resp := doCreate(t, h, p, minimalCreate("c1"))
	if _, ok := resp.Created["c1"]; !ok {
		t.Fatalf("c1 not in Created; notCreated=%v", resp.NotCreated)
	}
	createdID := resp.Created["c1"].ID

	// Retrieve the raw row directly from the store to inspect credential_ct.
	row, err := st.Meta().GetIMAPImportAccount(context.Background(), createdID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}

	// Must carry v1: prefix.
	if !strings.HasPrefix(string(row.CredentialCT), "v1:") {
		t.Fatalf("credential_ct must start with v1:, got %q", row.CredentialCT[:min(10, len(row.CredentialCT))])
	}

	// Must decrypt back to the original plaintext.
	plain, err := secrets.Open(testDataKey, row.CredentialCT)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	if string(plain) != "s3cr3t-p4ssw0rd" {
		t.Fatalf("decrypted credential = %q, want %q", plain, "s3cr3t-p4ssw0rd")
	}
}

// TestCreate_HorizonRelative30d checks that "30d" resolves to
// fakeNow - 30 days, truncated to midnight UTC.
func TestCreate_HorizonRelative30d(t *testing.T) {
	h, st, p := newHandlers(t)

	resp := doCreate(t, h, p, minimalCreate("c1"))
	if _, ok := resp.Created["c1"]; !ok {
		t.Fatalf("c1 not in Created; notCreated=%v", resp.NotCreated)
	}
	createdID := resp.Created["c1"].ID

	row, err := st.Meta().GetIMAPImportAccount(context.Background(), createdID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if row.BackfillFloorDate == nil {
		t.Fatalf("BackfillFloorDate must not be nil for 30d horizon")
	}
	expected := fakeNow.Add(-30 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	if !row.BackfillFloorDate.Equal(expected) {
		t.Fatalf("BackfillFloorDate = %v, want %v", *row.BackfillFloorDate, expected)
	}
	// Wire form should also carry the absolute date string.
	want := expected.Format("2006-01-02")
	if resp.Created["c1"].BackfillHorizon != want {
		t.Fatalf("wire BackfillHorizon = %q, want %q", resp.Created["c1"].BackfillHorizon, want)
	}
}

// TestCreate_HorizonAll checks that "all" stores a nil floor date and
// the wire form reports "all".
func TestCreate_HorizonAll(t *testing.T) {
	h, st, p := newHandlers(t)

	creates := map[string]any{
		"c1": map[string]any{
			"accountName":     "All horizon account",
			"host":            "imap.example.com",
			"port":            993,
			"tlsMode":         "implicit",
			"username":        "user@example.com",
			"authMethod":      "password",
			"backfillHorizon": "all",
			"credential":      "pass",
		},
	}
	resp := doCreate(t, h, p, creates)
	if _, ok := resp.Created["c1"]; !ok {
		t.Fatalf("c1 not in Created; notCreated=%v", resp.NotCreated)
	}
	createdID := resp.Created["c1"].ID

	row, err := st.Meta().GetIMAPImportAccount(context.Background(), createdID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if row.BackfillFloorDate != nil {
		t.Fatalf("BackfillFloorDate must be nil for horizon=all, got %v", *row.BackfillFloorDate)
	}
	if resp.Created["c1"].BackfillHorizon != "all" {
		t.Fatalf("wire BackfillHorizon = %q, want %q", resp.Created["c1"].BackfillHorizon, "all")
	}
}

// TestCreate_MissingRequiredFields verifies that missing required fields
// produce notCreated with invalidProperties.
func TestCreate_MissingRequiredFields(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				// intentionally omit most required fields
				"accountName": "Only Name",
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r.(setResponse)
	e, ok := resp.NotCreated["c1"]
	if !ok {
		t.Fatalf("expected notCreated[c1]")
	}
	if e.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties, got %q", e.Type)
	}
}

// TestCreate_InvalidTLSMode verifies that an unsupported tlsMode value
// is rejected.
func TestCreate_InvalidTLSMode(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"accountName":     "Bad TLS",
				"host":            "imap.example.com",
				"port":            993,
				"tlsMode":         "none", // rejected per REQ-IMAP-IMP-06
				"username":        "user",
				"authMethod":      "password",
				"backfillHorizon": "30d",
				"credential":      "pass",
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r.(setResponse)
	e, ok := resp.NotCreated["c1"]
	if !ok {
		t.Fatalf("expected notCreated[c1]")
	}
	if e.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties, got %q", e.Type)
	}
	if len(e.Properties) == 0 || e.Properties[0] != "tlsMode" {
		t.Fatalf("expected Properties=[tlsMode], got %v", e.Properties)
	}
}

// TestCreate_InvalidAuthMethod verifies that an unsupported authMethod
// value is rejected.
func TestCreate_InvalidAuthMethod(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"accountName":     "Bad Auth",
				"host":            "imap.example.com",
				"port":            993,
				"tlsMode":         "implicit",
				"username":        "user",
				"authMethod":      "kerberos", // not a valid value
				"backfillHorizon": "30d",
				"credential":      "pass",
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r.(setResponse)
	e, ok := resp.NotCreated["c1"]
	if !ok {
		t.Fatalf("expected notCreated[c1]")
	}
	if e.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties, got %q", e.Type)
	}
	if len(e.Properties) == 0 || e.Properties[0] != "authMethod" {
		t.Fatalf("expected Properties=[authMethod], got %v", e.Properties)
	}
}

// TestCreate_InvalidBackfillHorizon verifies that an unrecognised
// backfillHorizon form is rejected.
func TestCreate_InvalidBackfillHorizon(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"accountName":     "Bad Horizon",
				"host":            "imap.example.com",
				"port":            993,
				"tlsMode":         "implicit",
				"username":        "user",
				"authMethod":      "password",
				"backfillHorizon": "forever", // not a valid form
				"credential":      "pass",
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r.(setResponse)
	e, ok := resp.NotCreated["c1"]
	if !ok {
		t.Fatalf("expected notCreated[c1]")
	}
	if e.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties, got %q", e.Type)
	}
	if len(e.Properties) == 0 || e.Properties[0] != "backfillHorizon" {
		t.Fatalf("expected Properties=[backfillHorizon], got %v", e.Properties)
	}
}

// TestCreate_InvalidPort verifies that an out-of-range port is rejected.
func TestCreate_InvalidPort(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"accountName":     "Bad Port",
				"host":            "imap.example.com",
				"port":            99999,
				"tlsMode":         "implicit",
				"username":        "user",
				"authMethod":      "password",
				"backfillHorizon": "30d",
				"credential":      "pass",
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r.(setResponse)
	e, ok := resp.NotCreated["c1"]
	if !ok {
		t.Fatalf("expected notCreated[c1]")
	}
	if e.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties, got %q", e.Type)
	}
	if len(e.Properties) == 0 || e.Properties[0] != "port" {
		t.Fatalf("expected Properties=[port], got %v", e.Properties)
	}
}

// -- IMAPImport/set update -------------------------------------------

// TestUpdate_PreservesCredentialWhenOmitted verifies that an update
// patch without a credential field leaves the sealed credential_ct
// unchanged in the store.
func TestUpdate_PreservesCredentialWhenOmitted(t *testing.T) {
	h, st, p := newHandlers(t)

	resp := doCreate(t, h, p, minimalCreate("c1"))
	createdID := resp.Created["c1"].ID

	rowBefore, err := st.Meta().GetIMAPImportAccount(context.Background(), createdID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount before: %v", err)
	}
	ctBefore := make([]byte, len(rowBefore.CredentialCT))
	copy(ctBefore, rowBefore.CredentialCT)

	// Update accountName only; no credential field in the patch.
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			createdID: map[string]any{"accountName": "Renamed Gmail"},
		},
	})
	ur, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("update: %v", merr)
	}
	resp2 := ur.(setResponse)
	if len(resp2.NotUpdated) != 0 {
		t.Fatalf("unexpected notUpdated: %v", resp2.NotUpdated)
	}

	rowAfter, err := st.Meta().GetIMAPImportAccount(context.Background(), createdID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount after: %v", err)
	}
	// credential_ct must be byte-for-byte identical.
	if string(rowAfter.CredentialCT) != string(ctBefore) {
		t.Fatalf("credential_ct changed unexpectedly after update without credential")
	}
	// The name should have changed.
	if rowAfter.AccountName != "Renamed Gmail" {
		t.Fatalf("accountName = %q, want %q", rowAfter.AccountName, "Renamed Gmail")
	}
}

// TestUpdate_ResealsCredentialWhenProvided verifies that supplying a
// new credential in the patch replaces the sealed ciphertext.
func TestUpdate_ResealsCredentialWhenProvided(t *testing.T) {
	h, st, p := newHandlers(t)

	resp := doCreate(t, h, p, minimalCreate("c1"))
	createdID := resp.Created["c1"].ID

	rowBefore, err := st.Meta().GetIMAPImportAccount(context.Background(), createdID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount before: %v", err)
	}
	ctBefore := make([]byte, len(rowBefore.CredentialCT))
	copy(ctBefore, rowBefore.CredentialCT)

	// Update with a new credential.
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			createdID: map[string]any{"credential": "new-password-here"},
		},
	})
	ur, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("update: %v", merr)
	}
	resp2 := ur.(setResponse)
	if len(resp2.NotUpdated) != 0 {
		t.Fatalf("unexpected notUpdated: %v", resp2.NotUpdated)
	}

	rowAfter, err := st.Meta().GetIMAPImportAccount(context.Background(), createdID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount after: %v", err)
	}

	// credential_ct must have changed.
	if string(rowAfter.CredentialCT) == string(ctBefore) {
		t.Fatalf("credential_ct did not change after update with new credential")
	}
	// New ciphertext must carry v1: prefix.
	if !strings.HasPrefix(string(rowAfter.CredentialCT), "v1:") {
		t.Fatalf("new credential_ct missing v1: prefix")
	}
	// Must decrypt to the new plaintext.
	plain, err := secrets.Open(testDataKey, rowAfter.CredentialCT)
	if err != nil {
		t.Fatalf("secrets.Open new ct: %v", err)
	}
	if string(plain) != "new-password-here" {
		t.Fatalf("decrypted new credential = %q, want %q", plain, "new-password-here")
	}
}

// TestUpdate_RejectImmutableField verifies that passing an unknown /
// immutable property in an update patch produces notUpdated with
// invalidProperties.
func TestUpdate_RejectImmutableField(t *testing.T) {
	h, _, p := newHandlers(t)

	resp := doCreate(t, h, p, minimalCreate("c1"))
	createdID := resp.Created["c1"].ID

	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			createdID: map[string]any{
				"id": "override-id-attempt",
			},
		},
	})
	ur, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp2 := ur.(setResponse)
	e, ok := resp2.NotUpdated[createdID]
	if !ok {
		t.Fatalf("expected notUpdated[%s]", createdID)
	}
	if e.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties, got %q", e.Type)
	}
}

// TestUpdate_MigratingTransition exercises the complete-migration cutover
// transitions on the JMAP surface (REQ-IMAP-IMP-90/91/95): starting migration
// from enabled forces the backfill floor to "all"; setting migrated directly
// is rejected; starting migration from a non-enabled state is rejected; and a
// migrated account may be re-opened to enabled.
func TestUpdate_MigratingTransition(t *testing.T) {
	h, st, p := newHandlers(t)
	ctx := context.Background()

	resp := doCreate(t, h, p, minimalCreate("c1"))
	createdID := resp.Created["c1"].ID

	// Precondition: a 30d horizon resolves to a non-nil floor.
	row0, _ := st.Meta().GetIMAPImportAccount(ctx, createdID)
	if row0.BackfillFloorDate == nil {
		t.Fatal("precondition: 30d horizon should produce a non-nil floor")
	}

	setState := func(id, state string) setResponse {
		t.Helper()
		args, _ := json.Marshal(map[string]any{
			"accountId": accountID(p),
			"update":    map[string]any{id: map[string]any{"state": state}},
		})
		r, merr := setHandler{h: h}.executeAs(p, args)
		if merr != nil {
			t.Fatalf("set state=%s: %v", state, merr)
		}
		return r.(setResponse)
	}

	// enabled -> migrating: accepted; floor forced to "all" (nil).
	if r := setState(createdID, "migrating"); len(r.NotUpdated) != 0 {
		t.Fatalf("enabled->migrating rejected: %v", r.NotUpdated)
	}
	rowM, _ := st.Meta().GetIMAPImportAccount(ctx, createdID)
	if rowM.State != store.IMAPImportAccountStateMigrating {
		t.Errorf("state = %q; want migrating", rowM.State)
	}
	if rowM.BackfillFloorDate != nil {
		t.Errorf("backfill floor = %v; want nil (forced to all on migrating)", rowM.BackfillFloorDate)
	}

	// Direct set to migrated is rejected (worker-only transition).
	resp2 := doCreate(t, h, p, map[string]any{"c2": minimalCreate("c2")["c2"]})
	id2 := resp2.Created["c2"].ID
	if r := setState(id2, "migrated"); r.NotUpdated[jmapID(id2)].Type != "invalidProperties" {
		t.Errorf("direct set migrated should be invalidProperties, got %+v", r.NotUpdated)
	}

	// migrating from a non-enabled (disabled) state is rejected.
	if r := setState(id2, "disabled"); len(r.NotUpdated) != 0 {
		t.Fatalf("->disabled rejected: %v", r.NotUpdated)
	}
	if r := setState(id2, "migrating"); r.NotUpdated[jmapID(id2)].Type != "invalidProperties" {
		t.Errorf("migrating from disabled should be invalidProperties, got %+v", r.NotUpdated)
	}

	// Re-open: migrated -> enabled is accepted (REQ-IMAP-IMP-95). Put the
	// account into migrated via the store (the worker's terminal transition).
	if err := st.Meta().SetIMAPImportAccountState(ctx, createdID,
		store.IMAPImportAccountStateMigrated, "", nil); err != nil {
		t.Fatalf("set migrated via store: %v", err)
	}
	if r := setState(createdID, "enabled"); len(r.NotUpdated) != 0 {
		t.Fatalf("re-open migrated->enabled rejected: %v", r.NotUpdated)
	}
	rowR, _ := st.Meta().GetIMAPImportAccount(ctx, createdID)
	if rowR.State != store.IMAPImportAccountStateEnabled {
		t.Errorf("re-opened state = %q; want enabled", rowR.State)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			"nonexistent-id": map[string]any{"accountName": "x"},
		},
	})
	ur, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := ur.(setResponse)
	e, ok := resp.NotUpdated["nonexistent-id"]
	if !ok {
		t.Fatalf("expected notUpdated[nonexistent-id]")
	}
	if e.Type != "notFound" {
		t.Fatalf("expected notFound, got %q", e.Type)
	}
}

// -- IMAPImport/set destroy ------------------------------------------

// TestDestroy_Basic creates an account, destroys it, then verifies it
// no longer appears in /get.
func TestDestroy_Basic(t *testing.T) {
	h, _, p := newHandlers(t)

	resp := doCreate(t, h, p, minimalCreate("c1"))
	createdID := resp.Created["c1"].ID

	destroyArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"destroy":   []string{createdID},
	})
	dr, merr := setHandler{h: h}.executeAs(p, destroyArgs)
	if merr != nil {
		t.Fatalf("destroy: %v", merr)
	}
	resp2 := dr.(setResponse)
	if len(resp2.NotDestroyed) != 0 {
		t.Fatalf("unexpected notDestroyed: %v", resp2.NotDestroyed)
	}
	if len(resp2.Destroyed) != 1 || resp2.Destroyed[0] != createdID {
		t.Fatalf("expected Destroyed[%q], got %v", createdID, resp2.Destroyed)
	}

	// Verify gone from /get.
	getArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"ids":       []string{createdID},
	})
	gr, _ := getHandler{h: h}.executeAs(p, getArgs)
	gr2 := gr.(getResponse)
	if len(gr2.NotFound) != 1 || gr2.NotFound[0] != createdID {
		t.Fatalf("expected NotFound after destroy, got %+v", gr2)
	}
}

func TestDestroy_NotFound(t *testing.T) {
	h, _, p := newHandlers(t)
	destroyArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"destroy":   []string{"nonexistent-id"},
	})
	dr, merr := setHandler{h: h}.executeAs(p, destroyArgs)
	if merr != nil {
		t.Fatalf("destroy: %v", merr)
	}
	resp := dr.(setResponse)
	e, ok := resp.NotDestroyed["nonexistent-id"]
	if !ok {
		t.Fatalf("expected notDestroyed[nonexistent-id]")
	}
	if e.Type != "notFound" {
		t.Fatalf("expected notFound, got %q", e.Type)
	}
}

// -- Cross-principal access ------------------------------------------

// TestCrossPrincipal_GetRejectsOtherPrincipal verifies that principal
// B cannot issue a /get request using principal A's accountId.
func TestCrossPrincipal_GetRejectsOtherPrincipal(t *testing.T) {
	h, st, alice := newHandlers(t)
	bob := newSecondPrincipal(t, st)

	// Alice creates an account.
	resp := doCreate(t, h, alice, minimalCreate("c1"))

	// Bob tries to get using Alice's accountId.
	args, _ := json.Marshal(map[string]any{"accountId": accountID(alice)})
	_, merr := getHandler{h: h}.executeAs(bob, args)
	if merr == nil || merr.Type != "accountNotFound" {
		t.Fatalf("expected accountNotFound when Bob requests Alice's accountId, got %v", merr)
	}
	_ = resp
}

// TestCrossPrincipal_DestroyRejectsOtherPrincipal verifies that
// principal B cannot destroy principal A's account.
func TestCrossPrincipal_DestroyRejectsOtherPrincipal(t *testing.T) {
	h, st, alice := newHandlers(t)
	bob := newSecondPrincipal(t, st)

	// Alice creates an account.
	resp := doCreate(t, h, alice, minimalCreate("c1"))
	aliceAccountID := resp.Created["c1"].ID

	// Bob tries to destroy using his own accountId but Alice's record ID.
	destroyArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(bob),
		"destroy":   []string{aliceAccountID},
	})
	dr, merr := setHandler{h: h}.executeAs(bob, destroyArgs)
	if merr != nil {
		t.Fatalf("destroy: unexpected method error %v", merr)
	}
	resp2 := dr.(setResponse)
	// The destroy must be rejected: Alice's ID is invisible to Bob, so it
	// appears as notFound.
	if _, rejected := resp2.NotDestroyed[aliceAccountID]; !rejected {
		t.Fatalf("expected aliceAccountID to be in NotDestroyed when destroyed as Bob, got destroyed=%v", resp2.Destroyed)
	}
}

// TestCrossPrincipal_UpdateRejectsOtherPrincipal verifies that
// principal B cannot update principal A's account.
func TestCrossPrincipal_UpdateRejectsOtherPrincipal(t *testing.T) {
	h, st, alice := newHandlers(t)
	bob := newSecondPrincipal(t, st)

	resp := doCreate(t, h, alice, minimalCreate("c1"))
	aliceAccountID := resp.Created["c1"].ID

	updateArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(bob),
		"update": map[string]any{
			aliceAccountID: map[string]any{"accountName": "hijacked"},
		},
	})
	ur, merr := setHandler{h: h}.executeAs(bob, updateArgs)
	if merr != nil {
		t.Fatalf("update: unexpected method error %v", merr)
	}
	resp2 := ur.(setResponse)
	if _, rejected := resp2.NotUpdated[aliceAccountID]; !rejected {
		t.Fatalf("expected aliceAccountID to be in NotUpdated when updated as Bob, got updated=%v", resp2.Updated)
	}
}

// -- Round-trip: create + get consistency ----------------------------

func TestRoundTrip_CreateGetConsistency(t *testing.T) {
	h, _, p := newHandlers(t)

	resp := doCreate(t, h, p, minimalCreate("c1"))
	if len(resp.NotCreated) != 0 {
		t.Fatalf("unexpected notCreated: %v", resp.NotCreated)
	}
	createdID := resp.Created["c1"].ID
	created := resp.Created["c1"]

	// Fetch by ID via /get.
	getArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"ids":       []string{createdID},
	})
	gr, merr := getHandler{h: h}.executeAs(p, getArgs)
	if merr != nil {
		t.Fatalf("get: %v", merr)
	}
	got := gr.(getResponse)
	if len(got.List) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got.List))
	}
	g := got.List[0]

	// Non-secret fields from create should match /get.
	if g.ID != created.ID {
		t.Fatalf("id: create=%q get=%q", created.ID, g.ID)
	}
	if g.AccountName != created.AccountName {
		t.Fatalf("accountName: create=%q get=%q", created.AccountName, g.AccountName)
	}
	if g.Host != created.Host {
		t.Fatalf("host: create=%q get=%q", created.Host, g.Host)
	}
	if g.Port != created.Port {
		t.Fatalf("port: create=%d get=%d", created.Port, g.Port)
	}
	if g.TLSMode != created.TLSMode {
		t.Fatalf("tlsMode: create=%q get=%q", created.TLSMode, g.TLSMode)
	}
	if g.Username != created.Username {
		t.Fatalf("username: create=%q get=%q", created.Username, g.Username)
	}
	if g.AuthMethod != created.AuthMethod {
		t.Fatalf("authMethod: create=%q get=%q", created.AuthMethod, g.AuthMethod)
	}
	if g.BackfillHorizon != created.BackfillHorizon {
		t.Fatalf("backfillHorizon: create=%q get=%q", created.BackfillHorizon, g.BackfillHorizon)
	}
	if g.HasCredential != created.HasCredential {
		t.Fatalf("hasCredential: create=%v get=%v", created.HasCredential, g.HasCredential)
	}
}

// -- State envelope --------------------------------------------------

func TestStateEnvelope(t *testing.T) {
	h, _, p := newHandlers(t)

	// Get initial state.
	getArgs, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	gr, _ := getHandler{h: h}.executeAs(p, getArgs)
	initialState := gr.(getResponse).State
	if initialState == "" {
		t.Fatalf("initial state must not be empty")
	}

	// Create returns oldState and newState.
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create":    minimalCreate("c1"),
	})
	sr, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	setResp := sr.(setResponse)
	if setResp.OldState == "" {
		t.Fatalf("set response must include oldState")
	}
	if setResp.NewState == "" {
		t.Fatalf("set response must include newState")
	}
}

// -- IMAPImport/changes ----------------------------------------------

// doChanges calls IMAPImport/changes with the given sinceState and returns
// the response.
func doChanges(t *testing.T, h *handlerSet, p store.Principal, since string) changesResponse {
	t.Helper()
	args, _ := json.Marshal(map[string]any{
		"accountId":  accountID(p),
		"sinceState": since,
	})
	r, merr := changesHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("changes: %v", merr)
	}
	return r.(changesResponse)
}

// TestState_AdvancesOnMutation verifies that the per-principal IMAPImport
// state counter advances on a create / update / destroy and that a no-op set
// leaves it unchanged (REQ-IMAP-IMP-61, migration 0065).
func TestState_AdvancesOnMutation(t *testing.T) {
	h, _, p := newHandlers(t)

	getArgs, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	gr, _ := getHandler{h: h}.executeAs(p, getArgs)
	s0 := gr.(getResponse).State
	if s0 != "0" {
		t.Fatalf("initial state = %q, want 0", s0)
	}

	// Create bumps the state.
	cresp := doCreate(t, h, p, minimalCreate("c1"))
	if len(cresp.NotCreated) != 0 {
		t.Fatalf("unexpected notCreated: %v", cresp.NotCreated)
	}
	if cresp.OldState != "0" || cresp.NewState == cresp.OldState {
		t.Fatalf("create state envelope did not advance: old=%q new=%q", cresp.OldState, cresp.NewState)
	}
	id := cresp.Created["c1"].ID
	afterCreate := cresp.NewState

	// A set that mutates nothing (update of an unknown id) does NOT advance.
	noopArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update":    map[string]any{"does-not-exist": map[string]any{"accountName": "x"}},
	})
	nr, merr := setHandler{h: h}.executeAs(p, noopArgs)
	if merr != nil {
		t.Fatalf("noop set: %v", merr)
	}
	if got := nr.(setResponse).NewState; got != afterCreate {
		t.Fatalf("no-op set advanced state: %q -> %q", afterCreate, got)
	}

	// Update bumps the state.
	upArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update":    map[string]any{id: map[string]any{"accountName": "Renamed"}},
	})
	ur, merr := setHandler{h: h}.executeAs(p, upArgs)
	if merr != nil {
		t.Fatalf("update set: %v", merr)
	}
	afterUpdate := ur.(setResponse).NewState
	if afterUpdate == afterCreate {
		t.Fatalf("update did not advance state: still %q", afterUpdate)
	}

	// Destroy bumps the state.
	delArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"destroy":   []string{id},
	})
	dr, merr := setHandler{h: h}.executeAs(p, delArgs)
	if merr != nil {
		t.Fatalf("destroy set: %v", merr)
	}
	if dr.(setResponse).NewState == afterUpdate {
		t.Fatalf("destroy did not advance state: still %q", afterUpdate)
	}
}

// TestChanges_NoChangeWhenInSync verifies that IMAPImport/changes reports no
// changes when sinceState equals the current state (REQ-IMAP-IMP-61).
func TestChanges_NoChangeWhenInSync(t *testing.T) {
	h, _, p := newHandlers(t)

	getArgs, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	gr, _ := getHandler{h: h}.executeAs(p, getArgs)
	cur := gr.(getResponse).State

	resp := doChanges(t, h, p, cur)
	if resp.OldState != cur || resp.NewState != cur {
		t.Fatalf("state envelope: old=%q new=%q want %q", resp.OldState, resp.NewState, cur)
	}
	if len(resp.Created) != 0 || len(resp.Updated) != 0 || len(resp.Destroyed) != 0 {
		t.Fatalf("expected no changes; got created=%v updated=%v destroyed=%v",
			resp.Created, resp.Updated, resp.Destroyed)
	}
	if resp.HasMoreChanges {
		t.Fatalf("hasMoreChanges should be false")
	}
}

// TestChanges_ReportsDriftedAccounts verifies that when the client's
// sinceState is behind the current state, IMAPImport/changes reports every
// account as updated so the client re-fetches (REQ-IMAP-IMP-61).
func TestChanges_ReportsDriftedAccounts(t *testing.T) {
	h, _, p := newHandlers(t)

	// Snapshot the empty state, then create two accounts.
	getArgs, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	gr, _ := getHandler{h: h}.executeAs(p, getArgs)
	stale := gr.(getResponse).State

	c1 := doCreate(t, h, p, minimalCreate("c1"))
	id1 := c1.Created["c1"].ID
	c2args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{"c2": map[string]any{
			"accountName":     "Second",
			"host":            "imap.example.com",
			"port":            993,
			"tlsMode":         "implicit",
			"username":        "user2",
			"authMethod":      "password",
			"backfillHorizon": "30d",
			"credential":      "pw2",
		}},
	})
	c2r, merr := setHandler{h: h}.executeAs(p, c2args)
	if merr != nil {
		t.Fatalf("create c2: %v", merr)
	}
	id2 := c2r.(setResponse).Created["c2"].ID

	resp := doChanges(t, h, p, stale)
	if resp.OldState != stale {
		t.Fatalf("oldState = %q, want %q", resp.OldState, stale)
	}
	if resp.NewState == stale {
		t.Fatalf("newState did not advance from %q", stale)
	}
	got := map[string]bool{}
	for _, id := range resp.Updated {
		got[id] = true
	}
	if !got[id1] || !got[id2] || len(resp.Updated) != 2 {
		t.Fatalf("expected both accounts reported updated; got %v (want %s, %s)",
			resp.Updated, id1, id2)
	}
}

// TestChanges_RequiresSinceState verifies the sinceState argument is required.
func TestChanges_RequiresSinceState(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	_, merr := changesHandler{h: h}.executeAs(p, args)
	if merr == nil {
		t.Fatalf("expected error for missing sinceState")
	}
}

// -- Self-service folder map (REQ-IMAP-IMP-10/11/61) -----------------

// getFolderMap loads the wire-form folder map for the principal's single
// account via IMAPImport/get.
func getFolderMap(t *testing.T, h *handlerSet, p store.Principal) []jmapFolderMapEntry {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	r, merr := getHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("get: %v", merr)
	}
	resp := r.(getResponse)
	if len(resp.List) != 1 {
		t.Fatalf("expected 1 account, got %d", len(resp.List))
	}
	return resp.List[0].FolderMap
}

// folderMapAsMap flattens wire entries into upstream->herold for assertions.
func folderMapAsMap(entries []jmapFolderMapEntry) map[string]string {
	m := map[string]string{}
	for _, e := range entries {
		m[e.UpstreamFolder] = e.HeroldMailboxName
	}
	return m
}

// TestFolderMap_CreateAndEcho verifies that a folderMap supplied on create is
// persisted and echoed back on the created object and on get.
func TestFolderMap_CreateAndEcho(t *testing.T) {
	h, st, p := newHandlers(t)

	creates := map[string]any{
		"c1": map[string]any{
			"accountName":     "With map",
			"host":            "imap.example.com",
			"port":            993,
			"tlsMode":         "implicit",
			"username":        "user",
			"authMethod":      "password",
			"backfillHorizon": "30d",
			"credential":      "pw",
			"folderMap": []map[string]string{
				{"upstreamFolder": "Sent Mail", "heroldMailboxName": "Sent"},
				{"upstreamFolder": "Bin", "heroldMailboxName": "Trash"},
			},
		},
	}
	resp := doCreate(t, h, p, creates)
	created, ok := resp.Created["c1"]
	if !ok {
		t.Fatalf("c1 not created; notCreated=%v", resp.NotCreated)
	}
	if got := folderMapAsMap(created.FolderMap); got["Sent Mail"] != "Sent" || got["Bin"] != "Trash" {
		t.Fatalf("created folderMap = %v", got)
	}
	// Persisted in the store.
	stored, err := st.Meta().GetIMAPImportFolderMap(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportFolderMap: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored folder map len = %d, want 2", len(stored))
	}
	// Echoed on get.
	if got := folderMapAsMap(getFolderMap(t, h, p)); got["Sent Mail"] != "Sent" {
		t.Fatalf("get folderMap = %v", got)
	}
}

// TestFolderMap_UpdateReplaces verifies an update folderMap replaces the
// existing map, and that an explicit empty array clears it while an absent
// folderMap leaves it untouched.
func TestFolderMap_UpdateReplaces(t *testing.T) {
	h, _, p := newHandlers(t)
	resp := doCreate(t, h, p, map[string]any{
		"c1": map[string]any{
			"accountName":     "Acct",
			"host":            "imap.example.com",
			"port":            993,
			"tlsMode":         "implicit",
			"username":        "user",
			"authMethod":      "password",
			"backfillHorizon": "30d",
			"credential":      "pw",
			"folderMap": []map[string]string{
				{"upstreamFolder": "Old", "heroldMailboxName": "OldHerold"},
			},
		},
	})
	id := resp.Created["c1"].ID

	// Update with a new map: replaces.
	upArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{id: map[string]any{
			"folderMap": []map[string]string{
				{"upstreamFolder": "New", "heroldMailboxName": "NewHerold"},
			},
		}},
	})
	if _, merr := (setHandler{h: h}).executeAs(p, upArgs); merr != nil {
		t.Fatalf("update: %v", merr)
	}
	got := folderMapAsMap(getFolderMap(t, h, p))
	if _, stale := got["Old"]; stale {
		t.Fatalf("old mapping not replaced: %v", got)
	}
	if got["New"] != "NewHerold" {
		t.Fatalf("new mapping missing: %v", got)
	}

	// Update WITHOUT folderMap (rename only): map left untouched.
	renameArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update":    map[string]any{id: map[string]any{"accountName": "Renamed"}},
	})
	if _, merr := (setHandler{h: h}).executeAs(p, renameArgs); merr != nil {
		t.Fatalf("rename: %v", merr)
	}
	if got := folderMapAsMap(getFolderMap(t, h, p)); got["New"] != "NewHerold" {
		t.Fatalf("absent folderMap should leave map untouched; got %v", got)
	}

	// Update with an explicit empty array clears it.
	clearArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update":    map[string]any{id: map[string]any{"folderMap": []map[string]string{}}},
	})
	if _, merr := (setHandler{h: h}).executeAs(p, clearArgs); merr != nil {
		t.Fatalf("clear: %v", merr)
	}
	if got := getFolderMap(t, h, p); len(got) != 0 {
		t.Fatalf("folderMap not cleared: %v", got)
	}
}

// TestFolderMap_InvalidEntryRejected verifies that a folderMap entry with an
// empty name is rejected on create without creating the account.
func TestFolderMap_InvalidEntryRejected(t *testing.T) {
	h, st, p := newHandlers(t)
	resp := doCreate(t, h, p, map[string]any{
		"c1": map[string]any{
			"accountName":     "Bad map",
			"host":            "imap.example.com",
			"port":            993,
			"tlsMode":         "implicit",
			"username":        "user",
			"authMethod":      "password",
			"backfillHorizon": "30d",
			"credential":      "pw",
			"folderMap": []map[string]string{
				{"upstreamFolder": "Sent", "heroldMailboxName": ""},
			},
		},
	})
	if _, ok := resp.Created["c1"]; ok {
		t.Fatalf("account should not have been created with an invalid folderMap")
	}
	nc, ok := resp.NotCreated["c1"]
	if !ok {
		t.Fatalf("expected notCreated entry for c1")
	}
	if len(nc.Properties) == 0 || nc.Properties[0] != "folderMap" {
		t.Fatalf("notCreated properties = %v, want [folderMap]", nc.Properties)
	}
	// No account was persisted.
	all, err := st.Meta().ListIMAPImportAccountsByPrincipal(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected no accounts persisted, got %d", len(all))
	}
}

// -- Horizon variants ------------------------------------------------

func TestCreate_Horizon90d(t *testing.T) {
	h, st, p := newHandlers(t)
	creates := map[string]any{
		"c1": map[string]any{
			"accountName":     "90-day account",
			"host":            "imap.example.com",
			"port":            993,
			"tlsMode":         "implicit",
			"username":        "user",
			"authMethod":      "password",
			"backfillHorizon": "90d",
			"credential":      "pass",
		},
	}
	resp := doCreate(t, h, p, creates)
	if _, ok := resp.Created["c1"]; !ok {
		t.Fatalf("c1 not in Created; notCreated=%v", resp.NotCreated)
	}
	row, err := st.Meta().GetIMAPImportAccount(context.Background(), resp.Created["c1"].ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if row.BackfillFloorDate == nil {
		t.Fatalf("BackfillFloorDate must not be nil for 90d horizon")
	}
	expected := fakeNow.Add(-90 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	if !row.BackfillFloorDate.Equal(expected) {
		t.Fatalf("BackfillFloorDate = %v, want %v", *row.BackfillFloorDate, expected)
	}
}

func TestCreate_Horizon1y(t *testing.T) {
	h, st, p := newHandlers(t)
	creates := map[string]any{
		"c1": map[string]any{
			"accountName":     "1-year account",
			"host":            "imap.example.com",
			"port":            993,
			"tlsMode":         "starttls",
			"username":        "user",
			"authMethod":      "app_password",
			"backfillHorizon": "1y",
			"credential":      "app-pass",
		},
	}
	resp := doCreate(t, h, p, creates)
	if _, ok := resp.Created["c1"]; !ok {
		t.Fatalf("c1 not in Created; notCreated=%v", resp.NotCreated)
	}
	row, err := st.Meta().GetIMAPImportAccount(context.Background(), resp.Created["c1"].ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if row.BackfillFloorDate == nil {
		t.Fatalf("BackfillFloorDate must not be nil for 1y horizon")
	}
	expected := fakeNow.Add(-365 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	if !row.BackfillFloorDate.Equal(expected) {
		t.Fatalf("BackfillFloorDate = %v, want %v", *row.BackfillFloorDate, expected)
	}
}

func TestCreate_HorizonAbsoluteDate(t *testing.T) {
	h, st, p := newHandlers(t)
	creates := map[string]any{
		"c1": map[string]any{
			"accountName":     "Absolute date account",
			"host":            "imap.example.com",
			"port":            993,
			"tlsMode":         "implicit",
			"username":        "user",
			"authMethod":      "xoauth2",
			"backfillHorizon": "2025-06-01",
			"credential":      "token",
		},
	}
	resp := doCreate(t, h, p, creates)
	if _, ok := resp.Created["c1"]; !ok {
		t.Fatalf("c1 not in Created; notCreated=%v", resp.NotCreated)
	}
	row, err := st.Meta().GetIMAPImportAccount(context.Background(), resp.Created["c1"].ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if row.BackfillFloorDate == nil {
		t.Fatalf("BackfillFloorDate must not be nil for absolute date")
	}
	expected, _ := time.Parse("2006-01-02", "2025-06-01")
	expected = expected.UTC()
	if !row.BackfillFloorDate.Equal(expected) {
		t.Fatalf("BackfillFloorDate = %v, want %v", *row.BackfillFloorDate, expected)
	}
}

// min is a local helper so we do not depend on Go 1.21 min builtin in
// error messages.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
