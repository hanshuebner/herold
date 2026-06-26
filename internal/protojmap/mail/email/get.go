package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/mail"
	"sort"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// getRequest is the wire-form Email/get request (RFC 8621 §4.2).
type getRequest struct {
	AccountID           jmapID    `json:"accountId"`
	IDs                 *[]jmapID `json:"ids"`
	Properties          *[]string `json:"properties"`
	BodyProperties      *[]string `json:"bodyProperties"`
	FetchTextBodyValues bool      `json:"fetchTextBodyValues"`
	FetchHTMLBodyValues bool      `json:"fetchHTMLBodyValues"`
	FetchAllBodyValues  bool      `json:"fetchAllBodyValues"`
	MaxBodyValueBytes   int       `json:"maxBodyValueBytes"`
}

// getResponse is the wire-form response.
type getResponse struct {
	AccountID jmapID      `json:"accountId"`
	State     string      `json:"state"`
	List      []jmapEmail `json:"list"`
	NotFound  []jmapID    `json:"notFound"`
}

// getHandler implements Email/get.
type getHandler struct{ h *handlerSet }

func (g *getHandler) Method() string { return "Email/get" }

func (g *getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	callerPID, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}

	var req getRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	ownerPID, merr := resolveAccount(ctx, g.h.store.Meta(), callerPID, req.AccountID)
	if merr != nil {
		return nil, merr
	}

	state, err := currentState(ctx, g.h.store.Meta(), ownerPID)
	if err != nil {
		return nil, serverFail(err)
	}

	resp := getResponse{
		AccountID: req.AccountID,
		State:     state,
		List:      []jmapEmail{},
		NotFound:  []jmapID{},
	}

	if req.IDs == nil {
		// RFC 8621 §4.2 permits ids:null to mean "return all"; herold
		// refuses because servicing it would require an un-indexed
		// scan over every accessible message (REQ-PERF-INDEX-04).
		// Clients that need to walk the corpus page through Email/query
		// with explicit Limit + Position.
		return nil, protojmap.NewUnindexedScanError("requestTooLarge",
			"Email/get with ids=null is not supported; pass an explicit list of ids "+
				"or page through Email/query")
	}

	// Collect valid MessageIDs first so we can batch-fetch reactions.
	type entry struct {
		raw string
		mid store.MessageID
		msg store.Message
		ok  bool
	}
	entries := make([]entry, 0, len(*req.IDs))
	var validIDs []store.MessageID
	for _, raw := range *req.IDs {
		mid, ok := emailIDFromJMAP(raw)
		if !ok {
			entries = append(entries, entry{raw: raw})
			continue
		}
		m, err := loadMessageForPrincipal(ctx, g.h.store.Meta(), callerPID, mid)
		if err != nil {
			if errors.Is(err, errMessageMissing) {
				entries = append(entries, entry{raw: raw})
				continue
			}
			return nil, serverFail(err)
		}
		// Cross-account guard: the message must live in a mailbox owned
		// by the requested account; otherwise it does not exist for the
		// purposes of this Email/get call (RFC 8621 §4.2 + REQ-PROTO-33).
		if callerPID != ownerPID {
			mb, mberr := g.h.store.Meta().GetMailboxByID(ctx, m.MailboxID)
			if mberr != nil || mb.PrincipalID != ownerPID {
				entries = append(entries, entry{raw: raw})
				continue
			}
		}
		entries = append(entries, entry{raw: raw, mid: mid, msg: m, ok: true})
		validIDs = append(validIDs, mid)
	}

	batchReactions, err := g.h.store.Meta().BatchListEmailReactions(ctx, validIDs)
	if err != nil {
		return nil, serverFail(fmt.Errorf("email: load reactions: %w", err))
	}

	for _, e := range entries {
		if !e.ok {
			resp.NotFound = append(resp.NotFound, e.raw)
			continue
		}
		needBody := needBodyForMessage(
			e.msg,
			req.FetchTextBodyValues,
			req.FetchHTMLBodyValues,
			req.FetchAllBodyValues,
			req.Properties,
		)
		fetchBodyValues := req.FetchTextBodyValues || req.FetchHTMLBodyValues || req.FetchAllBodyValues
		rendered, err := g.renderOne(ctx, e.msg, needBody, fetchBodyValues, req.MaxBodyValueBytes, req.Properties)
		if err != nil {
			return nil, serverFail(err)
		}
		rendered.Reactions = reactionsToWire(batchReactions[e.mid])
		resp.List = append(resp.List, rendered)
	}
	return resp, nil
}

