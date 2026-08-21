package fileshare

import (
	"io"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// Register installs the FileShare datatype's method handlers under the
// CapabilityFileShares ("https://netzhansa.com/jmap/file-shares")
// capability and registers the per-server capability descriptor.
//
// The caller MUST gate this Register call behind the
// [server.attachment_shares].enabled sysconfig flag (REQ-SHARE-40):
// when false Register is not called and the capability does not appear
// in the session descriptor; the suite SPA hides the offload affordance.
//
// publicBaseURL is the externally-reachable HTTPS origin for public share
// URLs (e.g. "https://mail.example.com"). cfg carries the per-deployment
// lifecycle and quota values read from sysconfig. Neither sysconfig type
// nor any sysconfig import lives in this package.
func Register(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	logger *slog.Logger,
	clk clock.Clock,
	cfg store.FileSharesConfig,
	publicBaseURL string,
) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{
		store:         st,
		clk:           clk,
		logger:        logger,
		cfg:           cfg,
		publicBaseURL: publicBaseURL,
	}
	reg.Register(protojmap.CapabilityFileShares, getHandler{h: h})
	reg.Register(protojmap.CapabilityFileShares, changesHandler{h: h})
	reg.Register(protojmap.CapabilityFileShares, setHandler{h: h})
	reg.Register(protojmap.CapabilityFileShares, queryHandler{h: h})
	// The capability descriptor advertises the deployment's configured
	// default_ttl_seconds and max_ttl_seconds (and quota_max_bytes) so
	// the composer's expiry picker reflects the operator's sysconfig
	// rather than client-side constants. Per-request caps are still
	// re-read from cfg by the handlers; this descriptor is captured
	// once at Register (boot) time, consistent with the other static
	// capability descriptors in this server.
	reg.RegisterCapabilityDescriptor(protojmap.CapabilityFileShares, newCapabilityDescriptor(cfg))
}
