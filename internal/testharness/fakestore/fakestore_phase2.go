package fakestore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file extends fakestore with in-memory implementations of every
// Phase 2 store.Metadata method (queue, DKIM, ACME, webhooks, DMARC,
// mailbox ACL, JMAP states, TLS-RPT). Behaviour matches the SQL
// backends to within the tolerance the storetest matrix exercises
// (sentinel errors, deterministic iteration order, idempotency
// contracts). Backing maps live on Store; the methods here are added
// to the existing metaFace alias.

// queueData groups the queue subsystem's in-memory state to keep the
// Store struct lean. We attach it via a sync.Once initialiser hidden
// behind ensurePhase2 so existing constructors do not need editing.
type phase2Data struct {
	queue       map[store.QueueItemID]store.QueueItem
	nextQueueID store.QueueItemID

	dkimKeys    map[store.DKIMKeyID]store.DKIMKey
	nextDKIMID  store.DKIMKeyID
	acmeAccts   map[store.ACMEAccountID]store.ACMEAccount
	nextACMEAcc store.ACMEAccountID
	acmeOrders  map[store.ACMEOrderID]store.ACMEOrder
	nextACMEOrd store.ACMEOrderID
	acmeCerts   map[string]store.ACMECert

	webhooks    map[store.WebhookID]store.Webhook
	nextWebhook store.WebhookID

	dmarcReports map[store.DMARCReportID]store.DMARCReport
	dmarcRows    map[store.DMARCRowID]store.DMARCRow
	nextReportID store.DMARCReportID
	nextRowID    store.DMARCRowID

	mailboxACL map[store.MailboxACLID]store.MailboxACL
	nextACLID  store.MailboxACLID
	jmapStates map[store.PrincipalID]store.JMAPStates
	tlsrpt     map[store.TLSRPTFailureID]store.TLSRPTFailure
	nextTLSRPT store.TLSRPTFailureID

	emailSubmissions map[string]store.EmailSubmissionRow
	jmapIdentities   map[string]store.JMAPIdentity

	// catConfig is the LLM categoriser per-principal configuration
	// (REQ-FILT-210). Lazily initialised on first read so the existing
	// constructor does not need editing.
	catConfig map[store.PrincipalID]store.CategorisationConfig
}

// ensurePhase2 lazily initialises the Phase 2 in-memory state. Called
// from every Phase 2 method under s.mu (write or read lock) — when
// invoked under the read lock the caller must release-and-reacquire
// for the write path; we keep this simple by calling under s.mu held
// exclusively at first touch.
func (s *Store) ensurePhase2() {
	if s.phase2 != nil {
		return
	}
	s.phase2 = &phase2Data{
		queue:            make(map[store.QueueItemID]store.QueueItem),
		nextQueueID:      1,
		dkimKeys:         make(map[store.DKIMKeyID]store.DKIMKey),
		nextDKIMID:       1,
		acmeAccts:        make(map[store.ACMEAccountID]store.ACMEAccount),
		nextACMEAcc:      1,
		acmeOrders:       make(map[store.ACMEOrderID]store.ACMEOrder),
		nextACMEOrd:      1,
		acmeCerts:        make(map[string]store.ACMECert),
		webhooks:         make(map[store.WebhookID]store.Webhook),
		nextWebhook:      1,
		dmarcReports:     make(map[store.DMARCReportID]store.DMARCReport),
		dmarcRows:        make(map[store.DMARCRowID]store.DMARCRow),
		nextReportID:     1,
		nextRowID:        1,
		mailboxACL:       make(map[store.MailboxACLID]store.MailboxACL),
		nextACLID:        1,
		jmapStates:       make(map[store.PrincipalID]store.JMAPStates),
		tlsrpt:           make(map[store.TLSRPTFailureID]store.TLSRPTFailure),
		nextTLSRPT:       1,
		emailSubmissions: make(map[string]store.EmailSubmissionRow),
		jmapIdentities:   make(map[string]store.JMAPIdentity),
		catConfig:        make(map[store.PrincipalID]store.CategorisationConfig),
	}
}

// -- queue ------------------------------------------------------------

func (m *metaFace) EnqueueMessage(ctx context.Context, item store.QueueItem) (store.QueueItemID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	if item.IdempotencyKey != "" {
		for _, q := range s.phase2.queue {
			if q.IdempotencyKey == item.IdempotencyKey {
				return q.ID, fmt.Errorf("queue idempotency key %q: %w", item.IdempotencyKey, store.ErrConflict)
			}
		}
	}
	item.ID = s.phase2.nextQueueID
	s.phase2.nextQueueID++
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	if item.NextAttemptAt.IsZero() {
		item.NextAttemptAt = now
	}
	if item.State == store.QueueStateUnknown {
		item.State = store.QueueStateQueued
	}
	item.MailFrom = strings.ToLower(item.MailFrom)
	item.RcptTo = strings.ToLower(item.RcptTo)
	if item.BodyBlobHash != "" {
		s.blobRefs[item.BodyBlobHash]++
	}
	s.phase2.queue[item.ID] = item
	return item.ID, nil
}

