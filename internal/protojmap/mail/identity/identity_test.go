package identity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"path/filepath"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	s, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newHandlers builds the handler set directly (bypassing the
// CapabilityRegistry) so tests can drive Execute without injecting a
// principal through protojmap.PrincipalFromContext (the package-private
// context key is owned by the Core agent's protojmap package).
func newHandlers(t *testing.T) (*handlerSet, store.Store, store.Principal) {
	t.Helper()
	st := newStore(t)
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
	return &handlerSet{
		store:    st,
		identity: NewStoreWith(st, clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))),
		domains:  makeDomainsFn(st),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, st, p
}

func TestIdentity_Get_DefaultIdentityIsSynthesized(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/get: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"id":"default"`) {
		t.Fatalf("expected default identity in response: %s", js)
	}
	if !strings.Contains(string(js), `"email":"alice@example.test"`) {
		t.Fatalf("expected synthesized email: %s", js)
	}
}

func TestIdentity_Set_RejectsForeignDomain(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"alt": map[string]any{
				"name":  "Alice Elsewhere",
				"email": "alice@otherdomain.test",
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"forbiddenFrom"`) {
		t.Fatalf("expected forbiddenFrom in response: %s", js)
	}
}

func TestIdentity_Set_AcceptsLocalDomain(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"work": map[string]any{
				"name":          "Alice At Work",
				"email":         "alice.work@example.test",
				"textSignature": "Sent from work",
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"created"`) {
		t.Fatalf("expected created: %s", js)
	}
	if strings.Contains(string(js), `"notCreated"`) {
		t.Fatalf("unexpected notCreated: %s", js)
	}
}

func TestIdentity_Changes_NoOpWhenSameState(t *testing.T) {
	h, st, p := newHandlers(t)
	stState, err := st.Meta().GetJMAPStates(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("states: %v", err)
	}
	state := stateString(stState.Identity)
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID), "sinceState": state})
	resp, mErr := changesHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/changes: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"updated":[]`) {
		t.Fatalf("expected empty updated: %s", js)
	}
}

// -- REQ-AUTH-EXT-SUBMIT-05: external-submission state bump -----------

// TestIdentity_BumpIdentityPushState verifies that BumpIdentityPushState
// increments JMAPStateKindIdentity so JMAP push clients are notified when an
// external submission outcome transitions the identity state (e.g. auth-failed
// or unreachable).
func TestIdentity_BumpIdentityPushState(t *testing.T) {
	_, st, p := newHandlers(t)
	ctx := context.Background()
	idStore := NewStoreWith(st, clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))

	before, err := st.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates before: %v", err)
	}

	if err := idStore.BumpIdentityPushState(ctx, p.ID); err != nil {
		t.Fatalf("BumpIdentityPushState: %v", err)
	}

	after, err := st.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates after: %v", err)
	}
	if after.Identity <= before.Identity {
		t.Fatalf("identity state not incremented: before=%d after=%d", before.Identity, after.Identity)
	}
}

// -- REQ-PROTO-57 / REQ-STORE-35 Identity.signature extension ------

// TestIdentity_Get_IncludesSignature verifies that Identity/get reflects
// the signature extension property both for the principal-default
// (overlay) row and for a custom persisted identity.
func TestIdentity_Get_IncludesSignature(t *testing.T) {
	h, _, p := newHandlers(t)
	// Set the signature on the default identity via the overlay path.
	sig := "Cheers,\nAlice"
	updateArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			"default": map[string]any{"signature": sig},
		},
	})
	_, mErr := setHandler{h: h}.executeAs(p, updateArgs)
	if mErr != nil {
		t.Fatalf("Identity/set update default: %v", mErr)
	}
	getArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, mErr := getHandler{h: h}.executeAs(p, getArgs)
	if mErr != nil {
		t.Fatalf("Identity/get: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"signature":"Cheers,\nAlice"`) {
		t.Fatalf("default signature missing in response: %s", js)
	}
}

