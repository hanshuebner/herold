package maillist

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// unsubscribe.go generates the REQ-MLIST-56 List-Unsubscribe /
// List-Unsubscribe-Post header pair carried on every fanned-out copy of
// a list that has UnsubscribeEnabled set, and builds the token URL
// shared by the one-click endpoint (REQ-MLIST-57) and the self-service
// management page (REQ-MLIST-58/59) implemented in publicserver.go.
//
// The token URL necessarily varies per member (it addresses exactly one
// member of exactly one list, REQ-MLIST-59), so the two header lines
// below are the one piece of a fan-out copy's content that is NOT
// list-wide-identical across members, unlike List-ID/List-Post/
// Auto-Submitted (shape.go's buildPrependedHeaders). Expand.go applies
// them per member, after ARC-sealing (mailarc.Sealer.Seal is called once
// over the list-wide-identical content, REQ-MLIST-21): a per-recipient
// header is, by construction, never something a single shared signature
// can cover, exactly like the per-member VERP envelope MAIL FROM this
// package already produces (REQ-MLIST-50) -- List-Unsubscribe is the
// same pattern realised as a header instead of an envelope field. ARC/
// DKIM verifiers never require the absence of an unsigned header from a
// signature's covered set, so this costs nothing on the receiving end.

// UnsubscribeTokenTTL bounds how long a minted TokenPurposeUnsubscribe
// token stays valid (REQ-MLIST-58: "a bounded TTL"). Long enough that a
// subscriber returning to an old list message from months ago can still
// use its unsubscribe link, short enough that a token is not an
// effectively-permanent credential.
const UnsubscribeTokenTTL = 180 * 24 * time.Hour

// UnsubscribePath is the URL path template (fmt-style, one %d for the
// list id) the token URL is built from, and the pattern PublicServer's
// Handler registers GET/POST on.
const UnsubscribePath = "/lists/%d/unsubscribe"

// UnsubscribeURL mints a REQ-MLIST-56 TokenPurposeUnsubscribe token for
// (ml.ID, memberID) and renders the full herold-hosted HTTPS URL a
// fanned-out copy's List-Unsubscribe header carries: the same URL is
// also the one GET renders the REQ-MLIST-58 self-service management
// page at, and POST resolves as the RFC 8058 one-click / explicit
// unsubscribe action (REQ-MLIST-57).
func UnsubscribeURL(baseURL string, ts *TokenSigner, ml store.MailingList, memberID store.MailingListMemberID, now time.Time) (string, error) {
	token, err := ts.Sign(TokenPurposeUnsubscribe, ml.ID, memberID, now.Add(UnsubscribeTokenTTL))
	if err != nil {
		return "", fmt.Errorf("maillist: unsubscribe url: %w", err)
	}
	return strings.TrimRight(baseURL, "/") + fmt.Sprintf(UnsubscribePath, uint64(ml.ID)) + "?token=" + token, nil
}

// prependUnsubscribeHeaders splices the REQ-MLIST-56/57 header pair onto
// the very front of msg (an already fully rendered, and -- when
// ml.ARCSeal is on -- already ARC-sealed, fan-out copy). msg is expected
// to begin directly with a header line (ShapeMessage's / mailarc's own
// output shape), so a plain byte-level prepend keeps the header/body
// separator intact without re-parsing.
func prependUnsubscribeHeaders(msg []byte, unsubscribeURL string) []byte {
	var b bytes.Buffer
	b.Grow(len(msg) + 128)
	b.WriteString("List-Unsubscribe: <")
	b.WriteString(sanitizeForHeader(unsubscribeURL))
	b.WriteString(">\r\n")
	b.WriteString("List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n")
	b.Write(msg)
	return b.Bytes()
}

// mailingListIDFromPathValue parses the {id} path segment shared by
// every route this package mounts (/lists/{id}/...).
func mailingListIDFromPathValue(raw string) (store.MailingListID, bool) {
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return store.MailingListID(n), true
}
