package admin

import (
	"context"
	"net"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// stubResolver satisfies mailauth.Resolver with no-op methods. Smart-
// host BuildOutboundClient does not exercise the resolver; this is
// only here to satisfy the constructor's required-field check.
type stubResolver struct{}

func (stubResolver) TXTLookup(ctx context.Context, name string) ([]string, error) {
	return nil, nil
}
func (stubResolver) MXLookup(ctx context.Context, domain string) ([]*net.MX, error) {
	return nil, nil
}
func (stubResolver) IPLookup(ctx context.Context, host string) ([]net.IP, error) {
	return nil, nil
}

var _ mailauth.Resolver = stubResolver{}

func TestBuildOutboundClient_DisabledSmartHost(t *testing.T) {
	cfg := &sysconfig.Config{}
	c, err := BuildOutboundClient("client.test", cfg, stubResolver{}, nil, nil, clock.NewReal(), nil)
	if err != nil {
		t.Fatalf("BuildOutboundClient: %v", err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
}

func TestBuildOutboundClient_PasswordEnv(t *testing.T) {
	t.Setenv("HEROLD_TEST_SH_PW", "secret")
	cfg := &sysconfig.Config{}
	cfg.Server.SmartHost = sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        "smtp.example.com",
		Port:                        587,
		TLSMode:                     "starttls",
		AuthMethod:                  "plain",
		Username:                    "u",
		PasswordEnv:                 "$HEROLD_TEST_SH_PW",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "system_roots",
	}
	c, err := BuildOutboundClient("client.test", cfg, stubResolver{}, nil, nil, clock.NewReal(), nil)
	if err != nil {
		t.Fatalf("BuildOutboundClient: %v", err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
}

func TestBuildOutboundClient_PasswordEnvMissing(t *testing.T) {
	cfg := &sysconfig.Config{}
	cfg.Server.SmartHost = sysconfig.SmartHostConfig{
		Enabled:                     true,
		Host:                        "smtp.example.com",
		Port:                        587,
		TLSMode:                     "starttls",
		AuthMethod:                  "plain",
		Username:                    "u",
		FallbackPolicy:              "smart_host_only",
		ConnectTimeoutSeconds:       5,
		ReadTimeoutSeconds:          5,
		FallbackAfterFailureSeconds: 300,
		TLSVerifyMode:               "system_roots",
	}
	if _, err := BuildOutboundClient("client.test", cfg, stubResolver{}, nil, nil, clock.NewReal(), nil); err == nil {
		t.Fatal("expected error: no env or file ref")
	}
}