// -- REQ-IDENT-10..14: verifiedAt extension property ---------------

// TestIdentity_Get_DefaultVerifiedAtMatchesPrincipalCreatedAt verifies
// REQ-IDENT-02: the synthesised default identity is verified-by-
// construction and emits verifiedAt equal to the owning principal's
// CreatedAt. The wire form is the JMAP UTCDate ("YYYY-MM-DDTHH:MM:SSZ").
func TestIdentity_Get_DefaultVerifiedAtMatchesPrincipalCreatedAt(t *testing.T) {
	h, st, p := newHandlers(t)
	// Re-read the principal so CreatedAt reflects what the store
	// assigned at insert time, not the zero-valued probe sent in.
	got, err := st.Meta().GetPrincipalByID(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{"default"},
	})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/get: %v", mErr)
	}
	gr, ok := resp.(getResponse)
	if !ok {
		t.Fatalf("unexpected response shape: %T", resp)
	}
	if len(gr.List) != 1 {
		t.Fatalf("expected one identity in response, got %d", len(gr.List))
	}
	row := gr.List[0]
	if row.VerifiedAt == nil {
		t.Fatalf("default identity verifiedAt is nil; want principal CreatedAt")
	}
	wantStr := got.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
	gotJSON, _ := json.Marshal(row.VerifiedAt)
	// Marshaled value is a JSON string ("..."); compare unquoted.
	if string(gotJSON) != "\""+wantStr+"\"" {
		t.Fatalf("default verifiedAt = %s; want %q", gotJSON, wantStr)
	}
}

// TestIdentity_Get_PersistedRowVerifiedAtIsNull verifies REQ-IDENT-12:
// a freshly created persistent identity row reports verifiedAt = JSON
// null until the email round-trip flips verified_at_us.
func TestIdentity_Get_PersistedRowVerifiedAtIsNull(t *testing.T) {
	h, _, p := newHandlers(t)
	// Create a new persisted identity via Identity/set.
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"alt": map[string]any{
				"name":  "Alice Persona",
				"email": "alice.persona@example.test",
			},
		},
	})
	createResp, mErr := setHandler{h: h}.executeAs(p, createArgs)
	if mErr != nil {
		t.Fatalf("Identity/set create: %v", mErr)
	}
	created := createResp.(setResponse).Created["alt"]
	if created.VerifiedAt != nil {
		t.Fatalf("create response: verifiedAt should be null on a fresh row, got non-nil")
	}
	// Read back via Identity/get to confirm the JSON wire shape.
	getArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{created.ID},
	})
	getResp, mErr := getHandler{h: h}.executeAs(p, getArgs)
	if mErr != nil {
		t.Fatalf("Identity/get: %v", mErr)
	}
	js, _ := json.Marshal(getResp)
	if !strings.Contains(string(js), `"verifiedAt":null`) {
		t.Fatalf("expected verifiedAt:null in get response: %s", js)
	}
}

