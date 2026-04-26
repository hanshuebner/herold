package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
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
func buildOutboundQueue(
	cfg *sysconfig.Config,
	st store.Store,
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
	dsnFrom := "postmaster@" + cfg.Server.Hostname
	q := queue.New(queue.Options{
		Store:          st,
		Deliverer:      outboundDeliverer{client: client},
		Signer:         signer,
		Logger:         logger.With("subsystem", "queue"),
		Clock:          clk,
		Hostname:       cfg.Server.Hostname,
		DSNFromAddress: dsnFrom,
		ShutdownGrace:  cfg.Server.ShutdownGrace.AsDuration(),
		// Operator-supplied concurrency knobs (0 = queue built-in default).
		Concurrency: cfg.Server.Queue.Concurrency,
		PerHostMax:  cfg.Server.Queue.PerHostMax,
	})
	return q, nil
}
