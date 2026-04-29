package protojmap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

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
// can inspect it directly without going through HTTP.
func (s *Server) buildSessionDescriptor(ctx context.Context, r *http.Request, p store.Principal) sessionDescriptor {
	base := s.opts.BaseURL
	if base == "" && r != nil {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	base = strings.TrimRight(base, "/")
	accountID := AccountIDForPrincipal(p.ID)
	accountCaps := s.reg.AccountCapabilities()
	primary := make(map[CapabilityID]Id, len(accountCaps))
	for cap := range accountCaps {
		primary[cap] = accountID
	}
	desc := sessionDescriptor{
		Capabilities: s.reg.Capabilities(),
		Accounts: map[Id]accountDesc{
			accountID: {
				Name:                p.CanonicalEmail,
				IsPersonal:          true,
				IsReadOnly:          false,
				AccountCapabilities: accountCaps,
			},
		},
		PrimaryAccounts: primary,
		Username:        p.CanonicalEmail,
		APIURL:          base + "/jmap",
		DownloadURL:     base + "/jmap/download/{accountId}/{blobId}/{type}/{name}",
		UploadURL:       base + "/jmap/upload/{accountId}",
		EventSourceURL:  base + "/jmap/eventsource?types={types}&closeafter={closeafter}&ping={ping}",
		State:           s.sessionState(ctx),
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

// sessionState returns the session-state hash advertised in the
// session descriptor and on every POST /jmap response. RFC 8620 §2:
// "this is an opaque string ... when the value of any of the per-
// account state strings change ... the value of this string MUST also
// change". We compute it as a hash of the per-principal JMAPStates row
// plus the registered capability list so a capability hot-reload
// changes the hash too.
func (s *Server) sessionState(ctx context.Context) string {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return "anonymous"
	}
	st, err := s.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		// On read failure return a deterministic placeholder so the
		// response is still well-formed; a transient store hiccup must
		// not 500 the whole envelope.
		return "unknown"
	}
	caps := s.reg.SortedCapabilityIDs()
	h := sha256.New()
	fmt.Fprintf(h, "pid=%d;mb=%d;em=%d;th=%d;id=%d;es=%d;vr=%d;",
		st.PrincipalID, st.Mailbox, st.Email, st.Thread, st.Identity,
		st.EmailSubmission, st.VacationResponse)
	for _, c := range caps {
		fmt.Fprintf(h, "c=%s;", c)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
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
