package vacation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

func newStore(t *testing.T) *fakestore.Store {
	t.Helper()
	s, err := fakestore.New(fakestore.Options{
		Clock:   clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func setup(t *testing.T) (*handlerSet, *fakestore.Store, store.Principal) {
	t.Helper()
	st := newStore(t)
	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	return &handlerSet{store: st}, st, p
}

func TestVacationResponse_Get_FromSieveScript(t *testing.T) {
	h, st, p := setup(t)
	script := `require ["vacation"];
vacation :subject "Out of office" "I am away through Friday.";`
	if err := st.Meta().SetSieveScript(context.Background(), p.ID, script); err != nil {
		t.Fatalf("set sieve script: %v", err)
	}
	args, _ := json.Marshal(map[string]any{})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("VacationResponse/get: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"isEnabled":true`) {
		t.Fatalf("expected isEnabled=true: %s", js)
	}
	if !strings.Contains(string(js), `"subject":"Out of office"`) {
		t.Fatalf("expected subject: %s", js)
	}
	if !strings.Contains(string(js), `"textBody":"I am away through Friday."`) {
		t.Fatalf("expected textBody: %s", js)
	}
}

func TestVacationResponse_Get_EmptyScriptReturnsDisabled(t *testing.T) {
	h, _, p := setup(t)
	args, _ := json.Marshal(map[string]any{})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("VacationResponse/get: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"isEnabled":false`) {
		t.Fatalf("expected isEnabled=false: %s", js)
	}
}

func TestVacationResponse_Set_RoundTripsThroughSieve(t *testing.T) {
	h, st, p := setup(t)
	args, _ := json.Marshal(map[string]any{
		"update": map[string]any{
			"singleton": map[string]any{
				"isEnabled": true,
				"subject":   "Holiday",
				"textBody":  "Back next week.",
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("VacationResponse/set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"updated"`) {
		t.Fatalf("expected updated: %s", js)
	}
	// Verify the persisted Sieve script.
	persisted, err := st.Meta().GetSieveScript(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("get sieve: %v", err)
	}
	if !strings.Contains(persisted, "vacation") {
		t.Fatalf("expected vacation in script: %s", persisted)
	}
	if !strings.Contains(persisted, "Holiday") {
		t.Fatalf("expected subject in script: %s", persisted)
	}
	if !strings.Contains(persisted, "Back next week.") {
		t.Fatalf("expected body in script: %s", persisted)
	}
	// Round-trip: re-read via /get.
	args2, _ := json.Marshal(map[string]any{})
	resp2, _ := getHandler{h: h}.executeAs(p, args2)
	js2, _ := json.Marshal(resp2)
	if !strings.Contains(string(js2), `"subject":"Holiday"`) {
		t.Fatalf("round trip dropped subject: %s", js2)
	}
}

func TestVacationResponse_Set_RefusesComplexScript(t *testing.T) {
	h, st, p := setup(t)
	// A vacation embedded inside an `if` is too complex for round-trip.
	complex := `require ["vacation","fileinto","envelope"];
if envelope :is "to" "alice@example.test" {
  vacation "I'm out for the holiday";
}`
	if err := st.Meta().SetSieveScript(context.Background(), p.ID, complex); err != nil {
		t.Fatalf("set sieve: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"update": map[string]any{
			"singleton": map[string]any{
				"isEnabled": false,
			},
		},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("VacationResponse/set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"forbidden"`) {
		t.Fatalf("expected forbidden in response: %s", js)
	}
	if !strings.Contains(string(js), `ManageSieve`) {
		t.Fatalf("expected ManageSieve hint: %s", js)
	}
}

func TestSynthesizeVacation_RoundTripsRequiredFields(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)
	p := vacationParams{
		IsEnabled: true,
		FromDate:  &from,
		ToDate:    &to,
		Subject:   "On vacation",
		TextBody:  "Reply later.",
	}
	script := synthesizeVacation(p)
	parsed, err := readVacation(script)
	if err != nil {
		t.Fatalf("readVacation after synthesize: %v\nscript: %s", err, script)
	}
	if !parsed.IsEnabled {
		t.Fatalf("isEnabled lost in roundtrip")
	}
	if parsed.Subject != "On vacation" {
		t.Fatalf("subject lost: %q", parsed.Subject)
	}
	if parsed.TextBody != "Reply later." {
		t.Fatalf("body lost: %q", parsed.TextBody)
	}
}
