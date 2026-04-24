// Command herold-echo is a first-party plugin that exercises the SDK's
// handshake, configure, health, shutdown, and custom-method dispatch
// surfaces end-to-end. The integration test in internal/plugin drives this
// binary through every documented lifecycle transition.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

type echoHandler struct {
	options map[string]any
}

func (h *echoHandler) OnConfigure(_ context.Context, opts map[string]any) error {
	h.options = opts
	return nil
}

func (h *echoHandler) OnHealth(_ context.Context) error { return nil }

func (h *echoHandler) OnShutdown(_ context.Context) error { return nil }

// HandleCustom dispatches the plugin-specific methods. Treated by the SDK
// as a side-channel when the plugin's declared Type does not pin a fixed
// method set.
func (h *echoHandler) HandleCustom(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "echo.Ping":
		var p struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return map[string]any{"msg": p.Msg}, nil
	case "echo.Sleep":
		var p struct {
			Ms int `json:"ms"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		select {
		case <-time.After(time.Duration(p.Ms) * time.Millisecond):
			return map[string]any{"slept_ms": p.Ms}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	default:
		return nil, sdk.ErrMethodNotFound
	}
}

func main() {
	manifest := sdk.Manifest{
		Name:                  "herold-echo",
		Version:               "0.1.0",
		Type:                  plug.TypeEcho,
		Lifecycle:             plug.LifecycleLongRunning,
		MaxConcurrentRequests: 4,
		ABIVersion:            plug.ABIVersion,
		ShutdownGraceSec:      5,
		HealthIntervalSec:     30,
	}
	sdk.Logf("info", "echo plugin ready version=%s", manifest.Version)
	if err := sdk.Run(manifest, &echoHandler{}); err != nil {
		fmt.Fprintf(os.Stderr, "herold-echo: %v\n", err)
		os.Exit(1)
	}
}
