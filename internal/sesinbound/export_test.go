package sesinbound

// export_test.go exposes internals needed by black-box tests in
// package sesinbound_test.  Nothing in this file is compiled into
// production binaries.

import (
	"log/slog"
	"os"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
)

// NewForTest constructs a Handler wired for unit tests:
//   - The SNS signature verifier skips x509 chain verification so tests can
//     use ephemeral self-signed certificates.
//   - The S3 client is pointed at s3EndpointURL (a stub httptest.Server).
//
// All other production logic (signature algorithm, replay dedupe, bucket
// allowlist, topic allowlist, netguard on SubscribeURL) remains active.
func NewForTest(
	cfg Config,
	pipeline Pipeline,
	seenStore SeenStore,
	audit AuditLogger,
	s3EndpointURL string,
) *Handler {
	observe.RegisterSESMetrics()
	return &Handler{
		cfg:      cfg,
		ver:      newVerifierForTest(cfg.SignatureCertHostAllowlist),
		ded:      newDeduper(seenStore, 8192, 25*time.Hour),
		fetcher:  newS3FetcherWithEndpoint(cfg.AWSRegion, cfg.AWSAccessKeyID, cfg.AWSSecretAccessKey, cfg.AWSSessionToken, cfg.S3BucketAllowlist, s3EndpointURL),
		pipeline: pipeline,
		log:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
		audit:    audit,
	}
}
