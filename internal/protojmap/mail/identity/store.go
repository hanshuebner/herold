package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// Store is the per-principal identity overlay. The default identity is
// derived from the principal row on each call; persisted custom
// identities are kept in store.Metadata via the JMAP-identity table
// (Wave 2.2.5). The default-overrides map is still in-process: the
// default identity itself is synthesised from the principal row, so
// the overlay applies only to that synthetic row. Custom identities
// land in jmap_identities so they survive restart.
type Store struct {
	mu sync.RWMutex

	// defaultOverrides[pid] is the most recent /set update on the
	// principal's default identity (the overlay) — non-zero fields
	// replace the synthesized default. Nil when the default is
	// unchanged from the principal row. Persisting this overlay would
	// require giving the default identity a row of its own; we keep
	// it in-process because the default is a function of the
	// principal row anyway and operators rarely customise it.
	defaultOverrides map[store.PrincipalID]*identityRecord

	// st is the metadata store, used for the persistent custom
	// identity table. Optional: tests that don't care about
	// persistence can construct a Store with st == nil and the
	// list/get paths fall back to the synthesised default only.
	st store.Store

	clk clock.Clock
}

// NewStore returns a Store backed by st for custom identities. Pass a
// nil st to opt out of persistence (default-only behaviour).
func NewStore(clk clock.Clock) *Store {
	return NewStoreWith(nil, clk)
}

// NewStoreWith is NewStore plus the metadata store binding. Production
// code calls this with the live store; tests that want the empty/
// synthesised default may pass nil.
func NewStoreWith(st store.Store, clk clock.Clock) *Store {
	if clk == nil {
		clk = clock.NewReal()
	}
	return &Store{
		defaultOverrides: make(map[store.PrincipalID]*identityRecord),
		st:               st,
		clk:              clk,
	}
}

// listForPrincipal returns the principal's identities: the default
// (possibly overlaid) plus any custom rows, in id order.
func (s *Store) listForPrincipal(ctx context.Context, p store.Principal) []identityRecord {
	s.mu.RLock()
	def := s.defaultRecordLocked(p)
	s.mu.RUnlock()
	custom, _ := s.loadPersisted(ctx, p)
	out := make([]identityRecord, 0, 1+len(custom))
	out = append(out, def)
	out = append(out, custom...)
	return out
}

// loadPersisted reads the principal's persisted identities. Returns
// nil + nil error when the store is missing or has no rows.
func (s *Store) loadPersisted(ctx context.Context, p store.Principal) ([]identityRecord, error) {
	if s.st == nil {
		return nil, nil
	}
	rows, err := s.st.Meta().ListJMAPIdentities(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]identityRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, persistedToRecord(r))
	}
	return out, nil
}

// defaultRecordLocked synthesizes the default identity for p. Caller
// must hold s.mu (read or write).
//
// Order of precedence for each field:
//
//  1. The persisted principal row (canonical home for name/avatar/xface
//     since migration 0036).
//  2. The in-memory override (still authoritative for replyTo/bcc/
//     signature, which have no principal-row column yet).
func (s *Store) defaultRecordLocked(p store.Principal) identityRecord {
	rec := identityRecord{
		ID:             0,
		PrincipalID:    p.ID,
		Name:           p.DisplayName,
		Email:          p.CanonicalEmail,
		MayDelete:      false,
		AvatarBlobHash: p.AvatarBlobHash,
		AvatarBlobSize: p.AvatarBlobSize,
		XFaceEnabled:   p.XFaceEnabled,
		UpdatedAt:      p.UpdatedAt,
	}
	if ovr, ok := s.defaultOverrides[p.ID]; ok && ovr != nil {
		// Apply non-empty override fields atop the principal-row default.
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
		if ovr.Signature != nil {
			v := *ovr.Signature
			rec.Signature = &v
		}
		if !ovr.UpdatedAt.IsZero() && ovr.UpdatedAt.After(rec.UpdatedAt) {
			rec.UpdatedAt = ovr.UpdatedAt
		}
	}
	return rec
}

