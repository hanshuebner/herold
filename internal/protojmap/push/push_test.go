package push

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
	"github.com/hanshuebner/herold/internal/vapid"
)

// fixture builds the handler set + a fakestore + a known principal,
// returning everything the tests need to drive Execute directly.
type fixture struct {
	t       *testing.T
	store   *fakestore.Store
	pid     store.PrincipalID
	gh      *getHandler
	sh      *setHandler
	manager *vapid.Manager
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "user@example.com",
		DisplayName:    "User",
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	kp, err := vapid.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("vapid.Generate: %v", err)
	}
	mgr := vapid.NewWithKey(kp)
	h := &handlerSet{store: st, logger: nil, clk: clock.NewFake(clock.NewFake(clock.NewReal().Now()).Now()), vapid: mgr}
	return &fixture{
		t:       t,
		store:   st,
		pid:     p.ID,
		gh:      &getHandler{h: h},
		sh:      &setHandler{h: h},
		manager: mgr,
	}
}

func (f *fixture) ctx() context.Context {
	return contextWithTestPrincipal(context.Background(), store.Principal{
		ID:             f.pid,
		CanonicalEmail: "user@example.com",
	})
}

func (f *fixture) ctxFor(pid store.PrincipalID) context.Context {
	return contextWithTestPrincipal(context.Background(), store.Principal{
		ID:             pid,
		CanonicalEmail: "other@example.com",
	})
}

// validKeysJSON returns a wire-form keys object backed by valid 65-
// byte / 16-byte raw inputs the create handler will accept.
func validKeysJSON() jmapKeys {
	pub := make([]byte, 65)
	pub[0] = 0x04
	for i := 1; i < 65; i++ {
		pub[i] = byte(i)
	}
	authKey := make([]byte, 16)
	for i := range authKey {
		authKey[i] = byte(0x10 + i)
	}
	return jmapKeys{
		P256DH: base64.RawURLEncoding.EncodeToString(pub),
		Auth:   base64.RawURLEncoding.EncodeToString(authKey),
	}
}

// invokeSet is a small helper that JSON-marshals args and dispatches
// through the set handler.
func (f *fixture) invokeSet(ctx context.Context, args any) (setResponse, *protojmap.MethodError) {
	f.t.Helper()
	body, err := json.Marshal(args)
	if err != nil {
		f.t.Fatalf("marshal args: %v", err)
	}
	out, merr := f.sh.Execute(ctx, body)
	if merr != nil {
		return setResponse{}, merr
	}
	// We marshal+unmarshal to convert the typed response into a setResponse
	// the tests can read.
	raw, err := json.Marshal(out)
	if err != nil {
		f.t.Fatalf("marshal response: %v", err)
	}
	var resp setResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		f.t.Fatalf("unmarshal response: %v", err)
	}
	return resp, nil
}

func (f *fixture) invokeGet(ctx context.Context, args getRequest) (getResponse, *protojmap.MethodError) {
	f.t.Helper()
	body, err := json.Marshal(args)
	if err != nil {
		f.t.Fatalf("marshal args: %v", err)
	}
	out, merr := f.gh.Execute(ctx, body)
	if merr != nil {
		return getResponse{}, merr
	}
	raw, err := json.Marshal(out)
	if err != nil {
		f.t.Fatalf("marshal response: %v", err)
	}
	var resp getResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		f.t.Fatalf("unmarshal response: %v", err)
	}
	return resp, nil
}

func TestPushSet_Create_AllocatesVerificationCode(t *testing.T) {
	f := newFixture(t)
	resp, merr := f.invokeSet(f.ctx(), setRequest{
Create: map[string]json.RawMessage{
			"c1": mustJSON(pushCreateInput{
				DeviceClientID: "browser-1",
				URL:            "https://push.example.test/abc",
				Keys:           validKeysJSON(),
				Types:          []string{"Mailbox", "Email"},
			}),
		},
	})
	if merr != nil {
		t.Fatalf("Execute: %v", merr)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("Created len = %d, want 1: %+v", len(resp.Created), resp.NotCreated)
	}
	created, ok := resp.Created["c1"]
	if !ok {
		t.Fatalf("c1 not in created")
	}
	if created.ID == "" {
		t.Fatalf("created id empty")
	}
	if created.VerificationCode == nil || *created.VerificationCode == "" {
		t.Fatalf("verificationCode not returned on create: %+v", created)
	}
	if resp.NewState == resp.OldState {
		t.Fatalf("state not bumped: old=%s new=%s", resp.OldState, resp.NewState)
	}
}

