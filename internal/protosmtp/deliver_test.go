package protosmtp_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
)

// fakeChatJSON returns a marshalled OpenAI chat-completions response.
func fakeChatJSON(content string) string {
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": content}},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// TestDelivery_Categoriser_AddsCategoryKeyword exercises the
// REQ-FILT-200 happy path: the spam plugin returns Ham, the Sieve
// outcome is Keep to INBOX, and the categoriser is mocked to return
// "promotions"; the stored Email's Keywords slice must carry
// "$category-promotions".
func TestDelivery_Categoriser_AddsCategoryKeyword(t *testing.T) {
	// Fake LLM endpoint that always answers "promotions".
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, fakeChatJSON(`{"category":"promotions"}`))
	}))
	t.Cleanup(llm.Close)

	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	spamPlug := fakeplugin.New("spam", "spam")
	spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.05}`), nil
	})
	ha.RegisterPlugin("spam", spamPlug)
	invoker := &fakePluginInvoker{reg: ha.Plugins}
	spamCls := spam.New(invoker, ha.Logger, ha.Clock)

	resolver := newResolverAdapter(ha.DNS)
	dkimV := maildkim.New(resolver, ha.Logger, ha.Clock)
	spfV := mailspf.New(resolver, ha.Clock)
	dmarcV := maildmarc.New(resolver)
	arcV := mailarc.New(resolver)
	interp := sieve.NewInterpreter()
	tlsStore, _ := newTestTLSStore(t)

	// Categoriser deadlines are computed against the supplied Clock. The
	// harness's FakeClock is anchored months behind real wall-clock so a
	// fake-clock-driven deadline lands in the past and the upstream
	// httptest call is cancelled before it begins. The categoriser's
	// timeout discipline is exercised in internal/categorise/_test; here
	// we use a real clock so the deadline lines up with the live
	// httptest server.
	cat := categorise.New(categorise.Options{
		Store:           ha.Store,
		Logger:          ha.Logger,
		Clock:           clock.NewReal(),
		DefaultEndpoint: llm.URL,
		DefaultModel:    "test-model",
		DefaultTimeout:  3 * time.Second,
	})

	srv, err := protosmtp.New(protosmtp.Config{
		Store:      ha.Store,
		Directory:  dir,
		DKIM:       dkimV,
		SPF:        spfV,
		DMARC:      dmarcV,
		ARC:        arcV,
		Spam:       spamCls,
		Sieve:      interp,
		Categorise: cat,
		TLS:        tlsStore,
		Resolver:   resolver,
		Clock:      ha.Clock,
		Logger:     ha.Logger,
		Options: protosmtp.Options{
			Hostname:                 "mx.example.test",
			AuthservID:               "mx.example.test",
			MaxMessageSize:           65536,
			ReadTimeout:              5 * time.Second,
			WriteTimeout:             5 * time.Second,
			DataTimeout:              10 * time.Second,
			ShutdownGrace:            2 * time.Second,
			MaxRecipientsPerMessage:  5,
			MaxCommandsPerSession:    200,
			MaxConcurrentConnections: 32,
			MaxConcurrentPerIP:       16,
		},
	})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	ha.AttachSMTP("smtp", srv, protosmtp.RelayIn)

	// Drive a single SMTP delivery.
	c, err := ha.DialSMTPByName(ctx, "smtp")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	cli := newSMTPClient(c)
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: deal\r\n\r\nLimited offer just for you!\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	// Locate the inserted message and verify its keywords.
	mb, err := ha.Store.Meta().GetMailboxByName(ctx, pid, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName: %v", err)
	}
	msgs, err := ha.Store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	have := strings.Join(msgs[0].Keywords, ",")
	if !strings.Contains(have, "$category-promotions") {
		t.Fatalf("keywords = %v, want $category-promotions present", msgs[0].Keywords)
	}
}