// TestIdentity_Changes_CarriesVerifiedAt verifies REQ-IDENT-10:
// Identity/changes lists every changed identity by id; downstream
// Identity/get reads carry the verifiedAt property. Identity/changes
// itself returns only ids, not full objects (RFC 8620 §5.2), but the
// state-driven read it triggers must surface verifiedAt — which is what
// the wire-shape test above already covers for /get. This test exercises
// /changes specifically to make sure the state-counter path keeps
// returning the new identity in updated[].
func TestIdentity_Changes_CarriesVerifiedAt(t *testing.T) {
	h, st, p := newHandlers(t)
	ctx := context.Background()
	before, err := st.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates: %v", err)
	}
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"alt": map[string]any{
				"name":  "Alice Persona",
				"email": "alice.persona@example.test",
			},
		},
	})
	createResp, mErr := setHandler{h: h}.executeAs(p, createArgs)
	if mErr != nil {
		t.Fatalf("Identity/set create: %v", mErr)
	}
	created := createResp.(setResponse).Created["alt"]
	args, _ := json.Marshal(map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(p.ID),
		"sinceState": stateString(before.Identity),
	})
	resp, mErr := changesHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/changes: %v", mErr)
	}
	cr := resp.(changesResponse)
	found := false
	for _, id := range cr.Updated {
		if id == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected new identity id %q in updated[]: %+v", created.ID, cr.Updated)
	}
	// Now Identity/get on the same id and verify verifiedAt: null.
	getArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{created.ID},
	})
	getResp, mErr := getHandler{h: h}.executeAs(p, getArgs)
	if mErr != nil {
		t.Fatalf("Identity/get: %v", mErr)
	}
	js, _ := json.Marshal(getResp)
	if !strings.Contains(string(js), `"verifiedAt":null`) {
		t.Fatalf("expected verifiedAt:null after /changes read-through: %s", js)
	}
}

// TestIdentity_Set_Update_RejectsVerifiedAt verifies REQ-IDENT-13: any
// client attempt to toggle verifiedAt via Identity/set { update } is
// rejected with invalidProperties { verifiedAt }.
func TestIdentity_Set_Update_RejectsVerifiedAt(t *testing.T) {
	h, _, p := newHandlers(t)
	updateArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			"default": map[string]any{
				"verifiedAt": "2030-01-01T00:00:00Z",
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, updateArgs)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	sresp := resp.(setResponse)
	se, ok := sresp.NotUpdated["default"]
	if !ok {
		t.Fatalf("expected notUpdated[default]; got: %+v", sresp)
	}
	if se.Type != "invalidProperties" {
		t.Fatalf("notUpdated type = %q; want invalidProperties", se.Type)
	}
	if len(se.Properties) != 1 || se.Properties[0] != "verifiedAt" {
		t.Fatalf("notUpdated properties = %v; want [\"verifiedAt\"]", se.Properties)
	}
	if se.Description != "verifiedAt is server-managed" {
		t.Fatalf("notUpdated description = %q; want \"verifiedAt is server-managed\"",
			se.Description)
	}
}

// TestIdentity_Set_Create_FiresVerificationTrigger verifies REQ-IDENT-30:
// the hook is invoked with the freshly-created row after commit, and a
// hook error does NOT roll the create back — the suite must surface a
// Resend affordance (REQ-IDENT-41) instead.
func TestIdentity_Set_Create_FiresVerificationTrigger(t *testing.T) {
	h, _, p := newHandlers(t)
	var captured []store.JMAPIdentity
	h.verificationTrigger = func(_ context.Context, row store.JMAPIdentity) error {
		captured = append(captured, row)
		return errors.New("simulated SMTP failure")
	}
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"alt": map[string]any{
				"name":  "Alice Persona",
				"email": "alice.persona@example.test",
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, createArgs)
	if mErr != nil {
		t.Fatalf("Identity/set create: %v", mErr)
	}
	sresp := resp.(setResponse)
	if _, ok := sresp.Created["alt"]; !ok {
		t.Fatalf("expected create success despite trigger failure: %+v", sresp.NotCreated)
	}
	if len(captured) != 1 {
		t.Fatalf("expected trigger to fire once; got %d", len(captured))
	}
	if captured[0].PrincipalID != p.ID {
		t.Fatalf("trigger row.PrincipalID = %d; want %d", captured[0].PrincipalID, p.ID)
	}
	if captured[0].Email != "alice.persona@example.test" {
		t.Fatalf("trigger row.Email = %q; want alice.persona@example.test", captured[0].Email)
	}
	if captured[0].VerifiedAtUs != 0 {
		t.Fatalf("trigger row.VerifiedAtUs = %d; want 0 (unverified)", captured[0].VerifiedAtUs)
	}
}