func TestPushSet_Create_WithoutKeys(t *testing.T) {
	// RFC 8620 §7.2: keys is optional. A client that has no Web Push
	// gateway credentials (e.g. the jmap-test-suite) should be able to
	// create a subscription for the basic lifecycle without supplying
	// encryption keys.
	f := newFixture(t)
	resp, merr := f.invokeSet(f.ctx(), setRequest{
Create: map[string]json.RawMessage{
			"c1": mustJSON(pushCreateInput{
				DeviceClientID: "keyless-client",
				URL:            "https://push.example.test/keyless",
				Types:          []string{"Email"},
			}),
		},
	})
	if merr != nil {
		t.Fatalf("Execute: %v", merr)
	}
	if len(resp.NotCreated) != 0 {
		t.Fatalf("NotCreated non-empty: %+v", resp.NotCreated)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("Created len = %d, want 1", len(resp.Created))
	}
	created, ok := resp.Created["c1"]
	if !ok {
		t.Fatalf("c1 not in created")
	}
	if created.ID == "" {
		t.Fatalf("created id empty")
	}
	// Verify it round-trips through /get.
	getResp, merr := f.invokeGet(f.ctx(), getRequest{IDs: ptrSlice([]jmapID{created.ID})})
	if merr != nil {
		t.Fatalf("Get: %v", merr)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("Get list len = %d, want 1", len(getResp.List))
	}
}

func TestPushSet_Create_RejectsNonHTTPS(t *testing.T) {
	f := newFixture(t)
	resp, merr := f.invokeSet(f.ctx(), setRequest{
Create: map[string]json.RawMessage{
			"c1": mustJSON(pushCreateInput{
				URL:  "http://insecure.example.test",
				Keys: validKeysJSON(),
			}),
		},
	})
	if merr != nil {
		t.Fatalf("Execute: %v", merr)
	}
	if _, ok := resp.NotCreated["c1"]; !ok {
		t.Fatalf("expected NotCreated, got Created: %+v", resp.Created)
	}
	if resp.NotCreated["c1"].Type != "invalidProperties" {
		t.Fatalf("error type = %q, want invalidProperties", resp.NotCreated["c1"].Type)
	}
}

func TestPushSet_VerificationHandshake(t *testing.T) {
	f := newFixture(t)
	createResp, merr := f.invokeSet(f.ctx(), setRequest{
Create: map[string]json.RawMessage{
			"c1": mustJSON(pushCreateInput{
				URL:  "https://push.example.test/v",
				Keys: validKeysJSON(),
			}),
		},
	})
	if merr != nil {
		t.Fatalf("Create: %v", merr)
	}
	c := createResp.Created["c1"]
	code := *c.VerificationCode
	updateResp, merr := f.invokeSet(f.ctx(), setRequest{
Update: map[jmapID]json.RawMessage{
			c.ID: mustJSON(map[string]any{"verificationCode": code}),
		},
	})
	if merr != nil {
		t.Fatalf("Update: %v", merr)
	}
	if _, bad := updateResp.NotUpdated[c.ID]; bad {
		t.Fatalf("verification update rejected: %+v", updateResp.NotUpdated)
	}
	getResp, merr := f.invokeGet(f.ctx(), getRequest{
IDs:       ptrSlice([]jmapID{c.ID}),
	})
	if merr != nil {
		t.Fatalf("Get: %v", merr)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("Get list len = %d", len(getResp.List))
	}
	got := getResp.List[0]
	if got.VerificationCode != nil {
		t.Fatalf("verificationCode still returned after handshake: %v", *got.VerificationCode)
	}
}

