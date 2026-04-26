// Package sdk is the Go plugin SDK used by first-party plugins. It
// implements the JSON-RPC 2.0 stdio contract documented in
// docs/design/architecture/07-plugin-architecture.md: handshake, configure, health,
// shutdown, plus typed sub-interfaces for DNS, spam, directory, delivery,
// and event-publisher plugin kinds.
//
// Plugins written in languages other than Go consume the JSON-RPC contract
// directly; this SDK is not a prerequisite.
//
// Example — a minimal DNS plugin:
//
//	package main
//
//	import (
//	    "context"
//
//	    "github.com/hanshuebner/herold/internal/plugin"
//	    "github.com/hanshuebner/herold/plugins/sdk"
//	)
//
//	type handler struct{}
//
//	func (h *handler) OnConfigure(ctx context.Context, opts map[string]any) error { return nil }
//	func (h *handler) OnHealth(ctx context.Context) error                         { return nil }
//	func (h *handler) OnShutdown(ctx context.Context) error                       { return nil }
//
//	func (h *handler) DNSPresent(ctx context.Context, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
//	    return sdk.DNSPresentResult{ID: "record-1"}, nil
//	}
//	func (h *handler) DNSCleanup(ctx context.Context, in sdk.DNSCleanupParams) error { return nil }
//	func (h *handler) DNSList(ctx context.Context, in sdk.DNSListParams) ([]sdk.DNSRecord, error) {
//	    return nil, nil
//	}
//	func (h *handler) DNSReplace(ctx context.Context, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
//	    return sdk.DNSPresentResult{ID: "record-1"}, nil
//	}
//
//	func main() {
//	    sdk.Run(sdk.Manifest{Name: "example-dns", Version: "1.0.0", Type: plugin.TypeDNS,
//	        Lifecycle: plugin.LifecycleLongRunning, ABIVersion: plugin.ABIVersion}, &handler{})
//	}
//
// Ownership: plugin-platform-implementor.
package sdk
