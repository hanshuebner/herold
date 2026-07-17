package emailsubmission

import (
	"context"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// seedRecipientsOnSend upserts a SeenAddress row for every unique
// recipient in the recipients slice, subject to the exclusions defined
// by REQ-MAIL-11e..m:
//
//   - Seeding is skipped entirely when the principal has
//     seen_addresses_enabled = false (REQ-SET-15).
//   - Recipients that match any of the principal's identity email addresses
//     are skipped (REQ-MAIL-11e).
//   - Recipients that already have a Contact row in any of the principal's
//     address books are skipped (REQ-MAIL-11l / auto-promotion principle).
//
// Display names are extracted from the message's To/Cc/Bcc headers on a
// best-effort basis via parseDisplayNames. When multiple headers carry
// conflicting names for the same address the last one wins.
//
// This method is called as a goroutine from processCreate; errors are
// silently discarded (best-effort per spec).
func (h *handlerSet) seedRecipientsOnSend(
	ctx context.Context,
	p store.Principal,
	msg store.Message,
	recipients []string,
) {
	if !p.SeenAddressesEnabled {
		return
	}
	if len(recipients) == 0 {
		return
	}

	// Build a set of the principal's identity email addresses so we
	// can skip them quickly.
	identityEmails, err := buildIdentityEmailSet(ctx, h.store.Meta(), p.ID, p.CanonicalEmail)
	if err != nil {
		return
	}

	// Build a name map from the message headers for display-name enrichment.
	names := parseDisplayNames(msg.Envelope.To)
	for k, v := range parseDisplayNames(msg.Envelope.Cc) {
		names[k] = v
	}
	for k, v := range parseDisplayNames(msg.Envelope.Bcc) {
		names[k] = v
	}

	// Deduplicate recipients (case-insensitive).
	seen := make(map[string]struct{}, len(recipients))
	for _, addr := range recipients {
		lower := strings.ToLower(addr)
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}

		// Skip identity addresses.
		if _, isIdentity := identityEmails[lower]; isIdentity {
			continue
		}

		// Skip addresses that already have a Contact row.
		hasContact, cerr := addressHasContact(ctx, h.store.Meta(), p.ID, lower)
		if cerr != nil || hasContact {
			continue
		}

		displayName := names[lower]
		_, _, _ = h.store.Meta().UpsertSeenAddress(ctx, p.ID, lower, displayName, 1, 0)
	}
}

// buildIdentityEmailSet returns a set of lower-cased email addresses that
// are identity addresses for pid. It includes:
//   - the principal's canonical email (the synthesised default identity).
//   - every stored JMAPIdentity email.
func buildIdentityEmailSet(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	canonicalEmail string,
) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	out[strings.ToLower(canonicalEmail)] = struct{}{}
	identities, err := meta.ListJMAPIdentities(ctx, pid)
	if err != nil {
		return nil, err
	}
	for _, id := range identities {
		if id.Email != "" {
			out[strings.ToLower(id.Email)] = struct{}{}
		}
	}
	return out, nil
}

// addressHasContact returns true when the principal has at least one
// Contact with a matching email entry.
func addressHasContact(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	email string,
) (bool, error) {
	contacts, err := meta.ListContacts(ctx, store.ContactFilter{
		PrincipalID: &pid,
		HasEmail:    &email,
		Limit:       1,
	})
	if err != nil {
		return false, err
	}
	return len(contacts) > 0, nil
}

// parseDisplayNames extracts a map of lower-cased addr-spec -> display
// name from a comma-separated header value. The format accepted is the
// common RFC 5322 form: "Display Name <addr@example>" or plain
// "addr@example". When no display name is present, the value is "".
//
// Names are RFC 2047 decoded (issue #16): the outbound message's
// To/Cc/Bcc headers Q-encode any non-ASCII display name, and the raw
// `=?utf-8?q?...?=` form would otherwise be stored as the SeenAddress
// name and appear in the autocomplete dropdown instead of the
// human-readable rendering.
func parseDisplayNames(header string) map[string]string {
	out := make(map[string]string)
	if header == "" {
		return out
	}
	for _, part := range splitOnComma(header) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		l := strings.Index(part, "<")
		r := strings.LastIndex(part, ">")
		if l >= 0 && r > l {
			addr := strings.TrimSpace(part[l+1 : r])
			if addr == "" {
				continue
			}
			name := strings.TrimSpace(part[:l])
			// Strip surrounding quotes from display name.
			if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
				name = name[1 : len(name)-1]
			}
			out[strings.ToLower(addr)] = decodeDisplayName(name)
		} else if strings.Contains(part, "@") {
			out[strings.ToLower(part)] = ""
		}
	}
	return out
}

// decodeDisplayName runs name through the shared mailparse.DecodeHeader so
// an RFC 2047 Q-encoded or B-encoded display name like
// `=?utf-8?q?Hans_H=C3=BCbner?=` is normalised to its UTF-8 form ("Hans
// Hübner") before being stored as a SeenAddress / contact name, using the
// same htmlindex-backed charset registry the body decoder uses (re #257,
// re #259) rather than a bare mime.WordDecoder that only special-cases
// utf-8/iso-8859-1/us-ascii. Returns name unchanged when no encoded-word is
// present; degrades to a best-effort decode (never blank, never the raw
// encoded-word marker) for a charset unknown even to htmlindex.
func decodeDisplayName(name string) string {
	if name == "" || !strings.Contains(name, "=?") {
		return name
	}
	return mailparse.DecodeHeader(name)
}
