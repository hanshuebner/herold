package plugin_test

// client_semaphore_test.go pins the fix for a semaphore-corruption panic
// observed when exercising herold-spam-llm through the production
// admin.StartServer wiring (re #302): Manager.Start launches the
// supervise goroutine and returns without waiting for the plugin to
// finish its handshake, so a long-running plugin's Client can already be
// in use (via Plugin.Call) while its own Initialize/Configure calls are
// still holding the handshake-time semaphore that SetMaxConcurrent later
// replaces with a differently-sized one. Client.Call used to read c.sem
// twice -- once for Acquire, once implicitly via a deferred method value
// -- so a SetMaxConcurrent swap landing between those two reads made the
// deferred Release target an instance that never granted the permit,
// and golang.org/x/sync/semaphore panics with "released more than held".
//
// This test drives many concurrent Call()s against a loopback fake
// server while a separate goroutine repeatedly calls SetMaxConcurrent,
// which reproduced the panic reliably before the fix and must run clean
// (particularly under -race) afterwards.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/plugin"
)

// loopbackPluginServer reads JSON-RPC requests off r and immediately
// answers every one carrying an ID with an empty-result response on w. It
// stands in for a plugin process without needing a real subprocess.
func loopbackPluginServer(t *testing.T, r io.Reader, w io.Writer) {
	t.Helper()
	fr := plugin.NewFrameReader(r, 0)
	fw := plugin.NewFrameWriter(w)
	for {
		frame, err := fr.ReadFrame()
		if err != nil {
			return
		}
		var req plugin.Request
		if err := json.Unmarshal(frame, &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue // notification (e.g. cancel); nothing to answer.
		}
		_ = fw.WriteFrame(plugin.Response{
			JSONRPC: plugin.JSONRPCVersion,
			ID:      req.ID,
			Result:  json.RawMessage(`{}`),
		})
	}
}

// TestClient_CallSurvivesConcurrentSetMaxConcurrent hammers Call and
// SetMaxConcurrent concurrently and asserts no worker goroutine panics.
func TestClient_CallSurvivesConcurrentSetMaxConcurrent(t *testing.T) {
	// Two full-duplex in-memory pipes: one carries client->server bytes,
	// the other server->client bytes, matching the plugin's stdin/stdout
	// wiring (separate Reader and Writer) that Client expects.
	c2sServer, c2sClient := net.Pipe()
	s2cServer, s2cClient := net.Pipe()
	t.Cleanup(func() {
		_ = c2sServer.Close()
		_ = c2sClient.Close()
		_ = s2cServer.Close()
		_ = s2cClient.Close()
	})

	go loopbackPluginServer(t, c2sServer, s2cServer)

	client := plugin.NewClient(s2cClient, c2sClient, plugin.ClientOptions{
		Name:          "loopback-test",
		MaxConcurrent: 1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = client.Run(ctx) }()

	const workers = 8
	const duration = 300 * time.Millisecond
	deadline := time.Now().Add(duration)

	done := make(chan struct{})
	errCh := make(chan string, workers+1)

	for i := 0; i < workers; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errCh <- fmt.Sprintf("Call panicked: %v", r)
				}
				done <- struct{}{}
			}()
			for time.Now().Before(deadline) {
				callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = client.Call(callCtx, "test.echo", nil, nil)
				callCancel()
			}
		}()
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Sprintf("SetMaxConcurrent goroutine panicked: %v", r)
			}
			done <- struct{}{}
		}()
		n := int64(1)
		for time.Now().Before(deadline) {
			n = n%8 + 1
			client.SetMaxConcurrent(n)
		}
	}()

	for i := 0; i < workers+1; i++ {
		<-done
	}
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
}
