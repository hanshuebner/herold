package email

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/mailreact"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// handlerSet bundles dependencies the per-method handlers reach for.
type handlerSet struct {
	store   store.Store
	logger  *slog.Logger
	clk     clock.Clock
	parseFn parseFn
	// hostname is [server] hostname; used as the right-hand side of
	// auto-generated Message-ID headers. Empty falls back to the
	// "localhost" sentinel in generateMessageID, which leaks into
	// outbound Message-IDs and triggers spam heuristics on some
	// receivers.
	hostname string
	// reactionMailer sends outbound cross-server reaction emails
	// (REQ-FLOW-100..103). Nil disables cross-server propagation
	// (e.g. tests that only exercise local-path behaviour).
	reactionMailer *mailreact.Mailer
	// extImg drives the on-demand external-image internalization
	// triggered when an Email/get arrives for an importer-flagged
	// message (17-external-images.md REQ-EXTIMG-93). Zero value
	// disables the path; the renderer treats InternalizePending
	// messages as if the operator had selected mode = passthrough.
	extImg extimg.Config
	// bgWG tracks background goroutines started by Email/get handlers
	// (currently writePartIndexBackground). WaitBackgroundWrites blocks
	// until all outstanding background writes complete. Tests register a
	// t.Cleanup that calls WaitBackgroundWrites before closing the store
	// so background SQLite writes cannot race t.TempDir RemoveAll.
	bgWG *sync.WaitGroup
}

// RegisterOptions carries optional configuration for the Email/* handler
// registration. Zero-value is safe for tests that do not exercise
// reaction propagation.
type RegisterOptions struct {
	// ReactionMailer enables cross-server reaction email dispatch
	// (REQ-FLOW-100..103). When nil, reactions are local-only.
	ReactionMailer *mailreact.Mailer
	// LocalDomainFn is required when ReactionMailer is non-nil. Returns
	// true when domain is locally served.
	LocalDomainFn func(ctx context.Context, domain string) bool
	// ExtImg drives on-demand external-image internalization at first
	// Email/get for messages flagged by an importer
	// (17-external-images.md REQ-EXTIMG-93). Zero-value disables the
	// path.
	ExtImg extimg.Config
	// Hostname is [server] hostname; used as the right-hand side of
	// auto-generated Message-ID headers (the JMAP setHandler path that
	// composes RFC 5322 from properties). Empty triggers the
	// "localhost" sentinel, which leaks into outbound mail; production
	// callers MUST set this.
	Hostname string
}

// Register installs the Email/* handlers under the JMAP Mail
// capability. Called from internal/admin/server.go's StartServer
// alongside the parallel agent's Core registration.
func Register(reg *protojmap.CapabilityRegistry, st store.Store, logger *slog.Logger, clk clock.Clock) {
	RegisterWithOptions(reg, st, logger, clk, RegisterOptions{})
}

// RegisterWithOptions is Register with additional optional configuration.
func RegisterWithOptions(reg *protojmap.CapabilityRegistry, st store.Store, logger *slog.Logger, clk clock.Clock, ropts RegisterOptions) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{
		store:          st,
		logger:         logger,
		clk:            clk,
		parseFn:        defaultParseFn,
		hostname:       ropts.Hostname,
		reactionMailer: ropts.ReactionMailer,
		extImg:         ropts.ExtImg,
		bgWG:           new(sync.WaitGroup),
	}
	reg.Register(protojmap.CapabilityMail, &getHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &changesHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &queryHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &queryChangesHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &setHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &copyHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &importHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &parseHandler{h: h})

	// Whole-mailbox async bulk-mutation vendor extension (issue #149/#161,
	// REQ-PROTO-40..48). Registered under its own capability so clients can
	// detect availability before routing whole-mailbox bulk actions here
	// instead of refusing them. The background drain worker
	// (BulkJobWorker, bulkjob_worker.go) is started separately by the
	// composition root (internal/admin/server.go), not by Register.
	reg.Register(protojmap.CapabilityEmailBulkMutation, setByQueryHandler{h: h})
	reg.Register(protojmap.CapabilityEmailBulkMutation, bulkJobGetHandler{h: h})
	reg.RegisterCapabilityDescriptor(protojmap.CapabilityEmailBulkMutation, struct{}{})
}

// WaitBackgroundWrites blocks until all background goroutines started by
// Email/get handlers (writePartIndexBackground) have completed. Tests register
// a t.Cleanup that calls this before closing the store so background SQLite
// writes cannot race with t.TempDir RemoveAll cleanup.
func WaitBackgroundWrites(reg *protojmap.CapabilityRegistry) {
	raw, ok := reg.Resolve("Email/get")
	if !ok {
		return
	}
	h, ok := raw.(*getHandler)
	if !ok {
		return
	}
	h.h.bgWG.Wait()
}

// SetParser overrides the body parser injected into the handlers.
// Tests use this to make body parsing deterministic without touching
// real MIME blobs. Production callers leave it alone — Register
// installs mailparse.Parse via defaultParseFn.
func SetParser(reg *protojmap.CapabilityRegistry, fn func(io.Reader) (mailparse.Message, error)) {
	// Reach into each registered Email/* handler and patch its
	// parseFn pointer. This is a controlled mutation invoked at
	// server-construction time only; concurrent invocations during
	// request dispatch are not supported and are not expected.
	for _, name := range []string{"Email/get", "Email/parse", "Email/set", "Email/import"} {
		raw, ok := reg.Resolve(name)
		if !ok {
			continue
		}
		switch h := raw.(type) {
		case *getHandler:
			h.h.parseFn = fn
		case *parseHandler:
			h.h.parseFn = fn
		case *setHandler:
			h.h.parseFn = fn
		case *importHandler:
			h.h.parseFn = fn
		}
	}
}