// TestIdentity_Set_Create_SkipVerificationEmail verifies that when the
// skipVerificationEmail extension field is true the verification trigger is
// NOT called, while the identity row is still created successfully. This
// supports the OAuth fast-path in the add-identity wizard (re #105): the
// client sets skipVerificationEmail=true when it knows an OAuth flow will
// immediately verify ownership, avoiding the confusing verification email.
func TestIdentity_Set_Create_SkipVerificationEmail(t *testing.T) {
	h, _, p := newHandlers(t)
	triggerFired := false
	h.verificationTrigger = func(_ context.Context, _ store.JMAPIdentity) error {
		triggerFired = true
		return nil
	}
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"oauth": map[string]any{
				"name":                  "Alice Gmail",
				"email":                 "alice.gmail@example.test",
				"skipVerificationEmail": true,
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set create: %v", mErr)
	}
	sresp := resp.(setResponse)
	if _, ok := sresp.Created["oauth"]; !ok {
		t.Fatalf("expected create success: notCreated = %+v", sresp.NotCreated)
	}
	if triggerFired {
		t.Fatal("expected verification trigger NOT to fire when skipVerificationEmail=true")
	}
}

// TestIdentity_Set_Create_AllowAllExternalDomain verifies REQ-IDENT-20:
// when the policy hook permits the external domain, Identity/set { create }
// accepts the row even though the domain is not in ListLocalDomains.
func TestIdentity_Set_Create_AllowAllExternalDomain(t *testing.T) {
	h, _, p := newHandlers(t)
	h.externalDomain = func(string) bool { return true }
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"ext": map[string]any{
				"name":  "Alice External",
				"email": "alice@elsewhere.test",
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	sresp := resp.(setResponse)
	if _, ok := sresp.Created["ext"]; !ok {
		t.Fatalf("expected create success for external allow-all policy: %+v", sresp.NotCreated)
	}
	if got := sresp.Created["ext"]; got.VerifiedAt != nil {
		t.Fatalf("external create should be unverified; got verifiedAt = %v", got.VerifiedAt)
	}
}

// TestIdentity_Set_Create_DenyAllExternalDomain verifies the deny_all
// path: a hosted domain still works (alice@example.test), but a
// foreign domain is rejected with forbiddenFrom.
func TestIdentity_Set_Create_DenyAllExternalDomain(t *testing.T) {
	h, _, p := newHandlers(t)
	h.externalDomain = func(string) bool { return false }
	// Hosted-domain creates still work.
	hostedArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"local": map[string]any{
				"name":  "Alice Local",
				"email": "alice.local@example.test",
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, hostedArgs)
	if mErr != nil {
		t.Fatalf("Identity/set hosted: %v", mErr)
	}
	if _, ok := resp.(setResponse).Created["local"]; !ok {
		t.Fatalf("hosted create rejected unexpectedly: %+v", resp.(setResponse).NotCreated)
	}
	// External domain is denied.
	extArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"ext": map[string]any{
				"name":  "Alice External",
				"email": "alice@elsewhere.test",
			},
		},
	})
	extResp, mErr := setHandler{h: h}.executeAs(p, extArgs)
	if mErr != nil {
		t.Fatalf("Identity/set external: %v", mErr)
	}
	se, ok := extResp.(setResponse).NotCreated["ext"]
	if !ok {
		t.Fatalf("expected notCreated[ext]; got: %+v", extResp.(setResponse).Created)
	}
	if se.Type != "forbiddenFrom" {
		t.Fatalf("notCreated type = %q; want forbiddenFrom", se.Type)
	}
	if se.Description != "domain not permitted by server policy" {
		t.Fatalf("notCreated description = %q; want \"domain not permitted by server policy\"",
			se.Description)
	}
}

