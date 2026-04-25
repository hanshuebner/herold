// Command herold-events-nats is the first-party event-publisher plugin
// that forwards Herold events to a NATS server. The plugin satisfies
// sdk.EventsHandler: events.subscribe is a no-op (the plugin accepts
// every kind the server hands it), events.publish converts the typed
// event to the wire shape <subject_prefix>.<kind>.<subject> and
// publishes via core NATS or JetStream, and events.health reports the
// connection status.
//
// REQ-EVT-30..33: ships with v1, options expose URL, credentials, TLS,
// and an optional JetStream binding. Reconnection is the nats.go
// default (infinite reconnect with backoff); local buffering protects
// against a temporarily unreachable server.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

// natsHandler holds the live NATS connection plus per-instance config
// resolved from OnConfigure. The handler is safe for concurrent use:
// nats.Conn is internally synchronized.
type natsHandler struct {
	mu sync.Mutex

	url           string
	subjectPrefix string
	connectTO     time.Duration
	publishTO     time.Duration
	useJetStream  bool

	tlsCAFile   string
	tlsCertFile string
	tlsKeyFile  string

	username string
	password string
	nkeySeed string
	jwt      string

	conn *nats.Conn
	js   nats.JetStreamContext
}

func newHandler() *natsHandler {
	return &natsHandler{
		url:           "nats://localhost:4222",
		subjectPrefix: "herold",
		connectTO:     10 * time.Second,
		publishTO:     5 * time.Second,
	}
}

// OnConfigure resolves operator options and opens the NATS connection.
// Errors abort the configure RPC; the supervisor reports them as a
// configure failure and the plugin process exits.
func (h *natsHandler) OnConfigure(ctx context.Context, opts map[string]any) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if v, ok := opts["url"].(string); ok && v != "" {
		h.url = v
	}
	if v, ok := opts["subject_prefix"].(string); ok && v != "" {
		h.subjectPrefix = v
	}
	if v, ok := optInt(opts, "connect_timeout_sec"); ok && v > 0 {
		h.connectTO = time.Duration(v) * time.Second
	}
	if v, ok := optInt(opts, "publish_timeout_sec"); ok && v > 0 {
		h.publishTO = time.Duration(v) * time.Second
	}
	if v, ok := opts["jetstream"].(bool); ok {
		h.useJetStream = v
	}

	if v, ok := opts["tls_ca_file"].(string); ok {
		h.tlsCAFile = v
	}
	if v, ok := opts["tls_cert_file"].(string); ok {
		h.tlsCertFile = v
	}
	if v, ok := opts["tls_key_file"].(string); ok {
		h.tlsKeyFile = v
	}

	if v, ok := opts["username"].(string); ok {
		h.username = v
	}
	if v, ok := opts["password_env"].(string); ok && v != "" {
		h.password = os.Getenv(v)
	}
	if v, ok := opts["nkey_seed_env"].(string); ok && v != "" {
		h.nkeySeed = os.Getenv(v)
	}
	if v, ok := opts["jwt_env"].(string); ok && v != "" {
		h.jwt = os.Getenv(v)
	}

	natsOpts, err := h.buildOptions()
	if err != nil {
		return fmt.Errorf("herold-events-nats: build options: %w", err)
	}
	conn, err := natsOpts.Connect()
	if err != nil {
		return fmt.Errorf("herold-events-nats: connect %s: %w", h.url, err)
	}
	h.conn = conn

	if h.useJetStream {
		js, err := conn.JetStream()
		if err != nil {
			conn.Close()
			h.conn = nil
			return fmt.Errorf("herold-events-nats: jetstream context: %w", err)
		}
		h.js = js
	}
	sdk.Logf("info", "herold-events-nats connected url=%s jetstream=%t prefix=%s",
		h.url, h.useJetStream, h.subjectPrefix)
	return nil
}

