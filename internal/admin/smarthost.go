package admin

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// BuildOutboundClient constructs a *protosmtp.Client populated with
// the operator's [smart_host] block and a strict-secrets password
// resolver (REQ-FLOW-SMARTHOST-01..08). It is the single integration
// point for the queue worker (and any future call site) that needs an
// outbound SMTP Client wired to the system.toml config.
//
// The returned Client uses the direct-MX path when SmartHost.Enabled
// is false (default behaviour, REQ-FLOW-70..76) and forks to the
// smart-host path otherwise; per-domain overrides ride on
// SmartHost.PerDomain (REQ-FLOW-SMARTHOST-02).
//
// The password resolver is materialised lazily at delivery time so
// secrets are not held in process memory between rotations
// (STANDARDS §9). Validation has already refused inline secrets at
// config-load time; the resolver here only indirects through env /
// file references.
//
// hostname is the operator's outbound EHLO name (typically
// cfg.Server.Hostname); resolver is the mailauth-shaped DNS surface
// the direct-MX path consults; clk and logger are the usual time and
// log dependencies. mtaSTSCache and tlsRPT are optional and may be
// nil; nil collapses to no-MTA-STS / no-TLS-RPT respectively.
func BuildOutboundClient(
	hostname string,
	cfg *sysconfig.Config,
	resolver mailauth.Resolver,
	mtaSTSCache protosmtp.MTASTSCache,
	tlsRPT protosmtp.TLSRPTReporter,
	clk clock.Clock,
	logger *slog.Logger,
) (*protosmtp.Client, error) {
	if cfg == nil {
		return nil, errors.New("admin: BuildOutboundClient: nil cfg")
	}
	sh := cfg.Server.SmartHost
	resolverFn, err := smartHostPasswordResolver(sh)
	if err != nil {
		return nil, fmt.Errorf("admin: smart-host password resolver: %w", err)
	}
	return protosmtp.NewClient(protosmtp.ClientOptions{
		HostName:         hostname,
		Resolver:         resolver,
		Logger:           logger,
		Clock:            clk,
		MTASTSCache:      mtaSTSCache,
		TLSRPTReporter:   tlsRPT,
		DANE:             true,
		SmartHost:        sh,
		PasswordResolver: resolverFn,
	}), nil
}

// smartHostPasswordResolver builds the per-call secret fetcher the
// outbound Client invokes when AUTH is configured. Returns nil when
// AuthMethod is "none" — the Client never calls into a nil resolver
// in that case. Top-level config wins; per-domain overrides each
// build their own resolvers once the queue worker walks PerDomain
// (the Client carries one resolver, so per-domain auth uses the
// global block's secret unless we extend the Client surface).
func smartHostPasswordResolver(sh sysconfig.SmartHostConfig) (func() (string, error), error) {
	if !sh.Enabled || sh.AuthMethod == "none" {
		return nil, nil
	}
	switch {
	case sh.PasswordEnv != "":
		ref := sh.PasswordEnv
		return func() (string, error) {
			return sysconfig.ResolveSecretStrict(ref)
		}, nil
	case sh.PasswordFile != "":
		ref := "file:" + sh.PasswordFile
		return func() (string, error) {
			return sysconfig.ResolveSecretStrict(ref)
		}, nil
	default:
		return nil, fmt.Errorf("smart_host auth_method=%q but no password_env / password_file", sh.AuthMethod)
	}
}
