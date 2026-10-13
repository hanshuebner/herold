package protojmap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// sessionDescriptor is the JSON body returned by GET /.well-known/jmap
// (RFC 8620 §2). Field naming follows the spec verbatim; the Go field
// names are exported but the json tags pin the wire form.
type sessionDescriptor struct {
	Capabilities    map[CapabilityID]any `json:"capabilities"`
	Accounts        map[Id]accountDesc   `json:"accounts"`
	PrimaryAccounts map[CapabilityID]Id  `json:"primaryAccounts"`
	Username        string               `json:"username"`
	APIURL          string               `json:"apiUrl"`
	DownloadURL     string               `json:"downloadUrl"`
	UploadURL       string               `json:"uploadUrl"`
	EventSourceURL  string               `json:"eventSourceUrl"`
	State           string               `json:"state"`
}

// accountDesc is one entry in the session.accounts object.
type accountDesc struct {
	Name                string               `json:"name"`
	IsPersonal          bool                 `json:"isPersonal"`
	IsReadOnly          bool                 `json:"isReadOnly"`
	AccountCapabilities map[CapabilityID]any `json:"accountCapabilities"`
}

// handleSession serves GET /.well-known/jmap for the authenticated
// principal. The descriptor advertises every capability the registry
// knows about plus one account corresponding to the requesting
// principal.
//
// Wave 2.2 v1 deployments map one principal to one account; the
// "isPersonal" flag is true and the account id is the principal's
// stringified ID. Future shared-mailbox / impersonation support adds
// more accounts here.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		WriteJMAPError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	desc := s.buildSessionDescriptor(r.Context(), r, p)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(desc); err != nil {
		s.log.Warn("session.encode_failed", "err", err)
	}
}

// buildSessionDescriptor assembles the body. Split out so unit tests
// can inspect it directly without going through HTTP. It also refreshes
// the cached session-state digests from what it read, so the state it
// returns and the sessionState of later POST /jmap responses agree.
func (s *Server) buildSessionDescriptor(ctx context.Context, r *http.Request, p store.Principal) sessionDescriptor {
	base := s.baseURL(r)
	acctKey := s.accountsCacheKey()
	view, complete := s.buildAccountsView(ctx, p)
	acctDigest := s.stateCache.putAccounts(p.ID, acctKey, view, complete && s.epochs != nil)
	sessID := SessionIDFromContext(ctx)
	clogEpoch := s.epochs.Sessions()
	clog, clogUntil, clogComplete := s.loadClientLogCapability(ctx, sessID)
	clogDigest := s.stateCache.putClientlog(sessID, clogEpoch, clog, clogUntil, clogComplete && s.epochs != nil)
	caps := view.Capabilities
	// Inject the clientlog capability unconditionally so the SPA always
	// has a place to read telemetry_enabled and livetail_until.
	caps[CapabilityClientLog] = clog
	// Inject the per-principal extimg-internalize-backlog state
	// (REQ-EXTIMG-BG-21). The SPA reads this on bootstrap to decide
	// whether to render the "Images are being processed" notice.
	// Failures fall through silently — the SPA degrades to "no notice"
	// rather than refusing to bootstrap on a transient store hiccup.
	// The value is a point-in-time status the SPA refreshes on its own
	// InternalizeStatus push type (REQ-EXTIMG-BG-INTERNAL-30), so the
	// session state leaves it out.
	caps[CapabilityInternalizeStatus] = s.buildInternalizeStatusCapability(ctx, p)
	return sessionDescriptor{
		Capabilities:    caps,
		Accounts:        view.Accounts,
		PrimaryAccounts: view.PrimaryAccounts,
		Username:        p.CanonicalEmail,
		APIURL:          base + "/jmap",
		DownloadURL:     base + "/jmap/download/{accountId}/{blobId}/{type}/{name}",
		UploadURL:       base + "/jmap/upload/{accountId}",
		EventSourceURL:  base + "/jmap/eventsource?types={types}&closeafter={closeafter}&ping={ping}",
		State:           combineSessionState(acctDigest, clogDigest, p.CanonicalEmail, base),
	}
}