// buildOptions translates the resolved fields into a nats.Options. The
// caller holds h.mu.
func (h *natsHandler) buildOptions() (nats.Options, error) {
	o := nats.GetDefaultOptions()
	o.Url = h.url
	o.Timeout = h.connectTO
	o.AllowReconnect = true
	o.Name = "herold-events-nats"

	if h.username != "" {
		o.User = h.username
		o.Password = h.password
	}
	if h.nkeySeed != "" {
		opt, err := nats.NkeyOptionFromSeed(h.nkeySeed)
		if err != nil {
			return o, fmt.Errorf("nkey: %w", err)
		}
		if err := opt(&o); err != nil {
			return o, err
		}
	}
	if h.jwt != "" {
		// nats.UserJWTAndSeed wires the static JWT and seed; we accept
		// the seed via nkey_seed_env in the same configure call.
		if h.nkeySeed == "" {
			return o, errors.New("jwt requires nkey_seed")
		}
		opt := nats.UserJWTAndSeed(h.jwt, h.nkeySeed)
		if err := opt(&o); err != nil {
			return o, err
		}
	}

	tlsCfg, err := h.tlsConfig()
	if err != nil {
		return o, err
	}
	if tlsCfg != nil {
		o.Secure = true
		o.TLSConfig = tlsCfg
	}
	return o, nil
}

// tlsConfig assembles a *tls.Config from the configured ca/cert/key
// files. Returns nil when no TLS material is configured.
func (h *natsHandler) tlsConfig() (*tls.Config, error) {
	if h.tlsCAFile == "" && h.tlsCertFile == "" && h.tlsKeyFile == "" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if h.tlsCAFile != "" {
		pem, err := os.ReadFile(h.tlsCAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ca file contains no certificates")
		}
		cfg.RootCAs = pool
	}
	if h.tlsCertFile != "" || h.tlsKeyFile != "" {
		if h.tlsCertFile == "" || h.tlsKeyFile == "" {
			return nil, errors.New("tls_cert_file and tls_key_file must be set together")
		}
		cert, err := tls.LoadX509KeyPair(h.tlsCertFile, h.tlsKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load keypair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// OnHealth reports CONNECTED status. A reconnecting state still
// satisfies the probe so a transient blip does not bounce the plugin;
// any state worse than RECONNECTING is reported as unhealthy.
func (h *natsHandler) OnHealth(ctx context.Context) error {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return errors.New("nats: connection nil")
	}
	switch conn.Status() {
	case nats.CONNECTED, nats.RECONNECTING, nats.DRAINING_PUBS, nats.DRAINING_SUBS:
		return nil
	default:
		return fmt.Errorf("nats: status %s", conn.Status())
	}
}

// OnShutdown drains the connection within the supervisor's grace
// window and closes it.
func (h *natsHandler) OnShutdown(ctx context.Context) error {
	h.mu.Lock()
	conn := h.conn
	h.conn = nil
	h.js = nil
	h.mu.Unlock()
	if conn == nil {
		return nil
	}
	// Best-effort flush + drain; honour ctx deadline for the whole
	// shutdown operation. nats.Conn.FlushWithContext requires a
	// deadline; fall back to FlushTimeout when ctx has none.
	if _, ok := ctx.Deadline(); ok {
		if err := conn.FlushWithContext(ctx); err != nil {
			sdk.Logf("warn", "flush before drain failed: %v", err)
		}
	} else {
		if err := conn.FlushTimeout(h.publishTO); err != nil {
			sdk.Logf("warn", "flush before drain failed: %v", err)
		}
	}
	if err := conn.Drain(); err != nil {
		sdk.Logf("warn", "drain failed: %v", err)
	}
	conn.Close()
	return nil
}

// EventsSubscribe is a no-op on the plugin side: the dispatcher
// already filters events plugin-side filtering is not required by
// REQ-EVT in v1, but we accept a subscribe call for forward
// compatibility with the SDK contract.
func (h *natsHandler) EventsSubscribe(_ context.Context, _ sdk.EventsSubscribeParams) (sdk.EventsSubscribeResult, error) {
	return sdk.EventsSubscribeResult{Ack: true}, nil
}

// EventsPublish converts the typed event into a NATS message. The
// subject is computed as "<subject_prefix>.<kind>.<subject>". Body is
// the marshalled JSON event verbatim — receivers parse the same shape
// dispatcher.eventToParams emits.
func (h *natsHandler) EventsPublish(ctx context.Context, in sdk.EventsPublishParams) (sdk.EventsPublishResult, error) {
	h.mu.Lock()
	conn := h.conn
	js := h.js
	prefix := h.subjectPrefix
	useJS := h.useJetStream
	publishTO := h.publishTO
	h.mu.Unlock()

	if conn == nil {
		return sdk.EventsPublishResult{Ack: false}, errors.New("nats: connection not open")
	}
	subject, err := subjectFor(prefix, in.Event)
	if err != nil {
		return sdk.EventsPublishResult{Ack: false}, err
	}
	body, err := json.Marshal(in.Event)
	if err != nil {
		return sdk.EventsPublishResult{Ack: false}, fmt.Errorf("marshal event: %w", err)
	}
	pubCtx, cancel := context.WithTimeout(ctx, publishTO)
	defer cancel()
	if useJS {
		if js == nil {
			return sdk.EventsPublishResult{Ack: false}, errors.New("nats: jetstream context nil")
		}
		_, err := js.Publish(subject, body, nats.Context(pubCtx))
		if err != nil {
			return sdk.EventsPublishResult{Ack: false}, fmt.Errorf("jetstream publish: %w", err)
		}
		return sdk.EventsPublishResult{Ack: true}, nil
	}
	if err := conn.Publish(subject, body); err != nil {
		return sdk.EventsPublishResult{Ack: false}, fmt.Errorf("publish: %w", err)
	}
	// Core NATS publish is async; flushing inside the publish-timeout
	// window converts an unreachable server into an immediate error
	// the supervisor can retry.
	if err := conn.FlushWithContext(pubCtx); err != nil {
		return sdk.EventsPublishResult{Ack: false}, fmt.Errorf("flush: %w", err)
	}
	return sdk.EventsPublishResult{Ack: true}, nil
}

// EventsHealth reports the live NATS connection state.
func (h *natsHandler) EventsHealth(_ context.Context) (sdk.EventsHealthResult, error) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return sdk.EventsHealthResult{OK: false}, errors.New("nats: connection nil")
	}
	if conn.Status() != nats.CONNECTED {
		return sdk.EventsHealthResult{OK: false}, fmt.Errorf("nats: status %s", conn.Status())
	}
	return sdk.EventsHealthResult{OK: true}, nil
}

