package searchsnippet

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
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

func TestSearchSnippet_Get_HighlightsBodyHits(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p, _ := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	mb, _ := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	body := "Subject: Invoice 123\r\n\r\nPlease find attached our latest invoice for review.\r\n"
	ref, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	uid, _, err := st.Meta().InsertMessage(ctx, store.Message{
		Blob: ref,
		Size: int64(len(body)),
		Envelope: store.Envelope{
			Subject: "Invoice 123",
		},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	// Resolve the MessageID via list.
	msgs, _ := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
	var mid store.MessageID
	for _, m := range msgs {
		if m.UID == uid {
			mid = m.ID
		}
	}
	h := &handlerSet{store: st}
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"filter":    map[string]any{"body": "invoice"},
		"emailIds":  []string{toJMAPID(mid)},
	})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("SearchSnippet/get: %v", mErr)
	}
	g := resp.(getResponse)
	if len(g.List) != 1 {
		t.Fatalf("expected 1 snippet, got %d", len(g.List))
	}
	if g.List[0].Preview == nil || !strings.Contains(*g.List[0].Preview, "<mark>invoice</mark>") {
		t.Fatalf("expected highlight in preview: %v", g.List[0].Preview)
	}
}

func TestSearchSnippet_Get_HighlightsSubject(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p, _ := st.Meta().InsertPrincipal(ctx, store.Principal{
		CanonicalEmail: "alice@example.test",
	})
	mb, _ := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	body := "From: bob@example.test\r\n\r\nshort body.\r\n"
	ref, _ := st.Blobs().Put(ctx, bytes.NewReader([]byte(body)))
	uid, _, _ := st.Meta().InsertMessage(ctx, store.Message{
		Blob:     ref,
		Size:     int64(len(body)),
		Envelope: store.Envelope{Subject: "Quarterly invoice update"},
	}, []store.MessageMailbox{{MailboxID: mb.ID}})
	msgs, _ := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100, WithEnvelope: true})
	var mid store.MessageID
	for _, m := range msgs {
		if m.UID == uid {
			mid = m.ID
		}
	}
	h := &handlerSet{store: st}
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"filter":    map[string]any{"subject": "invoice"},
		"emailIds":  []string{toJMAPID(mid)},
	})
	resp, _ := getHandler{h: h}.executeAs(p, args)
	g := resp.(getResponse)
	if len(g.List) != 1 {
		t.Fatalf("expected 1 snippet")
	}
	want := "Quarterly <mark>invoice</mark> update"
	if g.List[0].Subject == nil || *g.List[0].Subject != want {
		t.Fatalf("expected highlighted subject %q, got %v", want, g.List[0].Subject)
	}
}

func TestSearchSnippet_Get_NotFoundForUnknownIDs(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	p, _ := st.Meta().InsertPrincipal(ctx, store.Principal{CanonicalEmail: "alice@example.test"})
	h := &handlerSet{store: st}
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"filter":    map[string]any{"body": "anything"},
		"emailIds":  []string{"99999"},
	})
	resp, _ := getHandler{h: h}.executeAs(p, args)
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"notFound":["99999"]`) {
		t.Fatalf("expected notFound: %s", js)
	}
}

func TestHighlight_WholeWordOnly(t *testing.T) {
	got := highlight("invoicing the team", []string{"invoice"})
	if strings.Contains(got, "<mark>") {
		t.Fatalf("partial match should not highlight: %q", got)
	}
}

func TestSnippet_CentresOnFirstMatch(t *testing.T) {
	prefix := strings.Repeat("padding ", 50)
	text := prefix + "the keyword foo appears here"
	out := snippet(text, []string{"foo"})
	if !strings.HasPrefix(out, "…") {
		t.Fatalf("expected ellipsis prefix: %q", out)
	}
	if !strings.Contains(out, "<mark>foo</mark>") {
		t.Fatalf("expected highlight in snippet: %q", out)
	}
}

func toJMAPID(id store.MessageID) string {
	b, _ := json.Marshal(id)
	return strings.Trim(string(b), `"`)
}