func (m *metaFace) ClaimDueQueueItems(ctx context.Context, now time.Time, max int) ([]store.QueueItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if max <= 0 {
		max = 100
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	candidates := make([]store.QueueItem, 0)
	for _, q := range s.phase2.queue {
		if (q.State == store.QueueStateQueued || q.State == store.QueueStateDeferred) &&
			!q.NextAttemptAt.After(now) {
			candidates = append(candidates, q)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].NextAttemptAt.Equal(candidates[j].NextAttemptAt) {
			return candidates[i].NextAttemptAt.Before(candidates[j].NextAttemptAt)
		}
		return candidates[i].ID < candidates[j].ID
	})
	if len(candidates) > max {
		candidates = candidates[:max]
	}
	for i := range candidates {
		q := candidates[i]
		q.State = store.QueueStateInflight
		q.LastAttemptAt = now
		s.phase2.queue[q.ID] = q
		candidates[i] = q
	}
	return candidates, nil
}

func (m *metaFace) CompleteQueueItem(ctx context.Context, id store.QueueItemID, success bool, errMsg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	q, ok := s.phase2.queue[id]
	if !ok {
		return fmt.Errorf("queue %d: %w", id, store.ErrNotFound)
	}
	if success {
		q.State = store.QueueStateDone
	} else {
		q.State = store.QueueStateFailed
	}
	q.LastError = errMsg
	q.LastAttemptAt = s.clk.Now()
	s.phase2.queue[id] = q
	if q.BodyBlobHash != "" {
		s.blobRefs[q.BodyBlobHash]--
		if s.blobRefs[q.BodyBlobHash] < 0 {
			s.blobRefs[q.BodyBlobHash] = 0
		}
	}
	return nil
}

func (m *metaFace) RescheduleQueueItem(ctx context.Context, id store.QueueItemID, nextAttempt time.Time, errMsg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	q, ok := s.phase2.queue[id]
	if !ok {
		return fmt.Errorf("queue %d: %w", id, store.ErrNotFound)
	}
	q.State = store.QueueStateDeferred
	q.Attempts++
	q.NextAttemptAt = nextAttempt
	q.LastError = errMsg
	s.phase2.queue[id] = q
	return nil
}

func (m *metaFace) HoldQueueItem(ctx context.Context, id store.QueueItemID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	q, ok := s.phase2.queue[id]
	if !ok {
		return fmt.Errorf("queue %d: %w", id, store.ErrNotFound)
	}
	q.State = store.QueueStateHeld
	s.phase2.queue[id] = q
	return nil
}

func (m *metaFace) ReleaseQueueItem(ctx context.Context, id store.QueueItemID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	q, ok := s.phase2.queue[id]
	if !ok {
		return fmt.Errorf("queue %d: %w", id, store.ErrNotFound)
	}
	q.State = store.QueueStateQueued
	q.NextAttemptAt = s.clk.Now()
	s.phase2.queue[id] = q
	return nil
}

func (m *metaFace) DeleteQueueItem(ctx context.Context, id store.QueueItemID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	q, ok := s.phase2.queue[id]
	if !ok {
		return fmt.Errorf("queue %d: %w", id, store.ErrNotFound)
	}
	delete(s.phase2.queue, id)
	if q.BodyBlobHash != "" {
		s.blobRefs[q.BodyBlobHash]--
		if s.blobRefs[q.BodyBlobHash] < 0 {
			s.blobRefs[q.BodyBlobHash] = 0
		}
	}
	return nil
}

