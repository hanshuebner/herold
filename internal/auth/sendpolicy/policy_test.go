package sendpolicy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/auth/sendpolicy"
	"github.com/hanshuebner/herold/internal/store"
)

// stubChecker is a test double for Checker that resolves addresses from
// a static map.  Key: lowercased addr-spec.  Value: the owning principal ID.
type stubChecker struct {
	m map[string]store.PrincipalID
	// errFor triggers an error return for the matching address.
	errFor string
}

func (s *stubChecker) PrincipalOwnsAddress(_ context.Context, p store.Principal, addr string) (bool, error) {
	if s.errFor != "" && s.errFor == addr {
		return false, errors.New("store: transient backend error")
	}
	pid, ok := s.m[addr]
	if !ok {
		return false, nil
	}
	return pid == p.ID, nil
}

func newStub(pairs ...any) *stubChecker {
	m := make(map[string]store.PrincipalID)
	for i := 0; i+1 < len(pairs); i += 2 {
		addr := pairs[i].(string)
		pid := pairs[i+1].(store.PrincipalID)
		m[addr] = pid
	}
	return &stubChecker{m: m}
}

func principal(id store.PrincipalID, canonical string, flags store.PrincipalFlags) store.Principal {
	return store.Principal{ID: id, CanonicalEmail: canonical, Flags: flags}
}

func TestCheckFrom_OwnAddress_Allowed(t *testing.T) {
	p := principal(1, "alice@example.test", 0)
	chk := newStub("alice@example.test", store.PrincipalID(1))
	dec, err := sendpolicy.CheckFrom(context.Background(), chk, p, nil, "alice@example.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("expected allowed, got reason=%q", dec.Reason)
	}
}

func TestCheckFrom_AliasAddress_Allowed(t *testing.T) {
	p := principal(1, "alice@example.test", 0)
	// alias "a2@example.test" also resolves to principal 1.
	chk := newStub(
		"alice@example.test", store.PrincipalID(1),
		"a2@example.test", store.PrincipalID(1),
	)
	dec, err := sendpolicy.CheckFrom(context.Background(), chk, p, nil, "a2@example.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("expected allowed, got reason=%q", dec.Reason)
	}
}

func TestCheckFrom_OtherAddress_Denied(t *testing.T) {
	p := principal(1, "alice@example.test", 0)
	// "bob@example.test" is owned by principal 2.
	chk := newStub(
		"alice@example.test", store.PrincipalID(1),
		"bob@example.test", store.PrincipalID(2),
	)
	dec, err := sendpolicy.CheckFrom(context.Background(), chk, p, nil, "bob@example.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Error("expected denied")
	}
	if dec.Reason != sendpolicy.ReasonNotOwned {
		t.Errorf("reason=%q want %q", dec.Reason, sendpolicy.ReasonNotOwned)
	}
}

func TestCheckFrom_ExternalAddress_Denied(t *testing.T) {
	p := principal(1, "alice@example.test", 0)
	chk := newStub("alice@example.test", store.PrincipalID(1))
	dec, err := sendpolicy.CheckFrom(context.Background(), chk, p, nil, "display@external.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Error("expected denied for external address")
	}
	if dec.Reason != sendpolicy.ReasonNotOwned {
		t.Errorf("reason=%q want %q", dec.Reason, sendpolicy.ReasonNotOwned)
	}
}

func TestCheckFrom_AdminBypass(t *testing.T) {
	p := principal(1, "alice@example.test", store.PrincipalFlagAdmin)
	// chk has no mapping for the from address — admin bypasses the check.
	chk := newStub()
	dec, err := sendpolicy.CheckFrom(context.Background(), chk, p, nil, "anyone@example.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("admin should be allowed, got reason=%q", dec.Reason)
	}
}

func TestCheckFrom_KeyAllowedAddresses_Allowed(t *testing.T) {
	p := principal(1, "alice@example.test", 0)
	chk := newStub("alice@example.test", store.PrincipalID(1))
	key := &store.APIKey{
		ID:                   10,
		AllowedFromAddresses: []string{"alice@example.test"},
	}
	dec, err := sendpolicy.CheckFrom(context.Background(), chk, p, key, "alice@example.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("expected allowed, got reason=%q", dec.Reason)
	}
}

func TestCheckFrom_KeyAllowedAddresses_Denied(t *testing.T) {
	p := principal(1, "alice@example.test", 0)
	// alias "b@example.test" also maps to principal 1, but the key only
	// permits "alice@example.test".
	chk := newStub(
		"alice@example.test", store.PrincipalID(1),
		"b@example.test", store.PrincipalID(1),
	)
	key := &store.APIKey{
		ID:                   10,
		AllowedFromAddresses: []string{"alice@example.test"},
	}
	dec, err := sendpolicy.CheckFrom(context.Background(), chk, p, key, "b@example.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Error("expected denied by key address constraint")
	}
	if dec.Reason != sendpolicy.ReasonKeyAddressConstraint {
		t.Errorf("reason=%q want %q", dec.Reason, sendpolicy.ReasonKeyAddressConstraint)
	}
}

func TestCheckFrom_KeyAllowedDomains_Allowed(t *testing.T) {
	p := principal(1, "alice@example.test", 0)
	chk := newStub("alice@example.test", store.PrincipalID(1))
	key := &store.APIKey{
		ID:                 10,
		AllowedFromDomains: []string{"example.test"},
	}
	dec, err := sendpolicy.CheckFrom(context.Background(), chk, p, key, "alice@example.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("expected allowed, got reason=%q", dec.Reason)
	}
}

func TestCheckFrom_KeyAllowedDomains_Denied(t *testing.T) {
	p := principal(1, "alice@example.test", 0)
	chk := newStub("alice@example.test", store.PrincipalID(1))
	key := &store.APIKey{
		ID:                 10,
		AllowedFromDomains: []string{"other.test"},
	}
	dec, err := sendpolicy.CheckFrom(context.Background(), chk, p, key, "alice@example.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Error("expected denied by key domain constraint")
	}
	if dec.Reason != sendpolicy.ReasonKeyDomainConstraint {
		t.Errorf("reason=%q want %q", dec.Reason, sendpolicy.ReasonKeyDomainConstraint)
	}
}

func TestCheckFrom_StoreError_Propagated(t *testing.T) {
	p := principal(1, "alice@example.test", 0)
	chk := &stubChecker{m: map[string]store.PrincipalID{}, errFor: "alice@example.test"}
	_, err := sendpolicy.CheckFrom(context.Background(), chk, p, nil, "alice@example.test")
	if err == nil {
		t.Fatal("expected error to propagate from Checker, got nil")
	}
}

func TestCheckFrom_SMTPUTFAddress_Allowed(t *testing.T) {
	// SMTPUTF8: non-ASCII mailbox names must work end-to-end (REQ-PROTO-12).
	p := principal(1, "用户@例子.测试", 0)
	chk := newStub("用户@例子.测试", store.PrincipalID(1))
	dec, err := sendpolicy.CheckFrom(context.Background(), chk, p, nil, "用户@例子.测试")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dec.Allowed {
		t.Errorf("expected allowed for SMTPUTF8 address, got reason=%q", dec.Reason)
	}
}