// propertiesNeedBody reports whether the properties list requests any
// property that ALWAYS requires full blob parsing regardless of the
// per-message BodyMetaComputed state. When props is nil (client did not
// specify a properties filter) we may still skip the blob when all
// individual messages have BodyMetaComputed; the call site handles that.
//
// Note: "preview" and "hasAttachment" are NOT in this list — they are
// served from precomputed metadata when BodyMetaComputed is true; they
// only force the blob path per-message when BodyMetaComputed is false
// (lazy fallback). See needBodyForMessage.
func propertiesNeedBody(props *[]string) bool {
	if props == nil {
		// nil means "all properties" — body-only props are in that set.
		return true
	}
	for _, p := range *props {
		switch p {
		case "bodyStructure", "textBody", "htmlBody", "attachments",
			"bodyValues", "references":
			return true
		}
		// Dynamic header accessors: "header:X:asY"
		if strings.HasPrefix(p, "header:") {
			return true
		}
	}
	return false
}

// needBodyForMessage returns true when the full body blob must be parsed
// for message m given the request's properties list and fetch flags.
//
// The decision is per-message because "preview" and "hasAttachment" are
// served from m.Preview / m.HasAttachment when m.BodyMetaComputed is true
// and only require the blob path when BodyMetaComputed is false.
func needBodyForMessage(
	m store.Message,
	fetchTextBodyValues, fetchHTMLBodyValues, fetchAllBodyValues bool,
	properties *[]string,
) bool {
	// Fetch flags always require the blob.
	if fetchTextBodyValues || fetchHTMLBodyValues || fetchAllBodyValues {
		return true
	}
	// Properties that unconditionally require the blob.
	if propertiesNeedBody(properties) {
		return true
	}
	// nil properties means "all" — the body-only check above already
	// returns true in that branch, but if BodyMetaComputed is true and
	// the caller supplied no explicit properties, we would still need
	// bodyStructure/textBody/htmlBody etc. So: nil properties -> need body.
	if properties == nil {
		return true
	}
	// preview / hasAttachment require the blob only when not precomputed.
	for _, p := range *properties {
		switch p {
		case "preview", "hasAttachment":
			if !m.BodyMetaComputed {
				return true
			}
		}
	}
	return false
}

// reactionsToWire converts the store's map[emoji]map[PrincipalID]struct{}
// into the JMAP wire form map[emoji][]principalID. Returns nil when the
// input is empty so the field is omitted from JSON (sparse by design).
func reactionsToWire(r map[string]map[store.PrincipalID]struct{}) map[string][]string {
	if len(r) == 0 {
		return nil
	}
	out := make(map[string][]string, len(r))
	for emoji, pids := range r {
		list := make([]string, 0, len(pids))
		for pid := range pids {
			list = append(list, strconv.FormatUint(uint64(pid), 10))
		}
		sort.Strings(list) // deterministic order for tests
		out[emoji] = list
	}
	return out
}