func (m *metaFace) GetQueueItem(ctx context.Context, id store.QueueItemID) (store.QueueItem, error) {
	if err := ctx.Err(); err != nil {
		return store.QueueItem{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.QueueItem{}, fmt.Errorf("queue %d: %w", id, store.ErrNotFound)
	}
	q, ok := s.phase2.queue[id]
	if !ok {
		return store.QueueItem{}, fmt.Errorf("queue %d: %w", id, store.ErrNotFound)
	}
	return q, nil
}

func (m *metaFace) ListQueueItems(ctx context.Context, filter store.QueueFilter) ([]store.QueueItem, error) {
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
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.QueueItem
	for _, q := range s.phase2.queue {
		if q.ID <= filter.AfterID {
			continue
		}
		if filter.State != store.QueueStateUnknown && q.State != filter.State {
			continue
		}
		if filter.PrincipalID != 0 && q.PrincipalID != filter.PrincipalID {
			continue
		}
		if filter.EnvelopeID != "" && q.EnvelopeID != filter.EnvelopeID {
			continue
		}
		if filter.RecipientDomain != "" {
			suffix := "@" + strings.ToLower(filter.RecipientDomain)
			if !strings.HasSuffix(strings.ToLower(q.RcptTo), suffix) {
				continue
			}
		}
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *metaFace) CountQueueByState(ctx context.Context) (map[store.QueueState]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[store.QueueState]int)
	if s.phase2 == nil {
		return out, nil
	}
	for _, q := range s.phase2.queue {
		out[q.State]++
	}
	return out, nil
}

// -- DKIM keys --------------------------------------------------------

func (m *metaFace) UpsertDKIMKey(ctx context.Context, key store.DKIMKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	dom := strings.ToLower(key.Domain)
	for id, k := range s.phase2.dkimKeys {
		if strings.EqualFold(k.Domain, dom) && k.Selector == key.Selector {
			k.Algorithm = key.Algorithm
			k.PrivateKeyPEM = key.PrivateKeyPEM
			k.PublicKeyB64 = key.PublicKeyB64
			k.Status = key.Status
			k.RotatedAt = now
			s.phase2.dkimKeys[id] = k
			return nil
		}
	}
	key.ID = s.phase2.nextDKIMID
	s.phase2.nextDKIMID++
	key.Domain = dom
	if key.CreatedAt.IsZero() {
		key.CreatedAt = now
	}
	key.RotatedAt = now
	s.phase2.dkimKeys[key.ID] = key
	return nil
}

func (m *metaFace) GetActiveDKIMKey(ctx context.Context, domain string) (store.DKIMKey, error) {
	if err := ctx.Err(); err != nil {
		return store.DKIMKey{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	dom := strings.ToLower(domain)
	if s.phase2 == nil {
		return store.DKIMKey{}, fmt.Errorf("dkim active %q: %w", dom, store.ErrNotFound)
	}
	var best store.DKIMKey
	bestID := store.DKIMKeyID(0)
	for id, k := range s.phase2.dkimKeys {
		if strings.EqualFold(k.Domain, dom) && k.Status == store.DKIMKeyStatusActive {
			if id > bestID {
				bestID = id
				best = k
			}
		}
	}
	if bestID == 0 {
		return store.DKIMKey{}, fmt.Errorf("dkim active %q: %w", dom, store.ErrNotFound)
	}
	return best, nil
}

func (m *metaFace) ListDKIMKeys(ctx context.Context, domain string) ([]store.DKIMKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	dom := strings.ToLower(domain)
	var out []store.DKIMKey
	for _, k := range s.phase2.dkimKeys {
		if strings.EqualFold(k.Domain, dom) {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Selector < out[j].Selector })
	return out, nil
}

func (m *metaFace) RotateDKIMKey(ctx context.Context, domain, oldSelector string, newKey store.DKIMKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	dom := strings.ToLower(domain)
	var oldID store.DKIMKeyID
	for id, k := range s.phase2.dkimKeys {
		if strings.EqualFold(k.Domain, dom) && k.Selector == oldSelector {
			oldID = id
			break
		}
	}
	if oldID == 0 {
		return fmt.Errorf("dkim retire %q/%q: %w", dom, oldSelector, store.ErrNotFound)
	}
	old := s.phase2.dkimKeys[oldID]
	old.Status = store.DKIMKeyStatusRetiring
	old.RotatedAt = now
	s.phase2.dkimKeys[oldID] = old
	// Upsert the new key as active.
	for id, k := range s.phase2.dkimKeys {
		if strings.EqualFold(k.Domain, dom) && k.Selector == newKey.Selector {
			k.Algorithm = newKey.Algorithm
			k.PrivateKeyPEM = newKey.PrivateKeyPEM
			k.PublicKeyB64 = newKey.PublicKeyB64
			k.Status = store.DKIMKeyStatusActive
			k.RotatedAt = now
			s.phase2.dkimKeys[id] = k
			return nil
		}
	}
	newKey.ID = s.phase2.nextDKIMID
	s.phase2.nextDKIMID++
	newKey.Domain = dom
	newKey.Status = store.DKIMKeyStatusActive
	if newKey.CreatedAt.IsZero() {
		newKey.CreatedAt = now
	}
	newKey.RotatedAt = now
	s.phase2.dkimKeys[newKey.ID] = newKey
	return nil
}

// -- ACME -------------------------------------------------------------

func (m *metaFace) UpsertACMEAccount(ctx context.Context, acc store.ACMEAccount) (store.ACMEAccount, error) {
	if err := ctx.Err(); err != nil {
		return store.ACMEAccount{}, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	email := strings.ToLower(acc.ContactEmail)
	for id, a := range s.phase2.acmeAccts {
		if a.DirectoryURL == acc.DirectoryURL && a.ContactEmail == email {
			a.AccountKeyPEM = acc.AccountKeyPEM
			a.KID = acc.KID
			s.phase2.acmeAccts[id] = a
			return a, nil
		}
	}
	acc.ID = s.phase2.nextACMEAcc
	s.phase2.nextACMEAcc++
	acc.ContactEmail = email
	if acc.CreatedAt.IsZero() {
		acc.CreatedAt = now
	}
	s.phase2.acmeAccts[acc.ID] = acc
	return acc, nil
}

func (m *metaFace) GetACMEAccount(ctx context.Context, directoryURL, contactEmail string) (store.ACMEAccount, error) {
	if err := ctx.Err(); err != nil {
		return store.ACMEAccount{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.ACMEAccount{}, fmt.Errorf("acme account: %w", store.ErrNotFound)
	}
	email := strings.ToLower(contactEmail)
	for _, a := range s.phase2.acmeAccts {
		if a.DirectoryURL == directoryURL && a.ContactEmail == email {
			return a, nil
		}
	}
	return store.ACMEAccount{}, fmt.Errorf("acme account: %w", store.ErrNotFound)
}

func (m *metaFace) ListACMEAccounts(ctx context.Context) ([]store.ACMEAccount, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	out := make([]store.ACMEAccount, 0, len(s.phase2.acmeAccts))
	for _, a := range s.phase2.acmeAccts {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *metaFace) InsertACMEOrder(ctx context.Context, order store.ACMEOrder) (store.ACMEOrder, error) {
	if err := ctx.Err(); err != nil {
		return store.ACMEOrder{}, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	if order.UpdatedAt.IsZero() {
		order.UpdatedAt = s.clk.Now()
	}
	order.ID = s.phase2.nextACMEOrd
	s.phase2.nextACMEOrd++
	// Defensive copy of Hostnames so callers cannot mutate stored state.
	if len(order.Hostnames) > 0 {
		hn := make([]string, len(order.Hostnames))
		copy(hn, order.Hostnames)
		order.Hostnames = hn
	}
	s.phase2.acmeOrders[order.ID] = order
	return order, nil
}

func (m *metaFace) UpdateACMEOrder(ctx context.Context, order store.ACMEOrder) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	if _, ok := s.phase2.acmeOrders[order.ID]; !ok {
		return fmt.Errorf("acme order %d: %w", order.ID, store.ErrNotFound)
	}
	order.UpdatedAt = s.clk.Now()
	if len(order.Hostnames) > 0 {
		hn := make([]string, len(order.Hostnames))
		copy(hn, order.Hostnames)
		order.Hostnames = hn
	}
	s.phase2.acmeOrders[order.ID] = order
	return nil
}

func (m *metaFace) GetACMEOrder(ctx context.Context, id store.ACMEOrderID) (store.ACMEOrder, error) {
	if err := ctx.Err(); err != nil {
		return store.ACMEOrder{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.ACMEOrder{}, fmt.Errorf("acme order %d: %w", id, store.ErrNotFound)
	}
	o, ok := s.phase2.acmeOrders[id]
	if !ok {
		return store.ACMEOrder{}, fmt.Errorf("acme order %d: %w", id, store.ErrNotFound)
	}
	return o, nil
}

func (m *metaFace) ListACMEOrdersByStatus(ctx context.Context, status store.ACMEOrderStatus) ([]store.ACMEOrder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.ACMEOrder
	for _, o := range s.phase2.acmeOrders {
		if o.Status == status {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.Before(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *metaFace) UpsertACMECert(ctx context.Context, cert store.ACMECert) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cert.Hostname = strings.ToLower(cert.Hostname)
	s.phase2.acmeCerts[cert.Hostname] = cert
	return nil
}

func (m *metaFace) GetACMECert(ctx context.Context, hostname string) (store.ACMECert, error) {
	if err := ctx.Err(); err != nil {
		return store.ACMECert{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.ACMECert{}, fmt.Errorf("acme cert %q: %w", hostname, store.ErrNotFound)
	}
	c, ok := s.phase2.acmeCerts[strings.ToLower(hostname)]
	if !ok {
		return store.ACMECert{}, fmt.Errorf("acme cert %q: %w", hostname, store.ErrNotFound)
	}
	return c, nil
}

func (m *metaFace) ListACMECertsExpiringBefore(ctx context.Context, t time.Time) ([]store.ACMECert, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.ACMECert
	for _, c := range s.phase2.acmeCerts {
		if c.NotAfter.Before(t) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NotAfter.Before(out[j].NotAfter) })
	return out, nil
}

// -- webhooks ---------------------------------------------------------

func (m *metaFace) InsertWebhook(ctx context.Context, w store.Webhook) (store.Webhook, error) {
	if err := ctx.Err(); err != nil {
		return store.Webhook{}, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	w.ID = s.phase2.nextWebhook
	s.phase2.nextWebhook++
	w.CreatedAt = now
	w.UpdatedAt = now
	if len(w.HMACSecret) > 0 {
		secret := make([]byte, len(w.HMACSecret))
		copy(secret, w.HMACSecret)
		w.HMACSecret = secret
	}
	s.phase2.webhooks[w.ID] = w
	return w, nil
}

func (m *metaFace) UpdateWebhook(ctx context.Context, w store.Webhook) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	existing, ok := s.phase2.webhooks[w.ID]
	if !ok {
		return fmt.Errorf("webhook %d: %w", w.ID, store.ErrNotFound)
	}
	w.CreatedAt = existing.CreatedAt
	w.UpdatedAt = s.clk.Now()
	if len(w.HMACSecret) > 0 {
		secret := make([]byte, len(w.HMACSecret))
		copy(secret, w.HMACSecret)
		w.HMACSecret = secret
	}
	s.phase2.webhooks[w.ID] = w
	return nil
}

func (m *metaFace) DeleteWebhook(ctx context.Context, id store.WebhookID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	if _, ok := s.phase2.webhooks[id]; !ok {
		return fmt.Errorf("webhook %d: %w", id, store.ErrNotFound)
	}
	delete(s.phase2.webhooks, id)
	return nil
}

func (m *metaFace) GetWebhook(ctx context.Context, id store.WebhookID) (store.Webhook, error) {
	if err := ctx.Err(); err != nil {
		return store.Webhook{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.Webhook{}, fmt.Errorf("webhook %d: %w", id, store.ErrNotFound)
	}
	w, ok := s.phase2.webhooks[id]
	if !ok {
		return store.Webhook{}, fmt.Errorf("webhook %d: %w", id, store.ErrNotFound)
	}
	return w, nil
}

func (m *metaFace) ListWebhooks(ctx context.Context, kind store.WebhookOwnerKind, ownerID string) ([]store.Webhook, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.Webhook
	for _, w := range s.phase2.webhooks {
		if kind != store.WebhookOwnerUnknown && (w.OwnerKind != kind || w.OwnerID != ownerID) {
			continue
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *metaFace) ListActiveWebhooksForDomain(ctx context.Context, domain string) ([]store.Webhook, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	dom := strings.ToLower(domain)
	if s.phase2 == nil {
		return nil, nil
	}
	// Build the principal-id -> domain map for the principal-owner case.
	principalDomain := make(map[string]string, len(s.principals))
	for _, p := range s.principals {
		idx := strings.IndexByte(p.CanonicalEmail, '@')
		if idx < 0 {
			continue
		}
		principalDomain[fmt.Sprintf("%d", p.ID)] = strings.ToLower(p.CanonicalEmail[idx+1:])
	}
	var out []store.Webhook
	for _, w := range s.phase2.webhooks {
		if !w.Active {
			continue
		}
		switch w.OwnerKind {
		case store.WebhookOwnerDomain:
			if strings.EqualFold(w.OwnerID, dom) {
				out = append(out, w)
			}
		case store.WebhookOwnerPrincipal:
			if d, ok := principalDomain[w.OwnerID]; ok && d == dom {
				out = append(out, w)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// -- DMARC ------------------------------------------------------------

func (m *metaFace) InsertDMARCReport(ctx context.Context, report store.DMARCReport, drows []store.DMARCRow) (store.DMARCReportID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	for _, r := range s.phase2.dmarcReports {
		if r.ReporterOrg == report.ReporterOrg && r.ReportID == report.ReportID {
			return r.ID, fmt.Errorf("dmarc report %s/%s: %w", report.ReporterOrg, report.ReportID, store.ErrConflict)
		}
	}
	report.ID = s.phase2.nextReportID
	s.phase2.nextReportID++
	report.ReporterEmail = strings.ToLower(report.ReporterEmail)
	report.Domain = strings.ToLower(report.Domain)
	s.phase2.dmarcReports[report.ID] = report
	for _, r := range drows {
		r.ID = s.phase2.nextRowID
		s.phase2.nextRowID++
		r.ReportID = report.ID
		r.HeaderFrom = strings.ToLower(r.HeaderFrom)
		r.EnvelopeFrom = strings.ToLower(r.EnvelopeFrom)
		r.EnvelopeTo = strings.ToLower(r.EnvelopeTo)
		s.phase2.dmarcRows[r.ID] = r
	}
	return report.ID, nil
}

func (m *metaFace) GetDMARCReport(ctx context.Context, id store.DMARCReportID) (store.DMARCReport, []store.DMARCRow, error) {
	if err := ctx.Err(); err != nil {
		return store.DMARCReport{}, nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.DMARCReport{}, nil, fmt.Errorf("dmarc report %d: %w", id, store.ErrNotFound)
	}
	rep, ok := s.phase2.dmarcReports[id]
	if !ok {
		return store.DMARCReport{}, nil, fmt.Errorf("dmarc report %d: %w", id, store.ErrNotFound)
	}
	var rows []store.DMARCRow
	for _, r := range s.phase2.dmarcRows {
		if r.ReportID == id {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rep, rows, nil
}

func (m *metaFace) ListDMARCReports(ctx context.Context, filter store.DMARCReportFilter) ([]store.DMARCReport, error) {
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
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.DMARCReport
	for _, r := range s.phase2.dmarcReports {
		if r.ID <= filter.AfterID {
			continue
		}
		if filter.Domain != "" && r.Domain != strings.ToLower(filter.Domain) {
			continue
		}
		if !filter.Since.IsZero() && r.DateBegin.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !r.DateBegin.Before(filter.Until) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *metaFace) DMARCAggregate(ctx context.Context, domain string, since, until time.Time) ([]store.DMARCAggregateRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	dom := strings.ToLower(domain)
	type key struct {
		hf   string
		disp int32
	}
	agg := make(map[key]*store.DMARCAggregateRow)
	for _, r := range s.phase2.dmarcRows {
		rep, ok := s.phase2.dmarcReports[r.ReportID]
		if !ok {
			continue
		}
		if rep.Domain != dom {
			continue
		}
		if !since.IsZero() && rep.DateBegin.Before(since) {
			continue
		}
		if !until.IsZero() && !rep.DateBegin.Before(until) {
			continue
		}
		k := key{r.HeaderFrom, r.Disposition}
		row := agg[k]
		if row == nil {
			row = &store.DMARCAggregateRow{HeaderFrom: r.HeaderFrom, Disposition: r.Disposition}
			agg[k] = row
		}
		row.Count += r.Count
		if r.SPFAligned {
			row.PassedSPF += r.Count
		}
		if r.DKIMAligned {
			row.PassedDKIM += r.Count
		}
	}
	out := make([]store.DMARCAggregateRow, 0, len(agg))
	for _, v := range agg {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HeaderFrom != out[j].HeaderFrom {
			return out[i].HeaderFrom < out[j].HeaderFrom
		}
		return out[i].Disposition < out[j].Disposition
	})
	return out, nil
}

// -- mailbox ACL ------------------------------------------------------

func aclMatch(acl store.MailboxACL, mailboxID store.MailboxID, principalID *store.PrincipalID) bool {
	if acl.MailboxID != mailboxID {
		return false
	}
	if principalID == nil {
		return acl.PrincipalID == nil
	}
	if acl.PrincipalID == nil {
		return false
	}
	return *acl.PrincipalID == *principalID
}

func (m *metaFace) SetMailboxACL(ctx context.Context, mailboxID store.MailboxID, principalID *store.PrincipalID, rights store.ACLRights, grantedBy store.PrincipalID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	for id, a := range s.phase2.mailboxACL {
		if aclMatch(a, mailboxID, principalID) {
			a.Rights = rights
			a.GrantedBy = grantedBy
			s.phase2.mailboxACL[id] = a
			return nil
		}
	}
	acl := store.MailboxACL{
		ID:        s.phase2.nextACLID,
		MailboxID: mailboxID,
		Rights:    rights,
		GrantedBy: grantedBy,
		CreatedAt: s.clk.Now(),
	}
	if principalID != nil {
		pp := *principalID
		acl.PrincipalID = &pp
	}
	s.phase2.nextACLID++
	s.phase2.mailboxACL[acl.ID] = acl
	return nil
}

func (m *metaFace) GetMailboxACL(ctx context.Context, mailboxID store.MailboxID) ([]store.MailboxACL, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var anyone []store.MailboxACL
	var named []store.MailboxACL
	for _, a := range s.phase2.mailboxACL {
		if a.MailboxID != mailboxID {
			continue
		}
		if a.PrincipalID == nil {
			anyone = append(anyone, a)
		} else {
			named = append(named, a)
		}
	}
	sort.Slice(anyone, func(i, j int) bool { return anyone[i].ID < anyone[j].ID })
	sort.Slice(named, func(i, j int) bool {
		return *named[i].PrincipalID < *named[j].PrincipalID
	})
	return append(anyone, named...), nil
}

func (m *metaFace) ListMailboxesAccessibleBy(ctx context.Context, pid store.PrincipalID) ([]store.Mailbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	allowed := make(map[store.MailboxID]struct{})
	for _, a := range s.phase2.mailboxACL {
		if a.Rights&store.ACLRightLookup == 0 {
			continue
		}
		if a.PrincipalID == nil || *a.PrincipalID == pid {
			allowed[a.MailboxID] = struct{}{}
		}
	}
	var out []store.Mailbox
	for id := range allowed {
		if mb, ok := s.mailboxes[id]; ok {
			out = append(out, mb)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *metaFace) RemoveMailboxACL(ctx context.Context, mailboxID store.MailboxID, principalID *store.PrincipalID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	for id, a := range s.phase2.mailboxACL {
		if aclMatch(a, mailboxID, principalID) {
			delete(s.phase2.mailboxACL, id)
			return nil
		}
	}
	return fmt.Errorf("mailbox acl %d: %w", mailboxID, store.ErrNotFound)
}

// -- JMAP states ------------------------------------------------------

func (m *metaFace) GetJMAPStates(ctx context.Context, pid store.PrincipalID) (store.JMAPStates, error) {
	if err := ctx.Err(); err != nil {
		return store.JMAPStates{}, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	st, ok := s.phase2.jmapStates[pid]
	if !ok {
		st = store.JMAPStates{PrincipalID: pid, UpdatedAt: s.clk.Now()}
		s.phase2.jmapStates[pid] = st
	}
	return st, nil
}

func (m *metaFace) IncrementJMAPState(ctx context.Context, pid store.PrincipalID, kind store.JMAPStateKind) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	st, ok := s.phase2.jmapStates[pid]
	if !ok {
		st = store.JMAPStates{PrincipalID: pid}
	}
	var ret int64
	switch kind {
	case store.JMAPStateKindMailbox:
		st.Mailbox++
		ret = st.Mailbox
	case store.JMAPStateKindEmail:
		st.Email++
		ret = st.Email
	case store.JMAPStateKindThread:
		st.Thread++
		ret = st.Thread
	case store.JMAPStateKindIdentity:
		st.Identity++
		ret = st.Identity
	case store.JMAPStateKindEmailSubmission:
		st.EmailSubmission++
		ret = st.EmailSubmission
	case store.JMAPStateKindVacationResponse:
		st.VacationResponse++
		ret = st.VacationResponse
	case store.JMAPStateKindSieve:
		st.Sieve++
		ret = st.Sieve
	default:
		return 0, fmt.Errorf("fakestore: unknown JMAPStateKind %d", kind)
	}
	st.UpdatedAt = s.clk.Now()
	s.phase2.jmapStates[pid] = st
	return ret, nil
}

// -- TLS-RPT ----------------------------------------------------------

func (m *metaFace) AppendTLSRPTFailure(ctx context.Context, f store.TLSRPTFailure) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	if f.RecordedAt.IsZero() {
		f.RecordedAt = s.clk.Now()
	}
	f.ID = s.phase2.nextTLSRPT
	s.phase2.nextTLSRPT++
	f.PolicyDomain = strings.ToLower(f.PolicyDomain)
	s.phase2.tlsrpt[f.ID] = f
	return nil
}

func (m *metaFace) ListTLSRPTFailures(ctx context.Context, policyDomain string, since, until time.Time) ([]store.TLSRPTFailure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	dom := strings.ToLower(policyDomain)
	var out []store.TLSRPTFailure
	for _, f := range s.phase2.tlsrpt {
		if f.PolicyDomain != dom {
			continue
		}
		if !since.IsZero() && f.RecordedAt.Before(since) {
			continue
		}
		if !until.IsZero() && !f.RecordedAt.Before(until) {
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RecordedAt.Equal(out[j].RecordedAt) {
			return out[i].RecordedAt.Before(out[j].RecordedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// -- JMAP EmailSubmission --------------------------------------------

func (m *metaFace) InsertEmailSubmission(ctx context.Context, row store.EmailSubmissionRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if row.ID == "" {
		return fmt.Errorf("fakestore: InsertEmailSubmission: empty id")
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	if row.CreatedAtUs == 0 {
		row.CreatedAtUs = now.UnixMicro()
	}
	if row.SendAtUs == 0 {
		row.SendAtUs = row.CreatedAtUs
	}
	if _, ok := s.phase2.emailSubmissions[row.ID]; ok {
		return fmt.Errorf("email submission %q: %w", row.ID, store.ErrConflict)
	}
	if len(row.Properties) > 0 {
		props := make([]byte, len(row.Properties))
		copy(props, row.Properties)
		row.Properties = props
	}
	s.phase2.emailSubmissions[row.ID] = row
	return nil
}

func (m *metaFace) GetEmailSubmission(ctx context.Context, id string) (store.EmailSubmissionRow, error) {
	if err := ctx.Err(); err != nil {
		return store.EmailSubmissionRow{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.EmailSubmissionRow{}, fmt.Errorf("email submission %q: %w", id, store.ErrNotFound)
	}
	r, ok := s.phase2.emailSubmissions[id]
	if !ok {
		return store.EmailSubmissionRow{}, fmt.Errorf("email submission %q: %w", id, store.ErrNotFound)
	}
	return r, nil
}

func (m *metaFace) ListEmailSubmissions(ctx context.Context, principal store.PrincipalID, filter store.EmailSubmissionFilter) ([]store.EmailSubmissionRow, error) {
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
	if s.phase2 == nil {
		return nil, nil
	}
	identitySet := make(map[string]struct{}, len(filter.IdentityIDs))
	for _, v := range filter.IdentityIDs {
		identitySet[v] = struct{}{}
	}
	emailSet := make(map[store.MessageID]struct{}, len(filter.EmailIDs))
	for _, v := range filter.EmailIDs {
		emailSet[v] = struct{}{}
	}
	threadSet := make(map[string]struct{}, len(filter.ThreadIDs))
	for _, v := range filter.ThreadIDs {
		threadSet[v] = struct{}{}
	}
	var out []store.EmailSubmissionRow
	for _, r := range s.phase2.emailSubmissions {
		if r.PrincipalID != principal {
			continue
		}
		if filter.AfterID != "" && r.ID <= filter.AfterID {
			continue
		}
		if len(identitySet) > 0 {
			if _, ok := identitySet[r.IdentityID]; !ok {
				continue
			}
		}
		if len(emailSet) > 0 {
			if _, ok := emailSet[r.EmailID]; !ok {
				continue
			}
		}
		if len(threadSet) > 0 {
			if _, ok := threadSet[r.ThreadID]; !ok {
				continue
			}
		}
		if filter.UndoStatus != "" && r.UndoStatus != filter.UndoStatus {
			continue
		}
		if filter.AfterUs != 0 && r.SendAtUs <= filter.AfterUs {
			continue
		}
		if filter.BeforeUs != 0 && r.SendAtUs >= filter.BeforeUs {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SendAtUs != out[j].SendAtUs {
			return out[i].SendAtUs < out[j].SendAtUs
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *metaFace) UpdateEmailSubmissionUndoStatus(ctx context.Context, id, undoStatus string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	r, ok := s.phase2.emailSubmissions[id]
	if !ok {
		return fmt.Errorf("email submission %q: %w", id, store.ErrNotFound)
	}
	r.UndoStatus = undoStatus
	s.phase2.emailSubmissions[id] = r
	return nil
}

func (m *metaFace) DeleteEmailSubmission(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	if _, ok := s.phase2.emailSubmissions[id]; !ok {
		return fmt.Errorf("email submission %q: %w", id, store.ErrNotFound)
	}
	delete(s.phase2.emailSubmissions, id)
	return nil
}

// -- JMAP Identity overlay -------------------------------------------

func cloneIdentity(r store.JMAPIdentity) store.JMAPIdentity {
	if len(r.ReplyToJSON) > 0 {
		v := make([]byte, len(r.ReplyToJSON))
		copy(v, r.ReplyToJSON)
		r.ReplyToJSON = v
	}
	if len(r.BccJSON) > 0 {
		v := make([]byte, len(r.BccJSON))
		copy(v, r.BccJSON)
		r.BccJSON = v
	}
	if r.Signature != nil {
		v := *r.Signature
		r.Signature = &v
	}
	return r
}

func (m *metaFace) InsertJMAPIdentity(ctx context.Context, row store.JMAPIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if row.ID == "" {
		return fmt.Errorf("fakestore: InsertJMAPIdentity: empty id")
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	if row.CreatedAtUs == 0 {
		row.CreatedAtUs = now.UnixMicro()
	}
	if row.UpdatedAtUs == 0 {
		row.UpdatedAtUs = row.CreatedAtUs
	}
	if _, ok := s.phase2.jmapIdentities[row.ID]; ok {
		return fmt.Errorf("jmap identity %q: %w", row.ID, store.ErrConflict)
	}
	s.phase2.jmapIdentities[row.ID] = cloneIdentity(row)
	return nil
}

func (m *metaFace) GetJMAPIdentity(ctx context.Context, id string) (store.JMAPIdentity, error) {
	if err := ctx.Err(); err != nil {
		return store.JMAPIdentity{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.JMAPIdentity{}, fmt.Errorf("jmap identity %q: %w", id, store.ErrNotFound)
	}
	r, ok := s.phase2.jmapIdentities[id]
	if !ok {
		return store.JMAPIdentity{}, fmt.Errorf("jmap identity %q: %w", id, store.ErrNotFound)
	}
	return cloneIdentity(r), nil
}

func (m *metaFace) ListJMAPIdentities(ctx context.Context, principal store.PrincipalID) ([]store.JMAPIdentity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.JMAPIdentity
	for _, r := range s.phase2.jmapIdentities {
		if r.PrincipalID == principal {
			out = append(out, cloneIdentity(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtUs != out[j].CreatedAtUs {
			return out[i].CreatedAtUs < out[j].CreatedAtUs
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *metaFace) UpdateJMAPIdentity(ctx context.Context, row store.JMAPIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	existing, ok := s.phase2.jmapIdentities[row.ID]
	if !ok {
		return fmt.Errorf("jmap identity %q: %w", row.ID, store.ErrNotFound)
	}
	existing.Name = row.Name
	existing.ReplyToJSON = append([]byte(nil), row.ReplyToJSON...)
	existing.BccJSON = append([]byte(nil), row.BccJSON...)
	existing.TextSignature = row.TextSignature
	existing.HTMLSignature = row.HTMLSignature
	if row.Signature != nil {
		v := *row.Signature
		existing.Signature = &v
	} else {
		existing.Signature = nil
	}
	existing.UpdatedAtUs = s.clk.Now().UnixMicro()
	s.phase2.jmapIdentities[row.ID] = existing
	return nil
}

func (m *metaFace) DeleteJMAPIdentity(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	if _, ok := s.phase2.jmapIdentities[id]; !ok {
		return fmt.Errorf("jmap identity %q: %w", id, store.ErrNotFound)
	}
	delete(s.phase2.jmapIdentities, id)
	return nil
}

// -- Categorisation (REQ-FILT-200..221) ------------------------------

// fakestoreDefaultCategorySet is the seeded category set
// (REQ-FILT-201/210). Kept here so the fakestore can mimic the
// production behaviour of seeding defaults on first read without
// depending on the categorise package.
var fakestoreDefaultCategorySet = []store.CategoryDef{
	{Name: "primary", Description: "Personal correspondence and important messages from people you know."},
	{Name: "social", Description: "Messages from social networks and dating sites."},
	{Name: "promotions", Description: "Marketing emails, offers, deals, newsletters."},
	{Name: "updates", Description: "Receipts, confirmations, statements, account notices."},
	{Name: "forums", Description: "Mailing-list digests, online community discussions."},
}

// fakestoreDefaultCategorisationPrompt is the seeded system prompt
// (REQ-FILT-211).
const fakestoreDefaultCategorisationPrompt = `You are an email-categorisation assistant. Given an email envelope and a short body excerpt, choose exactly one category from the supplied list whose description best fits the message, or return "none" if no category is a clear match. Respond ONLY with a single JSON object of the form {"category":"<name>"} where <name> is one of the listed category names or the literal "none". Do not include any other text.`

// GetCategorisationConfig returns the per-account categoriser
// configuration for pid; absent rows seed the documented defaults
// in-memory.
func (m *metaFace) GetCategorisationConfig(ctx context.Context, pid store.PrincipalID) (store.CategorisationConfig, error) {
	if err := ctx.Err(); err != nil {
		return store.CategorisationConfig{}, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	if s.phase2.catConfig == nil {
		s.phase2.catConfig = make(map[store.PrincipalID]store.CategorisationConfig)
	}
	if cfg, ok := s.phase2.catConfig[pid]; ok {
		return cloneCategorisationConfig(cfg), nil
	}
	cfg := store.CategorisationConfig{
		PrincipalID: pid,
		Prompt:      fakestoreDefaultCategorisationPrompt,
		CategorySet: append([]store.CategoryDef(nil), fakestoreDefaultCategorySet...),
		TimeoutSec:  5,
		Enabled:     true,
		UpdatedAtUs: s.clk.Now().UnixMicro(),
	}
	s.phase2.catConfig[pid] = cloneCategorisationConfig(cfg)
	return cfg, nil
}

// UpdateCategorisationConfig upserts the per-account categoriser row.
func (m *metaFace) UpdateCategorisationConfig(ctx context.Context, cfg store.CategorisationConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	if s.phase2.catConfig == nil {
		s.phase2.catConfig = make(map[store.PrincipalID]store.CategorisationConfig)
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 5
	}
	cfg.UpdatedAtUs = s.clk.Now().UnixMicro()
	s.phase2.catConfig[cfg.PrincipalID] = cloneCategorisationConfig(cfg)
	return nil
}

// cloneCategorisationConfig returns a deep copy so callers cannot
// mutate the in-memory row by holding the slice / pointer references
// the read returned.
func cloneCategorisationConfig(cfg store.CategorisationConfig) store.CategorisationConfig {
	out := cfg
	if cfg.CategorySet != nil {
		out.CategorySet = append([]store.CategoryDef(nil), cfg.CategorySet...)
	}
	if cfg.Endpoint != nil {
		v := *cfg.Endpoint
		out.Endpoint = &v
	}
	if cfg.Model != nil {
		v := *cfg.Model
		out.Model = &v
	}
	if cfg.APIKeyEnv != nil {
		v := *cfg.APIKeyEnv
		out.APIKeyEnv = &v
	}
	return out
}
