package searchsnippet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id.
type jmapID = string

// jmapSnippet is the wire-form SearchSnippet object (RFC 8621 §6.1).
// subject and preview are null when the search term does not appear in
// that field; a non-null value carries the text with matching terms
// wrapped in <mark>...</mark>.
type jmapSnippet struct {
	EmailID jmapID  `json:"emailId"`
	Subject *string `json:"subject"`
	Preview *string `json:"preview"`
}

// filterShape is the subset of Email/query's filter we use to derive
// search terms. Unknown fields are ignored — SearchSnippet does not
// validate the filter against Email/query's full schema; the caller
// has already done so when issuing Email/query.
type filterShape struct {
	Text           string        `json:"text,omitempty"`
	From           string        `json:"from,omitempty"`
	To             string        `json:"to,omitempty"`
	Cc             string        `json:"cc,omitempty"`
	Bcc            string        `json:"bcc,omitempty"`
	Subject        string        `json:"subject,omitempty"`
	Body           string        `json:"body,omitempty"`
	Header         []string      `json:"header,omitempty"`
	AttachmentName string        `json:"attachmentName,omitempty"`
	Operator       string        `json:"operator,omitempty"`
	Conditions     []filterShape `json:"conditions,omitempty"`
}

// extractSubjectTerms returns the terms that should be highlighted in
// the subject field. Per RFC 8621 §6.1 the subject is highlighted only
// when the filter matches the subject: the text: (all-field) and
// subject: conditions are in scope; body-only conditions are not.
func (f filterShape) extractSubjectTerms() []string {
	var out []string
	for _, t := range []string{f.Text, f.Subject} {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, splitTerms(t)...)
		}
	}
	for _, c := range f.Conditions {
		out = append(out, c.extractSubjectTerms()...)
	}
	return out
}

// extractPreviewTerms returns the terms that should be highlighted in
// the preview (body) field. body:, text: (all-field), from:, to:,
// cc:, bcc: and attachmentName: conditions all contribute.
func (f filterShape) extractPreviewTerms() []string {
	var out []string
	for _, t := range []string{f.Text, f.From, f.To, f.Cc, f.Bcc,
		f.Body, f.AttachmentName} {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, splitTerms(t)...)
		}
	}
	for _, c := range f.Conditions {
		out = append(out, c.extractPreviewTerms()...)
	}
	return out
}

