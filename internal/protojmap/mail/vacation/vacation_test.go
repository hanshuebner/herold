package vacation

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

func setup(t *testing.T) (*handlerSet, store.Store, store.Principal) {
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
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
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
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
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
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
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
	args2, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
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
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
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

func TestVacationResponse_Set_CannotCreateOrDestroy(t *testing.T) {
	h, _, p := setup(t)
	accountID := protojmap.AccountIDForPrincipal(p.ID)

	// Create should be rejected with singleton error.
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID,
		"create": map[string]any{
			"new1": map[string]any{"isEnabled": true},
		},
	})
	respCreate, mErr := setHandler{h: h}.executeAs(p, createArgs)
	if mErr != nil {
		t.Fatalf("VacationResponse/set create: %v", mErr)
	}
	jsCreate, _ := json.Marshal(respCreate)
	if !strings.Contains(string(jsCreate), `"singleton"`) {
		t.Errorf("expected singleton error for create: %s", jsCreate)
	}
	if strings.Contains(string(jsCreate), `"created"`) {
		t.Errorf("unexpected created key in response: %s", jsCreate)
	}

	// Destroy should be rejected with singleton error.
	destroyArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID,
		"destroy":   []string{"singleton"},
	})
	respDestroy, mErr := setHandler{h: h}.executeAs(p, destroyArgs)
	if mErr != nil {
		t.Fatalf("VacationResponse/set destroy: %v", mErr)
	}
	jsDestroy, _ := json.Marshal(respDestroy)
	if !strings.Contains(string(jsDestroy), `"singleton"`) {
		t.Errorf("expected singleton error for destroy: %s", jsDestroy)
	}
}

func TestVacationResponse_Set_DateRoundTrip(t *testing.T) {
	h, _, p := setup(t)
	accountID := protojmap.AccountIDForPrincipal(p.ID)

	setArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID,
		"update": map[string]any{
			"singleton": map[string]any{
				"isEnabled": true,
				"fromDate":  "2026-03-01T00:00:00Z",
				"toDate":    "2026-03-10T00:00:00Z",
				"subject":   "On vacation",
				"textBody":  "Be back soon.",
			},
		},
	})
	_, mErr := setHandler{h: h}.executeAs(p, setArgs)
	if mErr != nil {
		t.Fatalf("VacationResponse/set dates: %v", mErr)
	}

	getArgs, _ := json.Marshal(map[string]any{"accountId": accountID})
	resp, mErr := getHandler{h: h}.executeAs(p, getArgs)
	if mErr != nil {
		t.Fatalf("VacationResponse/get after set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	jsStr := string(js)
	if !strings.Contains(jsStr, `"fromDate":"2026-03-01T00:00:00Z"`) {
		t.Errorf("fromDate not preserved: %s", jsStr)
	}
	if !strings.Contains(jsStr, `"toDate":"2026-03-10T00:00:00Z"`) {
		t.Errorf("toDate not preserved: %s", jsStr)
	}
}

func TestVacationResponse_Set_HTMLBodyRoundTrip(t *testing.T) {
	h, _, p := setup(t)
	accountID := protojmap.AccountIDForPrincipal(p.ID)

	// Set HTML-only body.
	setArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID,
		"update": map[string]any{
			"singleton": map[string]any{
				"isEnabled": true,
				"htmlBody":  "<p>Out of office</p>",
			},
		},
	})
	_, mErr := setHandler{h: h}.executeAs(p, setArgs)
	if mErr != nil {
		t.Fatalf("VacationResponse/set htmlBody: %v", mErr)
	}

	getArgs, _ := json.Marshal(map[string]any{"accountId": accountID})
	resp, mErr := getHandler{h: h}.executeAs(p, getArgs)
	if mErr != nil {
		t.Fatalf("VacationResponse/get after set: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	jsStr := string(js)
	if !strings.Contains(jsStr, `"htmlBody"`) {
		t.Errorf("htmlBody not in response: %s", jsStr)
	}
	if !strings.Contains(jsStr, "Out of office") {
		t.Errorf("htmlBody content not preserved: %s", jsStr)
	}

	// Set both text and html bodies.
	setArgs2, _ := json.Marshal(map[string]any{
		"accountId": accountID,
		"update": map[string]any{
			"singleton": map[string]any{
				"isEnabled": true,
				"textBody":  "I am away.",
				"htmlBody":  "<p>I am <b>away</b>.</p>",
			},
		},
	})
	_, mErr = setHandler{h: h}.executeAs(p, setArgs2)
	if mErr != nil {
		t.Fatalf("VacationResponse/set text+html: %v", mErr)
	}
	resp2, mErr := getHandler{h: h}.executeAs(p, getArgs)
	if mErr != nil {
		t.Fatalf("VacationResponse/get after set text+html: %v", mErr)
	}
	js2, _ := json.Marshal(resp2)
	js2Str := string(js2)
	if !strings.Contains(js2Str, `"textBody":"I am away."`) {
		t.Errorf("textBody not preserved in text+html: %s", js2Str)
	}
	if !strings.Contains(js2Str, "I am") {
		t.Errorf("htmlBody content not preserved in text+html: %s", js2Str)
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