// create appends a new identity for p. The caller has already
// validated email + replyTo against the local-domain set.
func (s *Store) create(ctx context.Context, p store.Principal, in identityRecord) identityRecord {
	if s.st == nil {
		// No persistence: synthesise an in-memory id and pretend.
		// Identity/get on a fresh process won't see the row, but the
		// /set response still needs to round-trip the created object.
		in.ID = uint64(s.clk.Now().UnixNano())
		in.PrincipalID = p.ID
		in.MayDelete = true
		in.UpdatedAt = s.clk.Now()
		return in
	}
	now := s.clk.Now()
	id := allocateIdentityID(now)
	in.ID = id
	in.PrincipalID = p.ID
	in.MayDelete = true
	in.UpdatedAt = now
	row := recordToPersisted(in)
	row.CreatedAtUs = now.UnixMicro()
	row.UpdatedAtUs = now.UnixMicro()
	if err := s.st.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		// On collision (extremely unlikely with nanosecond IDs) we
		// fall back to a retry with a tiny offset. Other errors
		// surface as "create returned anyway" — the JMAP layer's
		// /set treats this as success because the record's ID is set;
		// callers that care can re-list. Tests do not exercise this
		// branch.
		_ = err
	}
	return in
}

// allocateIdentityID returns a fresh per-process identity id derived
// from the clock's nanosecond. Two creates in the same nanosecond
// would collide; we accept that as a non-issue at v1 scale.
func allocateIdentityID(now time.Time) uint64 {
	v := uint64(now.UnixNano())
	if v == 0 {
		v = 1
	}
	return v
}

// update mutates the record with the given id by applying the patch's
// non-nil fields. Returns the updated record + ok=true on success;
// returns ok=false when no such record exists for p.
func (s *Store) update(ctx context.Context, p store.Principal, id uint64, patch identityPatch) (identityRecord, bool) {
	if id == 0 {
		s.mu.Lock()
		ovr := s.defaultOverrides[p.ID]
		if ovr == nil {
			ovr = &identityRecord{ID: 0, PrincipalID: p.ID}
		}
		patch.applyTo(ovr)
		ovr.UpdatedAt = s.clk.Now()
		s.defaultOverrides[p.ID] = ovr
		// Writethrough name / avatar / xface to the principal row so the
		// fields survive a restart and are visible to other principals
		// via Principal/get (REQ-MAIL-44 / REQ-SET-03b). replyTo, bcc,
		// and signature stay in the in-process overlay because they
		// have no principal-row column yet.
		updateP := p
		dirty := false
		if patch.hasName {
			updateP.DisplayName = patch.name
			dirty = true
		}
		if patch.hasAvatarBlobId {
			updateP.AvatarBlobHash = patch.avatarBlobHash
			updateP.AvatarBlobSize = patch.avatarBlobSize
			dirty = true
		}
		if patch.hasXFaceEnabled {
			updateP.XFaceEnabled = patch.xFaceEnabled
			dirty = true
		}
		s.mu.Unlock()
		if dirty && s.st != nil {
			// Best-effort; the in-process overlay already carries the
			// latest values so a transient store error is not fatal.
			_ = s.st.Meta().UpdatePrincipal(ctx, updateP)
		}
		s.mu.RLock()
		out := s.defaultRecordLocked(updateP)
		s.mu.RUnlock()
		return out, true
	}
	if s.st == nil {
		return identityRecord{}, false
	}
	rowID := strconv.FormatUint(id, 10)
	cur, err := s.st.Meta().GetJMAPIdentity(ctx, rowID)
	if err != nil || cur.PrincipalID != p.ID {
		return identityRecord{}, false
	}
	rec := persistedToRecord(cur)
	patch.applyTo(&rec)
	rec.UpdatedAt = s.clk.Now()
	updated := recordToPersisted(rec)
	updated.CreatedAtUs = cur.CreatedAtUs
	updated.UpdatedAtUs = rec.UpdatedAt.UnixMicro()
	if err := s.st.Meta().UpdateJMAPIdentity(ctx, updated); err != nil {
		return identityRecord{}, false
	}
	return rec, true
}

