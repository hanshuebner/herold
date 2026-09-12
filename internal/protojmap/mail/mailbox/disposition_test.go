package mailbox_test

// Category disposition / priority wire surface (issue #333, ADR-0004,
// docs/design/web/requirements/05-categorisation.md REQ-CAT-01/04/05/
// 10/11). The store-level mechanics (dense renumbering, the pinned
// count) are covered by
// internal/store/storetest/storetest_mailboxdisposition.go; this file
// exercises only the Mailbox/get + Mailbox/set + Mailbox/changes wiring
// over real HTTP: round-tripping both properties, the Mailbox state
// advancing and being reported by Mailbox/changes, the sixth-pinned
// refusal (tooManyPinned), the system-mailbox refusal
// (invalidProperties), and dense renumbering after moving a label's
// rank.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

// TestMailbox_DispositionAndPriority_Acceptance runs the full #333
// acceptance scenario against the default SQLite-backed fixture.
func TestMailbox_DispositionAndPriority_Acceptance(t *testing.T) {
	runDispositionAndPriorityAcceptance(t, setupFixture(t))
}

// TestMailbox_DispositionAndPriority_Acceptance_Postgres is the same
// scenario against a Postgres-backed store opened from HEROLD_PG_DSN,
// so the acceptance check runs against both backends per STANDARDS.md
// §8. Skips (rather than fails) when HEROLD_PG_DSN is unset or
// unreachable, matching the rest of the suite's Postgres-leg pattern.
func TestMailbox_DispositionAndPriority_Acceptance_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, nil)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runDispositionAndPriorityAcceptance(t, setupFixtureWithStore(t, st))
}

func runDispositionAndPriorityAcceptance(t *testing.T, f *fixture) {
	inbox := mustInsertMailbox(t, f, "INBOX", store.MailboxAttrInbox)

	// -- create a label and set disposition + priority through
	// Mailbox/set, then read both back through Mailbox/get. --
	createRaw, createResp := createMailbox(t, f, "Newsletters", "pinned", intPtr(0))
	labelID := createResp.Created["mb"]["id"].(string)
	if got := createResp.Created["mb"]["disposition"]; got != "pinned" {
		t.Fatalf("create response disposition = %v, want pinned (raw=%s)", got, createRaw)
	}
	if got, ok := createResp.Created["mb"]["priority"].(float64); !ok || got != 0 {
		t.Fatalf("create response priority = %v, want 0 (raw=%s)", createResp.Created["mb"]["priority"], createRaw)
	}
	afterCreateState := createResp.NewState

	_, getRaw := f.invoke(t, "Mailbox/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{labelID},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(getRaw, &getResp); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("Mailbox/get returned %d mailboxes, want 1 (raw=%s)", len(getResp.List), getRaw)
	}
	if got := getResp.List[0]["disposition"]; got != "pinned" {
		t.Errorf("get disposition = %v, want pinned", got)
	}
	if got, ok := getResp.List[0]["priority"].(float64); !ok || got != 0 {
		t.Errorf("get priority = %v, want 0", getResp.List[0]["priority"])
	}

	// -- moving the label's disposition/priority again advances the
	// Mailbox state, and Mailbox/changes since the earlier state
	// reports the label's id as updated. --
	_, updRaw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update":    map[string]any{labelID: map[string]any{"disposition": "bundled"}},
	})
	var updResp struct {
		NewState   string                    `json:"newState"`
		Updated    map[string]any            `json:"updated"`
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(updRaw, &updResp); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}
	if len(updResp.NotUpdated) != 0 {
		t.Fatalf("bundled update rejected: %+v (raw=%s)", updResp.NotUpdated, updRaw)
	}
	if updResp.NewState == afterCreateState {
		t.Errorf("Mailbox state did not advance after disposition update: %q", updResp.NewState)
	}

	_, chRaw := f.invoke(t, "Mailbox/changes", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"sinceState": afterCreateState,
	})
	var chResp struct {
		Updated []string `json:"updated"`
	}
	if err := json.Unmarshal(chRaw, &chResp); err != nil {
		t.Fatalf("unmarshal changes: %v", err)
	}
	found := false
	for _, id := range chResp.Updated {
		if id == labelID {
			found = true
		}
	}
	if !found {
		t.Errorf("Mailbox/changes updated list %v does not contain %s (raw=%s)", chResp.Updated, labelID, chRaw)
	}

	// -- disposition/priority are refused on a system mailbox. --
	inboxIDStr := fmt.Sprintf("%d", inbox.ID)
	_, sysRaw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update":    map[string]any{inboxIDStr: map[string]any{"disposition": "pinned"}},
	})
	var sysResp struct {
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(sysRaw, &sysResp); err != nil {
		t.Fatalf("unmarshal system-mailbox update: %v", err)
	}
	if got := sysResp.NotUpdated[inboxIDStr]["type"]; got != "invalidProperties" {
		t.Errorf("disposition on INBOX = %v, want invalidProperties (raw=%s)", got, sysRaw)
	}

	// -- a sixth pinned label is refused with tooManyPinned. The label
	// created above was reassigned to "bundled" a moment ago; repin it
	// (1 pinned), then pin four more to reach the limit of five, then
	// attempt a sixth. --
	_, _ = f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update":    map[string]any{labelID: map[string]any{"disposition": "pinned"}},
	})
	for i := 0; i < 4; i++ {
		mustCreateMailbox(t, f, fmt.Sprintf("Pinned%d", i), "pinned", nil)
	}
	// Five pinned labels now exist (labelID + the four created above).
	_, sixthRaw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create":    map[string]any{"sixth": map[string]any{"name": "SixthPinned", "disposition": "pinned"}},
	})
	var sixthResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(sixthRaw, &sixthResp); err != nil {
		t.Fatalf("unmarshal sixth pinned: %v", err)
	}
	if len(sixthResp.Created) != 0 {
		t.Fatalf("sixth pinned label was created, want tooManyPinned refusal (raw=%s)", sixthRaw)
	}
	if got := sixthResp.NotCreated["sixth"]["type"]; got != "tooManyPinned" {
		t.Errorf("sixth pinned create error = %v, want tooManyPinned (raw=%s)", got, sixthRaw)
	}

	// -- dense renumbering: three fresh ranked labels [R0:0, R1:1,
	// R2:2]; moving R2 to rank 0 must push R0 and R1 down to 1 and 2. --
	_, r0resp := mustCreateMailbox(t, f, "R0", "filed", intPtr(0))
	r0ID := r0resp.Created["mb"]["id"].(string)
	_, r1resp := mustCreateMailbox(t, f, "R1", "filed", intPtr(1))
	r1ID := r1resp.Created["mb"]["id"].(string)
	_, r2resp := mustCreateMailbox(t, f, "R2", "filed", intPtr(2))
	r2ID := r2resp.Created["mb"]["id"].(string)

	assertPriority(t, f, r0ID, 0)
	assertPriority(t, f, r1ID, 1)
	assertPriority(t, f, r2ID, 2)

	_, moveRaw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update":    map[string]any{r2ID: map[string]any{"priority": 0}},
	})
	var moveResp struct {
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(moveRaw, &moveResp); err != nil {
		t.Fatalf("unmarshal move: %v", err)
	}
	if len(moveResp.NotUpdated) != 0 {
		t.Fatalf("move R2 to rank 0 rejected: %+v (raw=%s)", moveResp.NotUpdated, moveRaw)
	}

	assertPriority(t, f, r2ID, 0)
	assertPriority(t, f, r0ID, 1)
	assertPriority(t, f, r1ID, 2)
}

