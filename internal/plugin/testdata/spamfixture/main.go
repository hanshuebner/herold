// Command spamfixture is a throwaway spam-type plugin binary used only by
// internal/plugin's supervisor integration tests to exercise the
// REQ-FILT-12 temperature-pinning refusal at plugin load. It is not a
// first-party plugin (docs/design/server/architecture/07-plugin-architecture.md
// lists herold-spam-llm as the real one) and lives under testdata so `go
// build ./...` and `go vet ./...` skip it by the standard testdata
// convention; the test that uses it builds it explicitly by import path.
package main

import (
	"context"
	"os"
	"strconv"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

type handler struct{}

func (handler) OnConfigure(context.Context, map[string]any) error { return nil }
func (handler) OnHealth(context.Context) error                    { return nil }
func (handler) OnShutdown(context.Context) error                  { return nil }

func main() {
	manifest := sdk.Manifest{
		Name:       "spamfixture",
		Version:    "0.0.1",
		Type:       plug.TypeSpam,
		Lifecycle:  plug.LifecycleLongRunning,
		ABIVersion: plug.ABIVersion,
	}
	// HEROLD_TEST_SPAM_TEMPERATURE controls the declared temperature:
	// unset leaves Manifest.Temperature nil (undeclared, refused); a
	// parseable float pins it to that value ("0" is accepted, anything
	// else is refused).
	if s := os.Getenv("HEROLD_TEST_SPAM_TEMPERATURE"); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			os.Exit(2)
		}
		manifest.Temperature = &v
	}
	if err := sdk.Run(manifest, handler{}); err != nil {
		os.Exit(1)
	}
}
