package identity

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// Store is the per-principal identity overlay. The default identity is
// derived from the principal row on each call; this store carries
// custom identities and overrides on the default that clients have
// configured via Identity/set. v1 keeps the overlay in-process — see
// the package comment.
type Store struct {
	mu sync.RWMutex

	// records[pid] is the ordered list of identity records the
	// principal has registered. Records are appended on create; the
	// slice's order is the JMAP enumeration order.
	records map[store.PrincipalID][]identityRecord
	// nextID is the per-principal monotonic id allocator (default
	// identity is reserved as id 0 / "default"; allocated ids start at
	// 1).
	nextID map[store.PrincipalID]uint64
	// defaultOverrides[pid] is the most recent /set update on the
	// principal's default identity (the overlay) — non-zero fields
	// replace the synthesized default. Nil when the default is
	// unchanged from the principal row.
	defaultOverrides map[store.PrincipalID]*identityRecord

	clk clock.Clock
}

// NewStore returns an empty overlay store. clk is the injected clock
// used for UpdatedAt stamps (tests use a fake clock).
func NewStore(clk clock.Clock) *Store {
	if clk == nil {
		clk = clock.NewReal()
	}
	return &Store{
		records:          make(map[store.PrincipalID][]identityRecord),
		nextID:           make(map[store.PrincipalID]uint64),
		defaultOverrides: make(map[store.PrincipalID]*identityRecord),
		clk:              clk,
	}
}

// listForPrincipal returns the principal's identities: the default
// (possibly overlaid) plus any custom rows, in id order.
func (s *Store) listForPrincipal(_ context.Context, p store.Principal) []identityRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	def := s.defaultRecordLocked(p)
	custom := s.records[p.ID]
	out := make([]identityRecord, 0, 1+len(custom))
	out = append(out, def)
	out = append(out, custom...)
	return out
}

// defaultRecordLocked synthesizes the default identity for p. Caller
// must hold s.mu (read or write).
func (s *Store) defaultRecordLocked(p store.Principal) identityRecord {
	rec := identityRecord{
		ID:          0,
		PrincipalID: p.ID,
		Name:        p.DisplayName,
		Email:       p.CanonicalEmail,
		MayDelete:   false,
		UpdatedAt:   p.UpdatedAt,
	}
	if ovr, ok := s.defaultOverrides[p.ID]; ok && ovr != nil {
		// Apply non-empty override fields atop the synthesized default.
		if ovr.Name != "" {
			rec.Name = ovr.Name
		}
		if len(ovr.ReplyTo) > 0 {
			rec.ReplyTo = append([]emailAddress(nil), ovr.ReplyTo...)
		}
		if len(ovr.Bcc) > 0 {
			rec.Bcc = append([]emailAddress(nil), ovr.Bcc...)
		}
		if ovr.TextSignature != "" {
			rec.TextSignature = ovr.TextSignature
		}
		if ovr.HTMLSignature != "" {
			rec.HTMLSignature = ovr.HTMLSignature
		}
		if !ovr.UpdatedAt.IsZero() {
			rec.UpdatedAt = ovr.UpdatedAt
		}
	}
	return rec
}

// get returns the record with the given id for principal p, or
// ok=false. id == 0 returns the default (with overlays applied).
func (s *Store) get(_ context.Context, p store.Principal, id uint64) (identityRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id == 0 {
		return s.defaultRecordLocked(p), true
	}
	for _, rec := range s.records[p.ID] {
		if rec.ID == id {
			return rec, true
		}
	}
	return identityRecord{}, false
}

// create appends a new identity for p. The caller has already
// validated email + replyTo against the local-domain set.
func (s *Store) create(_ context.Context, p store.Principal, in identityRecord) identityRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID[p.ID]++
	id := s.nextID[p.ID]
	in.ID = id
	in.PrincipalID = p.ID
	in.MayDelete = true
	in.UpdatedAt = s.clk.Now()
	s.records[p.ID] = append(s.records[p.ID], in)
	return in
}