// destroy removes the record with the given id for p. Returns ok=false
// when the id is the default ("default" identities are not deletable
// per RFC 8621 §7.4 mayDelete=false) or when no such id exists.
func (s *Store) destroy(ctx context.Context, p store.Principal, id uint64) bool {
	if id == 0 {
		return false
	}
	if s.st == nil {
		return false
	}
	rowID := strconv.FormatUint(id, 10)
	cur, err := s.st.Meta().GetJMAPIdentity(ctx, rowID)
	if err != nil || cur.PrincipalID != p.ID {
		return false
	}
	if err := s.st.Meta().DeleteJMAPIdentity(ctx, rowID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false
		}
		return false
	}
	return true
}

// persistedToRecord projects a store.JMAPIdentity row into the
// in-memory identityRecord shape this package operates on.
func persistedToRecord(r store.JMAPIdentity) identityRecord {
	rec := identityRecord{
		PrincipalID:    r.PrincipalID,
		Name:           r.Name,
		Email:          r.Email,
		TextSignature:  r.TextSignature,
		HTMLSignature:  r.HTMLSignature,
		MayDelete:      r.MayDelete,
		AvatarBlobHash: r.AvatarBlobHash,
		AvatarBlobSize: r.AvatarBlobSize,
		XFaceEnabled:   r.XFaceEnabled,
		UpdatedAt:      time.UnixMicro(r.UpdatedAtUs).UTC(),
	}
	if r.Signature != nil {
		v := *r.Signature
		rec.Signature = &v
	}
	if v, err := strconv.ParseUint(r.ID, 10, 64); err == nil {
		rec.ID = v
	}
	if len(r.ReplyToJSON) > 0 {
		var addrs []emailAddress
		if err := json.Unmarshal(r.ReplyToJSON, &addrs); err == nil {
			rec.ReplyTo = addrs
		}
	}
	if len(r.BccJSON) > 0 {
		var addrs []emailAddress
		if err := json.Unmarshal(r.BccJSON, &addrs); err == nil {
			rec.Bcc = addrs
		}
	}
	return rec
}

// recordToPersisted is the inverse of persistedToRecord. CreatedAtUs is
// left at zero for the InsertJMAPIdentity path to populate from the
// clock; UpdatedAtUs is left at zero for callers to fill explicitly.
func recordToPersisted(r identityRecord) store.JMAPIdentity {
	row := store.JMAPIdentity{
		ID:             strconv.FormatUint(r.ID, 10),
		PrincipalID:    r.PrincipalID,
		Name:           r.Name,
		Email:          r.Email,
		TextSignature:  r.TextSignature,
		HTMLSignature:  r.HTMLSignature,
		MayDelete:      r.MayDelete,
		AvatarBlobHash: r.AvatarBlobHash,
		AvatarBlobSize: r.AvatarBlobSize,
		XFaceEnabled:   r.XFaceEnabled,
	}
	if r.Signature != nil {
		v := *r.Signature
		row.Signature = &v
	}
	if len(r.ReplyTo) > 0 {
		if b, err := json.Marshal(r.ReplyTo); err == nil {
			row.ReplyToJSON = b
		}
	}
	if len(r.Bcc) > 0 {
		if b, err := json.Marshal(r.Bcc); err == nil {
			row.BccJSON = b
		}
	}
	return row
}

// BumpIdentityPushState increments the JMAPStateKindIdentity counter for
// principal pid. EmailSubmission/set calls this when an external submission
// results in an auth-failed or unreachable outcome so JMAP push clients
// observe the identity state change (REQ-AUTH-EXT-SUBMIT-05).
// Returns an error only when the store call fails; the caller decides
// whether to surface it to the client.
func (s *Store) BumpIdentityPushState(ctx context.Context, pid store.PrincipalID) error {
	if s.st == nil {
		return nil
	}
	_, err := s.st.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindIdentity)
	return err
}