// renderOne produces the wire-form Email object. When needBody is true
// we round-trip through the blob store and parser to populate body
// properties and header accessors.
//
// fetchBodyValues is true when the request had fetchTextBodyValues /
// fetchHTMLBodyValues / fetchAllBodyValues set; it forces bodyValues into the
// response even when properties is a restrictive list that does not name
// "bodyValues" explicitly (per RFC 8621 §4.2).
func (g *getHandler) renderOne(
	ctx context.Context,
	m store.Message,
	needBody bool,
	fetchBodyValues bool,
	truncateAt int,
	properties *[]string,
) (jmapEmail, error) {
	if !needBody {
		return renderEmailMetadata(m), nil
	}
	// External-image internalization runs out-of-band in the
	// internalizeworker (REQ-EXTIMG-BG-01). Email/get does not block
	// on the rewrite anymore; instead, when the message still carries
	// InternalizePending, renderFull replaces external image src
	// attributes with a placeholder data URI (REQ-EXTIMG-BG-10) so the
	// user sees the body now without leaking open-rate before the
	// worker catches up. The wire object also carries
	// internalizePending=true so the SPA can show a "images being
	// processed" indicator (REQ-EXTIMG-BG-20).
	parser := g.h.parseFn
	if parser == nil {
		parser = defaultParseFn
	}
	return renderFullWithProperties(ctx, g.h.store.Blobs(), g.h.store.Meta(), m, fetchBodyValues, truncateAt, parser, properties, g.h.logger)
}

// wantProp reports whether the named property should be included in the
// response given the requested properties list. When properties is nil
// (meaning "all properties"), every property is included.
func wantProp(properties *[]string, name string) bool {
	if properties == nil {
		return true
	}
	for _, p := range *properties {
		if p == name {
			return true
		}
	}
	return false
}

