package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// parseRequest is the wire-form Email/parse request (RFC 8621 §4.9).
type parseRequest struct {
	AccountID           jmapID    `json:"accountId"`
	BlobIDs             []string  `json:"blobIds"`
	Properties          *[]string `json:"properties"`
	BodyProperties      *[]string `json:"bodyProperties"`
	FetchTextBodyValues bool      `json:"fetchTextBodyValues"`
	FetchHTMLBodyValues bool      `json:"fetchHTMLBodyValues"`
	FetchAllBodyValues  bool      `json:"fetchAllBodyValues"`
	MaxBodyValueBytes   int       `json:"maxBodyValueBytes"`
}

// parseResponse is the wire-form Email/parse response.
type parseResponse struct {
	AccountID   jmapID               `json:"accountId"`
	Parsed      map[string]jmapEmail `json:"parsed"`
	NotParsable []string             `json:"notParsable"`
	NotFound    []string             `json:"notFound"`
}

// parseHandler implements Email/parse — parses an uploaded blob without
// persisting a Message row. The returned Email object lacks id /
// mailboxIds / receivedAt because no row exists; clients use this to
// preview an attachment that itself contains an RFC 5322 message.
type parseHandler struct{ h *handlerSet }

func (p *parseHandler) Method() string { return "Email/parse" }

func (p *parseHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}
	var req parseRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	resp := parseResponse{
		AccountID:   req.AccountID,
		Parsed:      map[string]jmapEmail{},
		NotParsable: []string{},
		NotFound:    []string{},
	}

	wantBodies := req.FetchTextBodyValues || req.FetchHTMLBodyValues || req.FetchAllBodyValues
	parser := p.h.parseFn
	if parser == nil {
		parser = defaultParseFn
	}
	for _, blobID := range req.BlobIDs {
		body, err := readBlobBody(ctx, p.h.store.Blobs(), blobID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.NotFound = append(resp.NotFound, blobID)
				continue
			}
			return nil, serverFail(err)
		}
		parsed, perr := parser(bytes.NewReader(body))
		if perr != nil {
			resp.NotParsable = append(resp.NotParsable, blobID)
			continue
		}
		jm := jmapEmail{
			BlobID:     blobID,
			Size:       int64(len(body)),
			Keywords:   map[string]bool{},
			MailboxIDs: map[jmapID]bool{},
		}
		env := buildEnvelopeFromParsed(parsed)
		if env.Subject != "" {
			jm.Subject = env.Subject
		}
		if !env.Date.IsZero() {
			jm.SentAt = rfc3339UTC(env.Date)
		}
		jm.From = parseAddressList(env.From)
		jm.To = parseAddressList(env.To)
		jm.Cc = parseAddressList(env.Cc)
		jm.Bcc = parseAddressList(env.Bcc)
		jm.ReplyTo = parseAddressList(env.ReplyTo)
		if env.MessageID != "" {
			jm.MessageID = []string{env.MessageID}
		}
		if env.InReplyTo != "" {
			jm.InReplyTo = []string{env.InReplyTo}
		}
		if wantBodies {
			bs, values, textRefs, htmlRefs, attRefs := walkParts(parsed.Body, req.MaxBodyValueBytes)
			jm.BodyStructure = bs
			jm.BodyValues = values
			jm.TextBody = textRefs
			jm.HTMLBody = htmlRefs
			jm.Attachments = attRefs
			jm.HasAttachment = len(attRefs) > 0
			jm.Preview = previewFromValues(values, textRefs, 256)
		}
		resp.Parsed[blobID] = jm
	}
	return resp, nil
}
