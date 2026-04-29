package admin

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// seedQueueItem inserts one queue row. Returns the assigned ID.
func seedQueueItem(t *testing.T, env *cliTestEnv, state store.QueueState) store.QueueItemID {
	t.Helper()
	ctx := context.Background()
	// Inject a body blob so EnqueueMessage's refcount step succeeds.
	ref, err := env.store.Blobs().Put(ctx, bytes.NewReader([]byte("From: a@b\r\nTo: c@d\r\n\r\nbody")))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	id, err := env.store.Meta().EnqueueMessage(ctx, store.QueueItem{
		MailFrom:      "a@b",
		RcptTo:        "c@d",
		EnvelopeID:    "env-1",
		BodyBlobHash:  ref.Hash,
		State:         store.QueueStateQueued,
		NextAttemptAt: env.clk.Now(),
	})
	if err != nil {
		t.Fatalf("EnqueueMessage: %v", err)
	}
	if state != store.QueueStateQueued && state != store.QueueStateUnknown {
		// Transition the row into the requested state via the store helpers
		// the CLI itself exercises.
		switch state {
		case store.QueueStateHeld:
			if err := env.store.Meta().HoldQueueItem(ctx, id); err != nil {
				t.Fatalf("HoldQueueItem: %v", err)
			}
		case store.QueueStateDeferred:
			// The store rejects deferring a queued row; first claim, then
			// reschedule.
			if _, err := env.store.Meta().ClaimDueQueueItems(ctx, env.clk.Now(), 1); err != nil {
				t.Fatalf("Claim: %v", err)
			}
			if err := env.store.Meta().RescheduleQueueItem(ctx, id, env.clk.Now().Add(time.Minute), "transient"); err != nil {
				t.Fatalf("Reschedule: %v", err)
			}
		}
	}
	return id
}

func TestCLIQueueList_Empty(t *testing.T) {
	env := newCLITestEnv(t, nil)
	out, _, err := env.run("queue", "list", "--json")
	if err != nil {
		t.Fatalf("queue list: %v", err)
	}
	if !strings.Contains(out, `"items"`) {
		t.Fatalf("expected items field; got %s", out)
	}
}

func TestCLIQueueShow_NotFound(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("queue", "show", "9999")
	if err == nil {
		t.Fatalf("expected error for missing id")
	}
	if !strings.Contains(err.Error(), "404") && !strings.Contains(strings.ToLower(err.Error()), "not_found") {
		t.Fatalf("expected 404 / not_found, got: %v", err)
	}
}

func TestCLIQueueRetry_NotFound(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("queue", "retry", "9999")
	if err == nil {
		t.Fatalf("expected error for missing id")
	}
	if !strings.Contains(err.Error(), "404") && !strings.Contains(strings.ToLower(err.Error()), "not_found") {
		t.Fatalf("expected 404 / not_found, got: %v", err)
	}
}

func TestCLIQueueHoldRelease_HappyPath(t *testing.T) {
	env := newCLITestEnv(t, nil)
	id := seedQueueItem(t, env, store.QueueStateQueued)
	idStr := uintStr(uint64(id))

	if _, _, err := env.run("queue", "hold", idStr); err != nil {
		t.Fatalf("queue hold: %v", err)
	}
	// The fakestore's hold transition exposes the held state via show.
	out, _, err := env.run("queue", "show", idStr, "--json")
	if err != nil {
		t.Fatalf("queue show: %v", err)
	}
	if !strings.Contains(out, `"held"`) {
		t.Fatalf("expected held in output: %s", out)
	}

	if _, _, err := env.run("queue", "release", idStr); err != nil {
		t.Fatalf("queue release: %v", err)
	}
	out, _, err = env.run("queue", "show", idStr, "--json")
	if err != nil {
		t.Fatalf("queue show: %v", err)
	}
	if !strings.Contains(out, `"queued"`) {
		t.Fatalf("expected queued after release: %s", out)
	}
}

func TestCLIQueueDelete_RequiresConfirm(t *testing.T) {
	env := newCLITestEnv(t, nil)
	id := seedQueueItem(t, env, store.QueueStateQueued)
	idStr := uintStr(uint64(id))

	// Non-confirming input aborts.
	_, _, err := env.runWithStdin("no\n", "queue", "delete", idStr)
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected aborted error; got %v", err)
	}

	// --force bypass deletes the row.
	if _, _, err := env.run("queue", "delete", idStr, "--force"); err != nil {
		t.Fatalf("queue delete --force: %v", err)
	}
	if _, _, err := env.run("queue", "show", idStr); err == nil {
		t.Fatalf("expected 404 after delete")
	}
}

