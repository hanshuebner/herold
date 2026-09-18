package spam

// own_addresses.go — the single store-backed resolver every
// classification path (SMTP delivery, IMAP import, reclassify,
// apply-verdicts) calls to populate Request.OwnAddresses (re #386), so
// none of the four re-implements the alias/Identity/import-account
// lookup itself.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// ResolveOwnAddresses returns the lower-cased, deduplicated, sorted set
// of addresses that belong to principal pid: its canonical email, every
// non-expired alias that routes to it, the primary address of each of
// its verified Identities together with that Identity's alias addresses
// (re #387), and the addresses of its configured IMAP-import accounts:
// each account's owning Identity address (decision 10), its
// operator-configured OwnAddresses, and its learned LearnedAddresses
// (re #396, third round -- see internal/imapimport/ownaddresses.go). An
// Identity's own alias addresses are included only when the Identity
// itself is verified: an unverified Identity carries no confirmation
// the principal actually controls the address, so neither its primary
// nor its aliases belong in the set. IMAP-import account addresses are
// included unconditionally -- reaching that inbox at all already
// demonstrates the principal receives mail there.
//
// cache, when non-nil, memoizes the result per principal so a caller
// that classifies many messages for the same principal in a loop (spam
// reclassify, apply-verdicts, an IMAP-import sync run) reads the store
// once per principal for the whole batch rather than once per message.
// Pass nil for a one-off lookup (SMTP delivery, which classifies one
// message at a time).
func ResolveOwnAddresses(ctx context.Context, meta store.Metadata, pid store.PrincipalID, cache map[store.PrincipalID][]string) ([]string, error) {
	if pid == 0 {
		return nil, nil
	}
	if cache != nil {
		if v, ok := cache[pid]; ok {
			return v, nil
		}
	}
	addrs, err := resolveOwnAddresses(ctx, meta, pid)
	if err != nil {
		return nil, err
	}
	if cache != nil {
		cache[pid] = addrs
	}
	return addrs, nil
}

func resolveOwnAddresses(ctx context.Context, meta store.Metadata, pid store.PrincipalID) ([]string, error) {
	set := make(map[string]struct{})
	add := func(addr string) {
		addr = strings.ToLower(strings.TrimSpace(addr))
		if addr != "" {
			set[addr] = struct{}{}
		}
	}

	principal, err := meta.GetPrincipalByID(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("spam: resolve own addresses: get principal %d: %w", pid, err)
	}
	add(principal.CanonicalEmail)

	aliases, err := meta.ListAliases(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("spam: resolve own addresses: list aliases: %w", err)
	}
	now := time.Now()
	for _, a := range aliases {
		if a.TargetPrincipal != pid {
			continue
		}
		if a.ExpiresAt != nil && a.ExpiresAt.Before(now) {
			continue
		}
		add(a.LocalPart + "@" + a.Domain)
	}

	identities, err := meta.ListJMAPIdentities(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("spam: resolve own addresses: list identities for principal %d: %w", pid, err)
	}
	for _, idn := range identities {
		if idn.VerifiedAtUs == 0 {
			continue
		}
		add(idn.Email)
		for _, alias := range idn.Aliases {
			add(alias)
		}
	}

	accounts, err := meta.ListIMAPImportAccountsByPrincipal(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("spam: resolve own addresses: list imap import accounts for principal %d: %w", pid, err)
	}
	for _, acc := range accounts {
		if acc.IdentityID != "" {
			if idn, ierr := meta.GetJMAPIdentity(ctx, acc.IdentityID); ierr == nil {
				add(idn.Email)
			} else {
				add(acc.Username)
			}
		} else {
			// Legacy rows that predate decision 10's mandatory per-identity
			// scope carry no IdentityID; Username is the best-effort address
			// for those (it is the upstream login, often the address itself).
			add(acc.Username)
		}
		// OwnAddresses (operator-configured) and LearnedAddresses
		// (learned from Delivered-To/X-Original-To headers of messages
		// the upstream already accepted into this account, re #396,
		// third round) cover addresses the upstream mailbox receives at
		// that herold cannot derive from the owning Identity alone -- a
		// shared organisational mailbox's info@/vorstand@ aliases, for
		// example. See internal/imapimport/ownaddresses.go for how
		// LearnedAddresses is populated.
		for _, a := range acc.OwnAddresses {
			add(a)
		}
		for _, a := range acc.LearnedAddresses {
			add(a)
		}
	}

	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out, nil
}
