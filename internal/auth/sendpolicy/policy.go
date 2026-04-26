// Package sendpolicy provides the REQ-SEND-12 / REQ-FLOW-41 from-address
// ownership predicate shared by all three send surfaces:
//
//   - inbound SMTP submission listener (internal/protosmtp)
//   - HTTP send API (internal/protosend)
//   - JMAP EmailSubmission/set (internal/protojmap/mail/emailsubmission)
//
// The predicate is stateless given its inputs; callers provide the resolved
// store.Principal and the (optional) store.APIKey that authenticated the
// request.  The package has no circular-import risk: it imports only
// internal/store and the standard library.
package sendpolicy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// Source labels the send surface for audit-log and metric purposes.
// The vocabulary is closed so metric label cardinality stays bounded.
type Source string

const (
	// SourceSMTP is the SMTP submission listener path.
	SourceSMTP Source = "smtp"
	// SourceHTTP is the HTTP send API path (POST /api/v1/mail/send*).
	SourceHTTP Source = "http"
	// SourceJMAP is the JMAP EmailSubmission/set path.
	SourceJMAP Source = "jmap"
)

// Reason classifies a denial for structured logging and metrics.
type Reason string

const (
	// ReasonNotOwned means the from address belongs to no local address
	// owned by the principal (not canonical, not any alias).
	ReasonNotOwned Reason = "not_owned"
	// ReasonKeyAddressConstraint means the API key's allowed_from_addresses
	// list was set and the from address is not in it.
	ReasonKeyAddressConstraint Reason = "key_address_constraint"
	// ReasonKeyDomainConstraint means the API key's allowed_from_domains
	// list was set and the from domain is not in it.
	ReasonKeyDomainConstraint Reason = "key_domain_constraint"
)

// Decision is the result of CheckFrom.
type Decision struct {
	// Allowed reports whether the from address may be used.
	Allowed bool
	// Reason is set when Allowed == false.
	Reason Reason
}

// Checker resolves the store queries required by CheckFrom. The narrow
// interface enables a simple stub in tests.
type Checker interface {
	// PrincipalOwnsAddress reports whether addr (lowercased addr-spec)
	// is the principal's canonical address or resolves via an alias to
	// that principal.  Callers must lower-case addr before calling.
	PrincipalOwnsAddress(ctx context.Context, p store.Principal, addr string) (bool, error)
}

// StoreChecker implements Checker against a real store.Metadata.
type StoreChecker struct {
	Meta store.Metadata
}

// PrincipalOwnsAddress checks the canonical email first, then the alias
// table.  Both lookups are case-insensitive (addr must already be lower-
// cased by the caller).
func (c StoreChecker) PrincipalOwnsAddress(ctx context.Context, p store.Principal, addr string) (bool, error) {
	if strings.EqualFold(p.CanonicalEmail, addr) {
		return true, nil
	}
	local, domain, ok := splitAddr(addr)
	if !ok {
		return false, nil
	}
	pid, err := c.Meta.ResolveAlias(ctx, local, domain)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("sendpolicy: alias lookup: %w", err)
	}
	return pid == p.ID, nil
}

// CheckFrom enforces REQ-SEND-12 / REQ-FLOW-41.
//
// Rules (applied in order; first matching rule decides):
//
//  1. Admin principals (PrincipalFlagAdmin) are always allowed.
//  2. The from address must resolve to the principal via its canonical
//     email or an alias (ReasonNotOwned on failure).
//  3. If an APIKey is provided and its AllowedFromAddresses list is
//     non-empty, the address must be in that list
//     (ReasonKeyAddressConstraint on failure).
//  4. If an APIKey is provided and its AllowedFromDomains list is
//     non-empty, the from domain must be in that list
//     (ReasonKeyDomainConstraint on failure).
//
// addr must be a bare addr-spec (no display name, no angle brackets),
// already lower-cased by the caller.  key may be nil when the session
// was authenticated by password / SASL rather than a Bearer API key.
//
// An error from the underlying store is returned as-is with Allowed==false
// and Reason=="not_owned" so callers can distinguish a policy denial from
// a transient backend error.
func CheckFrom(ctx context.Context, chk Checker, p store.Principal, key *store.APIKey, addr string) (Decision, error) {
	// Rule 1: admins may send as anything local.
	if p.Flags.Has(store.PrincipalFlagAdmin) {
		return Decision{Allowed: true}, nil
	}

	// Rule 2: principal must own the address.
	owns, err := chk.PrincipalOwnsAddress(ctx, p, addr)
	if err != nil {
		return Decision{Allowed: false, Reason: ReasonNotOwned}, err
	}
	if !owns {
		return Decision{Allowed: false, Reason: ReasonNotOwned}, nil
	}

	if key == nil {
		return Decision{Allowed: true}, nil
	}

	// Rule 3: per-key address allowlist.
	if len(key.AllowedFromAddresses) > 0 {
		found := false
		for _, a := range key.AllowedFromAddresses {
			if strings.EqualFold(a, addr) {
				found = true
				break
			}
		}
		if !found {
			return Decision{Allowed: false, Reason: ReasonKeyAddressConstraint}, nil
		}
	}

	// Rule 4: per-key domain allowlist.
	if len(key.AllowedFromDomains) > 0 {
		_, domain, _ := splitAddr(addr)
		found := false
		for _, d := range key.AllowedFromDomains {
			if strings.EqualFold(d, domain) {
				found = true
				break
			}
		}
		if !found {
			return Decision{Allowed: false, Reason: ReasonKeyDomainConstraint}, nil
		}
	}

	return Decision{Allowed: true}, nil
}

// splitAddr splits "local@domain" into its two parts.  Returns ok==false
// when no '@' is present or when either part is empty.
func splitAddr(addr string) (local, domain string, ok bool) {
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 {
		return "", "", false
	}
	return addr[:at], addr[at+1:], true
}

// isNotFound returns true when err wraps store.ErrNotFound.
func isNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound)
}
