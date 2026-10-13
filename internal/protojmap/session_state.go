package protojmap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// The session `state` (RFC 8620 §2) is a digest of the Session object's
// content: "If the value of any other property on the Session object
// changes, this string will change." Clients compare it with the
// `sessionState` of each method response (RFC 8620 §3.4) to learn when
// to refetch the descriptor (REQ-PROTO-43). Per-account data-type state
// strings are separate (RFC 8620 §5.1) and reach clients through
// method responses and push StateChange events, so mail activity
// leaves the session state unchanged.
//
// The digest covers:
//
//   - accountsView: capabilities with their descriptors, the account set
//     with each account's name, isPersonal, isReadOnly and
//     accountCapabilities (the apps actions, REQ-APP-50, among them), and
//     primaryAccounts. ACL-visible foreign accounts (REQ-PROTO-33) and
//     sub-accounts (REQ-SUBACCT-03) are part of the account set.
//   - the clientlog capability of the request's session (REQ-OPS-211).
//   - username and the base URL the descriptor URLs are built on.
//
// The internalize-status capability is left out: it is a point-in-time
// backlog count with an as_of timestamp that the Suite refreshes on its
// InternalizeStatus push type (REQ-EXTIMG-BG-INTERNAL-30).
//
// POST /jmap reports the state on every response, so the store-derived
// parts are cached: accountsView per principal, keyed on the store's
// DescriptorEpochs.Accounts counter and the registry generation; the
// clientlog capability per session, keyed on DescriptorEpochs.Sessions
// and valid until a livetail expiry it carries. A store without
// DescriptorEpochs gets no caching.

// maxSessionStateEntries bounds each cache map; a full map is emptied
// before the next insert.
const maxSessionStateEntries = 10000

// accountsCacheKey identifies the inputs an accountsView digest was
// computed from.
type accountsCacheKey struct {
	epoch uint64
	reg   uint64
}

type accountsCacheEntry struct {
	key    accountsCacheKey
	digest string
}

type clientlogCacheEntry struct {
	epoch      uint64
	validUntil time.Time
	digest     string
}

// sessionStateCache holds the per-principal and per-session digests the
// session state is combined from. Safe for concurrent use.
type sessionStateCache struct {
	mu        sync.Mutex
	accounts  map[store.PrincipalID]accountsCacheEntry
	clientlog map[string]clientlogCacheEntry
}

func newSessionStateCache() *sessionStateCache {
	return &sessionStateCache{
		accounts:  make(map[store.PrincipalID]accountsCacheEntry),
		clientlog: make(map[string]clientlogCacheEntry),
	}
}

func (c *sessionStateCache) lookupAccounts(pid store.PrincipalID, key accountsCacheKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.accounts[pid]
	if !ok || e.key != key {
		return "", false
	}
	return e.digest, true
}

// putAccounts digests view and, when cacheable, records it for pid under
// key. key must have been read before view was built, so a write that
// lands while the view is being built leaves the entry stale.
func (c *sessionStateCache) putAccounts(pid store.PrincipalID, key accountsCacheKey, view accountsView, cacheable bool) string {
	digest := digestJSON(view)
	if !cacheable {
		return digest
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.accounts[pid]; !ok && len(c.accounts) >= maxSessionStateEntries {
		clear(c.accounts)
	}
	c.accounts[pid] = accountsCacheEntry{key: key, digest: digest}
	return digest
}

func (c *sessionStateCache) lookupClientlog(sessID string, epoch uint64, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.clientlog[sessID]
	if !ok || e.epoch != epoch {
		return "", false
	}
	if !e.validUntil.IsZero() && !now.Before(e.validUntil) {
		return "", false
	}
	return e.digest, true
}

// putClientlog digests capability and, when cacheable, records it for
// sessID under epoch until validUntil (zero: no expiry). epoch must have
// been read before the session row was.
func (c *sessionStateCache) putClientlog(sessID string, epoch uint64, capability clientLogCapability, validUntil time.Time, cacheable bool) string {
	digest := digestJSON(capability)
	if !cacheable || sessID == "" {
		return digest
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.clientlog[sessID]; !ok && len(c.clientlog) >= maxSessionStateEntries {
		clear(c.clientlog)
	}
	c.clientlog[sessID] = clientlogCacheEntry{epoch: epoch, validUntil: validUntil, digest: digest}
	return digest
}

// digestJSON hashes the JSON encoding of v. encoding/json writes map
// keys in sorted order, so equal values give equal digests.
func digestJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "unencodable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// combineSessionState folds the parts of the Session object into the
// state string.
func combineSessionState(accountsDigest, clientlogDigest, username, base string) string {
	h := sha256.New()
	fmt.Fprintf(h, "accounts=%s;clientlog=%s;username=%q;base=%q;",
		accountsDigest, clientlogDigest, username, base)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// accountsCacheKey returns the current key for accountsView digests.
func (s *Server) accountsCacheKey() accountsCacheKey {
	return accountsCacheKey{epoch: s.epochs.Accounts(), reg: s.reg.Generation()}
}

// sessionState returns the state string of the Session object the
// request's principal and session would be served (RFC 8620 §2, §3.4).
// It equals the `state` of a GET /.well-known/jmap made with the same
// credentials and Host.
func (s *Server) sessionState(ctx context.Context, r *http.Request) string {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return "anonymous"
	}
	cacheable := s.epochs != nil
	key := s.accountsCacheKey()
	acctDigest, hit := s.stateCache.lookupAccounts(p.ID, key)
	if !hit {
		view, complete := s.buildAccountsView(ctx, p)
		acctDigest = s.stateCache.putAccounts(p.ID, key, view, complete && cacheable)
	}
	sessID := SessionIDFromContext(ctx)
	epoch := s.epochs.Sessions()
	clogDigest, hit := s.stateCache.lookupClientlog(sessID, epoch, s.clk.Now())
	if !hit {
		capability, validUntil, complete := s.loadClientLogCapability(ctx, sessID)
		clogDigest = s.stateCache.putClientlog(sessID, epoch, capability, validUntil, complete && cacheable)
	}
	return combineSessionState(acctDigest, clogDigest, p.CanonicalEmail, s.baseURL(r))
}
