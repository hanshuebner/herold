package protosmtp

import (
	"context"
	"log/slog"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// seedFromAddress upserts a SeenAddress row for the message's From:
// address when all of the following hold (REQ-MAIL-11h):
//
//   - The principal has seen_addresses_enabled = true (REQ-SET-15).
//   - mailFrom (the SMTP MAIL FROM) is non-empty (null-sender skip).
//   - The From: header yields a parseable addr-spec.
//   - The address does not look like a postmaster/mailer-daemon address.
//   - The message does not carry a List-Id header (mailing list skip).
//   - The From address is not one of the principal's identity addresses.
//   - The From address does not already have a Contact row for the principal.
//
// Errors are logged at warn level and never returned (best-effort, per
// the fire-and-forget contract at the call site).
func seedFromAddress(
	ctx context.Context,
	st store.Store,
	log *slog.Logger,
	pid store.PrincipalID,
	mailFrom string,
	msg mailparse.Message,
) {
	// Null-sender skip.
	if mailFrom == "" || mailFrom == "<>" {
		return
	}

	// Mailing-list skip: List-Id header present.
	if msg.Headers.Get("List-Id") != "" {
		return
	}

	// Extract From address from the parsed envelope. The mailparse layer
	// has already decoded RFC 5322 address syntax.
	if len(msg.Envelope.From) == 0 {
		return
	}
	fromAddr := msg.Envelope.From[0].Address
	displayName := msg.Envelope.From[0].Name
	if fromAddr == "" {
		return
	}
	lower := strings.ToLower(fromAddr)

	// Postmaster / mailer-daemon skip.
	if isPostmasterLike(lower) {
		return
	}

	// Lookup principal to check seen_addresses_enabled.
	p, err := st.Meta().GetPrincipalByID(ctx, pid)
	if err != nil {
		log.WarnContext(ctx, "seenaddress: principal lookup failed",
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()),
			slog.Int64("principal_id", int64(pid)))
		return
	}
	if !p.SeenAddressesEnabled {
		return
	}

	// Identity skip.
	identityEmails, err := buildInboundIdentitySet(ctx, st.Meta(), pid, p.CanonicalEmail)
	if err != nil {
		log.WarnContext(ctx, "seenaddress: identity lookup failed",
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()))
		return
	}
	if _, isIdentity := identityEmails[lower]; isIdentity {
		return
	}

	// Contact skip.
	contacts, err := st.Meta().ListContacts(ctx, store.ContactFilter{
		PrincipalID: &pid,
		HasEmail:    &lower,
		Limit:       1,
	})
	if err != nil {
		log.WarnContext(ctx, "seenaddress: contact lookup failed",
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()))
		return
	}
	if len(contacts) > 0 {
		return
	}

	if _, _, err := st.Meta().UpsertSeenAddress(ctx, pid, lower, displayName, 0, 1); err != nil {
		log.WarnContext(ctx, "seenaddress: upsert failed",
			slog.String("activity", observe.ActivityInternal),
			slog.String("email", lower),
			slog.String("err", err.Error()))
	}
}

// buildInboundIdentitySet is the protosmtp-local equivalent of the
// emailsubmission package's buildIdentityEmailSet.
func buildInboundIdentitySet(
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

// isPostmasterLike returns true when the local-part of the address
// matches common postmaster/mailer-daemon patterns. These automated
// senders should never appear in recipient autocomplete.
func isPostmasterLike(email string) bool {
	at := strings.IndexByte(email, '@')
	var local string
	if at >= 0 {
		local = email[:at]
	} else {
		local = email
	}
	local = strings.ToLower(local)
	switch local {
	case "postmaster", "mailer-daemon", "noreply", "no-reply",
		"bounces", "bounce", "donotreply", "do-not-reply",
		"daemon", "mailerdaemon":
		return true
	}
	return false
}
