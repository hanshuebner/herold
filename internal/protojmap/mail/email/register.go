package email

import (
	"io"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// handlerSet bundles dependencies the per-method handlers reach for.
type handlerSet struct {
	store   store.Store
	logger  *slog.Logger
	clk     clock.Clock
	parseFn parseFn
}

// Register installs the Email/* handlers under the JMAP Mail
// capability. Called from internal/admin/server.go's StartServer
// alongside the parallel agent's Core registration.
func Register(reg *protojmap.CapabilityRegistry, st store.Store, logger *slog.Logger, clk clock.Clock) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{
		store:   st,
		logger:  logger,
		clk:     clk,
		parseFn: defaultParseFn,
	}
	reg.Register(protojmap.CapabilityMail, &getHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &changesHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &queryHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &queryChangesHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &setHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &copyHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &importHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &parseHandler{h: h})
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