// renderFullWithProperties extends renderFull to also populate dynamic
// header property accessors requested in properties.
//
// When properties is non-nil, only the explicitly requested body-level
// fields are populated; omitting them avoids returning MBs of body data
// for a "preview only" request. When properties is nil (JMAP default = all
// properties), all fields are populated.
//
// fetchBodyValues, when true, forces bodyValues to be populated regardless of
// the properties filter. Callers set this when the request had
// fetchTextBodyValues / fetchHTMLBodyValues / fetchAllBodyValues set (RFC
// 8621 §4.2).
//
// meta, when non-nil, enables the opportunistic body-meta persist: if the
// message's BodyMetaComputed is false, the computed preview and hasAttachment
// values are written back via SetMessageBodyMeta as a best-effort side effect
// so future Email/get calls can skip the blob. Errors are logged and ignored.
func renderFullWithProperties(
	ctx context.Context,
	blobs store.Blobs,
	meta store.Metadata,
	m store.Message,
	fetchBodyValues bool,
	truncateAt int,
	parser parseFn,
	properties *[]string,
	logger *slog.Logger,
) (jmapEmail, error) {
	out := renderEmailMetadata(m)

	rc, err := blobs.Get(ctx, m.Blob.Hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return out, nil
		}
		return jmapEmail{}, fmt.Errorf("email: blob: %w", err)
	}
	defer rc.Close()

	// TODO(REQ-STORE-19, Phase 2): stream via the seekable blob handle /
	// streaming parser; bounded at 64MiB until then.
	rawBody, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return jmapEmail{}, fmt.Errorf("email: read blob: %w", err)
	}

	// REQ-FLOW-34: inject the synthetic X-Herold-Recipient header so
	// header property accessors and body parts see it. No-op when
	// ReceivedTo is empty (pre-feature / non-inbound membership).
	rawBody = mailparse.InjectXHeroldRecipient(rawBody, m.ReceivedTo)

	parsed, err := parser(bytes.NewReader(rawBody))
	if err != nil {
		return out, nil
	}

	// Populate body parts. Load intrinsic image dimensions from the persisted
	// part index (re #47) so Width/Height appear on image EmailBodyParts.
	dims := loadPartDims(ctx, meta, m.Blob.Hash)
	bs, values, textParts, htmlParts, attParts := walkParts(parsed.Body, truncateAt, m.Blob.Hash, dims)
	if m.InternalizePending {
		// REQ-EXTIMG-BG-10: replace external image references in every
		// HTML body part with a placeholder data URI until the
		// background internalize-worker rewrites the blob. Failures
		// fall through silently — the user sees the original HTML
		// rather than a refused render.
		for _, p := range htmlParts {
			if p.PartID == nil {
				continue
			}
			bv, ok := values[*p.PartID]
			if !ok {
				continue
			}
			rewritten, rerr := extimg.RewriteForPlaceholder([]byte(bv.Value))
			if rerr != nil {
				continue
			}
			bv.Value = string(rewritten)
			values[*p.PartID] = bv
		}
	}

	// Compute preview and hasAttachment from the parsed body. We always
	// compute these so the opportunistic persist path below can call
	// SetMessageBodyMeta regardless of what properties were requested.
	computedPreview := previewFromValues(values, textParts, 256)
	computedHasAttachment := len(attParts) > 0

	// Opportunistic persist: if the message has not yet had its body-meta
	// computed, write it back now so future list-view calls skip the blob.
	// Best-effort: log and continue on error; never fail the GET.
	if !m.BodyMetaComputed && meta != nil {
		if serr := meta.SetMessageBodyMeta(ctx, m.ID, computedPreview, computedHasAttachment); serr != nil {
			if logger != nil {
				logger.LogAttrs(ctx, slog.LevelWarn, "email/get: persist body meta failed",
					slog.String("activity", "system"),
					slog.Uint64("message_id", uint64(m.ID)),
					slog.String("err", serr.Error()),
				)
			}
		}
	}

	// Property projection: only populate fields the client asked for.
	// When properties is nil (all properties), all fields are set.
	if wantProp(properties, "bodyStructure") {
		out.BodyStructure = bs
	}
	if fetchBodyValues ||
		wantProp(properties, "bodyValues") ||
		wantProp(properties, "textBody") ||
		wantProp(properties, "htmlBody") {
		// bodyValues is populated alongside the part arrays — the client
		// needs values to render the body it asked for.
		out.BodyValues = values
	}
	if wantProp(properties, "textBody") {
		out.TextBody = textParts
	}
	if wantProp(properties, "htmlBody") {
		out.HTMLBody = htmlParts
	}
	if wantProp(properties, "attachments") {
		out.Attachments = attParts
	}
	if wantProp(properties, "hasAttachment") {
		out.HasAttachment = computedHasAttachment
	}
	if wantProp(properties, "preview") {
		out.Preview = computedPreview
	}

	// Also populate References from the parsed message if the envelope
	// didn't carry it. Only if the client asked for it.
	if wantProp(properties, "references") && len(out.References) == 0 {
		if refs := parsed.Headers.Get("References"); refs != "" {
			out.References = splitMessageIDs(refs)
		}
	}

	// Populate dynamic header property accessors.
	if properties != nil {
		if out.HeaderProperties == nil {
			out.HeaderProperties = make(map[string]json.RawMessage)
		}
		for _, prop := range *properties {
			if !strings.HasPrefix(prop, "header:") {
				continue
			}
			val := resolveHeaderProperty(parsed, prop)
			out.HeaderProperties[prop] = val
		}
	} else {
		// properties == nil: populate all dynamic header properties that
		// appear in the parsed message. In practice JMAP clients that want
		// header accessors name them explicitly; nil means "standard props
		// only" per RFC 8621.  We skip the full-scan here (no-op for nil).
	}

	return out, nil
}