// splitTerms tokenises a free-text filter value into individual terms
// on whitespace. Quoted phrases are kept verbatim (modulo the quotes)
// so multi-word filters round-trip.
func splitTerms(s string) []string {
	var out []string
	cur := strings.Builder{}
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case !inQuote && (r == ' ' || r == '\t'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// getRequest is the inbound shape of SearchSnippet/get. The filter
// matches Email/query's filter; emailIds is the list of message ids
// the client wants highlighted.
type getRequest struct {
	AccountID jmapID          `json:"accountId"`
	Filter    json.RawMessage `json:"filter,omitempty"`
	EmailIDs  []jmapID        `json:"emailIds"`
}

// getResponse mirrors RFC 8621 §6.1.
type getResponse struct {
	AccountID string          `json:"accountId"`
	Filter    json.RawMessage `json:"filter,omitempty"`
	List      []jmapSnippet   `json:"list"`
	NotFound  []jmapID        `json:"notFound"`
}

// handlerSet binds the SearchSnippet handler to the store.
type handlerSet struct {
	store store.Store
}

// resolveAccount resolves the JMAP accountId to a target principal ID.
// Supports cross-account access when the caller holds ACL on the foreign account.
func resolveAccount(ctx context.Context, meta store.Metadata, callerP store.Principal, requested jmapID) (store.PrincipalID, *protojmap.MethodError) {
	return protojmap.ResolveAccount(ctx, meta, callerP.ID, requested)
}

// parseEmailID parses a wire-form email id into a MessageID.
func parseEmailID(id jmapID) (store.MessageID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.MessageID(v), true
}

// -- SearchSnippet/get ------------------------------------------------

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "SearchSnippet/get" }

func (g getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req getRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	callerP, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	targetPID, merr := resolveAccount(ctx, g.h.store.Meta(), callerP, req.AccountID)
	if merr != nil {
		return nil, merr
	}
	var filter filterShape
	if len(req.Filter) > 0 {
		if err := json.Unmarshal(req.Filter, &filter); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments",
				fmt.Sprintf("filter: %s", err.Error()))
		}
	}
	subjectTerms := filter.extractSubjectTerms()
	previewTerms := filter.extractPreviewTerms()
	resp := getResponse{
		AccountID: req.AccountID,
		Filter:    req.Filter,
		// List is always an array; NotFound is null (nil) when empty per
		// RFC 8621 §6 which requires notFound to be null or non-empty.
		List: []jmapSnippet{},
	}
	for _, id := range req.EmailIDs {
		mid, ok := parseEmailID(id)
		if !ok {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		msg, err := g.h.store.Meta().GetMessage(ctx, mid)
		if err != nil {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		// Authorisation: the message's mailbox must be owned by targetPID
		// and accessible to the caller (callerP).
		if !g.h.principalCanSeeMailbox(ctx, callerP.ID, targetPID, msg.MailboxID) {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		previewSrc := g.h.previewText(ctx, msg)
		// RFC 8621 §6.1: subject and preview are null when the search term
		// does not appear in that field.  Apply field-specific term sets so
		// that a body: search does not falsely highlight the subject and
		// vice-versa.
		subjectHighlight := highlight(collapseWhitespace(msg.Envelope.Subject), subjectTerms)
		previewHighlight := snippet(previewSrc, previewTerms)
		var subjectPtr, previewPtr *string
		if subjectHighlight != "" && strings.Contains(subjectHighlight, "<mark>") {
			subjectPtr = &subjectHighlight
		}
		if previewHighlight != "" && strings.Contains(previewHighlight, "<mark>") {
			previewPtr = &previewHighlight
		}
		resp.List = append(resp.List, jmapSnippet{
			EmailID: id,
			Subject: subjectPtr,
			Preview: previewPtr,
		})
	}
	return resp, nil
}

// principalCanSeeMailbox returns whether callerPID can read from mailboxID,
// which must be owned by targetPID. For the caller's own account, any
// owned mailbox is visible. For foreign accounts, the caller must hold at
// least the Lookup right via direct or "anyone" ACL row.
func (h *handlerSet) principalCanSeeMailbox(ctx context.Context, callerPID, targetPID store.PrincipalID, mailboxID store.MailboxID) bool {
	mb, err := h.store.Meta().GetMailboxByID(ctx, mailboxID)
	if err != nil {
		return false
	}
	if mb.PrincipalID != targetPID {
		return false
	}
	if callerPID == targetPID {
		return true
	}
	// Cross-account: check ACL for Lookup right.
	rows, err := h.store.Meta().GetMailboxACL(ctx, mailboxID)
	if err != nil {
		return false
	}
	for _, r := range rows {
		if r.Rights&store.ACLRightLookup == 0 {
			continue
		}
		if r.PrincipalID == nil {
			return true // "anyone" ACL row
		}
		if *r.PrincipalID == callerPID {
			return true
		}
	}
	return false
}

// previewText returns the message's body text, used as the basis for
// snippet rendering. We try the blob store first; on read failure we
// fall back to an empty string so the snippet is still well-formed.
func (h *handlerSet) previewText(ctx context.Context, msg store.Message) string {
	if msg.Blob.Hash == "" {
		return ""
	}
	rc, err := h.store.Blobs().Get(ctx, msg.Blob.Hash)
	if err != nil {
		return ""
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	parsed, err := mailparse.Parse(strings.NewReader(string(body)), mailparse.ParseOptions{StrictBoundary: false})
	if err != nil {
		// Unparsable bodies still produce a preview: use the raw
		// bytes verbatim, modulo whitespace folding.
		return collapseWhitespace(string(body))
	}
	if t := mailparse.PrimaryTextBody(parsed); t != "" {
		return collapseWhitespace(t)
	}
	return collapseWhitespace(string(body))
}