// baseURL is the origin the session descriptor's URLs are built on:
// Options.BaseURL when set, else the request's scheme and Host.
func (s *Server) baseURL(r *http.Request) string {
	base := s.opts.BaseURL
	if base == "" && r != nil {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return strings.TrimRight(base, "/")
}

// accountsView is the part of the session descriptor read from the
// principal's store rows and the capability registry: capabilities,
// accounts and primaryAccounts, before the clientlog and
// internalize-status capabilities are added.
type accountsView struct {
	Capabilities    map[CapabilityID]any `json:"capabilities"`
	Accounts        map[Id]accountDesc   `json:"accounts"`
	PrimaryAccounts map[CapabilityID]Id  `json:"primaryAccounts"`
}

// buildAccountsView assembles the accountsView for p. complete is false
// when a store read failed and the view omits what that read would have
// contributed; such a view is served but not cached.
func (s *Server) buildAccountsView(ctx context.Context, p store.Principal) (accountsView, bool) {
	complete := true
	accountID := AccountIDForPrincipal(p.ID)
	accountCaps := s.reg.AccountCapabilities()
	caps := s.reg.Capabilities()
	// Per-principal capabilities (REQ-APP-50): advertised, with their
	// account-level value, only to principals the provider offers them
	// to.
	for cap, prov := range s.reg.principalCapabilityProviders() {
		value, ok, err := prov.PrincipalCapability(ctx, p)
		if err != nil {
			loggerFromContext(ctx, s.log).Warn("session.principal_capability_failed",
				"activity", observe.ActivityInternal, "capability", string(cap), "err", err)
			complete = false
		}
		if err != nil || !ok {
			delete(caps, cap)
			delete(accountCaps, cap)
			continue
		}
		caps[cap] = struct{}{}
		accountCaps[cap] = value
	}
	primary := make(map[CapabilityID]Id, len(accountCaps))
	for cap := range accountCaps {
		primary[cap] = accountID
	}
	accounts := map[Id]accountDesc{
		accountID: {
			Name:                p.CanonicalEmail,
			IsPersonal:          true,
			IsReadOnly:          false,
			AccountCapabilities: accountCaps,
		},
	}
	// REQ-PROTO-33: surface every foreign account the caller can reach
	// via mailbox ACL as a secondary entry in `accounts`. RFC 8620 §2:
	// `accounts` carries every account visible to the user; primary/
	// secondary is encoded by membership (or absence) in
	// `primaryAccounts`. Failures fall through silently — a transient
	// store hiccup must not 500 session bootstrap. Per-mailbox ACL is
	// re-checked by the mail/* handlers; this surface is purely the
	// discovery hint.
	if !s.appendSecondaryAccounts(ctx, accounts, accountCaps, p.ID) {
		complete = false
	}
	// REQ-SUBACCT-03: surface every sub-account owned by the caller as
	// its own accounts[] entry. Unlike appendSecondaryAccounts (ACL
	// discovery), sub-accounts are unconditionally the caller's own --
	// no ACL grant is involved.
	if !s.appendSubAccounts(ctx, accounts, accountCaps, p.ID) {
		complete = false
	}
	return accountsView{Capabilities: caps, Accounts: accounts, PrimaryAccounts: primary}, complete
}

// appendSecondaryAccounts walks the caller's accessible-mailbox set,
// groups by owner principal, and emits one accounts[] entry per
// foreign owner (REQ-PROTO-33). IsReadOnly is true when no mailbox in
// the foreign account grants any write-capable bit (i, w, k, s, t, e).
// Anyone-rows count toward the rights mask alongside direct rows. It
// reports false when a store read failed and an account may be missing
// or mis-flagged.
func (s *Server) appendSecondaryAccounts(
	ctx context.Context,
	accounts map[Id]accountDesc,
	accountCaps map[CapabilityID]any,
	callerPID store.PrincipalID,
) bool {
	shared, err := s.store.Meta().ListMailboxesAccessibleBy(ctx, callerPID)
	if err != nil {
		return false
	}
	complete := true
	ownerMailboxes := make(map[store.PrincipalID][]store.Mailbox)
	for _, mb := range shared {
		if mb.PrincipalID == callerPID {
			continue
		}
		ownerMailboxes[mb.PrincipalID] = append(ownerMailboxes[mb.PrincipalID], mb)
	}
	const writeRights = store.ACLRightInsert | store.ACLRightWrite |
		store.ACLRightCreateMailbox | store.ACLRightSeen |
		store.ACLRightDeleteMessage | store.ACLRightExpunge
	for ownerPID, mboxes := range ownerMailboxes {
		owner, oerr := s.store.Meta().GetPrincipalByID(ctx, ownerPID)
		if oerr != nil {
			complete = false
			continue
		}
		readOnly := true
		for _, mb := range mboxes {
			rows, aerr := s.store.Meta().GetMailboxACL(ctx, mb.ID)
			if aerr != nil {
				complete = false
				continue
			}
			for _, row := range rows {
				if row.PrincipalID != nil && *row.PrincipalID != callerPID {
					continue
				}
				if row.Rights&writeRights != 0 {
					readOnly = false
					break
				}
			}
			if !readOnly {
				break
			}
		}
		ownerAccountID := AccountIDForPrincipal(ownerPID)
		if _, dup := accounts[ownerAccountID]; dup {
			continue
		}
		accounts[ownerAccountID] = accountDesc{
			Name:                owner.CanonicalEmail,
			IsPersonal:          false,
			IsReadOnly:          readOnly,
			AccountCapabilities: accountCaps,
		}
	}
	return complete
}

// appendSubAccounts adds one accounts[] entry per sub-principal owned
// by callerPID (REQ-SUBACCT-01/03). A sub-account genuinely belongs to
// the authenticated principal -- unlike an ACL-shared foreign account
// surfaced by appendSecondaryAccounts -- so isPersonal is true.
// isReadOnly is always false: the parent reaches every one of its own
// sub-accounts with full owner rights (protojmap.HasOwnerAccess is the
// seam every mail/* handler routes through). Failures fall through
// for the same reason appendSecondaryAccounts does: a transient store
// hiccup must not 500 session bootstrap; it reports false then.
func (s *Server) appendSubAccounts(
	ctx context.Context,
	accounts map[Id]accountDesc,
	accountCaps map[CapabilityID]any,
	callerPID store.PrincipalID,
) bool {
	subs, err := s.store.Meta().ListSubPrincipals(ctx, callerPID)
	if err != nil {
		return false
	}
	for _, sub := range subs {
		id := AccountIDForPrincipal(sub.ID)
		if _, dup := accounts[id]; dup {
			continue
		}
		accounts[id] = accountDesc{
			Name:                sub.CanonicalEmail,
			IsPersonal:          true,
			IsReadOnly:          false,
			AccountCapabilities: accountCaps,
		}
	}
	return true
}

// internalizeStatusDesc is the typed capability value carrying the
// per-principal extimg-internalize backlog state (REQ-EXTIMG-BG-21).
// Lower-case JSON tags so the SPA can decode without renaming.
type internalizeStatusDesc struct {
	PendingMessages uint64 `json:"pending_messages"`
	AsOf            string `json:"as_of"`
}

// buildInternalizeStatusCapability returns the typed capability value
// for the requesting principal. A failed count surfaces as
// pending_messages = 0 so the SPA renders no notice — the steady-
// state shape — rather than blocking session bootstrap on a store
// transient.
func (s *Server) buildInternalizeStatusCapability(ctx context.Context, p store.Principal) internalizeStatusDesc {
	desc := internalizeStatusDesc{
		AsOf: s.clk.Now().UTC().Format(time.RFC3339),
	}
	n, err := s.store.Meta().CountInternalizePending(ctx, p.ID)
	if err == nil {
		desc.PendingMessages = n
	}
	return desc
}

// AccountIDForPrincipal maps a store PrincipalID to the JMAP account
// id used in URLs and the session descriptor. We use the stringified
// numeric id; opaque to clients but stable across requests for the
// same principal.
func AccountIDForPrincipal(pid store.PrincipalID) Id {
	return fmt.Sprintf("a%d", pid)
}

// principalIDFromAccountID inverts AccountIDForPrincipal. Returns
// (0, false) on a malformed account id.
func principalIDFromAccountID(id Id) (store.PrincipalID, bool) {
	if len(id) < 2 || id[0] != 'a' {
		return 0, false
	}
	var n uint64
	for _, c := range id[1:] {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return store.PrincipalID(n), true
}

// coreCapability is the JSON-marshalable body of the JMAP Core
// capability descriptor. RFC 8620 §2 enumerates the fields verbatim.
type coreCapability struct {
	MaxSizeUpload         int64    `json:"maxSizeUpload"`
	MaxConcurrentUpload   int      `json:"maxConcurrentUpload"`
	MaxSizeRequest        int64    `json:"maxSizeRequest"`
	MaxConcurrentRequests int      `json:"maxConcurrentRequests"`
	MaxCallsInRequest     int      `json:"maxCallsInRequest"`
	MaxObjectsInGet       int      `json:"maxObjectsInGet"`
	MaxObjectsInSet       int      `json:"maxObjectsInSet"`
	CollationAlgorithms   []string `json:"collationAlgorithms"`
}

func coreCapabilityDescriptor(opts Options) coreCapability {
	return coreCapability{
		MaxSizeUpload:         opts.MaxSizeUpload,
		MaxConcurrentUpload:   4,
		MaxSizeRequest:        opts.MaxSizeRequest,
		MaxConcurrentRequests: opts.MaxConcurrentRequests,
		MaxCallsInRequest:     opts.MaxCallsInRequest,
		MaxObjectsInGet:       opts.MaxObjectsInGet,
		MaxObjectsInSet:       opts.MaxObjectsInSet,
		// RFC 8620 §6.1: i;ascii-numeric and i;ascii-casemap are the
		// minimum-mandatory collations; we advertise the standard pair
		// here. JMAP Mail adds i;unicode-casemap when registered.
		CollationAlgorithms: []string{"i;ascii-numeric", "i;ascii-casemap"},
	}
}
