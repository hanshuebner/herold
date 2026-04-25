// Package fakestore provides an in-memory implementation of store.Store for
// use by the test harness. Blob bodies live under a caller-supplied tempdir
// (typically t.TempDir); all other state lives in maps guarded by a single
// RWMutex. The implementation favours clarity over performance: Phase 1
// callers need round-trips to work, not a perf path.
//
// FTS is a trivial substring matcher over the indexed text. It honours the
// Query structure (field lists AND-combined) but does not tokenize; that is
// good enough for behaviour tests and deliberately weaker than the bleve
// backend so tests cannot accidentally depend on ranking subtleties.
package fakestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// Store is an in-memory store.Store backed by maps and a tempdir.
type Store struct {
	clk     clock.Clock
	blobDir string

	mu sync.RWMutex

	// primary rows
	principals map[store.PrincipalID]store.Principal
	byEmail    map[string]store.PrincipalID
	mailboxes  map[store.MailboxID]store.Mailbox
	messages   map[store.MessageID]store.Message
	aliases    map[store.AliasID]store.Alias
	aliasBy    map[string]store.AliasID // "local@domain" -> alias
	domains    map[string]store.Domain
	providers  map[string]store.OIDCProvider
	oidcLinks  map[string]store.OIDCLink // "provider|subject" -> link
	apiKeys    map[store.APIKeyID]store.APIKey
	keyByHash  map[string]store.APIKeyID

	// content-addressed blobs: refcount lives here (not in Metadata) because
	// the fake does not split the two surfaces; the test harness does not
	// exercise the real refcount path.
	blobSize map[string]int64
	blobRefs map[string]int

	// change feeds
	stateChanges map[store.PrincipalID][]store.StateChange
	changeSeq    map[store.PrincipalID]store.ChangeSeq
	ftsChanges   []store.FTSChange
	ftsSeq       uint64

	// cursors: key -> seq. Phase 1 carries one slot ("fts"); the same
	// table is forward-compatible with future change-feed consumers.
	cursors map[string]uint64

	// audit log: append-only, sorted by ID for deterministic iteration.
	auditLog       []store.AuditLogEntry
	nextAuditLogID store.AuditLogID

	// FTS documents: MessageID -> indexed text.
	ftsDocs map[store.MessageID]ftsDoc

	// sieveScripts holds the active Sieve script text per principal.
	// Absence means "no script"; GetSieveScript returns ("", nil).
	sieveScripts map[store.PrincipalID]string

	// phase2 holds Phase 2 in-memory tables (queue, DKIM, ACME,
	// webhooks, DMARC, mailbox ACL, JMAP states, TLS-RPT). Lazily
	// initialised on first Phase 2 method call so existing tests do
	// not pay the allocation cost.
	phase2 *phase2Data

	// monotonic ID counters
	nextPrincipalID store.PrincipalID
	nextMailboxID   store.MailboxID
	nextMessageID   store.MessageID
	nextAliasID     store.AliasID
	nextAPIKeyID    store.APIKeyID
	nextUIDValidity store.UIDValidity

	closed bool
}

type ftsDoc struct {
	msg  store.Message
	text string
}

// Options configures a new in-memory Store.
type Options struct {
	// Clock supplies timestamps; defaults to a FakeClock anchored at
	// 2026-01-01T00:00:00Z if nil.
	Clock clock.Clock
	// BlobDir is the filesystem root for blob bodies. Must already exist.
	// When empty, a temporary directory is created with os.MkdirTemp and
	// cleaned up in Close.
	BlobDir string
}

// New returns an in-memory Store ready for use.
func New(opts Options) (*Store, error) {
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	}
	dir := opts.BlobDir
	if dir == "" {
		d, err := os.MkdirTemp("", "herold-fakestore-blobs-*")
		if err != nil {
			return nil, fmt.Errorf("fakestore: mkdir blobs: %w", err)
		}
		dir = d
	}
	return &Store{
		clk:             clk,
		blobDir:         dir,
		principals:      make(map[store.PrincipalID]store.Principal),
		byEmail:         make(map[string]store.PrincipalID),
		mailboxes:       make(map[store.MailboxID]store.Mailbox),
		messages:        make(map[store.MessageID]store.Message),
		aliases:         make(map[store.AliasID]store.Alias),
		aliasBy:         make(map[string]store.AliasID),
		domains:         make(map[string]store.Domain),
		providers:       make(map[string]store.OIDCProvider),
		oidcLinks:       make(map[string]store.OIDCLink),
		apiKeys:         make(map[store.APIKeyID]store.APIKey),
		keyByHash:       make(map[string]store.APIKeyID),
		blobSize:        make(map[string]int64),
		blobRefs:        make(map[string]int),
		stateChanges:    make(map[store.PrincipalID][]store.StateChange),
		changeSeq:       make(map[store.PrincipalID]store.ChangeSeq),
		cursors:         make(map[string]uint64),
		ftsDocs:         make(map[store.MessageID]ftsDoc),
		sieveScripts:    make(map[store.PrincipalID]string),
		nextPrincipalID: 1,
		nextMailboxID:   1,
		nextMessageID:   1,
		nextAliasID:     1,
		nextAPIKeyID:    1,
		nextUIDValidity: 1,
		nextAuditLogID:  1,
	}, nil
}

// Meta returns the metadata repository surface.
func (s *Store) Meta() store.Metadata { return (*metaFace)(s) }

// Blobs returns the blob surface.
func (s *Store) Blobs() store.Blobs { return (*blobsFace)(s) }

