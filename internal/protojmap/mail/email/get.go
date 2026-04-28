package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"sort"
	"strconv"
	"strings"

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
	pid, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}

	var req getRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	state, err := currentState(ctx, g.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	resp := getResponse{
		AccountID: req.AccountID,
		State:     state,
		List:      []jmapEmail{},
		NotFound:  []jmapID{},
	}

	// Determine what level of rendering is needed based on requested properties.
	needBody := req.FetchTextBodyValues || req.FetchHTMLBodyValues || req.FetchAllBodyValues ||
		propertiesNeedBody(req.Properties)

	if req.IDs == nil {
		// Fetch all (rarely useful but spec-permitted).
		all, err := listPrincipalMessages(ctx, g.h.store.Meta(), pid)
		if err != nil {
			return nil, serverFail(err)
		}
		ids := make([]store.MessageID, len(all))
		for i, m := range all {
			ids[i] = m.ID
		}
		batchReactions, err := g.h.store.Meta().BatchListEmailReactions(ctx, ids)
		if err != nil {
			return nil, serverFail(fmt.Errorf("email: load reactions: %w", err))
		}
		for _, m := range all {
			rendered, err := g.renderOne(ctx, m, needBody, req.MaxBodyValueBytes, req.Properties)
			if err != nil {
				return nil, serverFail(err)
			}
			rendered.Reactions = reactionsToWire(batchReactions[m.ID])
			resp.List = append(resp.List, rendered)
		}
		return resp, nil
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
		m, err := loadMessageForPrincipal(ctx, g.h.store.Meta(), pid, mid)
		if err != nil {
			if errors.Is(err, errMessageMissing) {
				entries = append(entries, entry{raw: raw})
				continue
			}
			return nil, serverFail(err)
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
		rendered, err := g.renderOne(ctx, e.msg, needBody, req.MaxBodyValueBytes, req.Properties)
		if err != nil {
			return nil, serverFail(err)
		}
		rendered.Reactions = reactionsToWire(batchReactions[e.mid])
		resp.List = append(resp.List, rendered)
	}
	return resp, nil
}

// propertiesNeedBody reports whether the properties list requests any
// property that requires full blob parsing. When props is nil (client
// did not specify a properties filter), all properties are returned
// including body-level ones, so we always need the blob.
func propertiesNeedBody(props *[]string) bool {
	if props == nil {
		return true
	}
	for _, p := range *props {
		switch p {
		case "preview", "bodyStructure", "textBody", "htmlBody", "attachments",
			"bodyValues", "hasAttachment":
			return true
		}
		// Dynamic header accessors: "header:X:asY"
		if strings.HasPrefix(p, "header:") {
			return true
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
func (g *getHandler) renderOne(
	ctx context.Context,
	m store.Message,
	needBody bool,
	truncateAt int,
	properties *[]string,
) (jmapEmail, error) {
	if !needBody {
		return renderEmailMetadata(m), nil
	}
	parser := g.h.parseFn
	if parser == nil {
		parser = defaultParseFn
	}
	return renderFullWithProperties(ctx, g.h.store.Blobs(), m, truncateAt, parser, properties)
}

// renderFullWithProperties extends renderFull to also populate dynamic
// header property accessors requested in properties.
func renderFullWithProperties(
	ctx context.Context,
	blobs store.Blobs,
	m store.Message,
	truncateAt int,
	parser parseFn,
	properties *[]string,
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

	rawBody, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return jmapEmail{}, fmt.Errorf("email: read blob: %w", err)
	}

	parsed, err := parser(bytes.NewReader(rawBody))
	if err != nil {
		return out, nil
	}

	// Populate body parts.
	bs, values, textRefs, htmlRefs, attRefs := walkParts(parsed.Body, truncateAt)
	out.BodyStructure = bs
	out.BodyValues = values
	out.TextBody = textRefs
	out.HTMLBody = htmlRefs
	out.Attachments = attRefs
	out.HasAttachment = len(attRefs) > 0
	out.Preview = previewFromValues(values, textRefs, 256)

	// Also populate References from the parsed message if the envelope
	// didn't carry it.
	if len(out.References) == 0 {
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
			Name      *string      `json:"name"`
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
