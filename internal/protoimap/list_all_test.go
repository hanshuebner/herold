package protoimap

import (
	"context"
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// stubListMeta is a minimal store.Metadata stub used by listAllMessages
// tests. It implements only ListMessages by replaying preloaded pages
// keyed by AfterUID; every other Metadata method panics, which is what
// we want — listAllMessages must not call them.
type stubListMeta struct {
	store.Metadata // nil embedding -> any unimplemented call panics
	// pagesByAfter maps the requested AfterUID cursor to the page that
	// should be returned for that read. Limit is asserted to equal
	// listAllPageSize on every call.
	pagesByAfter map[store.UID][]store.Message
	limitSeen    []int
	withEnvSeen  []bool
	calls        int
	failOnCall   int // 1-based; 0 disables; helper returns errBoom.
}

var errBoom = errors.New("boom")

func (s *stubListMeta) ListMessages(_ context.Context, _ store.MailboxID, f store.MessageFilter) ([]store.Message, error) {
	s.calls++
	s.limitSeen = append(s.limitSeen, f.Limit)
	s.withEnvSeen = append(s.withEnvSeen, f.WithEnvelope)
	if s.failOnCall != 0 && s.calls == s.failOnCall {
		return nil, errBoom
	}
	page, ok := s.pagesByAfter[f.AfterUID]
	if !ok {
		return nil, nil
	}
	// Return a copy so the helper can't mutate our table by accident.
	out := make([]store.Message, len(page))
	copy(out, page)
	return out, nil
}

// makePage builds a slice of `n` messages with sequential UIDs starting
// from `startUID`. UIDs are what the helper uses to advance the cursor.
func makePage(startUID store.UID, n int) []store.Message {
	out := make([]store.Message, n)
	for i := 0; i < n; i++ {
		out[i] = store.Message{UID: startUID + store.UID(i)}
	}
	return out
}

func TestListAllMessages_PaginatesAcrossFullPages(t *testing.T) {
	// Three pages: 1000 + 1000 + 250. The helper should return 2250 rows
	// in UID-ascending order and stop after the short page.
	stub := &stubListMeta{
		pagesByAfter: map[store.UID][]store.Message{
			0:    makePage(1, 1000),    // UIDs 1..1000
			1000: makePage(1001, 1000), // UIDs 1001..2000
			2000: makePage(2001, 250),  // UIDs 2001..2250
		},
	}
	got, err := listAllMessages(context.Background(), stub, 7, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2250 {
		t.Fatalf("expected 2250 messages, got %d", len(got))
	}
	for i := 0; i < len(got); i++ {
		if want := store.UID(i + 1); got[i].UID != want {
			t.Fatalf("row %d: UID = %d, want %d", i, got[i].UID, want)
		}
	}
	if stub.calls != 3 {
		t.Fatalf("expected 3 ListMessages calls, got %d", stub.calls)
	}
	for i, lim := range stub.limitSeen {
		if lim != listAllPageSize {
			t.Fatalf("call %d: limit = %d, want %d", i, lim, listAllPageSize)
		}
	}
	for i, we := range stub.withEnvSeen {
		if !we {
			t.Fatalf("call %d: WithEnvelope=false, want true", i)
		}
	}
}

func TestListAllMessages_StopsOnShortFirstPage(t *testing.T) {
	stub := &stubListMeta{
		pagesByAfter: map[store.UID][]store.Message{
			0: makePage(1, 17),
		},
	}
	got, err := listAllMessages(context.Background(), stub, 1, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 17 {
		t.Fatalf("expected 17 messages, got %d", len(got))
	}
	if stub.calls != 1 {
		t.Fatalf("expected single ListMessages call, got %d", stub.calls)
	}
	if stub.withEnvSeen[0] {
		t.Fatalf("WithEnvelope should propagate as false")
	}
}

func TestListAllMessages_EmptyMailbox(t *testing.T) {
	stub := &stubListMeta{pagesByAfter: map[store.UID][]store.Message{}}
	got, err := listAllMessages(context.Background(), stub, 1, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil slice for empty mailbox, got %v", got)
	}
	if stub.calls != 1 {
		t.Fatalf("expected one ListMessages call, got %d", stub.calls)
	}
}

func TestListAllMessages_ExactlyOneFullPage(t *testing.T) {
	// Boundary case: backend returns exactly listAllPageSize rows on the
	// first call, then zero on the second. Helper must issue the second
	// call (it cannot tell from a full page alone whether more rows
	// remain) and then stop cleanly.
	stub := &stubListMeta{
		pagesByAfter: map[store.UID][]store.Message{
			0:                          makePage(1, listAllPageSize),
			store.UID(listAllPageSize): nil, // empty second page
		},
	}
	got, err := listAllMessages(context.Background(), stub, 1, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != listAllPageSize {
		t.Fatalf("expected %d messages, got %d", listAllPageSize, len(got))
	}
	if stub.calls != 2 {
		t.Fatalf("expected 2 ListMessages calls, got %d", stub.calls)
	}
}

func TestListAllMessages_PropagatesError(t *testing.T) {
	stub := &stubListMeta{
		pagesByAfter: map[store.UID][]store.Message{
			0:    makePage(1, listAllPageSize),
			1000: makePage(1001, 1),
		},
		failOnCall: 2,
	}
	_, err := listAllMessages(context.Background(), stub, 1, true)
	if !errors.Is(err, errBoom) {
		t.Fatalf("expected errBoom, got %v", err)
	}
}