// FTS returns the full-text search surface.
func (s *Store) FTS() store.FTS { return (*ftsFace)(s) }

// Close releases the blob directory. Subsequent calls are no-ops.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	// Best-effort cleanup; the tempdir was ours to manage when BlobDir was
	// empty. Caller-supplied dirs are not removed.
	if s.blobDir != "" && strings.Contains(s.blobDir, "herold-fakestore-blobs-") {
		_ = os.RemoveAll(s.blobDir)
	}
	return nil
}

// metaFace is a type alias that exposes the Metadata methods without letting
// the Store struct's other methods satisfy the interface accidentally.
type metaFace Store

func (m *metaFace) s() *Store { return (*Store)(m) }

// canonEmail lowercases and trims an email address for map-key use.
func canonEmail(e string) string { return strings.TrimSpace(strings.ToLower(e)) }

func aliasKey(local, domain string) string {
	return strings.ToLower(local) + "@" + strings.ToLower(domain)
}

func oidcLinkKey(provider, subject string) string {
	return provider + "|" + subject
}

// ---------- Metadata: principals ----------

func (m *metaFace) GetPrincipalByID(ctx context.Context, id store.PrincipalID) (store.Principal, error) {
	if err := ctx.Err(); err != nil {
		return store.Principal{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.principals[id]
	if !ok {
		return store.Principal{}, fmt.Errorf("principal %d: %w", id, store.ErrNotFound)
	}
	return p, nil
}

func (m *metaFace) GetPrincipalByEmail(ctx context.Context, email string) (store.Principal, error) {
	if err := ctx.Err(); err != nil {
		return store.Principal{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byEmail[canonEmail(email)]
	if !ok {
		return store.Principal{}, fmt.Errorf("principal %q: %w", email, store.ErrNotFound)
	}
	return s.principals[id], nil
}

func (m *metaFace) InsertPrincipal(ctx context.Context, p store.Principal) (store.Principal, error) {
	if err := ctx.Err(); err != nil {
		return store.Principal{}, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	email := canonEmail(p.CanonicalEmail)
	if _, exists := s.byEmail[email]; exists {
		return store.Principal{}, fmt.Errorf("principal %q: %w", email, store.ErrConflict)
	}
	now := s.clk.Now()
	p.ID = s.nextPrincipalID
	s.nextPrincipalID++
	p.CanonicalEmail = email
	p.CreatedAt = now
	p.UpdatedAt = now
	s.principals[p.ID] = p
	s.byEmail[email] = p.ID
	return p, nil
}

func (m *metaFace) UpdatePrincipal(ctx context.Context, p store.Principal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.principals[p.ID]
	if !ok {
		return fmt.Errorf("principal %d: %w", p.ID, store.ErrNotFound)
	}
	// If the email changed, remap byEmail.
	newEmail := canonEmail(p.CanonicalEmail)
	if newEmail != existing.CanonicalEmail {
		if _, exists := s.byEmail[newEmail]; exists {
			return fmt.Errorf("principal email %q: %w", newEmail, store.ErrConflict)
		}
		delete(s.byEmail, existing.CanonicalEmail)
		s.byEmail[newEmail] = p.ID
	}
	p.CanonicalEmail = newEmail
	p.CreatedAt = existing.CreatedAt
	p.UpdatedAt = s.clk.Now()
	s.principals[p.ID] = p
	return nil
}

// ---------- Metadata: mailboxes ----------

func (m *metaFace) GetMailboxByID(ctx context.Context, id store.MailboxID) (store.Mailbox, error) {
	if err := ctx.Err(); err != nil {
		return store.Mailbox{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	mb, ok := s.mailboxes[id]
	if !ok {
		return store.Mailbox{}, fmt.Errorf("mailbox %d: %w", id, store.ErrNotFound)
	}
	return mb, nil
}

func (m *metaFace) ListMailboxes(ctx context.Context, principalID store.PrincipalID) ([]store.Mailbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.Mailbox
	for _, mb := range s.mailboxes {
		if mb.PrincipalID == principalID {
			out = append(out, mb)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *metaFace) InsertMailbox(ctx context.Context, mb store.Mailbox) (store.Mailbox, error) {
	if err := ctx.Err(); err != nil {
		return store.Mailbox{}, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.mailboxes {
		if existing.PrincipalID == mb.PrincipalID && existing.Name == mb.Name {
			return store.Mailbox{}, fmt.Errorf("mailbox %q: %w", mb.Name, store.ErrConflict)
		}
	}
	now := s.clk.Now()
	mb.ID = s.nextMailboxID
	s.nextMailboxID++
	mb.UIDValidity = s.nextUIDValidity
	s.nextUIDValidity++
	if mb.UIDNext == 0 {
		mb.UIDNext = 1
	}
	mb.CreatedAt = now
	mb.UpdatedAt = now
	s.mailboxes[mb.ID] = mb

	// Append mailbox-created state change.
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: mb.PrincipalID,
		Kind:        store.ChangeKindMailboxCreated,
		MailboxID:   mb.ID,
		ProducedAt:  now,
	})
	return mb, nil
}

func (m *metaFace) DeleteMailbox(ctx context.Context, id store.MailboxID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.mailboxes[id]
	if !ok {
		return fmt.Errorf("mailbox %d: %w", id, store.ErrNotFound)
	}
	now := s.clk.Now()
	// Remove all messages in the mailbox, decrementing blob refcounts and
	// appending destroyed entries.
	for mid, msg := range s.messages {
		if msg.MailboxID != id {
			continue
		}
		if msg.Blob.Hash != "" {
			s.blobRefs[msg.Blob.Hash]--
			if s.blobRefs[msg.Blob.Hash] < 0 {
				s.blobRefs[msg.Blob.Hash] = 0
			}
		}
		delete(s.messages, mid)
		delete(s.ftsDocs, mid)
		s.appendStateChangeLocked(store.StateChange{
			PrincipalID: mb.PrincipalID,
			Kind:        store.ChangeKindMessageDestroyed,
			MailboxID:   id,
			MessageID:   mid,
			MessageUID:  msg.UID,
			ProducedAt:  now,
		})
		s.appendFTSChangeLocked(store.FTSChange{
			PrincipalID: mb.PrincipalID,
			MailboxID:   id,
			MessageID:   mid,
			Kind:        store.ChangeKindMessageDestroyed,
			ProducedAt:  now,
		})
	}
	delete(s.mailboxes, id)
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: mb.PrincipalID,
		Kind:        store.ChangeKindMailboxDestroyed,
		MailboxID:   id,
		ProducedAt:  now,
	})
	return nil
}

// ---------- Metadata: messages ----------

func (m *metaFace) GetMessage(ctx context.Context, id store.MessageID) (store.Message, error) {
	if err := ctx.Err(); err != nil {
		return store.Message{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	msg, ok := s.messages[id]
	if !ok {
		return store.Message{}, fmt.Errorf("message %d: %w", id, store.ErrNotFound)
	}
	return msg, nil
}

func (m *metaFace) InsertMessage(ctx context.Context, msg store.Message) (store.UID, store.ModSeq, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.mailboxes[msg.MailboxID]
	if !ok {
		return 0, 0, fmt.Errorf("mailbox %d: %w", msg.MailboxID, store.ErrNotFound)
	}
	p, ok := s.principals[mb.PrincipalID]
	if !ok {
		return 0, 0, fmt.Errorf("principal %d: %w", mb.PrincipalID, store.ErrNotFound)
	}
	// Quota check: current total vs QuotaBytes (0 = unlimited).
	if p.QuotaBytes > 0 {
		var used int64
		for _, m2 := range s.messages {
			if existingMb, ok := s.mailboxes[m2.MailboxID]; ok && existingMb.PrincipalID == p.ID {
				used += m2.Size
			}
		}
		if used+msg.Size > p.QuotaBytes {
			return 0, 0, fmt.Errorf("principal %d: %w", p.ID, store.ErrQuotaExceeded)
		}
	}
	now := s.clk.Now()
	msg.ID = s.nextMessageID
	s.nextMessageID++
	msg.UID = mb.UIDNext
	mb.UIDNext++
	mb.HighestModSeq++
	msg.ModSeq = mb.HighestModSeq
	if msg.InternalDate.IsZero() {
		msg.InternalDate = now
	}
	if msg.ReceivedAt.IsZero() {
		msg.ReceivedAt = now
	}
	mb.UpdatedAt = now
	s.mailboxes[mb.ID] = mb
	s.messages[msg.ID] = msg
	if msg.Blob.Hash != "" {
		s.blobRefs[msg.Blob.Hash]++
	}
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: mb.PrincipalID,
		Kind:        store.ChangeKindMessageCreated,
		MailboxID:   mb.ID,
		MessageID:   msg.ID,
		MessageUID:  msg.UID,
		ProducedAt:  now,
	})
	s.appendFTSChangeLocked(store.FTSChange{
		PrincipalID: mb.PrincipalID,
		MailboxID:   mb.ID,
		MessageID:   msg.ID,
		Kind:        store.ChangeKindMessageCreated,
		ProducedAt:  now,
	})
	return msg.UID, msg.ModSeq, nil
}

func (m *metaFace) UpdateMessageFlags(
	ctx context.Context,
	id store.MessageID,
	flagAdd, flagClear store.MessageFlags,
	keywordAdd, keywordClear []string,
	unchangedSince store.ModSeq,
) (store.ModSeq, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	msg, ok := s.messages[id]
	if !ok {
		return 0, fmt.Errorf("message %d: %w", id, store.ErrNotFound)
	}
	if unchangedSince != 0 && msg.ModSeq > unchangedSince {
		return 0, fmt.Errorf("message %d unchangedsince: %w", id, store.ErrConflict)
	}
	mb, ok := s.mailboxes[msg.MailboxID]
	if !ok {
		return 0, fmt.Errorf("mailbox %d: %w", msg.MailboxID, store.ErrNotFound)
	}
	msg.Flags = (msg.Flags | flagAdd) &^ flagClear
	// Keyword delta
	if len(keywordAdd) > 0 || len(keywordClear) > 0 {
		set := make(map[string]struct{}, len(msg.Keywords))
		for _, k := range msg.Keywords {
			set[strings.ToLower(k)] = struct{}{}
		}
		for _, k := range keywordAdd {
			set[strings.ToLower(k)] = struct{}{}
		}
		for _, k := range keywordClear {
			delete(set, strings.ToLower(k))
		}
		if len(set) == 0 {
			msg.Keywords = nil
		} else {
			out := make([]string, 0, len(set))
			for k := range set {
				out = append(out, k)
			}
			sort.Strings(out)
			msg.Keywords = out
		}
	}
	mb.HighestModSeq++
	msg.ModSeq = mb.HighestModSeq
	now := s.clk.Now()
	mb.UpdatedAt = now
	s.mailboxes[mb.ID] = mb
	s.messages[msg.ID] = msg
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID: mb.PrincipalID,
		Kind:        store.ChangeKindMessageUpdated,
		MailboxID:   mb.ID,
		MessageID:   msg.ID,
		MessageUID:  msg.UID,
		ProducedAt:  now,
	})
	s.appendFTSChangeLocked(store.FTSChange{
		PrincipalID: mb.PrincipalID,
		MailboxID:   mb.ID,
		MessageID:   msg.ID,
		Kind:        store.ChangeKindMessageUpdated,
		ProducedAt:  now,
	})
	return msg.ModSeq, nil
}

func (m *metaFace) ExpungeMessages(ctx context.Context, mailboxID store.MailboxID, ids []store.MessageID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.mailboxes[mailboxID]
	if !ok {
		return fmt.Errorf("mailbox %d: %w", mailboxID, store.ErrNotFound)
	}
	now := s.clk.Now()
	var found int
	for _, id := range ids {
		msg, ok := s.messages[id]
		if !ok || msg.MailboxID != mailboxID {
			continue
		}
		found++
		if msg.Blob.Hash != "" {
			s.blobRefs[msg.Blob.Hash]--
			if s.blobRefs[msg.Blob.Hash] < 0 {
				s.blobRefs[msg.Blob.Hash] = 0
			}
		}
		delete(s.messages, id)
		delete(s.ftsDocs, id)
		mb.HighestModSeq++
		s.appendStateChangeLocked(store.StateChange{
			PrincipalID: mb.PrincipalID,
			Kind:        store.ChangeKindMessageDestroyed,
			MailboxID:   mailboxID,
			MessageID:   id,
			MessageUID:  msg.UID,
			ProducedAt:  now,
		})
		s.appendFTSChangeLocked(store.FTSChange{
			PrincipalID: mb.PrincipalID,
			MailboxID:   mailboxID,
			MessageID:   id,
			Kind:        store.ChangeKindMessageDestroyed,
			ProducedAt:  now,
		})
	}
	if found == 0 && len(ids) > 0 {
		return fmt.Errorf("expunge: %w", store.ErrNotFound)
	}
	mb.UpdatedAt = now
	s.mailboxes[mailboxID] = mb
	return nil
}

func (m *metaFace) UpdateMailboxModseqAndAppendChange(
	ctx context.Context,
	mailboxID store.MailboxID,
	change store.StateChange,
) (store.ModSeq, store.ChangeSeq, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.mailboxes[mailboxID]
	if !ok {
		return 0, 0, fmt.Errorf("mailbox %d: %w", mailboxID, store.ErrNotFound)
	}
	mb.HighestModSeq++
	now := s.clk.Now()
	mb.UpdatedAt = now
	s.mailboxes[mailboxID] = mb
	if change.ProducedAt.IsZero() {
		change.ProducedAt = now
	}
	if change.PrincipalID == 0 {
		change.PrincipalID = mb.PrincipalID
	}
	if change.MailboxID == 0 {
		change.MailboxID = mailboxID
	}
	s.appendStateChangeLocked(change)
	// The appended change now carries Seq; read it back.
	feed := s.stateChanges[change.PrincipalID]
	seq := feed[len(feed)-1].Seq
	return mb.HighestModSeq, seq, nil
}

// ---------- Metadata: change feed ----------

func (m *metaFace) ReadChangeFeed(
	ctx context.Context,
	principalID store.PrincipalID,
	fromSeq store.ChangeSeq,
	max int,
) ([]store.StateChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	feed := s.stateChanges[principalID]
	var out []store.StateChange
	for _, c := range feed {
		if c.Seq <= fromSeq {
			continue
		}
		out = append(out, c)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out, nil
}

// ---------- Metadata: aliases, domains ----------

func (m *metaFace) InsertAlias(ctx context.Context, a store.Alias) (store.Alias, error) {
	if err := ctx.Err(); err != nil {
		return store.Alias{}, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := aliasKey(a.LocalPart, a.Domain)
	if _, exists := s.aliasBy[key]; exists {
		return store.Alias{}, fmt.Errorf("alias %q: %w", key, store.ErrConflict)
	}
	a.ID = s.nextAliasID
	s.nextAliasID++
	if a.CreatedAt.IsZero() {
		a.CreatedAt = s.clk.Now()
	}
	s.aliases[a.ID] = a
	s.aliasBy[key] = a.ID
	return a, nil
}

func (m *metaFace) ResolveAlias(ctx context.Context, localPart, domain string) (store.PrincipalID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id, ok := s.aliasBy[aliasKey(localPart, domain)]; ok {
		a := s.aliases[id]
		if a.ExpiresAt != nil && !a.ExpiresAt.After(s.clk.Now()) {
			return 0, fmt.Errorf("alias %q expired: %w", aliasKey(localPart, domain), store.ErrNotFound)
		}
		return a.TargetPrincipal, nil
	}
	// Fall back to canonical principal address.
	email := canonEmail(localPart + "@" + domain)
	if id, ok := s.byEmail[email]; ok {
		return id, nil
	}
	return 0, fmt.Errorf("alias %q: %w", email, store.ErrNotFound)
}

func (m *metaFace) InsertDomain(ctx context.Context, d store.Domain) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	name := strings.ToLower(d.Name)
	if _, exists := s.domains[name]; exists {
		return fmt.Errorf("domain %q: %w", name, store.ErrConflict)
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = s.clk.Now()
	}
	d.Name = name
	s.domains[name] = d
	return nil
}

func (m *metaFace) GetDomain(ctx context.Context, name string) (store.Domain, error) {
	if err := ctx.Err(); err != nil {
		return store.Domain{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.domains[strings.ToLower(name)]
	if !ok {
		return store.Domain{}, fmt.Errorf("domain %q: %w", name, store.ErrNotFound)
	}
	return d, nil
}

func (m *metaFace) ListLocalDomains(ctx context.Context) ([]store.Domain, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.Domain
	for _, d := range s.domains {
		if d.IsLocal {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *metaFace) DeleteDomain(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(name)
	if _, ok := s.domains[key]; !ok {
		return fmt.Errorf("domain %q: %w", key, store.ErrNotFound)
	}
	delete(s.domains, key)
	return nil
}

func (m *metaFace) ListAliases(ctx context.Context, domain string) ([]store.Alias, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	dom := strings.ToLower(strings.TrimSpace(domain))
	out := make([]store.Alias, 0, len(s.aliases))
	for _, a := range s.aliases {
		if dom != "" && strings.ToLower(a.Domain) != dom {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].LocalPart < out[j].LocalPart
	})
	return out, nil
}

func (m *metaFace) DeleteAlias(ctx context.Context, id store.AliasID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.aliases[id]
	if !ok {
		return fmt.Errorf("alias %d: %w", id, store.ErrNotFound)
	}
	delete(s.aliases, id)
	delete(s.aliasBy, aliasKey(a.LocalPart, a.Domain))
	return nil
}

// ---------- Metadata: OIDC, API keys ----------

func (m *metaFace) InsertOIDCProvider(ctx context.Context, p store.OIDCProvider) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.providers[p.Name]; exists {
		return fmt.Errorf("oidc provider %q: %w", p.Name, store.ErrConflict)
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = s.clk.Now()
	}
	s.providers[p.Name] = p
	return nil
}

func (m *metaFace) GetOIDCProvider(ctx context.Context, name string) (store.OIDCProvider, error) {
	if err := ctx.Err(); err != nil {
		return store.OIDCProvider{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.providers[name]
	if !ok {
		return store.OIDCProvider{}, fmt.Errorf("oidc provider %q: %w", name, store.ErrNotFound)
	}
	return p, nil
}

func (m *metaFace) LinkOIDC(ctx context.Context, link store.OIDCLink) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := oidcLinkKey(link.ProviderName, link.Subject)
	if _, exists := s.oidcLinks[key]; exists {
		return fmt.Errorf("oidc link %q: %w", key, store.ErrConflict)
	}
	if link.LinkedAt.IsZero() {
		link.LinkedAt = s.clk.Now()
	}
	s.oidcLinks[key] = link
	return nil
}

func (m *metaFace) LookupOIDCLink(ctx context.Context, provider, subject string) (store.OIDCLink, error) {
	if err := ctx.Err(); err != nil {
		return store.OIDCLink{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	link, ok := s.oidcLinks[oidcLinkKey(provider, subject)]
	if !ok {
		return store.OIDCLink{}, fmt.Errorf("oidc link %s/%s: %w", provider, subject, store.ErrNotFound)
	}
	return link, nil
}

func (m *metaFace) InsertAPIKey(ctx context.Context, k store.APIKey) (store.APIKey, error) {
	if err := ctx.Err(); err != nil {
		return store.APIKey{}, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.keyByHash[k.Hash]; exists {
		return store.APIKey{}, fmt.Errorf("api key: %w", store.ErrConflict)
	}
	k.ID = s.nextAPIKeyID
	s.nextAPIKeyID++
	k.CreatedAt = s.clk.Now()
	s.apiKeys[k.ID] = k
	s.keyByHash[k.Hash] = k.ID
	return k, nil
}

func (m *metaFace) GetAPIKeyByHash(ctx context.Context, hash string) (store.APIKey, error) {
	if err := ctx.Err(); err != nil {
		return store.APIKey{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.keyByHash[hash]
	if !ok {
		return store.APIKey{}, fmt.Errorf("api key: %w", store.ErrNotFound)
	}
	return s.apiKeys[id], nil
}

func (m *metaFace) TouchAPIKey(ctx context.Context, id store.APIKeyID, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.apiKeys[id]
	if !ok {
		return fmt.Errorf("api key %d: %w", id, store.ErrNotFound)
	}
	k.LastUsedAt = at
	s.apiKeys[id] = k
	return nil
}

func (m *metaFace) ListAPIKeysByPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.APIKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]store.APIKey, 0)
	for _, k := range s.apiKeys {
		if k.PrincipalID == pid {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *metaFace) DeleteAPIKey(ctx context.Context, id store.APIKeyID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.apiKeys[id]
	if !ok {
		return fmt.Errorf("api key %d: %w", id, store.ErrNotFound)
	}
	delete(s.apiKeys, id)
	delete(s.keyByHash, k.Hash)
	return nil
}

func (m *metaFace) ListOIDCLinksByPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.OIDCLink, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]store.OIDCLink, 0)
	for _, link := range s.oidcLinks {
		if link.PrincipalID == pid {
			out = append(out, link)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderName < out[j].ProviderName })
	return out, nil
}

// ---------- Metadata: Wave 2 additions -------------------------------

// ListPrincipals returns principals with ID > after, ordered by ID
// ascending. Deterministic iteration is required by storetest; the map
// iteration is therefore funneled through a sorted key slice.
func (m *metaFace) ListPrincipals(ctx context.Context, after store.PrincipalID, limit int) ([]store.Principal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]store.PrincipalID, 0, len(s.principals))
	for id := range s.principals {
		if id > after {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]store.Principal, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.principals[id])
	}
	return out, nil
}

// DeletePrincipal cascades every row belonging to pid in one pass.
// The fakestore has no transactions so "atomic" here means "under the
// single writer lock"; that is enough for test determinism.
func (m *metaFace) DeletePrincipal(ctx context.Context, pid store.PrincipalID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.principals[pid]
	if !ok {
		return fmt.Errorf("principal %d: %w", pid, store.ErrNotFound)
	}
	// Messages -> decrement refcounts + drop FTS docs.
	for mid, msg := range s.messages {
		mb, ok := s.mailboxes[msg.MailboxID]
		if !ok || mb.PrincipalID != pid {
			continue
		}
		if msg.Blob.Hash != "" {
			s.blobRefs[msg.Blob.Hash]--
			if s.blobRefs[msg.Blob.Hash] < 0 {
				s.blobRefs[msg.Blob.Hash] = 0
			}
		}
		delete(s.messages, mid)
		delete(s.ftsDocs, mid)
	}
	// Mailboxes.
	for mbID, mb := range s.mailboxes {
		if mb.PrincipalID == pid {
			delete(s.mailboxes, mbID)
		}
	}
	// Aliases.
	for aid, a := range s.aliases {
		if a.TargetPrincipal == pid {
			delete(s.aliases, aid)
			delete(s.aliasBy, aliasKey(a.LocalPart, a.Domain))
		}
	}
	// OIDC links.
	for k, link := range s.oidcLinks {
		if link.PrincipalID == pid {
			delete(s.oidcLinks, k)
		}
	}
	// API keys.
	for kid, key := range s.apiKeys {
		if key.PrincipalID == pid {
			delete(s.apiKeys, kid)
			delete(s.keyByHash, key.Hash)
		}
	}
	// State-change feed.
	delete(s.stateChanges, pid)
	delete(s.changeSeq, pid)
	// Sieve script (mirrors the ON DELETE CASCADE in the SQL backends).
	delete(s.sieveScripts, pid)
	// Phase 2 cascades: queue rows submitted by this principal,
	// jmap_states row, mailbox ACL grants whose grantee or grantor was
	// this principal. Mirrors the SQL ON DELETE CASCADE in
	// migrations/0004_phase2.sql.
	if s.phase2 != nil {
		for qid, q := range s.phase2.queue {
			if q.PrincipalID == pid {
				if q.BodyBlobHash != "" {
					s.blobRefs[q.BodyBlobHash]--
					if s.blobRefs[q.BodyBlobHash] < 0 {
						s.blobRefs[q.BodyBlobHash] = 0
					}
				}
				delete(s.phase2.queue, qid)
			}
		}
		delete(s.phase2.jmapStates, pid)
		for aclID, a := range s.phase2.mailboxACL {
			if (a.PrincipalID != nil && *a.PrincipalID == pid) || a.GrantedBy == pid {
				delete(s.phase2.mailboxACL, aclID)
			}
		}
	}
	// Audit log: drop entries that target or originate from this
	// principal. Iterate and rebuild; audit volumes are low in tests.
	if len(s.auditLog) > 0 {
		kept := s.auditLog[:0]
		for _, e := range s.auditLog {
			if auditEntryPrincipalID(e) == pid {
				continue
			}
			kept = append(kept, e)
		}
		s.auditLog = kept
	}
	// Principal row + email index.
	delete(s.byEmail, p.CanonicalEmail)
	delete(s.principals, pid)
	return nil
}

// ListOIDCProviders returns every configured provider, ordered by
// Name for deterministic test iteration.
func (m *metaFace) ListOIDCProviders(ctx context.Context) ([]store.OIDCProvider, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.providers))
	for n := range s.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]store.OIDCProvider, 0, len(names))
	for _, n := range names {
		out = append(out, s.providers[n])
	}
	return out, nil
}

// DeleteOIDCProvider cascades every link that points at the provider.
func (m *metaFace) DeleteOIDCProvider(ctx context.Context, id store.OIDCProviderID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.providers[string(id)]; !ok {
		return fmt.Errorf("oidc provider %q: %w", id, store.ErrNotFound)
	}
	delete(s.providers, string(id))
	for k, link := range s.oidcLinks {
		if link.ProviderName == string(id) {
			delete(s.oidcLinks, k)
		}
	}
	return nil
}

// UnlinkOIDC removes the single oidc_links row for (pid, providerID).
func (m *metaFace) UnlinkOIDC(ctx context.Context, pid store.PrincipalID, providerID store.OIDCProviderID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, link := range s.oidcLinks {
		if link.PrincipalID == pid && link.ProviderName == string(providerID) {
			delete(s.oidcLinks, k)
			return nil
		}
	}
	return fmt.Errorf("oidc link %s/%d: %w", providerID, pid, store.ErrNotFound)
}

// GetFTSCursor returns the cursor for key, or (0, nil) if none.
func (m *metaFace) GetFTSCursor(ctx context.Context, key string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cursors[key], nil
}

// SetFTSCursor upserts the cursor for key.
func (m *metaFace) SetFTSCursor(ctx context.Context, key string, seq uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[key] = seq
	return nil
}

// AppendAuditLog adds a row to the in-memory audit log.
func (m *metaFace) AppendAuditLog(ctx context.Context, entry store.AuditLogEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.ID = s.nextAuditLogID
	s.nextAuditLogID++
	if entry.At.IsZero() {
		entry.At = s.clk.Now()
	}
	// Copy Metadata so callers cannot mutate stored state afterwards.
	if len(entry.Metadata) > 0 {
		cp := make(map[string]string, len(entry.Metadata))
		for k, v := range entry.Metadata {
			cp[k] = v
		}
		entry.Metadata = cp
	}
	s.auditLog = append(s.auditLog, entry)
	return nil
}

// ListAuditLog applies the filter and returns a copy of matching rows.
func (m *metaFace) ListAuditLog(ctx context.Context, filter store.AuditLogFilter) ([]store.AuditLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.AuditLogEntry
	for _, e := range s.auditLog {
		if e.ID <= filter.AfterID {
			continue
		}
		if filter.PrincipalID != 0 && auditEntryPrincipalID(e) != filter.PrincipalID {
			continue
		}
		if filter.Action != "" && e.Action != filter.Action {
			continue
		}
		if !filter.Since.IsZero() && e.At.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !e.At.Before(filter.Until) {
			continue
		}
		// Copy the Metadata map so caller mutations do not bleed back.
		cp := e
		if len(e.Metadata) > 0 {
			m := make(map[string]string, len(e.Metadata))
			for k, v := range e.Metadata {
				m[k] = v
			}
			cp.Metadata = m
		}
		out = append(out, cp)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ---------- Metadata: IMAP mailbox surface -------------------------

// GetMailboxByName returns the mailbox with the given name owned by
// principalID. Returns store.ErrNotFound when no such mailbox exists.
func (m *metaFace) GetMailboxByName(ctx context.Context, principalID store.PrincipalID, name string) (store.Mailbox, error) {
	if err := ctx.Err(); err != nil {
		return store.Mailbox{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, mb := range s.mailboxes {
		if mb.PrincipalID == principalID && mb.Name == name {
			return mb, nil
		}
	}
	return store.Mailbox{}, fmt.Errorf("mailbox %q: %w", name, store.ErrNotFound)
}

// ListMessages returns the messages in mailboxID in UID-ascending
// order, honouring filter.AfterUID and filter.Limit.
func (m *metaFace) ListMessages(ctx context.Context, mailboxID store.MailboxID, filter store.MessageFilter) ([]store.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.Message
	for _, msg := range s.messages {
		if msg.MailboxID != mailboxID {
			continue
		}
		if msg.UID <= filter.AfterUID {
			continue
		}
		out = append(out, msg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SetMailboxSubscribed toggles the MailboxAttrSubscribed bit.
func (m *metaFace) SetMailboxSubscribed(ctx context.Context, mailboxID store.MailboxID, subscribed bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.mailboxes[mailboxID]
	if !ok {
		return fmt.Errorf("mailbox %d: %w", mailboxID, store.ErrNotFound)
	}
	if subscribed {
		mb.Attributes |= store.MailboxAttrSubscribed
	} else {
		mb.Attributes &^= store.MailboxAttrSubscribed
	}
	mb.UpdatedAt = s.clk.Now()
	s.mailboxes[mailboxID] = mb
	return nil
}

// RenameMailbox updates the Name field, returning store.ErrConflict if
// the destination collides with an existing mailbox for the same
// principal.
func (m *metaFace) RenameMailbox(ctx context.Context, mailboxID store.MailboxID, newName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.mailboxes[mailboxID]
	if !ok {
		return fmt.Errorf("mailbox %d: %w", mailboxID, store.ErrNotFound)
	}
	for id, existing := range s.mailboxes {
		if id == mailboxID {
			continue
		}
		if existing.PrincipalID == mb.PrincipalID && existing.Name == newName {
			return fmt.Errorf("mailbox %q: %w", newName, store.ErrConflict)
		}
	}
	mb.Name = newName
	mb.UpdatedAt = s.clk.Now()
	s.mailboxes[mailboxID] = mb
	return nil
}

// ---------- Metadata: Sieve scripts -------------------------------

// GetSieveScript returns the active Sieve script text for pid, or
// ("", nil) when no script is on record.
func (m *metaFace) GetSieveScript(ctx context.Context, pid store.PrincipalID) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sieveScripts[pid], nil
}

// SetSieveScript upserts the script text for pid; an empty text
// deletes the row (so a subsequent GetSieveScript returns "").
func (m *metaFace) SetSieveScript(ctx context.Context, pid store.PrincipalID, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if text == "" {
		delete(s.sieveScripts, pid)
		return nil
	}
	s.sieveScripts[pid] = text
	return nil
}

// auditEntryPrincipalID mirrors the logic used by the SQL backends so
// filtering behaviour is identical across implementations.
func auditEntryPrincipalID(e store.AuditLogEntry) store.PrincipalID {
	if strings.HasPrefix(e.Subject, "principal:") {
		if id, err := parseUint(e.Subject[len("principal:"):]); err == nil {
			return store.PrincipalID(id)
		}
	}
	if e.ActorKind == store.ActorPrincipal {
		if id, err := parseUint(e.ActorID); err == nil {
			return store.PrincipalID(id)
		}
	}
	return 0
}

// parseUint is a tiny wrapper around strconv.ParseUint that avoids a
// direct import in this section (keeps the imports block compact).
func parseUint(s string) (uint64, error) {
	var n uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("parse uint: bad char %q", c)
		}
		n = n*10 + uint64(c-'0')
	}
	if len(s) == 0 {
		return 0, fmt.Errorf("parse uint: empty")
	}
	return n, nil
}

// appendStateChangeLocked requires s.mu held exclusively. It assigns the
// principal-local Seq in the change before appending it.
func (s *Store) appendStateChangeLocked(c store.StateChange) {
	s.changeSeq[c.PrincipalID]++
	c.Seq = s.changeSeq[c.PrincipalID]
	s.stateChanges[c.PrincipalID] = append(s.stateChanges[c.PrincipalID], c)
}

// appendFTSChangeLocked requires s.mu held exclusively. It assigns the
// global Seq before appending.
func (s *Store) appendFTSChangeLocked(c store.FTSChange) {
	s.ftsSeq++
	c.Seq = s.ftsSeq
	s.ftsChanges = append(s.ftsChanges, c)
}

// ---------- Blobs ----------

type blobsFace Store

func (b *blobsFace) s() *Store { return (*Store)(b) }

// canonicalizeCRLF rewrites any bare LF (not preceded by CR) as CRLF, and any
// bare CR (not followed by LF) as CRLF. That matches RFC 5322 message
// canonicalization (REQ-STORE-10).
func canonicalizeCRLF(in []byte) []byte {
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch c {
		case '\r':
			out = append(out, '\r')
			if i+1 < len(in) && in[i+1] == '\n' {
				out = append(out, '\n')
				i++
			} else {
				out = append(out, '\n')
			}
		case '\n':
			out = append(out, '\r', '\n')
		default:
			out = append(out, c)
		}
	}
	return out
}

func (b *blobsFace) Put(ctx context.Context, r io.Reader) (store.BlobRef, error) {
	if err := ctx.Err(); err != nil {
		return store.BlobRef{}, err
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return store.BlobRef{}, fmt.Errorf("fakestore blob put: %w", err)
	}
	canon := canonicalizeCRLF(raw)
	sum := sha256.Sum256(canon)
	hash := hex.EncodeToString(sum[:])
	s := b.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.blobSize[hash]; !exists {
		path := filepath.Join(s.blobDir, hash)
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, canon, 0o600); err != nil {
			return store.BlobRef{}, fmt.Errorf("fakestore blob write: %w", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return store.BlobRef{}, fmt.Errorf("fakestore blob rename: %w", err)
		}
		s.blobSize[hash] = int64(len(canon))
	}
	return store.BlobRef{Hash: hash, Size: int64(len(canon))}, nil
}

func (b *blobsFace) Get(ctx context.Context, hash string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := b.s()
	s.mu.RLock()
	_, ok := s.blobSize[hash]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("blob %s: %w", hash, store.ErrNotFound)
	}
	f, err := os.Open(filepath.Join(s.blobDir, hash))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("blob %s: %w", hash, store.ErrNotFound)
		}
		return nil, fmt.Errorf("fakestore blob open: %w", err)
	}
	return f, nil
}

func (b *blobsFace) Stat(ctx context.Context, hash string) (int64, int, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	s := b.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	size, ok := s.blobSize[hash]
	if !ok {
		return 0, 0, fmt.Errorf("blob %s: %w", hash, store.ErrNotFound)
	}
	return size, s.blobRefs[hash], nil
}

func (b *blobsFace) Delete(ctx context.Context, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := b.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blobSize[hash]; !ok {
		return fmt.Errorf("blob %s: %w", hash, store.ErrNotFound)
	}
	delete(s.blobSize, hash)
	delete(s.blobRefs, hash)
	path := filepath.Join(s.blobDir, hash)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fakestore blob delete: %w", err)
	}
	return nil
}

// ---------- FTS ----------

type ftsFace Store

func (f *ftsFace) s() *Store { return (*Store)(f) }

func (f *ftsFace) IndexMessage(ctx context.Context, msg store.Message, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := f.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ftsDocs[msg.ID] = ftsDoc{msg: msg, text: strings.ToLower(text)}
	return nil
}

func (f *ftsFace) RemoveMessage(ctx context.Context, id store.MessageID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := f.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ftsDocs, id)
	return nil
}