// resolveHeaderProperty decodes a dynamic header accessor like
// "header:Subject:asText" or "header:References:asMessageIds".
// Returns the JSON-encoded value or JSON null when not present.
func resolveHeaderProperty(parsed mailparse.Message, prop string) json.RawMessage { //nolint:gocritic
	// prop format: "header:<HeaderName>:<form>" or "header:<HeaderName>"
	// (raw form when no form suffix).
	parts := strings.SplitN(prop, ":", 3)
	if len(parts) < 2 {
		return json.RawMessage("null")
	}
	headerName := parts[1]
	form := "asRaw"
	if len(parts) == 3 {
		form = parts[2]
	}

	// Header lookup is case-insensitive.
	raw := parsed.Headers.Get(headerName)
	if raw == "" {
		return jsonNull()
	}

	switch strings.ToLower(form) {
	case "asraw", "":
		// RFC 8621 §4.1.2.4: asRaw returns the raw header value with
		// leading space preserved.
		encoded, _ := json.Marshal(" " + strings.TrimSpace(raw))
		return json.RawMessage(encoded)
	case "astext":
		// Decoded text; for Subject this is the RFC 2047-decoded value.
		dec := new(mime.WordDecoder)
		decoded, err := dec.DecodeHeader(strings.TrimSpace(raw))
		if err != nil {
			decoded = strings.TrimSpace(raw)
		}
		if decoded == "" {
			return jsonNull()
		}
		encoded, _ := json.Marshal(decoded)
		return json.RawMessage(encoded)
	case "asdate":
		// RFC 8621 §4.1.2.4: asDate returns a UTCDate string.
		t, err := mail.ParseDate(strings.TrimSpace(raw))
		if err != nil {
			return jsonNull()
		}
		encoded, _ := json.Marshal(rfc3339UTC(t))
		return json.RawMessage(encoded)
	case "asaddresses":
		// Array of jmapAddress.
		addrs := parseAddressList(raw)
		if addrs == nil {
			return json.RawMessage("[]")
		}
		encoded, _ := json.Marshal(addrs)
		return json.RawMessage(encoded)
	case "asgroupedaddresses":
		// Array of {name, addresses}. We flatten groups into flat addresses
		// for simplicity (RFC 8621 §4.1.2.4 says group name is preserved).
		addrs := parseAddressList(raw)
		if addrs == nil {
			return json.RawMessage("[]")
		}
		// Wrap all in a single unnamed group.
		type groupedAddress struct {
			Name      *string       `json:"name"`
			Addresses []jmapAddress `json:"addresses"`
		}
		group := groupedAddress{Name: nil, Addresses: addrs}
		encoded, _ := json.Marshal([]groupedAddress{group})
		return json.RawMessage(encoded)
	case "asmessageids":
		// Array of message-id strings (angle brackets stripped).
		ids := splitMessageIDs(raw)
		if len(ids) == 0 {
			return json.RawMessage("[]")
		}
		encoded, _ := json.Marshal(ids)
		return json.RawMessage(encoded)
	case "asurls":
		// Array of URL strings.
		urls := extractURLs(raw)
		if len(urls) == 0 {
			return json.RawMessage("[]")
		}
		encoded, _ := json.Marshal(urls)
		return json.RawMessage(encoded)
	}
	return jsonNull()
}

// splitMessageIDs parses a space-separated list of message-id values,
// stripping angle brackets.
func splitMessageIDs(raw string) []string {
	raw = strings.TrimSpace(raw)
	var out []string
	for _, part := range strings.Fields(raw) {
		part = strings.Trim(part, "<>")
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// extractURLs extracts URL-like strings from a header value.
func extractURLs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Simple: split on whitespace and return non-empty tokens.
	var out []string
	for _, part := range strings.Fields(raw) {
		part = strings.Trim(part, "<>\"'")
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func jsonNull() json.RawMessage { return json.RawMessage("null") }

// principalFromCtx is a thin wrapper around protojmap.PrincipalFromContext
// used by every handler in this package.
func principalFromCtx(ctx context.Context) (store.PrincipalID, *protojmap.MethodError) {
	return requirePrincipal(func() (store.PrincipalID, bool) {
		p, ok := protojmap.PrincipalFromContext(ctx)
		if !ok {
			return 0, false
		}
		return p.ID, true
	})
}
