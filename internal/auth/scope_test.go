package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/auth"
)

func TestScopeSet_HasAndHasAll(t *testing.T) {
	t.Parallel()
	s := auth.NewScopeSet(auth.ScopeAdmin, auth.ScopeMailSend)
	if !s.Has(auth.ScopeAdmin) {
		t.Fatalf("Has(admin) = false, want true")
	}
	if s.Has(auth.ScopeChatRead) {
		t.Fatalf("Has(chat.read) = true, want false")
	}
	if !s.HasAll(auth.ScopeAdmin, auth.ScopeMailSend) {
		t.Fatalf("HasAll(admin, mail.send) = false, want true")
	}
	if s.HasAll(auth.ScopeAdmin, auth.ScopeChatRead) {
		t.Fatalf("HasAll(admin, chat.read) = true, want false")
	}
}

func TestAllEndUserScopes_NoAdminNoWebhook(t *testing.T) {
	t.Parallel()
	for _, s := range auth.AllEndUserScopes {
		if s == auth.ScopeAdmin {
			t.Fatalf("AllEndUserScopes contains admin: %v", auth.AllEndUserScopes)
		}
		if s == auth.ScopeWebhookPublish {
			t.Fatalf("AllEndUserScopes contains webhook.publish: %v", auth.AllEndUserScopes)
		}
	}
}

func TestParseScope_Unknown(t *testing.T) {
	t.Parallel()
	if _, err := auth.ParseScope("admin"); err != nil {
		t.Fatalf("ParseScope(admin): %v", err)
	}
	if _, err := auth.ParseScope("nope"); !errors.Is(err, auth.ErrUnknownScope) {
		t.Fatalf("ParseScope(nope) error = %v, want ErrUnknownScope", err)
	}
}

func TestParseScopeList_DedupAndOrder(t *testing.T) {
	t.Parallel()
	got, err := auth.ParseScopeList("mail.send, admin , mail.send")
	if err != nil {
		t.Fatalf("ParseScopeList: %v", err)
	}
	if len(got) != 2 || got[0] != auth.ScopeAdmin || got[1] != auth.ScopeMailSend {
		t.Fatalf("ParseScopeList = %v, want [admin mail.send]", got)
	}
	if _, err := auth.ParseScopeList("admin,,mail.send"); err == nil {
		t.Fatalf("ParseScopeList empty entry: want error")
	}
	if _, err := auth.ParseScopeList("admin,bogus"); !errors.Is(err, auth.ErrUnknownScope) {
		t.Fatalf("ParseScopeList unknown: %v, want ErrUnknownScope", err)
	}
}

func TestScopeSet_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	s := auth.NewScopeSet(auth.ScopeMailSend, auth.ScopeAdmin)
	enc, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got, want := string(enc), `["admin","mail.send"]`; got != want {
		t.Fatalf("Marshal = %s, want %s", got, want)
	}
	var back auth.ScopeSet
	if err := json.Unmarshal(enc, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !back.HasAll(auth.ScopeAdmin, auth.ScopeMailSend) {
		t.Fatalf("round trip lost members: %v", back)
	}
	// Unknown rejected at unmarshal.
	if err := json.Unmarshal([]byte(`["bogus"]`), &back); err == nil ||
		!strings.Contains(err.Error(), "unknown scope") {
		t.Fatalf("Unmarshal unknown: %v", err)
	}
}

func TestRequireScope_Errors(t *testing.T) {
	t.Parallel()
	if err := auth.RequireScope(context.Background(), auth.ScopeAdmin); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("nil context error = %v, want ErrUnauthenticated", err)
	}
	ctx := auth.WithContext(context.Background(), &auth.AuthContext{
		PrincipalID: 1,
		Scopes:      auth.NewScopeSet(auth.ScopeMailSend),
		Listener:    "public",
	})
	if err := auth.RequireScope(ctx, auth.ScopeMailSend); err != nil {
		t.Fatalf("RequireScope match: %v", err)
	}
	if err := auth.RequireScope(ctx, auth.ScopeAdmin); !errors.Is(err, auth.ErrInsufficientScope) {
		t.Fatalf("RequireScope mismatch error = %v, want ErrInsufficientScope", err)
	}
}
