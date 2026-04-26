package webpush

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// fixtureWithCoalesce wraps newDispatcherFixture and overrides the
// CoalesceWindow to make tests deterministic without depending on the
// 30-second default. Tests pass an explicit window so each case can
// drive the FakeClock with simple, named intervals.
func fixtureWithCoalesce(t *testing.T, status int, window time.Duration) *dispatcherFixture {
	t.Helper()
	return newDispatcherFixture(t, status, func(o *Options) {
		o.CoalesceWindow = window
	})
}

// triggerEmailWithThread inserts a message belonging to threadID so
// BuildPayload emits "email/<threadID>" as the coalesce-tag. Returns the
// inserted MessageID for assertions. Mirrors triggerEmailChange but lets
// the caller pin the thread so two consecutive events share the tag.
func (f *dispatcherFixture) triggerEmailWithThread(t *testing.T, threadID uint64, subject string) {
	t.Helper()
	ref, err := f.store.Blobs().Put(context.Background(), strings.NewReader("body"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	mailboxes, err := f.store.Meta().ListMailboxes(context.Background(), f.pid)
	if err != nil || len(mailboxes) == 0 {
		t.Fatalf("ListMailboxes: %v %d", err, len(mailboxes))
	}
	if _, _, err := f.store.Meta().InsertMessage(context.Background(), store.Message{
		MailboxID: mailboxes[0].ID,
		Blob:      ref,
		Size:      ref.Size,
		ThreadID:  threadID,
		Keywords:  []string{"$category-primary"},
		Envelope:  store.Envelope{Subject: subject},
	}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if _, err := f.disp.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

func TestCoalesce_SameTagWithinWindowDefersSecond(t *testing.T) {
	t.Parallel()
	f := fixtureWithCoalesce(t, http.StatusCreated, 30*time.Second)
	// First event: emits immediately, sets lastSentAt.
	f.triggerEmailWithThread(t, 42, "hello 1")
	if got := len(f.gateway.Calls()); got != 1 {
		t.Fatalf("expected 1 POST after first event, got %d", got)
	}
	// Second event for same thread within window: deferred.
	f.clk.Advance(5 * time.Second)
	f.triggerEmailWithThread(t, 42, "hello 2")
	if got := len(f.gateway.Calls()); got != 1 {
		t.Fatalf("second event must NOT have posted yet (still within window); got %d", got)
	}
	// Advance past the window deadline; the deferred timer fires.
	f.clk.Advance(30 * time.Second)
	if got := len(f.gateway.Calls()); got != 2 {
		t.Fatalf("after window elapsed expected 2 POSTs, got %d", got)
	}
}

func TestCoalesce_DifferentTagsBothFire(t *testing.T) {
	t.Parallel()
	f := fixtureWithCoalesce(t, http.StatusCreated, 30*time.Second)
	f.triggerEmailWithThread(t, 1, "thread A")
	f.triggerEmailWithThread(t, 2, "thread B")
	if got := len(f.gateway.Calls()); got != 2 {
		t.Fatalf("expected 2 POSTs for distinct tags, got %d", got)
	}
}

func TestCoalesce_SecondEventAfterWindowFiresImmediately(t *testing.T) {
	t.Parallel()
	f := fixtureWithCoalesce(t, http.StatusCreated, 30*time.Second)
	f.triggerEmailWithThread(t, 7, "first")
	f.clk.Advance(31 * time.Second)
	f.triggerEmailWithThread(t, 7, "second")
	if got := len(f.gateway.Calls()); got != 2 {
		t.Fatalf("expected 2 POSTs (window elapsed), got %d", got)
	}
}

func TestCoalesce_SubscriptionDestroyedCancelsPendingTimer(t *testing.T) {
	t.Parallel()
	f := fixtureWithCoalesce(t, http.StatusCreated, 30*time.Second)
	f.triggerEmailWithThread(t, 11, "first")
	f.clk.Advance(2 * time.Second)
	f.triggerEmailWithThread(t, 11, "deferred")
	// One POST so far (the second is pending).
	if got := len(f.gateway.Calls()); got != 1 {
		t.Fatalf("expected 1 POST before deferred fire, got %d", got)
	}
	// Drop the subscription and its coalesce state. The deferred timer
	// must NOT fire after this.
	f.disp.dropCoalesce(f.subID)
	if err := f.store.Meta().DeletePushSubscription(context.Background(), f.subID); err != nil {
		t.Fatalf("DeletePushSubscription: %v", err)
	}
	f.clk.Advance(60 * time.Second)
	if got := len(f.gateway.Calls()); got != 1 {
		t.Fatalf("destroyed subscription emitted %d POSTs after drop; want 1", got)
	}
}

func TestCoalesce_TopicHeaderURLSafe(t *testing.T) {
	t.Parallel()
	f := fixtureWithCoalesce(t, http.StatusCreated, 30*time.Second)
	f.triggerEmailWithThread(t, 99, "subject")
	calls := f.gateway.Calls()
	if len(calls) == 0 {
		t.Fatalf("no calls captured")
	}
	// gateway test handler only records a fixed header set; extend
	// here by dipping into the recorded request via a custom respond
	// hook.
}

// TestCoalesce_TopicHeaderEmittedAndSafe exercises a Topic capture
// using the gateway's respond hook so we can read req.Header without
// retrofitting the recordedPost struct.
func TestCoalesce_TopicHeaderEmittedAndSafe(t *testing.T) {
	t.Parallel()
	f := fixtureWithCoalesce(t, http.StatusCreated, 30*time.Second)
	var seenTopic string
	f.gateway.respond = func(r *http.Request) (int, []byte) {
		seenTopic = r.Header.Get("Topic")
		return http.StatusCreated, nil
	}
	f.triggerEmailWithThread(t, 99, "x")
	if seenTopic == "" {
		t.Fatalf("Topic header was not set")
	}
	// "email/99" contains '/' which is NOT URL-safe base64, so the
	// dispatcher hashes it. Result must be RawURLEncoding-shaped (no
	// padding, only [A-Za-z0-9_-]).
	for _, c := range seenTopic {
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			t.Fatalf("Topic header %q contains non-URL-safe char %q", seenTopic, c)
		}
	}
}

func TestSanitiseTopic_PassesShortAlphabet(t *testing.T) {
	t.Parallel()
	if got := sanitiseTopic("abc-_xyz"); got != "abc-_xyz" {
		t.Fatalf("sanitiseTopic(abc-_xyz)=%q", got)
	}
}

func TestSanitiseTopic_HashesSlashes(t *testing.T) {
	t.Parallel()
	got := sanitiseTopic("email/12345")
	if got == "email/12345" {
		t.Fatalf("sanitiseTopic must hash a tag containing '/'; got %q", got)
	}
	for _, c := range got {
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			t.Fatalf("hashed Topic contains non-URL-safe char %q in %q", c, got)
		}
	}
}