// TestIdentity_CapabilityRegisteredWhenEnabled verifies REQ-IDENT-11:
// the CapabilityIdentityVerification URI is advertised in the JMAP
// session descriptor's capabilities map. The admin server is the
// canonical caller of RegisterCapabilityDescriptor; this test exercises
// the same registry surface directly so the capability constant is
// covered by go test before the admin-server build is run.
func TestIdentity_CapabilityRegisteredWhenEnabled(t *testing.T) {
	reg := protojmap.NewCapabilityRegistry()
	reg.RegisterCapabilityDescriptor(protojmap.CapabilityIdentityVerification, struct{}{})
	if !reg.HasCapability(protojmap.CapabilityIdentityVerification) {
		t.Fatal("CapabilityIdentityVerification should be registered")
	}
	caps := reg.Capabilities()
	if _, ok := caps[protojmap.CapabilityIdentityVerification]; !ok {
		t.Fatalf("CapabilityIdentityVerification missing from caps map: %v", caps)
	}
}

// TestIdentity_Set_AcceptsSignature exercises the create + update
// signature paths plus the explicit-null clear.
func TestIdentity_Set_AcceptsSignature(t *testing.T) {
	h, _, p := newHandlers(t)
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"alt": map[string]any{
				"name":      "Alice Personal",
				"email":     "alice.personal@example.test",
				"signature": "Best,\nA.",
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, createArgs)
	if mErr != nil {
		t.Fatalf("Identity/set create: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"signature":"Best,\nA."`) {
		t.Fatalf("created identity missing signature: %s", js)
	}
	// Find the created id so we can flip the signature.
	sresp := resp.(setResponse)
	created, ok := sresp.Created["alt"]
	if !ok {
		t.Fatalf("create response missing alt: %+v", sresp.Created)
	}
	updateArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			created.ID: map[string]any{"signature": "Updated"},
		},
	})
	resp2, mErr := setHandler{h: h}.executeAs(p, updateArgs)
	if mErr != nil {
		t.Fatalf("Identity/set update: %v", mErr)
	}
	js2, _ := json.Marshal(resp2)
	if !strings.Contains(string(js2), `"signature":"Updated"`) {
		t.Fatalf("updated signature missing: %s", js2)
	}
	// Clear via explicit null.
	clearArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			created.ID: map[string]any{"signature": nil},
		},
	})
	_, mErr3 := setHandler{h: h}.executeAs(p, clearArgs)
	if mErr3 != nil {
		t.Fatalf("Identity/set clear: %v", mErr3)
	}
	getArgs3, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp3, _ := getHandler{h: h}.executeAs(p, getArgs3)
	js3, _ := json.Marshal(resp3)
	if strings.Contains(string(js3), `"signature":"Updated"`) {
		t.Fatalf("signature did not clear: %s", js3)
	}
}

// -- REQ-IDENT-70: isDefault extension property --------------------

// createIdentity is a test helper that issues an Identity/set { create }
// and returns the created jmapIdentity object.
func createIdentity(t *testing.T, h *handlerSet, p store.Principal, clientID, name, email string) jmapIdentity {
	t.Helper()
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			clientID: map[string]any{"name": name, "email": email},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set create %s: %v", clientID, mErr)
	}
	created, ok := resp.(setResponse).Created[clientID]
	if !ok {
		t.Fatalf("create %s rejected: %+v", clientID, resp.(setResponse).NotCreated)
	}
	return created
}

