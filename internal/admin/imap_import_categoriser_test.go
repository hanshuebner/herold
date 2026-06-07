package admin

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/llmtest"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

// stubLLMClient is a minimal llmtest.ChatCompleter whose Complete always
// returns a fixed JSON response so categorise.Categoriser returns a fixed
// category without a real LLM endpoint.
type stubLLMClient struct {
	resp string
}

func (s *stubLLMClient) Complete(_ context.Context, _, _ string) (string, error) {
	return s.resp, nil
}

// Verify stubLLMClient satisfies the interface at compile time.
var _ llmtest.ChatCompleter = (*stubLLMClient)(nil)

// minimalRFC822 is a minimal valid RFC 822 message used to seed the blob store.
const minimalRFC822 = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: Test message\r\n" +
	"Message-ID: <test-cat-01@example.com>\r\n" +
	"\r\n" +
	"Hello, this is a test message for categorisation.\r\n"

// seedPrincipalAndMessage inserts a principal, an INBOX mailbox, and one
// message in the given store. Returns the principal and message rows.
func seedPrincipalAndMessage(t *testing.T, ctx context.Context, st store.Store, msgIDHeader string) (store.Principal, store.Message) {
	t.Helper()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "cattest@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	raw := strings.Replace(minimalRFC822,
		"<test-cat-01@example.com>",
		fmt.Sprintf("<%s>", msgIDHeader),
		1)

	blobRef, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(raw)))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}

	parsedMsg, parseErr := mailparse.Parse(bytes.NewReader([]byte(raw)), mailparse.NewParseOptions())
	if parseErr != nil {
		t.Fatalf("mailparse.Parse: %v", parseErr)
	}

	storeMsg := store.Message{
		PrincipalID:  p.ID,
		Size:         int64(len(raw)),
		Blob:         blobRef,
		InternalDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		ReceivedAt:   time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Envelope: store.Envelope{
			Subject:   parsedMsg.Envelope.Subject,
			From:      "alice@example.com",
			To:        "bob@example.com",
			MessageID: parsedMsg.Envelope.MessageID,
		},
	}
	target := store.MessageMailbox{MailboxID: mb.ID}
	_, _, err = st.Meta().InsertMessage(ctx, storeMsg, []store.MessageMailbox{target})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	// Retrieve the inserted message (InsertMessage doesn't return MessageID directly).
	norm := mailparse.NormalizeMessageID(parsedMsg.Envelope.MessageID)
	inserted, err := st.Meta().GetMessageByMessageIDHeader(ctx, p.ID, norm)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	return p, inserted
}

// TestIMAPImportCategoriserAdapter_AppliesCategory verifies that when the
// LLM assigns a category, the adapter applies the corresponding
// $category-<name> keyword to the stored message via UpdateMessageFlags.
func TestIMAPImportCategoriserAdapter_AppliesCategory(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)

	// Seed a principal and a message.
	p, msg := seedPrincipalAndMessage(t, ctx, st, "test-apply@example.com")

	// Build a categorise.Categoriser with an injected stub LLM client that
	// returns a fixed category. The injected client bypasses endpoint
	// validation per categorise.resolveClient's documented behaviour.
	// We also need the categorisation config to be Enabled.
	// GetCategorisationConfig seeds the row with Enabled:true on first access,
	// but the model must be non-empty (resolveClient checks this). Since the
	// injected LLMClient path skips the endpoint/model resolution, we supply
	// a model name for the Categoriser.
	cat := categorise.New(categorise.Options{
		Store:        st,
		Logger:       slog.Default(),
		Clock:        clk,
		LLMClient:    &stubLLMClient{resp: `{"categories":["work","newsletters"],"assigned":"work"}`},
		DefaultModel: "stub",
	})
	if cat == nil {
		t.Fatal("categorise.New returned nil")
	}

	logger := slog.Default()
	adapter := newIMAPImportCategoriserAdapter(st, cat, logger)

	// Trigger categorisation.
	err := adapter.Categorise(ctx,
		fmt.Sprint(p.ID),
		fmt.Sprint(msg.ID),
		"INBOX",
	)
	if err != nil {
		t.Fatalf("Categorise returned non-nil: %v", err)
	}

	// Assert that $category-work keyword was applied.
	updated, getErr := st.Meta().GetMessage(ctx, msg.ID)
	if getErr != nil {
		t.Fatalf("GetMessage after Categorise: %v", getErr)
	}
	var found bool
	for _, kw := range updated.Keywords {
		if kw == "$category-work" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected $category-work keyword, got Keywords=%v", updated.Keywords)
	}
}