// TestMailbox_Destroy_RenumbersRankedSurvivors covers the #333
// verification finding: Mailbox/set destroy of a ranked label shares
// store.Metadata.DeleteMailbox with IMAP DELETE, which must renumber
// the principal's remaining ranked labels densely and report the
// shift through Mailbox/changes -- not just leave a gap.
func TestMailbox_Destroy_RenumbersRankedSurvivors(t *testing.T) {
	runDestroyRenumbersRankedSurvivors(t, setupFixture(t))
}

// TestMailbox_Destroy_RenumbersRankedSurvivors_Postgres is the same
// scenario against a Postgres-backed store, skipping when HEROLD_PG_DSN
// is unset or unreachable.
func TestMailbox_Destroy_RenumbersRankedSurvivors_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, nil)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runDestroyRenumbersRankedSurvivors(t, setupFixtureWithStore(t, st))
}

func runDestroyRenumbersRankedSurvivors(t *testing.T, f *fixture) {
	// Three ranked labels [R0:0, R1:1, R2:2].
	_, r0resp := mustCreateMailbox(t, f, "DR0", "filed", intPtr(0))
	r0ID := r0resp.Created["mb"]["id"].(string)
	_, r1resp := mustCreateMailbox(t, f, "DR1", "filed", intPtr(1))
	r1ID := r1resp.Created["mb"]["id"].(string)
	_, r2resp := mustCreateMailbox(t, f, "DR2", "filed", intPtr(2))
	r2ID := r2resp.Created["mb"]["id"].(string)

	assertPriority(t, f, r0ID, 0)
	assertPriority(t, f, r1ID, 1)
	assertPriority(t, f, r2ID, 2)

	_, stateRaw := f.invoke(t, "Mailbox/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{r0ID},
	})
	var stateResp struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(stateRaw, &stateResp); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	beforeState := stateResp.State

	// Destroy the middle rank: R1 (priority 1). The sole higher-ranked
	// survivor, R2, must shift from priority 2 down to 1; R0 is
	// untouched.
	_, destroyRaw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"destroy":   []string{r1ID},
	})
	var destroyResp struct {
		Destroyed    []string                  `json:"destroyed"`
		NotDestroyed map[string]map[string]any `json:"notDestroyed"`
	}
	if err := json.Unmarshal(destroyRaw, &destroyResp); err != nil {
		t.Fatalf("unmarshal destroy: %v", err)
	}
	if len(destroyResp.NotDestroyed) != 0 {
		t.Fatalf("destroy R1 rejected: %+v (raw=%s)", destroyResp.NotDestroyed, destroyRaw)
	}

	assertPriority(t, f, r0ID, 0)
	assertPriority(t, f, r2ID, 1)

	_, chRaw := f.invoke(t, "Mailbox/changes", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"sinceState": beforeState,
	})
	var chResp struct {
		Updated   []string `json:"updated"`
		Destroyed []string `json:"destroyed"`
	}
	if err := json.Unmarshal(chRaw, &chResp); err != nil {
		t.Fatalf("unmarshal changes: %v", err)
	}
	destroyedFound := false
	for _, id := range chResp.Destroyed {
		if id == r1ID {
			destroyedFound = true
		}
	}
	if !destroyedFound {
		t.Errorf("Mailbox/changes destroyed list %v does not contain %s (raw=%s)", chResp.Destroyed, r1ID, chRaw)
	}
	updatedFound := false
	for _, id := range chResp.Updated {
		if id == r2ID {
			updatedFound = true
		}
	}
	if !updatedFound {
		t.Errorf("Mailbox/changes updated list %v does not contain the renumbered %s (raw=%s)", chResp.Updated, r2ID, chRaw)
	}
}

