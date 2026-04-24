package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
)

// Manifest is the plugin self-description returned by the initialize RPC.
// It is a convenience type layered over plug.Manifest so plugin authors do
// not have to learn the supervisor-side package.
type Manifest struct {
	Name                  string
	Version               string
	Type                  plug.PluginType
	Lifecycle             plug.Lifecycle
	MaxConcurrentRequests int
	ABIVersion            int
	Capabilities          []string
	OptionsSchema         map[string]plug.OptionSchema
	ShutdownGraceSec      int
	HealthIntervalSec     int
}

func (m Manifest) toProtocol() plug.Manifest {
	abi := m.ABIVersion
	if abi == 0 {
		abi = plug.ABIVersion
	}
	return plug.Manifest{
		Name:                  m.Name,
		Version:               m.Version,
		ABIVersion:            abi,
		Type:                  m.Type,
		Lifecycle:             m.Lifecycle,
		Capabilities:          m.Capabilities,
		OptionsSchema:         m.OptionsSchema,
		MaxConcurrentRequests: m.MaxConcurrentRequests,
		ShutdownGraceSec:      m.ShutdownGraceSec,
		HealthIntervalSec:     m.HealthIntervalSec,
	}
}

// Handler is the minimum surface every plugin exposes. Kind-specific
// plugins additionally implement one of the sub-interfaces below (for
// example DNSHandler). Custom plugins may implement CustomHandler to
// dispatch arbitrary type-specific methods — see herold-echo for the
// canonical example.
type Handler interface {
	OnConfigure(ctx context.Context, opts map[string]any) error
	OnHealth(ctx context.Context) error
	OnShutdown(ctx context.Context) error
}

// DNSHandler corresponds to the dns.* methods in docs/requirements/11-plugins.md.
type DNSHandler interface {
	DNSPresent(ctx context.Context, in DNSPresentParams) (DNSPresentResult, error)
	DNSCleanup(ctx context.Context, in DNSCleanupParams) error
	DNSList(ctx context.Context, in DNSListParams) ([]DNSRecord, error)
	DNSReplace(ctx context.Context, in DNSPresentParams) (DNSPresentResult, error)
}

// SpamHandler corresponds to the spam.* methods.
type SpamHandler interface {
	SpamClassify(ctx context.Context, in SpamClassifyParams) (SpamClassifyResult, error)
	SpamHealth(ctx context.Context) (SpamHealthResult, error)
}

// EventsHandler corresponds to the events.* methods.
type EventsHandler interface {
	EventsSubscribe(ctx context.Context, in EventsSubscribeParams) (EventsSubscribeResult, error)
	EventsPublish(ctx context.Context, in EventsPublishParams) (EventsPublishResult, error)
	EventsHealth(ctx context.Context) (EventsHealthResult, error)
}

// DirectoryHandler corresponds to the directory.* methods.
type DirectoryHandler interface {
	DirectoryLookup(ctx context.Context, in DirectoryLookupParams) (DirectoryLookupResult, error)
	DirectoryAuthenticate(ctx context.Context, in DirectoryAuthenticateParams) (DirectoryAuthenticateResult, error)
	DirectoryListAliases(ctx context.Context, in DirectoryListAliasesParams) ([]string, error)
}

// DeliveryHandler corresponds to the delivery.* methods.
type DeliveryHandler interface {
	DeliveryPre(ctx context.Context, in DeliveryPreParams) (DeliveryPreResult, error)
	DeliveryPost(ctx context.Context, in DeliveryPostParams) error
}