// update mutates the record with the given id by applying the patch's
// non-nil fields. Returns the updated record + ok=true on success;
// returns ok=false when no such record exists for p.
func (s *Store) update(_ context.Context, p store.Principal, id uint64, patch identityPatch) (identityRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == 0 {
		ovr := s.defaultOverrides[p.ID]
		if ovr == nil {
			ovr = &identityRecord{ID: 0, PrincipalID: p.ID}
		}
		patch.applyTo(ovr)
		ovr.UpdatedAt = s.clk.Now()
		s.defaultOverrides[p.ID] = ovr
		return s.defaultRecordLocked(p), true
	}
	list := s.records[p.ID]
	for i := range list {
		if list[i].ID == id {
			patch.applyTo(&list[i])
			list[i].UpdatedAt = s.clk.Now()
			s.records[p.ID] = list
			return list[i], true
		}
	}
	return identityRecord{}, false
}

// destroy removes the record with the given id for p. Returns ok=false
// when the id is the default ("default" identities are not deletable
// per RFC 8621 §7.4 mayDelete=false) or when no such id exists.
func (s *Store) destroy(_ context.Context, p store.Principal, id uint64) bool {
	if id == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.records[p.ID]
	for i := range list {
		if list[i].ID == id {
			s.records[p.ID] = append(list[:i], list[i+1:]...)
			return true
		}
	}
	return false
}

// snapshot returns the principal's identities in id order, used by
// /get and /changes. The slice is a fresh copy safe to mutate.
func (s *Store) snapshot(ctx context.Context, p store.Principal) []identityRecord {
	out := s.listForPrincipal(ctx, p)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Snapshot is the exported view of the principal's identities used by
// sibling packages (EmailSubmission resolves IdentityID to email via
// this surface).
func (s *Store) Snapshot(ctx context.Context, p store.Principal) []IdentityView {
	rows := s.snapshot(ctx, p)
	out := make([]IdentityView, len(rows))
	for i, r := range rows {
		out[i] = IdentityView{r: r}
	}
	return out
}

// IdentityView is the exported, read-only projection of one identity
// record. It exposes only the fields a sibling package needs to
// resolve an IdentityID to its email address; mutations remain
// internal to this package.
type IdentityView struct {
	r identityRecord
}

// JMAPID returns the wire-form JMAP id of this identity ("default" for
// the principal-derived identity, otherwise the decimal overlay id).
func (v IdentityView) JMAPID() string { return renderID(v.r.ID) }

// Email returns the addr-spec the identity sends from.
func (v IdentityView) Email() string { return v.r.Email }

// identityPatch is the JSON-decoded patch applied by /set update. It is
// declared here (instead of in methods.go) so the Store can apply it
// without importing the JSON layer.
type identityPatch struct {
	hasName          bool
	name             string
	hasReplyTo       bool
	replyTo          []emailAddress
	hasBcc           bool
	bcc              []emailAddress
	hasTextSignature bool
	textSignature    string
	hasHTMLSignature bool
	htmlSignature    string
}

func (p identityPatch) applyTo(r *identityRecord) {
	if p.hasName {
		r.Name = p.name
	}
	if p.hasReplyTo {
		r.ReplyTo = append([]emailAddress(nil), p.replyTo...)
	}
	if p.hasBcc {
		r.Bcc = append([]emailAddress(nil), p.bcc...)
	}
	if p.hasTextSignature {
		r.TextSignature = p.textSignature
	}
	if p.hasHTMLSignature {
		r.HTMLSignature = p.htmlSignature
	}
}

// since UpdatedAt isn't a JMAP-visible field, expose it for tests.
func (s *Store) lastUpdated(p store.Principal) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := p.UpdatedAt
	if ovr, ok := s.defaultOverrides[p.ID]; ok && ovr != nil && ovr.UpdatedAt.After(t) {
		t = ovr.UpdatedAt
	}
	for _, r := range s.records[p.ID] {
		if r.UpdatedAt.After(t) {
			t = r.UpdatedAt
		}
	}
	return t
}
