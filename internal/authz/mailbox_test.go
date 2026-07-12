package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

func mailbox(id store.MailboxID, owner store.PrincipalID) store.Mailbox {
	return store.Mailbox{ID: id, PrincipalID: owner, Name: "Shared/support"}
}

func TestResolveMailbox_ImplicitOwnerAdmin(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{} // empty: no grant rows at all
	owner := principal(1, 0)
	mb := mailbox(10, 1)

	got, err := ResolveMailbox(ctx, r, owner, mb)
	if err != nil {
		t.Fatalf("ResolveMailbox: %v", err)
	}
	if got != store.GrantLevelAdmin {
		t.Errorf("owner ResolveMailbox = %q; want admin (REQ-AC-51)", got)
	}
}

func TestResolveMailbox_ImplicitOwnerIgnoresStoreError(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{err: errors.New("store down")}
	owner := principal(1, 0)
	mb := mailbox(10, 1)

	got, err := ResolveMailbox(ctx, r, owner, mb)
	if err != nil {
		t.Fatalf("ResolveMailbox: %v", err)
	}
	if got != store.GrantLevelAdmin {
		t.Errorf("owner ResolveMailbox with store error = %q; want admin (implicit, no store read needed)", got)
	}
}

func TestResolveMailbox_ExplicitGrantTiers(t *testing.T) {
	ctx := context.Background()
	mb := mailbox(10, 1) // owned by principal 1
	grantee := principal(2, 0)

	cases := []struct {
		level store.GrantLevel
		want  store.GrantLevel
	}{
		{store.GrantLevelRead, store.GrantLevelRead},
		{store.GrantLevelWrite, store.GrantLevelWrite},
		{store.GrantLevelAdmin, store.GrantLevelAdmin},
	}
	for _, c := range cases {
		r := &fakeReader{grants: map[store.PrincipalID][]store.Grant{
			2: {{ResourceKind: store.GrantResourceMailbox, ResourceID: "10", Level: c.level}},
		}}
		got, err := ResolveMailbox(ctx, r, grantee, mb)
		if err != nil {
			t.Fatalf("ResolveMailbox(%s): %v", c.level, err)
		}
		if got != c.want {
			t.Errorf("ResolveMailbox with %s grant = %q; want %q", c.level, got, c.want)
		}
	}
}

func TestResolveMailbox_NoGrantDenied(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{}
	mb := mailbox(10, 1)
	grantee := principal(2, 0)

	got, err := ResolveMailbox(ctx, r, grantee, mb)
	if err != nil {
		t.Fatalf("ResolveMailbox: %v", err)
	}
	if got != "" {
		t.Errorf("ResolveMailbox with no grant = %q; want empty (default-deny)", got)
	}
}

func TestResolveMailbox_GrantOnDifferentMailboxIgnored(t *testing.T) {
	ctx := context.Background()
	mb := mailbox(10, 1)
	grantee := principal(2, 0)
	r := &fakeReader{grants: map[store.PrincipalID][]store.Grant{
		2: {{ResourceKind: store.GrantResourceMailbox, ResourceID: "999", Level: store.GrantLevelAdmin}},
	}}
	got, err := ResolveMailbox(ctx, r, grantee, mb)
	if err != nil {
		t.Fatalf("ResolveMailbox: %v", err)
	}
	if got != "" {
		t.Errorf("ResolveMailbox with grant on a different mailbox = %q; want empty", got)
	}
}

func TestResolveMailbox_SuperadminTopsOut(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{}
	mb := mailbox(10, 1)
	super := principal(3, store.PrincipalFlagAdmin|store.PrincipalFlagSuperAdmin)

	got, err := ResolveMailbox(ctx, r, super, mb)
	if err != nil {
		t.Fatalf("ResolveMailbox: %v", err)
	}
	if got != store.GrantLevelAdmin {
		t.Errorf("superadmin ResolveMailbox = %q; want admin", got)
	}
}

func TestResolveMailbox_FailClosedOnStoreError(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{err: errors.New("store down")}
	mb := mailbox(10, 1)
	grantee := principal(2, 0) // not the owner, no flags

	_, err := ResolveMailbox(ctx, r, grantee, mb)
	if err == nil {
		t.Errorf("ResolveMailbox returned nil error on store failure; want the error (fail-closed)")
	}
}

func TestRightsForMailboxLevel_TierContainment(t *testing.T) {
	read := RightsForMailboxLevel(store.GrantLevelRead)
	write := RightsForMailboxLevel(store.GrantLevelWrite)
	admin := RightsForMailboxLevel(store.GrantLevelAdmin)

	if read&write != read {
		t.Errorf("write tier %v does not contain read tier %v", write, read)
	}
	if write&admin != write {
		t.Errorf("admin tier %v does not contain write tier %v", admin, write)
	}
	if admin&store.ACLRightAdmin == 0 {
		t.Errorf("admin tier missing the 'a' bit")
	}
	if read&store.ACLRightAdmin != 0 {
		t.Errorf("read tier must not include the 'a' bit")
	}
	if RightsForMailboxLevel("") != 0 {
		t.Errorf("zero level must expand to zero rights")
	}
	if admin != store.ACLRightsAll {
		t.Errorf("admin tier = %v; want the full ACLRightsAll mask %v", admin, store.ACLRightsAll)
	}
}

func TestMailboxLevelForRights_Collapse(t *testing.T) {
	cases := []struct {
		rights store.ACLRights
		want   store.GrantLevel
	}{
		{0, ""},
		{store.ACLRightLookup, store.GrantLevelRead},
		{store.ACLRightLookup | store.ACLRightRead | store.ACLRightSeen, store.GrantLevelRead},
		{store.ACLRightInsert, store.GrantLevelWrite},
		{store.ACLRightLookup | store.ACLRightRead | store.ACLRightWrite, store.GrantLevelWrite},
		{store.ACLRightAdmin, store.GrantLevelAdmin},
		{store.ACLRightsAll, store.GrantLevelAdmin},
	}
	for _, c := range cases {
		got := MailboxLevelForRights(c.rights)
		if got != c.want {
			t.Errorf("MailboxLevelForRights(%v) = %q; want %q", c.rights, got, c.want)
		}
	}
}

func TestMailboxLevelForRights_RoundTripsWithExpansion(t *testing.T) {
	// Expanding a tier then collapsing the result must return the same
	// tier: the two directions of the RFC 4314 <-> grant-tier bridge
	// agree with each other.
	for _, level := range []store.GrantLevel{store.GrantLevelRead, store.GrantLevelWrite, store.GrantLevelAdmin} {
		rights := RightsForMailboxLevel(level)
		if got := MailboxLevelForRights(rights); got != level {
			t.Errorf("MailboxLevelForRights(RightsForMailboxLevel(%s)) = %q; want %q", level, got, level)
		}
	}
}
