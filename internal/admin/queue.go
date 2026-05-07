package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// buildDKIMSigner returns a queue.Signer backed by the operator's DKIM
// key store. Returns (nil, nil) when no signer can be constructed; the
// queue tolerates a nil Signer and logs a warn for any Submission with
// Sign=true. A non-nil error indicates a misconfiguration the caller
// should surface.
//
// The queue.Signer interface (Sign(ctx, domain, message) ([]byte, error))
// is satisfied by *maildkim.Signer directly so no adapter is required.
func buildDKIMSigner(st store.Store, logger *slog.Logger, clk clock.Clock) (queue.Signer, error) {
	if st == nil {
		return nil, errors.New("admin: buildDKIMSigner: nil store")
	}
	mgr := keymgmt.NewManager(st.Meta(), logger.With("subsystem", "dkim-keymgmt"), clk, nil)
	return maildkim.NewSigner(mgr, logger.With("subsystem", "dkim-signer"), clk), nil
}

// outboundDeliverer adapts *protosmtp.Client (which implements its own
// Deliver method against protosmtp.DeliveryRequest) to queue.Deliverer
// (which calls Deliver against queue.DeliveryRequest). The adapter is a
// pure shape translator: it re-keys the recipient field and copies the
// outcome status verbatim.
//
// Lives in admin (not protosmtp) because the queue's interface is
// queue-internal and protosmtp deliberately does not import queue
// (the queue depends on protosmtp at runtime, the reverse is forbidden
// by package layering — see internal/queue/types.go and
// internal/protosmtp/outbound.go).
type outboundDeliverer struct {
	client *protosmtp.Client
}

// Deliver implements queue.Deliverer.
func (d outboundDeliverer) Deliver(ctx context.Context, req queue.DeliveryRequest) (queue.DeliveryOutcome, error) {
	if d.client == nil {
		return queue.DeliveryOutcome{
			Status: queue.DeliveryStatusTransient,
			Detail: "outbound SMTP client unavailable",
		}, errors.New("admin: outboundDeliverer: nil client")
	}
	out, err := d.client.Deliver(ctx, protosmtp.DeliveryRequest{
		MailFrom:   req.MailFrom,
		RcptTo:     req.Recipient,
		Message:    req.Message,
		REQUIRETLS: req.REQUIRETLS,
	})
	mapped := queue.DeliveryOutcome{
		Code:         out.SMTPCode,
		EnhancedCode: out.EnhancedCode,
		Detail:       out.Diagnostic,
	}
	switch out.Status {
	case protosmtp.DeliverySuccess:
		mapped.Status = queue.DeliveryStatusSuccess
	case protosmtp.DeliveryPermanent:
		mapped.Status = queue.DeliveryStatusPermanent
	case protosmtp.DeliveryTransient:
		mapped.Status = queue.DeliveryStatusTransient
	default:
		// DeliveryUnknown — treat as transient; the queue translates a
		// nil-error transient into a reschedule which is the safest
		// recovery path.
		mapped.Status = queue.DeliveryStatusTransient
		if mapped.Detail == "" {
			mapped.Detail = "deliverer returned unknown status"
		}
	}
	return mapped, err
}

// localIngester is the slice of *protosmtp.Server that loopbackDeliverer
// depends on, defined as an interface so tests can substitute a stub
// without spinning up the full SMTP server.
type localIngester interface {
	IngestBytes(ctx context.Context, req protosmtp.IngestRequest) error
}

// loopbackDeliverer wraps an outbound queue.Deliverer with a local-
// domain short-circuit: when the recipient's domain is hosted by this
// herold instance (Domain.IsLocal == true) and resolves to a known
// principal, the message is delivered into the local store via
// protosmtp.Server.IngestBytes instead of dialled out via SMTP. This
// keeps "send to a principal on this server" working in test
// deployments where example.local has no real MX, and in production
// where users on the same domain message one another without bouncing
// the body through the public internet.
//
// Non-local domains, or local domains where the recipient does not
// resolve, fall through to the wrapped outbound deliverer unchanged
// (the outbound path then surfaces "no such recipient" or "no MX" the
// way it always has).
type loopbackDeliverer struct {
	inner queue.Deliverer
	smtp  localIngester
	meta  store.Metadata
	dir   addressResolver
	log   *slog.Logger
}

// addressResolver is the slice of *directory.Directory loopbackDeliverer
// uses; carved out as an interface for the same testability reason as
// localIngester.
type addressResolver interface {
	ResolveAddress(ctx context.Context, local, domain string) (directory.PrincipalID, error)
}