// TestIMAPImportCategoriserAdapter_NoCategory verifies that when the LLM
// returns "none" (no category assigned), the adapter does not apply any
// $category- keyword and returns nil.
func TestIMAPImportCategoriserAdapter_NoCategory(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)

	p, msg := seedPrincipalAndMessage(t, ctx, st, "test-none@example.com")

	cat := categorise.New(categorise.Options{
		Store:        st,
		Logger:       slog.Default(),
		Clock:        clk,
		LLMClient:    &stubLLMClient{resp: `{"categories":["work","newsletters"],"assigned":null}`},
		DefaultModel: "stub",
	})

	adapter := newIMAPImportCategoriserAdapter(st, cat, slog.Default())

	if err := adapter.Categorise(ctx, fmt.Sprint(p.ID), fmt.Sprint(msg.ID), "INBOX"); err != nil {
		t.Fatalf("Categorise returned non-nil: %v", err)
	}

	updated, getErr := st.Meta().GetMessage(ctx, msg.ID)
	if getErr != nil {
		t.Fatalf("GetMessage after Categorise: %v", getErr)
	}
	for _, kw := range updated.Keywords {
		if strings.HasPrefix(kw, "$category-") {
			t.Errorf("expected no $category- keyword when LLM returns null, got keyword %q", kw)
		}
	}
}

// TestIMAPImportCategoriserAdapter_NilCategoriser verifies that when a nil
// *categorise.Categoriser is passed, Categorise returns nil without panicking.
// This exercises the nil-safe path in CategoriseRich (categoriser returns nil
// when constructed without a store).
func TestIMAPImportCategoriserAdapter_NilCategoriser(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)

	p, msg := seedPrincipalAndMessage(t, ctx, st, "test-nil-cat@example.com")

	// Pass nil categoriser: CategoriseRich is nil-safe.
	adapter := newIMAPImportCategoriserAdapter(st, nil, slog.Default())

	if err := adapter.Categorise(ctx, fmt.Sprint(p.ID), fmt.Sprint(msg.ID), "INBOX"); err != nil {
		t.Fatalf("Categorise with nil categoriser returned non-nil: %v", err)
	}

	// No keywords should have been applied.
	updated, getErr := st.Meta().GetMessage(ctx, msg.ID)
	if getErr != nil {
		t.Fatalf("GetMessage: %v", getErr)
	}
	for _, kw := range updated.Keywords {
		if strings.HasPrefix(kw, "$category-") {
			t.Errorf("expected no $category- keyword from nil categoriser, got %q", kw)
		}
	}
}

// TestIMAPImportCategoriserAdapter_FailureIsSwallowed verifies that when the
// message does not exist in the store (e.g. the ID is invalid), Categorise
// returns nil rather than propagating the store error, matching the
// non-blocking semantics requirement (REQ-FILT-230).
func TestIMAPImportCategoriserAdapter_FailureIsSwallowed(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)

	cat := categorise.New(categorise.Options{
		Store:        st,
		Logger:       slog.Default(),
		Clock:        clk,
		LLMClient:    &stubLLMClient{resp: `{"categories":["work"],"assigned":"work"}`},
		DefaultModel: "stub",
	})

	adapter := newIMAPImportCategoriserAdapter(st, cat, slog.Default())

	// Use a non-existent principal ID and message ID. GetMessage returns
	// ErrNotFound; the adapter must swallow it.
	err := adapter.Categorise(ctx, "999", "999", "INBOX")
	if err != nil {
		t.Errorf("Categorise must swallow store errors; got: %v", err)
	}
}

// TestIMAPImportCategoriserAdapter_BadPrincipalID verifies that a malformed
// principal ID string is swallowed rather than propagated.
func TestIMAPImportCategoriserAdapter_BadPrincipalID(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportCategoriserAdapter(st, nil, slog.Default())

	if err := adapter.Categorise(ctx, "not-a-number", "1", "INBOX"); err != nil {
		t.Errorf("bad principal ID must be swallowed, got: %v", err)
	}
}

// TestIMAPImportCategoriserAdapter_ZeroMessageID verifies that a zero message
// ID is a no-op and returns nil (the worker may pass 0 on a dedup hit with no
// Message-ID header).
func TestIMAPImportCategoriserAdapter_ZeroMessageID(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportCategoriserAdapter(st, nil, slog.Default())

	if err := adapter.Categorise(ctx, "1", "0", "INBOX"); err != nil {
		t.Errorf("zero message ID must return nil, got: %v", err)
	}
}