// subjectFor extracts the kind + subject suffix from the event
// envelope and concatenates the configured prefix. Empty subject
// suffix is allowed and yields "<prefix>.<kind>".
func subjectFor(prefix string, ev map[string]any) (string, error) {
	kind, _ := ev["kind"].(string)
	if kind == "" {
		return "", errors.New("event.kind missing")
	}
	subj, _ := ev["subject"].(string)
	parts := []string{prefix, kind}
	if subj != "" {
		parts = append(parts, sanitizeSubject(subj))
	}
	return strings.Join(parts, "."), nil
}

// sanitizeSubject replaces NATS subject-illegal characters in the
// operator-readable subject. NATS subjects use "." as a hierarchy
// separator, so "." passes through (a subject of "example.com"
// becomes a two-level suffix). "*" and ">" are wildcards; whitespace
// is invalid.
func sanitizeSubject(in string) string {
	r := strings.NewReplacer("*", "_", ">", "_", " ", "_", "\t", "_")
	return r.Replace(in)
}

// optInt extracts an int option from the options map, accepting both
// JSON numbers (float64 from encoding/json) and strings.
func optInt(opts map[string]any, key string) (int, bool) {
	v, ok := opts[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	case string:
		var n int
		if _, err := fmt.Sscanf(x, "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

func main() {
	manifest := sdk.Manifest{
		Name:                  "herold-events-nats",
		Version:               "0.1.0",
		Type:                  plug.TypeEvents,
		Lifecycle:             plug.LifecycleLongRunning,
		MaxConcurrentRequests: 32,
		ABIVersion:            plug.ABIVersion,
		ShutdownGraceSec:      10,
		HealthIntervalSec:     30,
		OptionsSchema: map[string]plug.OptionSchema{
			"url":                 {Type: "string", Default: "nats://localhost:4222"},
			"subject_prefix":      {Type: "string", Default: "herold"},
			"connect_timeout_sec": {Type: "integer", Default: 10},
			"publish_timeout_sec": {Type: "integer", Default: 5},
			"jetstream":           {Type: "boolean", Default: false},
			"tls_ca_file":         {Type: "string"},
			"tls_cert_file":       {Type: "string"},
			"tls_key_file":        {Type: "string"},
			"username":            {Type: "string"},
			"password_env":        {Type: "string", Secret: true},
			"nkey_seed_env":       {Type: "string", Secret: true},
			"jwt_env":             {Type: "string", Secret: true},
		},
	}
	if err := sdk.Run(manifest, newHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "herold-events-nats: %v\n", err)
		os.Exit(1)
	}
}