// intPtr returns a pointer to v, for the inline priority literals above.
func intPtr(v int) *int { return &v }

// createMailbox issues a Mailbox/set create for a single label named
// name with the given disposition/priority and returns the raw
// response body plus its decoded shape.
func createMailbox(t *testing.T, f *fixture, name, disposition string, priority *int) (string, struct {
	NewState   string                    `json:"newState"`
	Created    map[string]map[string]any `json:"created"`
	NotCreated map[string]map[string]any `json:"notCreated"`
}) {
	t.Helper()
	props := map[string]any{"name": name, "disposition": disposition}
	if priority != nil {
		props["priority"] = *priority
	}
	_, raw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create":    map[string]any{"mb": props},
	})
	var resp struct {
		NewState   string                    `json:"newState"`
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal create %s: %v", name, err)
	}
	if len(resp.NotCreated) != 0 {
		t.Fatalf("create %s failed: %+v (raw=%s)", name, resp.NotCreated, raw)
	}
	return string(raw), resp
}

// mustCreateMailbox is createMailbox without the disposition-provided
// requirement -- used where the disposition argument itself is the
// point under test (e.g. the pinned-limit and reorder cases). It fails
// the test if the create is refused.
func mustCreateMailbox(t *testing.T, f *fixture, name, disposition string, priority *int) (string, struct {
	Created    map[string]map[string]any `json:"created"`
	NotCreated map[string]map[string]any `json:"notCreated"`
}) {
	t.Helper()
	props := map[string]any{"name": name}
	if disposition != "" {
		props["disposition"] = disposition
	}
	if priority != nil {
		props["priority"] = *priority
	}
	_, raw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create":    map[string]any{"mb": props},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal create %s: %v", name, err)
	}
	if len(resp.NotCreated) != 0 {
		t.Fatalf("create %s failed: %+v (raw=%s)", name, resp.NotCreated, raw)
	}
	return string(raw), resp
}

// assertPriority reads mailboxID's priority through Mailbox/get and
// fails the test if it does not equal want.
func assertPriority(t *testing.T, f *fixture, mailboxID string, want int) {
	t.Helper()
	_, raw := f.invoke(t, "Mailbox/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{mailboxID},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal get(%s): %v", mailboxID, err)
	}
	if len(resp.List) != 1 {
		t.Fatalf("Mailbox/get(%s) returned %d mailboxes", mailboxID, len(resp.List))
	}
	got, ok := resp.List[0]["priority"].(float64)
	if !ok {
		t.Fatalf("Mailbox/get(%s) priority = %v, want %d", mailboxID, resp.List[0]["priority"], want)
	}
	if int(got) != want {
		t.Errorf("Mailbox/get(%s) priority = %d, want %d", mailboxID, int(got), want)
	}
}