// CustomHandler lets a plugin declare arbitrary type-specific methods
// beyond the fixed per-type RPCs. Return (nil, ErrMethodNotFound) for
// methods the plugin does not recognise; the SDK translates that into a
// JSON-RPC method-not-found error on the wire.
type CustomHandler interface {
	HandleCustom(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// ErrMethodNotFound is the sentinel a CustomHandler returns for unknown
// methods. The SDK maps it to JSON-RPC code ErrCodeMethodNotFound.
var ErrMethodNotFound = errors.New("sdk: method not found")

// Param / result types. One per RPC method.

// DNSPresentParams carries a zone record to create or upsert.
type DNSPresentParams struct {
	Zone       string `json:"zone"`
	RecordType string `json:"record_type"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	TTL        int    `json:"ttl"`
}

// DNSPresentResult carries the opaque id used for later cleanup.
type DNSPresentResult struct {
	ID string `json:"id"`
}

// DNSCleanupParams targets a previously-created record.
type DNSCleanupParams struct {
	ID string `json:"id"`
}

// DNSListParams is the filter for the list RPC.
type DNSListParams struct {
	Zone       string `json:"zone"`
	RecordType string `json:"record_type"`
	Name       string `json:"name,omitempty"`
}

// DNSRecord is one entry returned by the list RPC.
type DNSRecord struct {
	ID    string `json:"id"`
	Value string `json:"value"`
	TTL   int    `json:"ttl"`
}

// SpamClassifyParams is the payload for spam.classify. The shape
// mirrors internal/spam.Request field-for-field (flat keys, same JSON
// tags) so the supervisor and plugin agree on the wire format without
// a translation layer. Keeping it flat reads better in the LLM prompt
// than a nested envelope/headers map would.
type SpamClassifyParams struct {
	From         []string `json:"from"`
	To           []string `json:"to"`
	Cc           []string `json:"cc,omitempty"`
	Subject      string   `json:"subject"`
	ReceivedDate string   `json:"received_date,omitempty"`
	DKIMPass     bool     `json:"dkim_pass"`
	SPFPass      bool     `json:"spf_pass"`
	DMARCPass    bool     `json:"dmarc_pass"`
	FromDomain   string   `json:"from_domain,omitempty"`
	BodyExcerpt  string   `json:"body_excerpt"`
}

// SpamClassifyResult is the verdict for one message.
type SpamClassifyResult struct {
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason,omitempty"`
}

// SpamHealthResult is the shape spam.health returns.
type SpamHealthResult struct {
	OK         bool  `json:"ok"`
	LatencyMsP int64 `json:"latency_ms_p50,omitempty"`
}

// EventsSubscribeParams is sent once at configure time.
type EventsSubscribeParams struct {
	Types  []string       `json:"types"`
	Filter map[string]any `json:"filter,omitempty"`
}

// EventsSubscribeResult carries the subscription ack.
type EventsSubscribeResult struct {
	Ack bool `json:"ack"`
}

// EventsPublishParams is one event delivered via events.publish.
type EventsPublishParams struct {
	Event map[string]any `json:"event"`
}

// EventsPublishResult is the per-event plugin-side receipt.
type EventsPublishResult struct {
	Ack bool `json:"ack"`
}

// EventsHealthResult is the shape events.health returns.
type EventsHealthResult struct {
	OK        bool  `json:"ok"`
	BacklogMs int64 `json:"backlog_ms,omitempty"`
}

// DirectoryLookupParams is the email argument for directory.lookup.
type DirectoryLookupParams struct {
	Email string `json:"email"`
}

// DirectoryLookupResult carries the principal if one was found.
type DirectoryLookupResult struct {
	Principal map[string]any `json:"principal,omitempty"`
}

// DirectoryAuthenticateParams carries a credential.
type DirectoryAuthenticateParams struct {
	Credential map[string]any `json:"credential"`
}

// DirectoryAuthenticateResult carries the principal id or a refusal reason.
type DirectoryAuthenticateResult struct {
	PrincipalID string `json:"principal_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// DirectoryListAliasesParams targets a mailbox.
type DirectoryListAliasesParams struct {
	Email string `json:"email"`
}

// DeliveryPreParams is the synchronous pre-delivery query.
type DeliveryPreParams struct {
	Envelope map[string]any `json:"envelope"`
	Headers  map[string]any `json:"headers"`
}

// DeliveryPreResult carries the gating decision.
type DeliveryPreResult struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// DeliveryPostParams is the fire-and-forget post-delivery notice.
type DeliveryPostParams struct {
	Envelope map[string]any `json:"envelope"`
	Headers  map[string]any `json:"headers"`
	Verdict  string         `json:"verdict"`
	Mailbox  string         `json:"mailbox"`
}

// DNS RPC method names.
const (
	MethodDNSPresent = "dns.present"
	MethodDNSCleanup = "dns.cleanup"
	MethodDNSList    = "dns.list"
	MethodDNSReplace = "dns.replace"

	MethodSpamClassify = "spam.classify"
	MethodSpamHealth   = "spam.health"

	MethodEventsSubscribe = "events.subscribe"
	MethodEventsPublish   = "events.publish"
	MethodEventsHealth    = "events.health"

	MethodDirectoryLookup       = "directory.lookup"
	MethodDirectoryAuthenticate = "directory.authenticate"
	MethodDirectoryListAliases  = "directory.list_aliases"

	MethodDeliveryPre  = "delivery.pre"
	MethodDeliveryPost = "delivery.post"
)

// writerMu guards every write to the outbound stream (responses and
// notifications). Plugin code is free to call Logf / Metric / Notify from
// multiple goroutines concurrently.
var (
	writerMu sync.Mutex
	// outWriter is the io.Writer notifications and responses are flushed to.
	// It is settable from tests and otherwise defaults to os.Stdout.
	outWriter io.Writer = os.Stdout
)

// SetOutputWriter replaces the outbound writer. Passing nil resets to
// os.Stdout. Exposed for test harnesses; production plugins never call this.
func SetOutputWriter(w io.Writer) {
	writerMu.Lock()
	defer writerMu.Unlock()
	if w == nil {
		outWriter = os.Stdout
		return
	}
	outWriter = w
}

// SetInputReader replaces the inbound reader. Passing nil resets to
// os.Stdin. Exposed for test harnesses; production plugins never call this.
func SetInputReader(r io.Reader) {
	inputMu.Lock()
	defer inputMu.Unlock()
	if r == nil {
		inReader = os.Stdin
		return
	}
	inReader = r
}

var (
	inputMu  sync.Mutex
	inReader io.Reader = os.Stdin
)

func writeFrame(v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("sdk: marshal frame: %w", err)
	}
	writerMu.Lock()
	defer writerMu.Unlock()
	if _, err := outWriter.Write(buf); err != nil {
		return err
	}
	if _, err := outWriter.Write([]byte{'\n'}); err != nil {
		return err
	}
	if f, ok := outWriter.(interface{ Flush() error }); ok {
		_ = f.Flush()
	}
	return nil
}

// Logf emits a plugin.log notification to the supervisor. Level values
// follow slog conventions: "debug", "info", "warn", "error".
func Logf(level, format string, args ...any) {
	_ = writeFrame(plug.Request{
		JSONRPC: plug.JSONRPCVersion,
		Method:  plug.MethodLog,
		Params:  mustMarshal(plug.LogParams{Level: level, Msg: fmt.Sprintf(format, args...)}),
	})
}

// Metric emits a plugin.metric notification. Kind defaults to "counter".
func Metric(name string, labels map[string]string, value float64) {
	_ = writeFrame(plug.Request{
		JSONRPC: plug.JSONRPCVersion,
		Method:  plug.MethodMetric,
		Params:  mustMarshal(plug.MetricParams{Name: name, Kind: "counter", Value: value, Labels: labels}),
	})
}

// Notify emits a plugin.notify notification.
func Notify(kind string, payload map[string]any) {
	_ = writeFrame(plug.Request{
		JSONRPC: plug.JSONRPCVersion,
		Method:  plug.MethodNotify,
		Params:  mustMarshal(plug.NotifyParams{Type: kind, Payload: payload}),
	})
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// Dropping a log frame is strictly better than panicking the plugin.
		return json.RawMessage(`{}`)
	}
	return b
}

// Run is the plugin main() body. It blocks until EOF on stdin, a shutdown
// RPC, or SIGTERM, then returns. Non-nil return values always indicate an
// unrecoverable condition — plugins should log and exit non-zero.
func Run(manifest Manifest, handler Handler) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	shutdownCh := make(chan struct{})
	var shutdownOnce sync.Once
	signalShutdown := func() { shutdownOnce.Do(func() { close(shutdownCh) }) }

	// Signal handler goroutine: exits once cancel or shutdownCh is signalled.
	go func() {
		select {
		case <-sigCh:
			signalShutdown()
		case <-shutdownCh:
			return
		case <-ctx.Done():
			return
		}
	}()

	inputMu.Lock()
	in := inReader
	inputMu.Unlock()
	fr := plug.NewFrameReader(in, plug.DefaultMaxFrameBytes)

	for {
		select {
		case <-shutdownCh:
			return runShutdown(handler, manifest)
		default:
		}

		raw, err := fr.ReadFrame()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return runShutdown(handler, manifest)
			}
			return err
		}

		var req plug.Request
		if err := json.Unmarshal(raw, &req); err != nil {
			writeErr(nil, plug.ErrCodeParseError, "parse error: "+err.Error())
			continue
		}

		// Build a per-request context, wiring timeout_ms when present.
		reqCtx := ctx
		var reqCancel context.CancelFunc = func() {}
		if dl, ok := extractTimeout(req.Params); ok {
			reqCtx, reqCancel = context.WithTimeout(ctx, dl)
		}

		switch req.Method {
		case plug.MethodInitialize:
			writeResult(req.ID, plug.InitializeResult{Manifest: manifest.toProtocol()})
		case plug.MethodConfigure:
			var p plug.ConfigureParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
				break
			}
			if err := handler.OnConfigure(reqCtx, p.Options); err != nil {
				writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
				break
			}
			writeResult(req.ID, plug.ConfigureResult{OK: true})
		case plug.MethodHealth:
			if err := handler.OnHealth(reqCtx); err != nil {
				writeResult(req.ID, plug.HealthResult{OK: false, Reason: err.Error()})
				break
			}
			writeResult(req.ID, plug.HealthResult{OK: true})
		case plug.MethodShutdown:
			writeResult(req.ID, plug.ConfigureResult{OK: true})
			reqCancel()
			signalShutdown()
			return runShutdown(handler, manifest)
		case plug.MethodCancel:
			// Best-effort: ignore for simple handlers. Plugins that need to
			// abort in-flight work observe cancellation via per-request ctx,
			// which needs per-request wiring beyond v1 scope.
		default:
			dispatchTypeSpecific(reqCtx, req, handler)
		}
		reqCancel()
	}
}

func runShutdown(handler Handler, m Manifest) error {
	grace := 10 * time.Second
	if s := os.Getenv("HEROLD_PLUGIN_SHUTDOWN_GRACE"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			grace = d
		}
	} else if m.ShutdownGraceSec > 0 {
		grace = time.Duration(m.ShutdownGraceSec) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	return handler.OnShutdown(ctx)
}

func dispatchTypeSpecific(ctx context.Context, req plug.Request, handler Handler) {
	switch req.Method {
	case MethodDNSPresent, MethodDNSReplace:
		h, ok := handler.(DNSHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement DNS handler")
			return
		}
		var p DNSPresentParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
			return
		}
		var (
			res DNSPresentResult
			err error
		)
		if req.Method == MethodDNSPresent {
			res, err = h.DNSPresent(ctx, p)
		} else {
			res, err = h.DNSReplace(ctx, p)
		}
		if err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, res)
	case MethodDNSCleanup:
		h, ok := handler.(DNSHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement DNS handler")
			return
		}
		var p DNSCleanupParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
			return
		}
		if err := h.DNSCleanup(ctx, p); err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, map[string]any{"ok": true})
	case MethodDNSList:
		h, ok := handler.(DNSHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement DNS handler")
			return
		}
		var p DNSListParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
			return
		}
		rs, err := h.DNSList(ctx, p)
		if err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, rs)
	case MethodSpamClassify:
		h, ok := handler.(SpamHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement spam handler")
			return
		}
		var p SpamClassifyParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
			return
		}
		res, err := h.SpamClassify(ctx, p)
		if err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, res)
	case MethodSpamHealth:
		h, ok := handler.(SpamHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement spam handler")
			return
		}
		res, err := h.SpamHealth(ctx)
		if err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, res)
	case MethodEventsSubscribe:
		h, ok := handler.(EventsHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement events handler")
			return
		}
		var p EventsSubscribeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
			return
		}
		res, err := h.EventsSubscribe(ctx, p)
		if err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, res)
	case MethodEventsPublish:
		h, ok := handler.(EventsHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement events handler")
			return
		}
		var p EventsPublishParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
			return
		}
		res, err := h.EventsPublish(ctx, p)
		if err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, res)
	case MethodEventsHealth:
		h, ok := handler.(EventsHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement events handler")
			return
		}
		res, err := h.EventsHealth(ctx)
		if err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, res)
	case MethodDirectoryLookup:
		h, ok := handler.(DirectoryHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement directory handler")
			return
		}
		var p DirectoryLookupParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
			return
		}
		res, err := h.DirectoryLookup(ctx, p)
		if err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, res)
	case MethodDirectoryAuthenticate:
		h, ok := handler.(DirectoryHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement directory handler")
			return
		}
		var p DirectoryAuthenticateParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
			return
		}
		res, err := h.DirectoryAuthenticate(ctx, p)
		if err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, res)
	case MethodDirectoryListAliases:
		h, ok := handler.(DirectoryHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement directory handler")
			return
		}
		var p DirectoryListAliasesParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
			return
		}
		res, err := h.DirectoryListAliases(ctx, p)
		if err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, res)
	case MethodDeliveryPre:
		h, ok := handler.(DeliveryHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement delivery handler")
			return
		}
		var p DeliveryPreParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
			return
		}
		res, err := h.DeliveryPre(ctx, p)
		if err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, res)
	case MethodDeliveryPost:
		h, ok := handler.(DeliveryHandler)
		if !ok {
			writeErr(req.ID, plug.ErrCodeMethodNotFound, "plugin does not implement delivery handler")
			return
		}
		var p DeliveryPostParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeErr(req.ID, plug.ErrCodeInvalidParams, err.Error())
			return
		}
		if err := h.DeliveryPost(ctx, p); err != nil {
			writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
			return
		}
		writeResult(req.ID, map[string]any{"ok": true})
	default:
		// Arbitrary custom method: defer to CustomHandler if the plugin
		// opts in. Plugins like herold-echo use this for echo.Ping /
		// echo.Sleep without extending the type-specific dispatch above.
		if ch, ok := handler.(CustomHandler); ok {
			res, err := ch.HandleCustom(ctx, req.Method, req.Params)
			if err != nil {
				if errors.Is(err, ErrMethodNotFound) {
					writeErr(req.ID, plug.ErrCodeMethodNotFound, "method not found: "+req.Method)
					return
				}
				writeErr(req.ID, plug.ErrCodeInternalError, err.Error())
				return
			}
			writeResult(req.ID, res)
			return
		}
		writeErr(req.ID, plug.ErrCodeMethodNotFound, "method not found: "+req.Method)
	}
}

// extractTimeout peeks at the params for an optional integer "timeout_ms"
// field. Per-call deadlines are a Herold convention layered on top of
// JSON-RPC so the supervisor and the plugin agree on cancellation time.
func extractTimeout(params json.RawMessage) (time.Duration, bool) {
	if len(params) == 0 {
		return 0, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(params, &m); err != nil {
		return 0, false
	}
	raw, ok := m["timeout_ms"]
	if !ok {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		// Accept strings too, for robustness when callers stringify ints.
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, false
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v <= 0 {
			return 0, false
		}
		n = v
	}
	if n <= 0 {
		return 0, false
	}
	return time.Duration(n) * time.Millisecond, true
}

func writeResult(id json.RawMessage, result any) {
	if id == nil {
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		writeErr(id, plug.ErrCodeInternalError, err.Error())
		return
	}
	_ = writeFrame(plug.Response{
		JSONRPC: plug.JSONRPCVersion,
		ID:      id,
		Result:  raw,
	})
}

func writeErr(id json.RawMessage, code int, msg string) {
	if id == nil {
		// Notifications do not receive error replies.
		return
	}
	_ = writeFrame(plug.Response{
		JSONRPC: plug.JSONRPCVersion,
		ID:      id,
		Error:   &plug.Error{Code: code, Message: msg},
	})
}
