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
	AccountID   jmapID                     `json:"accountId"`
	Parsed      map[string]jmapParsedEmail `json:"parsed"`
	NotParsable []string                   `json:"notParsable"`
	NotFound    []string                   `json:"notFound"`
}

// jmapParsedEmail wraps jmapEmail and overrides MarshalJSON so that
// server-set properties (id, threadId, mailboxIds, keywords,
// receivedAt) render as JSON null rather than empty-string / {} for
// blobs that have no associated stored Email row (RFC 8621 §4.9).
type jmapParsedEmail struct {
	jmapEmail
}

// MarshalJSON outputs the parsed email with null for server-set fields.
func (e jmapParsedEmail) MarshalJSON() ([]byte, error) {
	type alias struct {
		ID            json.RawMessage      `json:"id"`
		BlobID        string               `json:"blobId"`
		ThreadID      json.RawMessage      `json:"threadId"`
		MailboxIDs    json.RawMessage      `json:"mailboxIds"`
		Keywords      json.RawMessage      `json:"keywords"`
		Size          int64                `json:"size"`
		ReceivedAt    json.RawMessage      `json:"receivedAt"`
		SnoozedUntil  *string              `json:"snoozedUntil"`
		Reactions     map[string][]string  `json:"reactions,omitempty"`
		From          []jmapAddress        `json:"from,omitempty"`
		To            []jmapAddress        `json:"to,omitempty"`
		Cc            []jmapAddress        `json:"cc,omitempty"`
		Bcc           []jmapAddress        `json:"bcc,omitempty"`
		ReplyTo       []jmapAddress        `json:"replyTo,omitempty"`
		Sender        []jmapAddress        `json:"sender,omitempty"`
		Subject       string               `json:"subject,omitempty"`
		MessageID     []string             `json:"messageId,omitempty"`
		InReplyTo     []string             `json:"inReplyTo,omitempty"`
		References    []string             `json:"references,omitempty"`
		SentAt        string               `json:"sentAt,omitempty"`
		BodyStructure *bodyPart            `json:"bodyStructure,omitempty"`
		BodyValues    map[string]bodyValue `json:"bodyValues,omitempty"`
		TextBody      []bodyPart           `json:"textBody,omitempty"`
		HTMLBody      []bodyPart           `json:"htmlBody,omitempty"`
		Attachments   []bodyPart           `json:"attachments,omitempty"`
		HasAttachment bool                 `json:"hasAttachment"`
		Preview       string               `json:"preview,omitempty"`
	}
	return json.Marshal(alias{
		ID:            json.RawMessage("null"),
		BlobID:        e.BlobID,
		ThreadID:      json.RawMessage("null"),
		MailboxIDs:    json.RawMessage("null"),
		Keywords:      json.RawMessage("null"),
		Size:          e.Size,
		ReceivedAt:    json.RawMessage("null"),
		SnoozedUntil:  e.SnoozedUntil,
		Reactions:     e.Reactions,
		From:          e.From,
		To:            e.To,
		Cc:            e.Cc,
		Bcc:           e.Bcc,
		ReplyTo:       e.ReplyTo,
		Sender:        e.Sender,
		Subject:       e.Subject,
		MessageID:     e.MessageID,
		InReplyTo:     e.InReplyTo,
		References:    e.References,
		SentAt:        e.SentAt,
		BodyStructure: e.BodyStructure,
		BodyValues:    e.BodyValues,
		TextBody:      e.TextBody,
		HTMLBody:      e.HTMLBody,
		Attachments:   e.Attachments,
		HasAttachment: e.HasAttachment,
		Preview:       e.Preview,
	})
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
		Parsed:      map[string]jmapParsedEmail{},
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
		// RFC 8621 §4.9: server-set properties that do not apply to a
		// parsed blob are null (not empty string or {}).
		jm := jmapEmail{
			BlobID: blobID,
			Size:   int64(len(body)),
			// MailboxIDs / Keywords left nil → marshals as null when
			// the overall object goes through jmapParsedEmail below.
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
			// Pass empty msgBlobHash for parsed emails since they have no
			// persistent message blob hash (they're parsed from an uploaded
			// blob that may not be a stored message).
			bs, values, textParts, htmlParts, attParts := walkParts(parsed.Body, req.MaxBodyValueBytes, blobID)
			jm.BodyStructure = bs
			jm.BodyValues = values
			jm.TextBody = textParts
			jm.HTMLBody = htmlParts
			jm.Attachments = attParts
			jm.HasAttachment = len(attParts) > 0
			jm.Preview = previewFromValues(values, textParts, 256)
		}
		// Wrap in jmapParsedEmail which renders server-set fields as null.
		resp.Parsed[blobID] = jmapParsedEmail{jm}
	}
	return resp, nil
}
