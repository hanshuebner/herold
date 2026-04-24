package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ABIVersion is the plugin ABI version this build of Herold understands.
// Incompatible plugins are rejected at handshake time (REQ-PLUG-20).
const ABIVersion = 1

// JSONRPCVersion is the JSON-RPC protocol level used on stdio.
const JSONRPCVersion = "2.0"

// Standard JSON-RPC 2.0 error codes plus the Herold-reserved range
// (-32000..-32099) noted in docs/architecture/07-plugin-architecture.md.
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternalError  = -32603

	// ErrCodeHeroldBase is the first code in the Herold-defined range. All
	// codes in [-32099, -32000] are reserved for the supervisor and plugins
	// to signal implementation-defined failures.
	ErrCodeHeroldBase = -32099
	ErrCodeHeroldTop  = -32000

	// ErrCodeCancelled is returned by a plugin that observed a cancel
	// notification and aborted work in progress (REQ-PLUG-33).
	ErrCodeCancelled = -32000
	// ErrCodeTimeout is returned by the supervisor when an RPC exceeded its
	// per-call deadline before a reply arrived.
	ErrCodeTimeout = -32001
	// ErrCodeUnavailable is returned when the plugin is in a non-healthy
	// state (restarting, disabled, or shutting down).
	ErrCodeUnavailable = -32002
	// ErrCodeABIMismatch is reported when a plugin declares an ABI version
	// the server does not implement.
	ErrCodeABIMismatch = -32003
	// ErrCodeOverloaded indicates the plugin's bounded request queue is full.
	ErrCodeOverloaded = -32004
)

// Request is a JSON-RPC 2.0 request or notification. ID is nil for
// notifications. Params is opaque JSON passed through to the handler.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response. Either Result or Error is set, never
// both. ID mirrors the request ID; it is null when a request ID could not be
// determined (for example, a parse error on the incoming frame).
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is the JSON-RPC 2.0 error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface so callers can treat JSON-RPC errors
// as Go errors transparently.
func (e *Error) Error() string {
	if e == nil {
		return "<nil rpc error>"
	}
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// IsHeroldCode reports whether code is inside the Herold-reserved range.
func IsHeroldCode(code int) bool {
	return code >= ErrCodeHeroldBase && code <= ErrCodeHeroldTop
}

// Manifest is the plugin self-description returned by the initialize RPC.
// Fields match docs/requirements/11-plugins.md §Manifest.
type Manifest struct {
	Name                  string                     `json:"name"`
	Version               string                     `json:"version"`
	ABIVersion            int                        `json:"abi_version"`
	Type                  PluginType                 `json:"type"`
	Lifecycle             Lifecycle                  `json:"lifecycle"`
	Capabilities          []string                   `json:"capabilities,omitempty"`
	OptionsSchema         map[string]OptionSchema    `json:"options_schema,omitempty"`
	MaxConcurrentRequests int                        `json:"max_concurrent_requests,omitempty"`
	ShutdownGraceSec      int                        `json:"shutdown_grace_sec,omitempty"`
	HealthIntervalSec     int                        `json:"health_interval_sec,omitempty"`
	Extra                 map[string]json.RawMessage `json:"-"`
}

// OptionSchema is one entry in a Manifest's options_schema map. It is
// intentionally small: JSON-schema-ish per REQ-PLUG-21.
type OptionSchema struct {
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	Default  any    `json:"default,omitempty"`
}

// PluginType is the declared kind of a plugin (REQ-PLUG-10).
type PluginType string

// Known plugin types. Adding one requires matching SDK dispatch wiring.
const (
	TypeDNS       PluginType = "dns"
	TypeSpam      PluginType = "spam"
	TypeEvents    PluginType = "event-publisher"
	TypeDirectory PluginType = "directory"
	TypeDelivery  PluginType = "delivery-hook"
	TypeEcho      PluginType = "echo"
)

// Lifecycle describes how the supervisor runs the child process.
type Lifecycle string

// Lifecycle modes per docs/architecture/07-plugin-architecture.md.
const (
	LifecycleLongRunning Lifecycle = "long-running"
	LifecycleOnDemand    Lifecycle = "on-demand"
)

// ErrInvalidManifest is returned when manifest validation fails.
var ErrInvalidManifest = errors.New("plugin: invalid manifest")

// Validate applies the minimum structural checks; plugin-kind specific
// capability checks happen in the supervisor before a plugin is marked
// healthy.
func (m Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("%w: name empty", ErrInvalidManifest)
	}
	if m.Version == "" {
		return fmt.Errorf("%w: version empty", ErrInvalidManifest)
	}
	if m.ABIVersion != ABIVersion {
		return fmt.Errorf("%w: abi_version=%d, server=%d", ErrInvalidManifest, m.ABIVersion, ABIVersion)
	}
	switch m.Type {
	case TypeDNS, TypeSpam, TypeEvents, TypeDirectory, TypeDelivery, TypeEcho:
	default:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidManifest, m.Type)
	}
	switch m.Lifecycle {
	case LifecycleLongRunning, LifecycleOnDemand:
	default:
		return fmt.Errorf("%w: unknown lifecycle %q", ErrInvalidManifest, m.Lifecycle)
	}
	return nil
}

// InitializeParams is the payload sent by the supervisor on the initialize
// RPC. The plugin returns InitializeResult with its Manifest.
type InitializeParams struct {
	ServerVersion string `json:"server_version"`
	ABIVersion    int    `json:"abi_version"`
}

// InitializeResult is returned by the plugin from the initialize RPC.
type InitializeResult struct {
	Manifest Manifest `json:"manifest"`
}

// ConfigureParams carries the operator-supplied options map. The plugin
// validates the map against its own declared options_schema.
type ConfigureParams struct {
	Options map[string]any `json:"options"`
}

// ConfigureResult is an empty success marker. Validation failures are
// reported as JSON-RPC errors instead.
type ConfigureResult struct {
	OK bool `json:"ok"`
}

// HealthResult is the plugin's response to a health probe. OK=false puts the
// plugin in the unhealthy state; the supervisor will restart it.
type HealthResult struct {
	OK       bool   `json:"ok"`
	Reason   string `json:"reason,omitempty"`
	LatencyP int64  `json:"latency_ms_p50,omitempty"`
}

// ShutdownParams is the payload sent by the supervisor on the shutdown RPC.
type ShutdownParams struct {
	GraceSec int `json:"grace_sec,omitempty"`
}

// CancelParams is the payload sent by the supervisor to abort an in-flight
// request. Plugins should stop work and reply with ErrCodeCancelled.
type CancelParams struct {
	ID json.RawMessage `json:"id"`
}

// LogParams is the payload for plugin-to-server log notifications.
type LogParams struct {
	Level  string         `json:"level"`
	Msg    string         `json:"msg"`
	Fields map[string]any `json:"fields,omitempty"`
}

// MetricParams is the payload for plugin-to-server metric notifications.
type MetricParams struct {
	Name   string            `json:"name"`
	Kind   string            `json:"kind"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}

// NotifyParams is the payload for plugin-to-server notify notifications.
// These surface as server-level events (limited use; see architecture doc).
type NotifyParams struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Standard method names used on the stdio channel.
const (
	MethodInitialize = "initialize"
	MethodConfigure  = "configure"
	MethodHealth     = "health"
	MethodShutdown   = "shutdown"
	MethodCancel     = "cancel"

	MethodLog    = "log"
	MethodMetric = "metric"
	MethodNotify = "notify"
)
