// Command delayspamfixture is a throwaway spam-type plugin binary used
// only by internal/plugin and internal/admin tests to make the
// StartServer-vs-plugin-configure race (re #398) deterministic instead
// of dependent on host load: HEROLD_TEST_CONFIGURE_DELAY_MS sleeps
// inside OnConfigure for the given number of milliseconds before
// returning, giving a test a plugin whose handshake+configure round
// trip takes exactly as long as the test wants. Once configured it
// always answers spam.classify with a fixed "ham" verdict so a caller
// can tell "classified" apart from "not configured" without needing a
// real model behind it. It lives under testdata so `go build ./...`
// and `go vet ./...` skip it by the standard testdata convention; the
// tests that use it build it explicitly by import path.
package main

import (
	"context"
	"os"
	"strconv"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

type handler struct{}

func (handler) OnConfigure(ctx context.Context, _ map[string]any) error {
	if ms := os.Getenv("HEROLD_TEST_CONFIGURE_DELAY_MS"); ms != "" {
		n, err := strconv.Atoi(ms)
		if err != nil {
			return err
		}
		select {
		case <-time.After(time.Duration(n) * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (handler) OnHealth(context.Context) error   { return nil }
func (handler) OnShutdown(context.Context) error { return nil }

func (handler) SpamClassify(context.Context, sdk.SpamClassifyParams) (sdk.SpamClassifyResult, error) {
	return sdk.SpamClassifyResult{
		Verdict:    "ham",
		Confidence: 0.01,
		Reason:     "delayspamfixture: always ham",
	}, nil
}

func (handler) SpamHealth(context.Context) (sdk.SpamHealthResult, error) {
	return sdk.SpamHealthResult{OK: true}, nil
}

func main() {
	temp := 0.0
	manifest := sdk.Manifest{
		Name:        "delayspamfixture",
		Version:     "0.0.1",
		Type:        plug.TypeSpam,
		Lifecycle:   plug.LifecycleLongRunning,
		ABIVersion:  plug.ABIVersion,
		Temperature: &temp,
	}
	if err := sdk.Run(manifest, handler{}); err != nil {
		os.Exit(1)
	}
}