func TestPushSet_VerificationHandshake_RejectsWrongCode(t *testing.T) {
	f := newFixture(t)
	createResp, merr := f.invokeSet(f.ctx(), setRequest{
Create: map[string]json.RawMessage{
			"c1": mustJSON(pushCreateInput{
				URL:  "https://push.example.test/v",
				Keys: validKeysJSON(),
			}),
		},
	})
	if merr != nil {
		t.Fatalf("Create: %v", merr)
	}
	c := createResp.Created["c1"]
	updateResp, merr := f.invokeSet(f.ctx(), setRequest{
Update: map[jmapID]json.RawMessage{
			c.ID: mustJSON(map[string]any{"verificationCode": "definitely-wrong"}),
		},
	})
	if merr != nil {
		t.Fatalf("Update: %v", merr)
	}
	se, ok := updateResp.NotUpdated[c.ID]
	if !ok {
		t.Fatalf("wrong code accepted")
	}
	if se.Type != "invalidProperties" {
		t.Fatalf("error type = %q, want invalidProperties", se.Type)
	}
}

func TestPushSet_RejectsImmutableUpdate(t *testing.T) {
	f := newFixture(t)
	createResp, _ := f.invokeSet(f.ctx(), setRequest{
Create: map[string]json.RawMessage{
			"c1": mustJSON(pushCreateInput{
				URL:  "https://push.example.test/u",
				Keys: validKeysJSON(),
			}),
		},
	})
	c := createResp.Created["c1"]
	updateResp, _ := f.invokeSet(f.ctx(), setRequest{
Update: map[jmapID]json.RawMessage{
			c.ID: mustJSON(map[string]any{"url": "https://other.example.test"}),
		},
	})
	se, ok := updateResp.NotUpdated[c.ID]
	if !ok {
		t.Fatalf("immutable url accepted")
	}
	if se.Type != "invalidProperties" {
		t.Fatalf("error type = %q, want invalidProperties (got %v)", se.Type, se)
	}
}

func TestPushSet_Destroy_Roundtrip(t *testing.T) {
	f := newFixture(t)
	createResp, _ := f.invokeSet(f.ctx(), setRequest{
Create: map[string]json.RawMessage{
			"c1": mustJSON(pushCreateInput{
				URL:  "https://push.example.test/d",
				Keys: validKeysJSON(),
			}),
		},
	})
	c := createResp.Created["c1"]
	destroyResp, _ := f.invokeSet(f.ctx(), setRequest{
Destroy:   []jmapID{c.ID},
	})
	if len(destroyResp.Destroyed) != 1 || destroyResp.Destroyed[0] != c.ID {
		t.Fatalf("Destroyed = %+v", destroyResp.Destroyed)
	}
	getResp, _ := f.invokeGet(f.ctx(), getRequest{IDs: ptrSlice([]jmapID{c.ID})})
	if len(getResp.NotFound) != 1 {
		t.Fatalf("NotFound after destroy = %+v", getResp.NotFound)
	}
}

func TestPush_CrossPrincipalDenied(t *testing.T) {
	f := newFixture(t)
	createResp, _ := f.invokeSet(f.ctx(), setRequest{
Create: map[string]json.RawMessage{
			"c1": mustJSON(pushCreateInput{
				URL:  "https://push.example.test/x",
				Keys: validKeysJSON(),
			}),
		},
	})
	c := createResp.Created["c1"]

	// Insert another principal and switch the auth context to them.
	other, err := f.store.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "other@example.com",
		DisplayName: "Other", QuotaBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal other: %v", err)
	}

	// /get from the other principal must NOT see our subscription
	// (no list rows + targeted ID returns notFound).
	getResp, _ := f.invokeGet(f.ctxFor(other.ID), getRequest{
		IDs: ptrSlice([]jmapID{c.ID}),
	})
	if len(getResp.List) != 0 {
		t.Fatalf("foreign principal saw list: %+v", getResp.List)
	}
	if len(getResp.NotFound) != 1 {
		t.Fatalf("expected NotFound on foreign id; got %+v", getResp)
	}
	// /set update must surface notFound (not "forbidden") so the
	// existence of the foreign row is not confirmable.
	updateResp, _ := f.invokeSet(f.ctxFor(other.ID), setRequest{
		Update: map[jmapID]json.RawMessage{
			c.ID: mustJSON(map[string]any{"types": []string{"Mailbox"}}),
		},
	})
	if updateResp.NotUpdated[c.ID].Type != "notFound" {
		t.Fatalf("expected notFound on foreign update, got %+v", updateResp.NotUpdated)
	}
	// /set destroy must also surface notFound.
	destroyResp, _ := f.invokeSet(f.ctxFor(other.ID), setRequest{
		Destroy: []jmapID{c.ID},
	})
	if len(destroyResp.Destroyed) != 0 {
		t.Fatalf("foreign principal destroyed our row: %+v", destroyResp)
	}
	if destroyResp.NotDestroyed[c.ID].Type != "notFound" {
		t.Fatalf("expected notFound on foreign destroy, got %+v", destroyResp.NotDestroyed)
	}
}