func (f *ftsFace) Query(ctx context.Context, principalID store.PrincipalID, q store.Query) ([]store.MessageRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := f.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var terms []string
	if q.Text != "" {
		terms = append(terms, strings.ToLower(q.Text))
	}
	addLower := func(xs []string) {
		for _, x := range xs {
			if strings.TrimSpace(x) == "" {
				continue
			}
			terms = append(terms, strings.ToLower(x))
		}
	}
	addLower(q.Subject)
	addLower(q.From)
	addLower(q.To)
	addLower(q.Body)
	addLower(q.AttachmentName)

	var out []store.MessageRef
	for _, doc := range s.ftsDocs {
		mb, ok := s.mailboxes[doc.msg.MailboxID]
		if !ok || mb.PrincipalID != principalID {
			continue
		}
		if q.MailboxID != 0 && mb.ID != q.MailboxID {
			continue
		}
		// Empty query matches everything the principal owns.
		matched := true
		score := 1.0
		for _, t := range terms {
			if !strings.Contains(doc.text, t) {
				matched = false
				break
			}
			score += 1.0
		}
		if !matched {
			continue
		}
		out = append(out, store.MessageRef{
			MessageID: doc.msg.ID,
			MailboxID: doc.msg.MailboxID,
			Score:     score,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].MessageID < out[j].MessageID
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (f *ftsFace) ReadChangeFeedForFTS(ctx context.Context, cursor uint64, max int) ([]store.FTSChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := f.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.FTSChange
	for _, c := range s.ftsChanges {
		if c.Seq <= cursor {
			continue
		}
		out = append(out, c)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out, nil
}

func (f *ftsFace) Commit(ctx context.Context) error {
	return ctx.Err()
}