// TestIdentity_DecodePatch_AcceptsIsDefault verifies decodePatch reads
// the herold isDefault extension property instead of rejecting it as an
// unknown property (REQ-IDENT-70).
func TestIdentity_DecodePatch_AcceptsIsDefault(t *testing.T) {
	_, st, _ := newHandlers(t)
	patch, perr := decodePatch(context.Background(), st, json.RawMessage(`{"isDefault":true}`))
	if perr != nil {
		t.Fatalf("decodePatch rejected isDefault: %+v", perr)
	}
	if !patch.hasIsDefault || !patch.isDefault {
		t.Fatalf("patch = %+v; want hasIsDefault && isDefault", patch)
	}
	patch, perr = decodePatch(context.Background(), st, json.RawMessage(`{"isDefault":false}`))
	if perr != nil {
		t.Fatalf("decodePatch rejected isDefault:false: %+v", perr)
	}
	if !patch.hasIsDefault || patch.isDefault {
		t.Fatalf("patch = %+v; want hasIsDefault && !isDefault", patch)
	}
	// A non-boolean value is invalidProperties { isDefault }.
	if _, perr = decodePatch(context.Background(), st, json.RawMessage(`{"isDefault":"yes"}`)); perr == nil {
		t.Fatal("decodePatch accepted a non-boolean isDefault")
	} else if perr.Type != "invalidProperties" ||
		len(perr.Properties) != 1 || perr.Properties[0] != "isDefault" {
		t.Fatalf("setError = %+v; want invalidProperties [isDefault]", perr)
	}
}

// TestIdentity_Get_EmitsIsDefault verifies REQ-IDENT-70: every Identity
// in an Identity/get response carries isDefault, and with no persisted
// row flagged the synthesised default is the default.
func TestIdentity_Get_EmitsIsDefault(t *testing.T) {
	h, _, p := newHandlers(t)
	createIdentity(t, h, p, "alt", "Alice Alt", "alice.alt@example.test")
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/get: %v", mErr)
	}
	gr := resp.(getResponse)
	if len(gr.List) != 2 {
		t.Fatalf("expected 2 identities, got %d", len(gr.List))
	}
	defaults := 0
	for _, row := range gr.List {
		if row.IsDefault {
			defaults++
			if row.ID != "default" {
				t.Fatalf("default identity = %s, want \"default\"", row.ID)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("expected exactly one isDefault identity, got %d", defaults)
	}
	// isDefault must always be emitted, even for the non-default row.
	js, _ := json.Marshal(resp)
	if strings.Count(string(js), `"isDefault"`) != 2 {
		t.Fatalf("isDefault not emitted on every identity: %s", js)
	}
}

// TestIdentity_Set_IsDefault_SwitchesAtomically verifies REQ-IDENT-70:
// setting isDefault:true on a custom identity moves the default off the
// synthesised default; the single-default invariant holds.
func TestIdentity_Set_IsDefault_SwitchesAtomically(t *testing.T) {
	h, _, p := newHandlers(t)
	alt := createIdentity(t, h, p, "alt", "Alice Alt", "alice.alt@example.test")

	updateArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			alt.ID: map[string]any{"isDefault": true},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, updateArgs)
	if mErr != nil {
		t.Fatalf("Identity/set update: %v", mErr)
	}
	updated := resp.(setResponse).Updated[alt.ID]
	if updated == nil || !updated.IsDefault {
		t.Fatalf("updated alt identity is not the default: %+v", updated)
	}
	// Re-read: exactly the custom row is default now.
	getArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	getResp, _ := getHandler{h: h}.executeAs(p, getArgs)
	defaults := map[string]bool{}
	n := 0
	for _, row := range getResp.(getResponse).List {
		defaults[row.ID] = row.IsDefault
		if row.IsDefault {
			n++
		}
	}
	if n != 1 || !defaults[alt.ID] {
		t.Fatalf("expected only %s default; got %v", alt.ID, defaults)
	}
}

