package categorise_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/store/storetest/backends"
)

func TestSenderRuleCategory_CaseInsensitiveExactMatch(t *testing.T) {
	rules := []store.SenderCategoryRule{
		{Sender: "news@shop.example", Category: "promotions"},
		{Sender: "Mixed@Case.Example", Category: "social"},
	}
	cases := []struct {
		from []mail.Address
		want string
	}{
		{[]mail.Address{{Address: "NEWS@Shop.Example"}}, "promotions"},
		{[]mail.Address{{Address: "mixed@case.example"}}, "social"},
		{[]mail.Address{{Address: "other@shop.example"}}, ""},
		{[]mail.Address{{Address: "news@shop.example.evil"}}, ""},
		{[]mail.Address{{Address: "x@y.example"}, {Address: "news@shop.example"}}, "promotions"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := categorise.SenderRuleCategory(rules, c.from); got != c.want {
			t.Errorf("SenderRuleCategory(%v) = %q, want %q", c.from, got, c.want)
		}
	}
	if got := categorise.SenderRuleCategory(nil, []mail.Address{{Address: "a@b.example"}}); got != "" {
		t.Errorf("no rules: got %q", got)
	}
}

func TestFromAddresses(t *testing.T) {
	got := categorise.FromAddresses(`"Bob B" <Bob@Sender.Test>, alice@x.example`)
	if len(got) != 2 || got[0] != "bob@sender.test" || got[1] != "alice@x.example" {
		t.Fatalf("FromAddresses = %v", got)
	}
	if got := categorise.FromAddresses("not an address"); got != nil {
		t.Fatalf("unparseable = %v, want nil", got)
	}
}

// ruledMessage returns a parsed message from the given From header value.
func ruledMessage(t *testing.T, from string) string {
	t.Helper()
	return "From: " + from + "\r\nTo: alice@example.test\r\nSubject: hello\r\n\r\nGood morning.\r\n"
}

func insertFrom(t *testing.T, st store.Store, pid store.PrincipalID, mb store.MailboxID, from string, keywords ...string) {
	t.Helper()
	ctx := context.Background()
	body := ruledMessage(t, from)
	ref, err := st.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	_, _, err = st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  pid,
		Size:         int64(len(body)),
		Blob:         ref,
		ReceivedAt:   time.Now(),
		InternalDate: time.Now(),
		Envelope:     store.Envelope{From: from, Subject: "hello"},
	}, []store.MessageMailbox{{MailboxID: mb, Keywords: keywords}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
}

func categoryOf(t *testing.T, st store.Store, mb store.MailboxID, from string) []string {
	t.Helper()
	msgs, err := st.Meta().ListMessages(context.Background(), mb, store.MessageFilter{Limit: 100, WithEnvelope: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var out []string
	for _, m := range msgs {
		if m.Envelope.From == from {
			out = append(out, categoryKeywords(m.Keywords)...)
		}
	}
	return out
}

func categoryKeywords(keys []string) []string {
	var out []string
	for _, k := range keys {
		if strings.HasPrefix(k, categorise.CategoryKeywordPrefix) {
			out = append(out, k)
		}
	}
	return out
}

func newPrincipal(t *testing.T, st store.Store) store.PrincipalID {
	t.Helper()
	p, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		CanonicalEmail: "alice@example.test",
		PasswordHash:   "hash",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	return p.ID
}

// A message from a ruled sender is categorised from the rule with zero
// classifier calls; a message from any other sender still reaches the
// classifier.
func TestCategorise_SenderRule_SkipsClassifier(t *testing.T) {
	backends.Each(t, func(t *testing.T, st store.Store) {
		fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, newCategoriserReply("updates"))
		})
		pid := newPrincipal(t, st)
		ctx := context.Background()
		cfg, err := st.Meta().GetCategorisationConfig(ctx, pid)
		if err != nil {
			t.Fatal(err)
		}
		cfg.SenderRules = []store.SenderCategoryRule{{Sender: "bob@sender.test", Category: "social"}}
		if err := st.Meta().UpdateCategorisationConfig(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		c := categorise.New(categorise.Options{
			Store: st, Clock: clock.NewFake(time.Now()),
			DefaultEndpoint: fs.srv.URL, DefaultModel: "m",
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})

		// parsedMessage is From: bob@sender.test.
		got, err := c.Categorise(ctx, pid, parsedMessage(t), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got != "social" {
			t.Fatalf("ruled sender category = %q, want social", got)
		}
		if n := fs.requests.Load(); n != 0 {
			t.Fatalf("classifier calls for ruled sender = %d, want 0", n)
		}

		// Control: rule removed, same message reaches the classifier.
		cfg.SenderRules = nil
		if err := st.Meta().UpdateCategorisationConfig(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		got, err = c.Categorise(ctx, pid, parsedMessage(t), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got != "updates" || fs.requests.Load() != 1 {
			t.Fatalf("unruled: category=%q calls=%d, want updates/1", got, fs.requests.Load())
		}
	})
}

// RecategoriseSender rewrites the keyword of the sender's stored mail
// (matching the From address case-insensitively and exactly) and leaves
// other senders, look-alike addresses, and Trash memberships untouched.
func TestRecategoriseSender_AppliesToSenderOnly(t *testing.T) {
	backends.Each(t, func(t *testing.T, st store.Store) {
		pid := newPrincipal(t, st)
		ctx := context.Background()
		inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: pid, Name: "INBOX", Attributes: store.MailboxAttrInbox})
		if err != nil {
			t.Fatal(err)
		}
		trash, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: pid, Name: "Trash", Attributes: store.MailboxAttrTrash})
		if err != nil {
			t.Fatal(err)
		}
		insertFrom(t, st, pid, inbox.ID, "Bob <BOB@Sender.Test>", "$category-promotions", "$category-forums")
		insertFrom(t, st, pid, inbox.ID, "bob@sender.test")
		insertFrom(t, st, pid, inbox.ID, "other@sender.test", "$category-updates")
		insertFrom(t, st, pid, inbox.ID, "bob@sender.test.evil", "$category-updates")
		insertFrom(t, st, pid, trash.ID, "trashed@sender.test")
		insertFrom(t, st, pid, trash.ID, "bob@sender.test", "$category-promotions")

		n, err := categorise.RecategoriseSender(ctx, st, pid, "Bob@Sender.Test", "social")
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("recategorised = %d, want 2", n)
		}
		if got := categoryOf(t, st, inbox.ID, "Bob <BOB@Sender.Test>"); len(got) != 1 || got[0] != "$category-social" {
			t.Errorf("display-name form keywords = %v", got)
		}
		if got := categoryOf(t, st, inbox.ID, "bob@sender.test"); len(got) != 1 || got[0] != "$category-social" {
			t.Errorf("plain form keywords = %v", got)
		}
		if got := categoryOf(t, st, inbox.ID, "other@sender.test"); len(got) != 1 || got[0] != "$category-updates" {
			t.Errorf("other sender keywords = %v", got)
		}
		if got := categoryOf(t, st, inbox.ID, "bob@sender.test.evil"); len(got) != 1 || got[0] != "$category-updates" {
			t.Errorf("look-alike sender keywords = %v", got)
		}
		if got := categoryOf(t, st, trash.ID, "bob@sender.test"); len(got) != 1 || got[0] != "$category-promotions" {
			t.Errorf("trash membership keywords = %v", got)
		}

		// Idempotent: a second run changes nothing it has to rewrite.
		before, err := st.Meta().ReadChangeFeed(ctx, pid, 0, 10000)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := categorise.RecategoriseSender(ctx, st, pid, "bob@sender.test", "social"); err != nil {
			t.Fatal(err)
		}
		after, err := st.Meta().ReadChangeFeed(ctx, pid, 0, 10000)
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Errorf("second run wrote %d change entries, want 0", len(after)-len(before))
		}
	})
}
