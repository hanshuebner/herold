package mailreact

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/textproto"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// ReactorInfo describes the principal who added the reaction.
type ReactorInfo struct {
	// PrincipalID is the local principal.
	PrincipalID store.PrincipalID
	// Address is the reactor's primary email address (the MAIL FROM /
	// From: header value).
	Address string
	// DisplayName is the reactor's human-readable name for the body
	// fallback text.
	DisplayName string
	// Domain is the lowercased domain portion of Address.
	Domain string
}

// OriginalEmailInfo carries the metadata from the original email needed
// to build the reaction email's headers.
type OriginalEmailInfo struct {
	// MessageID is the Message-ID header value of the original email
	// (without angle brackets).
	MessageID string
	// Subject is the original Subject header value.
	Subject string
	// References is the original References header value (may be empty).
	References string
	// AllRecipients is every address that received the original email
	// (To + Cc + Bcc flat list, used to find non-local recipients).
	AllRecipients []string
}

// Dispatcher enqueues outbound reaction emails. Injected so tests can
// verify queuing behaviour without a real queue worker.
type Dispatcher interface {
	Submit(ctx context.Context, msg queue.Submission) (queue.EnvelopeID, error)
}

// Options configures the reaction mailer.
type Options struct {
	// LocalDomainFn returns true when domain is served locally.
	// Required: skips recipients on local domains (REQ-FLOW-100).
	LocalDomainFn func(ctx context.Context, domain string) bool
	// Dispatcher is the outbound queue.
	Dispatcher Dispatcher
	// Hostname is the local server hostname used to generate fresh
	// Message-ID values.
	Hostname string
	// Logger is optional; defaults to slog.Default.
	Logger *slog.Logger
	// Clock is optional; defaults to clock.NewReal.
	Clock clock.Clock
}

// Mailer sends reaction emails for cross-server propagation.
type Mailer struct {
	opts Options
	log  *slog.Logger
	clk  clock.Clock
}

// New constructs a Mailer.  Panics when opts.LocalDomainFn or
// opts.Dispatcher are nil.
func New(opts Options) *Mailer {
	if opts.LocalDomainFn == nil {
		panic("mailreact.New: LocalDomainFn is required")
	}
	if opts.Dispatcher == nil {
		panic("mailreact.New: Dispatcher is required")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}
	return &Mailer{opts: opts, log: log, clk: clk}
}

// BuildAndEnqueue constructs one outbound reaction email per external
// recipient and submits them to the outbound queue.  Local recipients
// are skipped (they see the reaction natively via Email.reactions).
// The function returns after all queue items have been enqueued; the
// actual SMTP delivery is asynchronous.
//
// Returns the number of queue items enqueued.
func (m *Mailer) BuildAndEnqueue(
	ctx context.Context,
	reactor ReactorInfo,
	emoji string,
	orig OriginalEmailInfo,
) (int, error) {
	observe.RegisterReactionMetrics()

	now := m.clk.Now()
	var externalRcpts []string
	for _, addr := range orig.AllRecipients {
		domain := domainOf(addr)
		if domain == "" {
			continue
		}
		if m.opts.LocalDomainFn(ctx, domain) {
			observe.ReactionOutboundTotal.WithLabelValues("skipped_local").Inc()
			continue
		}
		externalRcpts = append(externalRcpts, addr)
	}
	if len(externalRcpts) == 0 {
		return 0, nil
	}

	body, err := buildReactionBody(reactor.DisplayName, reactor.Address, emoji, orig, now, m.opts.Hostname)
	if err != nil {
		return 0, fmt.Errorf("mailreact: build body: %w", err)
	}

	pid := reactor.PrincipalID
	sub := queue.Submission{
		PrincipalID:   &pid,
		MailFrom:      reactor.Address,
		Recipients:    externalRcpts,
		Body:          bytes.NewReader(body),
		Sign:          true,
		SigningDomain: reactor.Domain,
	}
	if _, err := m.opts.Dispatcher.Submit(ctx, sub); err != nil {
		return 0, fmt.Errorf("mailreact: enqueue: %w", err)
	}

	for range externalRcpts {
		observe.ReactionOutboundTotal.WithLabelValues("queued").Inc()
	}
	m.log.InfoContext(ctx, "mailreact: queued reaction email",
		slog.String("emoji", emoji),
		slog.String("reactor", reactor.Address),
		slog.Int("recipients", len(externalRcpts)),
	)
	return len(externalRcpts), nil
}

// buildReactionBody assembles the RFC 5322 reaction email bytes.
func buildReactionBody(
	displayName, reactorAddr, emoji string,
	orig OriginalEmailInfo,
	now time.Time,
	hostname string,
) ([]byte, error) {
	var buf bytes.Buffer
	newMsgID := fmt.Sprintf("<%d.reaction@%s>", now.UnixNano(), hostname)
	subject := orig.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	// Build References: original References + original Message-ID.
	var refs string
	origMsgID := "<" + orig.MessageID + ">"
	if orig.References != "" {
		refs = orig.References + " " + origMsgID
	} else {
		refs = origMsgID
	}

	// Build multipart/alternative body.
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)
	boundary := mw.Boundary()

	plainHdr := textproto.MIMEHeader{}
	plainHdr.Set("Content-Type", "text/plain; charset=utf-8")
	pw, err := mw.CreatePart(plainHdr)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(pw, "%s reacted with %s to your message.\r\n", displayName, emoji)

	htmlHdr := textproto.MIMEHeader{}
	htmlHdr.Set("Content-Type", "text/html; charset=utf-8")
	hw, err := mw.CreatePart(htmlHdr)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(hw, "<p>%s reacted with <span style=\"font-size:1.5em\">%s</span> to your message.</p>\r\n",
		htmlEscape(displayName), emoji)

	if err := mw.Close(); err != nil {
		return nil, err
	}

	// Write RFC 5322 headers.
	fmt.Fprintf(&buf, "From: %s\r\n", reactorAddr)
	fmt.Fprintf(&buf, "Date: %s\r\n", now.UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000"))
	fmt.Fprintf(&buf, "Message-ID: %s\r\n", newMsgID)
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	fmt.Fprintf(&buf, "In-Reply-To: %s\r\n", origMsgID)
	fmt.Fprintf(&buf, "References: %s\r\n", refs)
	fmt.Fprintf(&buf, "X-Tabard-Reaction-To: %s\r\n", origMsgID)
	fmt.Fprintf(&buf, "X-Tabard-Reaction-Emoji: %s\r\n", emoji)
	fmt.Fprintf(&buf, "X-Tabard-Reaction-Action: add\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=%q\r\n", boundary)
	fmt.Fprintf(&buf, "\r\n")
	if _, err := io.Copy(&buf, &bodyBuf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// domainOf extracts the lowercased domain part of addr.
func domainOf(addr string) string {
	i := strings.LastIndex(addr, "@")
	if i < 0 {
		return ""
	}
	return strings.ToLower(addr[i+1:])
}

// htmlEscape escapes the minimal HTML entities needed in the body fallback.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
