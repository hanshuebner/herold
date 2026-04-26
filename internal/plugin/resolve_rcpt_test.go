package plugin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/plugin"
)

// TestInvokeResolveRcpt_UnknownPlugin verifies the manager-level
// "plugin not registered" path classifies as ErrResolveRcptUnavailable
// — the SMTP layer maps that to defer 4.4.3 (REQ-DIR-RCPT-04).
func TestInvokeResolveRcpt_UnknownPlugin(t *testing.T) {
	mgr := plugin.NewManager(plugin.ManagerOptions{ServerVersion: "test"})
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = mgr.Shutdown(ctx)
	})
	_, err := mgr.InvokeResolveRcpt(context.Background(), "nope", directory.ResolveRcptRequest{
		Recipient: "x@y",
		Envelope:  directory.ResolveRcptEnvelope{SourceIP: "1.1.1.1"},
	})
	if err == nil {
		t.Fatalf("expected error for unknown plugin")
	}
	if !errors.Is(err, directory.ErrResolveRcptUnavailable) {
		t.Fatalf("err class: %v (want ErrResolveRcptUnavailable)", err)
	}
}