// TestIdentity_Set_IsDefaultFalse_FallsBackToSynthesisedDefault verifies
// the chosen REQ-IDENT-70 edge-case rule: setting isDefault:false on the
// current default falls back to the synthesised default identity rather
// than leaving the principal with zero defaults.
func TestIdentity_Set_IsDefaultFalse_FallsBackToSynthesisedDefault(t *testing.T) {
	h, _, p := newHandlers(t)
	alt := createIdentity(t, h, p, "alt", "Alice Alt", "alice.alt@example.test")
	// Make alt the default.
	mkUpdate := func(id string, isDefault bool) (any, *protojmap.MethodError) {
		args, _ := json.Marshal(map[string]any{
			"accountId": protojmap.AccountIDForPrincipal(p.ID),
			"update": map[string]any{
				id: map[string]any{"isDefault": isDefault},
			},
		})
		return setHandler{h: h}.executeAs(p, args)
	}
	if _, mErr := mkUpdate(alt.ID, true); mErr != nil {
		t.Fatalf("set alt default: %v", mErr)
	}
	// Now clear it: the synthesised default takes over.
	resp, mErr := mkUpdate(alt.ID, false)
	if mErr != nil {
		t.Fatalf("clear alt default: %v", mErr)
	}
	updated := resp.(setResponse).Updated[alt.ID]
	if updated == nil || updated.IsDefault {
		t.Fatalf("alt should no longer be default: %+v", updated)
	}
	getArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	getResp, _ := getHandler{h: h}.executeAs(p, getArgs)
	n := 0
	for _, row := range getResp.(getResponse).List {
		if row.IsDefault {
			n++
			if row.ID != "default" {
				t.Fatalf("fallback default = %s, want \"default\"", row.ID)
			}
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one default after fallback, got %d", n)
	}
}

// TestIdentity_Set_IsDefault_BumpsState verifies REQ-IDENT-70: an
// isDefault mutation bumps the principal's Identity JMAP state counter.
func TestIdentity_Set_IsDefault_BumpsState(t *testing.T) {
	h, st, p := newHandlers(t)
	alt := createIdentity(t, h, p, "alt", "Alice Alt", "alice.alt@example.test")
	before, err := st.Meta().GetJMAPStates(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates: %v", err)
	}
	updateArgs, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update": map[string]any{
			alt.ID: map[string]any{"isDefault": true},
		},
	})
	_, mErr := setHandler{h: h}.executeAs(p, updateArgs)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	after, err := st.Meta().GetJMAPStates(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates: %v", err)
	}
	if after.Identity <= before.Identity {
		t.Fatalf("identity state not bumped: before=%d after=%d", before.Identity, after.Identity)
	}
}

// -- Task 2: duplicate-email rejection -----------------------------

// TestIdentity_Set_Create_RejectsDuplicateOfDefaultEmail verifies that
// Identity/set { create } rejects a create whose email matches the
// principal's synthesised default identity.
func TestIdentity_Set_Create_RejectsDuplicateOfDefaultEmail(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"dup": map[string]any{"name": "Dup", "email": "alice@example.test"},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	se, ok := resp.(setResponse).NotCreated["dup"]
	if !ok {
		t.Fatalf("expected notCreated[dup]; got created: %+v", resp.(setResponse).Created)
	}
	if se.Type != "invalidProperties" ||
		len(se.Properties) != 1 || se.Properties[0] != "email" {
		t.Fatalf("setError = %+v; want invalidProperties [email]", se)
	}
	if se.Description != "an identity with this email already exists" {
		t.Fatalf("description = %q", se.Description)
	}
}

// TestIdentity_Set_Create_RejectsDuplicateOfCustomEmail verifies that a
// second create with the same email as an existing custom identity is
// rejected, and that the comparison is case-insensitive.
func TestIdentity_Set_Create_RejectsDuplicateOfCustomEmail(t *testing.T) {
	h, _, p := newHandlers(t)
	createIdentity(t, h, p, "first", "Alice Alt", "alice.alt@example.test")
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"create": map[string]any{
			"second": map[string]any{"name": "Dup", "email": "Alice.Alt@Example.Test"},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	se, ok := resp.(setResponse).NotCreated["second"]
	if !ok {
		t.Fatalf("expected notCreated[second]; got created: %+v", resp.(setResponse).Created)
	}
	if se.Type != "invalidProperties" || len(se.Properties) != 1 || se.Properties[0] != "email" {
		t.Fatalf("setError = %+v; want invalidProperties [email]", se)
	}
}