func TestPushGet_AllRows(t *testing.T) {
	f := newFixture(t)
	for i, name := range []string{"c1", "c2", "c3"} {
		_, merr := f.invokeSet(f.ctx(), setRequest{
			Create: map[string]json.RawMessage{
				name: mustJSON(pushCreateInput{
					URL:  "https://push.example.test/r" + name,
					Keys: validKeysJSON(),
				}),
			},
		})
		if merr != nil {
			t.Fatalf("Create %d: %v", i, merr)
		}
	}
	getResp, merr := f.invokeGet(f.ctx(), getRequest{})
	if merr != nil {
		t.Fatalf("Get all: %v", merr)
	}
	if len(getResp.List) != 3 {
		t.Fatalf("Get all list len = %d, want 3", len(getResp.List))
	}
}

func TestCapabilityDescriptor_AdvertisesVAPIDKey(t *testing.T) {
	kp, err := vapid.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	mgr := vapid.NewWithKey(kp)
	desc := buildCapabilityDescriptor(mgr)
	if desc.ApplicationServerKey == "" {
		t.Fatalf("ApplicationServerKey empty when VAPID configured")
	}
	if desc.ApplicationServerKey != kp.PublicKeyB64URL {
		t.Fatalf("ApplicationServerKey mismatch")
	}
}

func TestCapabilityDescriptor_OmitsKeyWhenUnconfigured(t *testing.T) {
	desc := buildCapabilityDescriptor(vapid.New())
	if desc.ApplicationServerKey != "" {
		t.Fatalf("ApplicationServerKey set on unconfigured manager: %q", desc.ApplicationServerKey)
	}
	// JSON-encode and confirm the field is omitted (omitempty).
	body, err := json.Marshal(desc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(body), "applicationServerKey") {
		t.Fatalf("applicationServerKey present in JSON: %s", body)
	}
}

func TestPushSet_QuietHoursValidation(t *testing.T) {
	f := newFixture(t)
	resp, _ := f.invokeSet(f.ctx(), setRequest{
Create: map[string]json.RawMessage{
			"c1": mustJSON(pushCreateInput{
				URL:  "https://push.example.test/qh",
				Keys: validKeysJSON(),
				QuietHours: &jmapQuietHours{
					StartHourLocal: 25, // out of range
					EndHourLocal:   7,
					TZ:             "Europe/Berlin",
				},
			}),
		},
	})
	if _, ok := resp.NotCreated["c1"]; !ok {
		t.Fatalf("invalid quiet hours accepted")
	}
}

func TestPushSet_QuietHoursRejectsBadTZ(t *testing.T) {
	f := newFixture(t)
	resp, _ := f.invokeSet(f.ctx(), setRequest{
Create: map[string]json.RawMessage{
			"c1": mustJSON(pushCreateInput{
				URL:  "https://push.example.test/qh",
				Keys: validKeysJSON(),
				QuietHours: &jmapQuietHours{
					StartHourLocal: 22,
					EndHourLocal:   7,
					TZ:             "Definitely/NotAReal/Zone",
				},
			}),
		},
	})
	if _, ok := resp.NotCreated["c1"]; !ok {
		t.Fatalf("invalid tz accepted")
	}
}

// -- helpers ----------------------------------------------------------

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func ptrSlice(s []jmapID) *[]jmapID { return &s }
