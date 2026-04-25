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
// `null` for subject / preview means "no match in this field"; we
// always emit a string here (possibly the unhighlighted leading text)
// because clients prefer a value over nil.
type jmapSnippet struct {
	EmailID jmapID `json:"emailId"`
	Subject string `json:"subject"`
	Preview string `json:"preview"`
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

// extractTerms walks the (possibly nested) filter and returns the
// flat list of terms to highlight. Operators (and / or / not) are
// flattened — a SearchSnippet only renders the highlight overlay, so
// we surface every term in the tree.
func (f filterShape) extractTerms() []string {
	var out []string
	for _, t := range []string{f.Text, f.From, f.To, f.Cc, f.Bcc,
		f.Subject, f.Body, f.AttachmentName} {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, splitTerms(t)...)
		}
	}
	for _, c := range f.Conditions {
		out = append(out, c.extractTerms()...)
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

func accountIDForPrincipal(p store.Principal) string {
	return strconv.FormatUint(uint64(p.ID), 10)
}

func validateAccountID(p store.Principal, requested jmapID) *protojmap.MethodError {
	if requested == "" {
		return nil
	}
	if requested != accountIDForPrincipal(p) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
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
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	var filter filterShape
	if len(req.Filter) > 0 {
		if err := json.Unmarshal(req.Filter, &filter); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments",
				fmt.Sprintf("filter: %s", err.Error()))
		}
	}
	terms := filter.extractTerms()
	resp := getResponse{
		AccountID: accountIDForPrincipal(p),
		Filter:    req.Filter,
		List:      []jmapSnippet{},
		NotFound:  []jmapID{},
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
		// Authorisation: the message must belong to a mailbox the
		// principal owns. We re-list the principal's mailboxes (cached
		// per call would be a Wave 2.3 polish).
		if !g.h.principalOwnsMailbox(ctx, p, msg.MailboxID) {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		previewSrc := g.h.previewText(ctx, msg)
		resp.List = append(resp.List, jmapSnippet{
			EmailID: id,
			Subject: highlight(collapseWhitespace(msg.Envelope.Subject), terms),
			Preview: snippet(previewSrc, terms),
		})
	}
	return resp, nil
}

// principalOwnsMailbox returns whether p is the owner of mailboxID.
// We do not consult the ACL surface here — SearchSnippet returns the
// caller's own preview state, not shared mailbox content (Phase 3
// extends this to ACL-readable mailboxes).
func (h *handlerSet) principalOwnsMailbox(ctx context.Context, p store.Principal, mailboxID store.MailboxID) bool {
	mb, err := h.store.Meta().GetMailboxByID(ctx, mailboxID)
	if err != nil {
		return false
	}
	return mb.PrincipalID == p.ID
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