func TestCLIQueueStats_RendersAllStates(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedQueueItem(t, env, store.QueueStateQueued)
	out, _, err := env.run("queue", "stats", "--json")
	if err != nil {
		t.Fatalf("queue stats: %v", err)
	}
	for _, s := range []string{"queued", "deferred", "inflight", "done", "failed", "held"} {
		if !strings.Contains(out, `"`+s+`"`) {
			t.Fatalf("stats missing state %s: %s", s, out)
		}
	}
}

func TestCLIQueueFlush_DeferredOnly(t *testing.T) {
	env := newCLITestEnv(t, nil)
	// Reject other states.
	_, _, err := env.run("queue", "flush", "--state=queued", "--force")
	if err == nil {
		t.Fatalf("expected reject for state=queued")
	}
	// Deferred works (even with no rows).
	if _, _, err := env.run("queue", "flush", "--state=deferred", "--force"); err != nil {
		t.Fatalf("flush deferred: %v", err)
	}
}

// TestCLIQueueList_HumanFormat exercises the default (non-JSON) human-
// readable table output for queue list. Non-TTY buffers normally fall
// through to JSON; we verify the human path by simulating a TTY writer
// via globalOptions{jsonOut: false} and calling writeQueueListHuman
// directly, and separately verify that the --json flag yields valid JSON
// from the real cobra dispatch path.
func TestCLIQueueList_HumanFormat(t *testing.T) {
	// Build a sample page envelope that matches the shape the REST API
	// returns from the queue list handler.
	out := map[string]any{
		"items": []any{
			map[string]any{
				"id":              float64(1),
				"state":           "deferred",
				"rcpt_to":         "sender@example.org",
				"mail_from":       "from@example.org",
				"attempts":        float64(6),
				"last_attempt_at": "2026-04-28T23:39:37Z",
				"last_error":      "mx resolution: null MX (RFC 7505)",
			},
			map[string]any{
				"id":              float64(2),
				"state":           "failed",
				"rcpt_to":         "sender@example.local",
				"mail_from":       "from@example.org",
				"attempts":        float64(0),
				"last_attempt_at": "2026-04-28T06:29:05Z",
				"last_error":      "550 no MX or A/AAAA for example.local",
			},
		},
		"next": nil,
	}

	var buf bytes.Buffer
	if err := writeQueueListHuman(&buf, out); err != nil {
		t.Fatalf("writeQueueListHuman: %v", err)
	}
	human := buf.String()

	// Header must be present.
	if !strings.Contains(human, "STATE") || !strings.Contains(human, "RCPT-TO") {
		t.Errorf("expected header columns STATE and RCPT-TO in:\n%s", human)
	}
	// Both rows must be present.
	if !strings.Contains(human, "sender@example.org") {
		t.Errorf("expected sender@example.org in:\n%s", human)
	}
	if !strings.Contains(human, "sender@example.local") {
		t.Errorf("expected sender@example.local in:\n%s", human)
	}
	if !strings.Contains(human, "deferred") {
		t.Errorf("expected state 'deferred' in:\n%s", human)
	}
	// Error text should be visible.
	if !strings.Contains(human, "RFC 7505") {
		t.Errorf("expected error snippet 'RFC 7505' in:\n%s", human)
	}
	// Must not contain raw Go map syntax.
	if strings.Contains(human, "map[") {
		t.Errorf("must not contain raw map[] syntax in:\n%s", human)
	}
}

func TestCLIQueueList_EmptyHuman(t *testing.T) {
	out := map[string]any{
		"items": []any{},
	}
	var buf bytes.Buffer
	if err := writeQueueListHuman(&buf, out); err != nil {
		t.Fatalf("writeQueueListHuman: %v", err)
	}
	human := buf.String()
	if !strings.Contains(human, "empty") {
		t.Errorf("expected empty queue message; got:\n%s", human)
	}
}

func TestCLIQueueList_JSONFlag(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedQueueItem(t, env, store.QueueStateDeferred)

	out, _, err := env.run("queue", "list", "--json")
	if err != nil {
		t.Fatalf("queue list --json: %v", err)
	}
	if !strings.Contains(out, `"items"`) {
		t.Fatalf("expected items field in JSON output; got %s", out)
	}
	if !strings.Contains(out, `"state"`) {
		t.Fatalf("expected state field in JSON output; got %s", out)
	}
	if !strings.Contains(out, `"rcpt_to"`) {
		t.Fatalf("expected rcpt_to field in JSON output; got %s", out)
	}
}

// uintStr is a tiny helper because strconv.FormatUint pulls in another
// import in three different test files.
func uintStr(n uint64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}
