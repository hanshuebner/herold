package imapimport

import (
	"io"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// CapabilityIMAPImport is the JMAP capability URI for the IMAPImport
// datatype (REQ-IMAP-IMP-61).
const CapabilityIMAPImport protojmap.CapabilityID = "https://netzhansa.com/jmap/imap-import"

// Register installs the IMAPImport datatype method handlers under
// CapabilityIMAPImport and registers the per-server capability descriptor.
//
// dataKey is the AEAD data key (loaded via secrets.LoadDataKey at boot)
// used to seal upstream credentials before storage (REQ-IMAP-IMP-70,
// decision 2). Root passes secrets.LoadDataKey(cfg.Server.Secrets) here
// in wave 5; the parameter must not be nil in production but may be a
// test key in tests.
//
// NOTE for wave 5 wiring: call Register unconditionally (IMAP import
// does not have an on/off sysconfig gate in v1; every server advertises
// the capability). The call site in internal/admin/server.go
// (composeAdminAndUI) should look like:
//
//	jmapimapimport.Register(jmapSrv.Registry(), st,
//	    logger.With("subsystem", "jmap-imap-import"), clk,
//	    imapImportDataKey) // wave-5 placeholder: pass secrets.LoadDataKey result
func Register(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	logger *slog.Logger,
	clk clock.Clock,
	dataKey []byte,
) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{
		store:   st,
		clk:     clk,
		logger:  logger,
		dataKey: dataKey,
	}
	reg.Register(CapabilityIMAPImport, getHandler{h: h})
	reg.Register(CapabilityIMAPImport, changesHandler{h: h})
	reg.Register(CapabilityIMAPImport, setHandler{h: h})
	reg.RegisterCapabilityDescriptor(CapabilityIMAPImport, struct{}{})
}