// Deliver implements queue.Deliverer.
func (d loopbackDeliverer) Deliver(ctx context.Context, req queue.DeliveryRequest) (queue.DeliveryOutcome, error) {
	local, dom := splitEmailAddr(req.Recipient)
	if dom == "" || d.smtp == nil || d.meta == nil || d.dir == nil {
		return d.inner.Deliver(ctx, req)
	}
	domLower := strings.ToLower(dom)
	domRow, err := d.meta.GetDomain(ctx, domLower)
	if err != nil || !domRow.IsLocal {
		return d.inner.Deliver(ctx, req)
	}
	pid, rerr := d.dir.ResolveAddress(ctx, strings.ToLower(local), domLower)
	if rerr != nil {
		if errors.Is(rerr, directory.ErrNotFound) {
			// Domain is local but the recipient doesn't exist — this is
			// a permanent failure. Returning it lets the queue emit a
			// proper bounce DSN to the sender instead of retrying for
			// hours against MX records that don't exist.
			return queue.DeliveryOutcome{
				Status: queue.DeliveryStatusPermanent,
				Detail: "no such recipient on this server",
			}, nil
		}
		// Anything else (a transient store error) reschedules.
		return queue.DeliveryOutcome{
			Status: queue.DeliveryStatusTransient,
			Detail: "directory lookup failed: " + rerr.Error(),
		}, nil
	}
	if d.log != nil {
		d.log.InfoContext(ctx, "queue: loopback delivery",
			slog.String("recipient", req.Recipient),
			slog.String("mail_from", req.MailFrom))
	}
	if err := d.smtp.IngestBytes(ctx, protosmtp.IngestRequest{
		Body:         req.Message,
		MailFrom:     req.MailFrom,
		SourceIP:     "127.0.0.1",
		IngestSource: "loopback",
		Recipients: []protosmtp.IngestRecipient{{
			Addr:        req.Recipient,
			PrincipalID: pid,
		}},
	}); err != nil {
		return queue.DeliveryOutcome{
			Status: queue.DeliveryStatusTransient,
			Detail: "loopback ingest failed: " + err.Error(),
		}, err
	}
	return queue.DeliveryOutcome{Status: queue.DeliveryStatusSuccess}, nil
}

// buildOutboundQueue constructs the production *queue.Queue alongside
// the DKIM signer and SMTP-client deliverer. Returns the queue handle
// and a non-nil error on construction failure.
//
// The queue does not start its scheduler on New; the caller registers
// q.Run against the lifecycle errgroup.
//
// reporter is the autodns.Reporter wired for TLS-RPT failure ingestion;
// nil means no TLS-RPT reporting. When non-nil the reporter's Append
// method is called on every outbound TLS failure recorded by the SMTP
// client.
//
// smtpServer + dir, when both non-nil, install the loopback short-
// circuit on the deliverer chain so messages addressed to local
// principals stay on this host instead of being MX-resolved.
func buildOutboundQueue(
	cfg *sysconfig.Config,
	st store.Store,
	dir *directory.Directory,
	smtpServer *protosmtp.Server,
	resolver mailauth.Resolver,
	reporter protosmtp.TLSRPTReporter,
	logger *slog.Logger,
	clk clock.Clock,
) (*queue.Queue, error) {
	if cfg == nil {
		return nil, errors.New("admin: buildOutboundQueue: nil cfg")
	}
	if st == nil {
		return nil, errors.New("admin: buildOutboundQueue: nil store")
	}
	signer, err := buildDKIMSigner(st, logger, clk)
	if err != nil {
		return nil, fmt.Errorf("admin: build DKIM signer: %w", err)
	}
	client, err := BuildOutboundClient(
		cfg.Server.Hostname,
		cfg,
		resolver,
		nil,      // MTASTSCache: optional, none wired in production today
		reporter, // TLSRPTReporter: wired by the caller
		clk,
		logger.With("subsystem", "smtp-outbound"),
	)
	if err != nil {
		return nil, fmt.Errorf("admin: build outbound smtp client: %w", err)
	}
	var deliverer queue.Deliverer = outboundDeliverer{client: client}
	if smtpServer != nil && dir != nil {
		deliverer = loopbackDeliverer{
			inner: deliverer,
			smtp:  smtpServer,
			meta:  st.Meta(),
			dir:   dir,
			log:   logger.With("subsystem", "queue-loopback"),
		}
	}
	dsnFrom := "postmaster@" + cfg.Server.Hostname
	q := queue.New(queue.Options{
		Store:          st,
		Deliverer:      deliverer,
		Signer:         signer,
		Logger:         logger.With("subsystem", "queue"),
		Clock:          clk,
		Hostname:       cfg.Server.Hostname,
		DSNFromAddress: dsnFrom,
		ShutdownGrace:  cfg.Server.ShutdownGrace.AsDuration(),
		// Operator-supplied concurrency knobs (0 = queue built-in default).
		Concurrency:       cfg.Server.Queue.Concurrency,
		PerHostMax:        cfg.Server.Queue.PerHostMax,
		DelayDSNThreshold: cfg.Server.Queue.DelayDSNThreshold.AsDuration(),
	})
	return q, nil
}