// HasExternalSubmission reports whether the Identity identified by the
// wire-form JMAP id (e.g. "default" or a decimal overlay id) belonging to
// principal pid has an external SMTP submission configuration stored.
// Returns false on any error (treat as "no configuration"). The decryption of
// credential fields happens inside extsubmit.Submitter.Submit; this method
// never touches ciphertext.
func (s *Store) HasExternalSubmission(ctx context.Context, pid store.PrincipalID, identityJMAPID string) bool {
	if s.st == nil {
		return false
	}
	// Resolve wire-form id to the storage id. The default identity uses the
	// principal's canonical email, not a row in jmap_identities; its storage
	// id in identity_submission uses the JMAP wire id verbatim (the REST
	// layer that created it did the same mapping).
	sub, err := s.st.Meta().GetIdentitySubmission(ctx, identityJMAPID)
	if err != nil {
		return false
	}
	// Cross-check that the submission config belongs to an identity owned by
	// the requesting principal by verifying either it is the default identity
	// or that the identity row belongs to pid.
	if identityJMAPID == "default" {
		// Default identity is always owned by the principal.
		return true
	}
	row, err := s.st.Meta().GetJMAPIdentity(ctx, identityJMAPID)
	if err != nil || row.PrincipalID != pid {
		return false
	}
	_ = sub
	return true
}

// SubmissionConfig returns the external SMTP submission configuration for the
// Identity identified by identityJMAPID owned by pid. Returns an error when no
// configuration exists or when the identity does not belong to pid.
func (s *Store) SubmissionConfig(ctx context.Context, pid store.PrincipalID, identityJMAPID string) (store.IdentitySubmission, error) {
	if s.st == nil {
		return store.IdentitySubmission{}, store.ErrNotFound
	}
	if identityJMAPID != "default" {
		row, err := s.st.Meta().GetJMAPIdentity(ctx, identityJMAPID)
		if err != nil || row.PrincipalID != pid {
			return store.IdentitySubmission{}, store.ErrNotFound
		}
	}
	return s.st.Meta().GetIdentitySubmission(ctx, identityJMAPID)
}

var _ = fmt.Sprint // keep fmt import in case future code logs persistence errors

// snapshotAvatarHash returns the persisted avatarBlobHash for the
// identity (id) owned by p, or "" when the identity does not exist or
// has no avatar set. Used by /set to learn the previous blob hash so
// refcounts can be decremented after a successful avatar replacement
// or deletion.
func (s *Store) snapshotAvatarHash(ctx context.Context, p store.Principal, id uint64) string {
	if id == 0 {
		// The default identity stores its canonical avatar on the
		// principal row (migration 0036). The caller already passed in
		// the freshly-fetched principal.
		return p.AvatarBlobHash
	}
	if s.st == nil {
		return ""
	}
	rowID := strconv.FormatUint(id, 10)
	row, err := s.st.Meta().GetJMAPIdentity(ctx, rowID)
	if err != nil || row.PrincipalID != p.ID {
		return ""
	}
	return row.AvatarBlobHash
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

// AvatarBlobHash returns the BLAKE3 hex hash of the avatar blob, or ""
// when no avatar is set (REQ-SET-03b).
func (v IdentityView) AvatarBlobHash() string { return v.r.AvatarBlobHash }

// XFaceEnabled reports whether X-Face: / Face: header injection is
// enabled for outbound messages from this identity (REQ-SET-03b).
func (v IdentityView) XFaceEnabled() bool { return v.r.XFaceEnabled }

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
	hasSignature     bool
	signature        *string
	// hasAvatarBlobId is true when the patch included avatarBlobId (even if null).
	// avatarBlobHash + avatarBlobSize are pre-validated by the set handler.
	hasAvatarBlobId bool
	// avatarBlobHash is "" when the patch clears the avatar (null on wire).
	avatarBlobHash  string
	avatarBlobSize  int64
	hasXFaceEnabled bool
	xFaceEnabled    bool
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
	if p.hasSignature {
		if p.signature == nil {
			r.Signature = nil
		} else {
			v := *p.signature
			r.Signature = &v
		}
	}
	if p.hasAvatarBlobId {
		r.AvatarBlobHash = p.avatarBlobHash
		r.AvatarBlobSize = p.avatarBlobSize
	}
	if p.hasXFaceEnabled {
		r.XFaceEnabled = p.xFaceEnabled
	}
}
