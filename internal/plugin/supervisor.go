package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
)

// activity constants mirror observe.Activity* but are aliased here so callers
// inside the package can use short names without importing observe directly
// at every log site.
const (
	actSystem   = observe.ActivitySystem
	actAudit    = observe.ActivityAudit
	actAccess   = observe.ActivityAccess
	actInternal = observe.ActivityInternal
)

// State is the lifecycle state of a supervised plugin.
type State int32

// Plugin lifecycle states. Transitions go forward through initializing ->
// configuring -> healthy, or forward to stopping / exited on shutdown, or
// to unhealthy when a health probe fails.
const (
	StateStarting State = iota
	StateInitializing
	StateConfiguring
	StateHealthy
	StateUnhealthy
	StateStopping
	StateExited
	StateDisabled
)

// String returns a stable human-readable state name for logs and metrics.
func (s State) String() string {
	switch s {
	case StateStarting:
		return "starting"
	case StateInitializing:
		return "initializing"
	case StateConfiguring:
		return "configuring"
	case StateHealthy:
		return "healthy"
	case StateUnhealthy:
		return "unhealthy"
	case StateStopping:
		return "stopping"
	case StateExited:
		return "exited"
	case StateDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// Spec is one plugin declaration from system.toml (REQ-PLUG-10). Manager
// consumes these when starting or reloading.
type Spec struct {
	Name      string
	Path      string
	Args      []string
	Env       []string
	WorkDir   string
	Type      PluginType
	Lifecycle Lifecycle

	Options map[string]any

	// HealthInterval overrides the manifest-supplied interval. Zero defers
	// to the manifest, which itself defaults to 30s when unset.
	HealthInterval time.Duration
	// CallTimeout is the default deadline applied to every RPC; zero defers
	// to per-type defaults (DNS 30s, spam 5s, directory 2s, delivery.pre 2s,
	// delivery.post 10s). The echo type defaults to 5s.
	CallTimeout time.Duration
	// ShutdownGrace overrides the manifest-supplied grace period. Zero
	// defers to the manifest which defaults to 10s.
	ShutdownGrace time.Duration

	// MaxCrashes bounds how many restarts the supervisor will perform inside
	// CrashWindow before disabling the plugin (REQ-PLUG-05).
	MaxCrashes  int
	CrashWindow time.Duration
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	Logger *slog.Logger
	Clock  clock.Clock
	// ServerVersion is sent in the initialize RPC.
	ServerVersion string
}

// Manager supervises N plugins. One Manager per server.
type Manager struct {
	opts ManagerOptions

	mu      sync.Mutex
	plugins map[string]*Plugin
}

// NewManager returns an empty Manager. Plugins are added via Start or
// Reload; Shutdown tears all of them down.
func NewManager(opts ManagerOptions) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = clock.NewReal()
	}
	if opts.ServerVersion == "" {
		opts.ServerVersion = "0.0.0"
	}
	// Register the plugin collector set on Manager construction.
	// Idempotent across multiple managers in one process Registry.
	observe.RegisterPluginMetrics()
	return &Manager{
		opts:    opts,
		plugins: make(map[string]*Plugin),
	}
}

// Start spawns one plugin per spec and drives it through handshake +
// configure. The returned error is non-nil only if spec is malformed; actual
// process failures are surfaced via the Plugin's state and logged.
func (m *Manager) Start(ctx context.Context, spec Spec) (*Plugin, error) {
	if spec.Name == "" {
		return nil, errors.New("plugin: spec.Name empty")
	}
	if spec.Path == "" {
		return nil, errors.New("plugin: spec.Path empty")
	}
	m.mu.Lock()
	if _, dup := m.plugins[spec.Name]; dup {
		m.mu.Unlock()
		return nil, fmt.Errorf("plugin: %q already running", spec.Name)
	}
	p := newPlugin(m, spec)
	m.plugins[spec.Name] = p
	m.mu.Unlock()

	p.startSupervise(ctx)
	return p, nil
}

// Get returns the plugin registered under name, or nil.
func (m *Manager) Get(name string) *Plugin {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.plugins[name]
}

// List returns a snapshot of the plugins currently registered.
func (m *Manager) List() []*Plugin {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		out = append(out, p)
	}
	return out
}

// Shutdown stops every plugin. It waits for each shutdown to complete up to
// the per-plugin grace window.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	plugins := make([]*Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		plugins = append(plugins, p)
	}
	m.mu.Unlock()

	var firstErr error
	for _, p := range plugins {
		if err := p.Stop(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Plugin is the supervisor handle for one running plugin.
type Plugin struct {
	mgr  *Manager
	spec Spec

	// logger is pre-scoped with subsystem=plugin and plugin=<name> so every
	// log call in this package automatically carries both attributes. Per-call
	// sites add "activity" and event-specific attrs only (REQ-OPS-86).
	logger *slog.Logger

	state    atomic.Int32 // State
	manifest atomic.Pointer[Manifest]
	pid      atomic.Int32

	mu           sync.Mutex
	cmd          *exec.Cmd
	client       *Client
	stopCh       chan struct{}
	doneCh       chan struct{}
	cancel       context.CancelFunc
	crashes      []time.Time
	restartCount int // incremented each time the child process is relaunched
}

func newPlugin(m *Manager, spec Spec) *Plugin {
	return &Plugin{
		mgr:    m,
		spec:   spec,
		logger: m.opts.Logger.With("subsystem", "plugin", "plugin", spec.Name),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Name returns the plugin's configured name.
func (p *Plugin) Name() string { return p.spec.Name }

// Type returns the plugin type; zero value until handshake completes.
func (p *Plugin) Type() PluginType {
	if m := p.manifest.Load(); m != nil {
		return m.Type
	}
	return p.spec.Type
}

// State returns the current lifecycle state.
func (p *Plugin) State() State { return State(p.state.Load()) }

// PID returns the running child process's PID, or 0 if no child is running.
func (p *Plugin) PID() int { return int(p.pid.Load()) }

// Manifest returns the last successfully parsed manifest, or nil.
func (p *Plugin) Manifest() *Manifest { return p.manifest.Load() }

func (p *Plugin) setState(s State) {
	prev := State(p.state.Swap(int32(s)))
	if prev != s {
		p.logger.Info("plugin state changed",
			"activity", actSystem,
			"from", prev.String(),
			"to", s.String())
	}
	if observe.PluginUp != nil {
		if s == StateHealthy {
			observe.PluginUp.WithLabelValues(p.spec.Name).Set(1)
		} else {
			observe.PluginUp.WithLabelValues(p.spec.Name).Set(0)
		}
	}
}

// startSupervise launches the goroutine that owns the plugin's lifecycle.
// It respects ctx for shutdown as well as explicit Stop calls.
func (p *Plugin) startSupervise(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	go p.superviseLoop(runCtx)
}

// Call issues a type-specific RPC on the plugin. The caller supplies a ctx
// with a deadline. Callers must not invoke Call before State() reports
// StateHealthy for a long-running plugin; on-demand plugins route through
// Invoke instead.
func (p *Plugin) Call(ctx context.Context, method string, params, result any) error {
	start := p.mgr.opts.Clock.Now()
	if p.spec.Lifecycle == LifecycleOnDemand {
		err := p.invokeOnDemand(ctx, method, params, result)
		recordPluginCall(p.spec.Name, method, start, p.mgr.opts.Clock.Now(), err)
		return err
	}
	p.mu.Lock()
	c := p.client
	p.mu.Unlock()
	if c == nil {
		err := &Error{Code: ErrCodeUnavailable, Message: "plugin client not running"}
		recordPluginCall(p.spec.Name, method, start, p.mgr.opts.Clock.Now(), err)
		return err
	}
	err := c.Call(ctx, method, params, result)
	recordPluginCall(p.spec.Name, method, start, p.mgr.opts.Clock.Now(), err)
	return err
}

// recordPluginCall normalises an RPC outcome onto the bounded label
// vocabulary {ok, error, timeout, unavailable} and records the metric
// pair. nil collectors are tolerated for tests that bypass
// RegisterPluginMetrics.
func recordPluginCall(name, method string, start, end time.Time, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
		if pErr, ok := err.(*Error); ok {
			switch pErr.Code {
			case ErrCodeUnavailable:
				outcome = "unavailable"
			case ErrCodeTimeout:
				outcome = "timeout"
			}
		}
	}
	if observe.PluginCallsTotal != nil {
		observe.PluginCallsTotal.WithLabelValues(name, method, outcome).Inc()
	}
	if observe.PluginCallDuration != nil {
		observe.PluginCallDuration.WithLabelValues(name, method).Observe(end.Sub(start).Seconds())
	}
}

// Stop signals the supervise loop to exit. It blocks until the plugin is
// fully torn down or ctx is cancelled.
func (p *Plugin) Stop(ctx context.Context) error {
	p.mu.Lock()
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		// Don't cancel immediately; let supervise loop run graceful shutdown.
	}
	select {
	case <-p.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Plugin) superviseLoop(ctx context.Context) {
	defer close(p.doneCh)

	if p.spec.Lifecycle == LifecycleOnDemand {
		// On-demand plugins are launched per-call in invokeOnDemand. The
		// supervise loop exists only to observe stopCh.
		p.setState(StateHealthy)
		select {
		case <-ctx.Done():
		case <-p.stopCh:
		}
		p.setState(StateExited)
		return
	}

	backoff := newBackoff(time.Second, 60*time.Second, p.mgr.opts.Clock, nil)
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		default:
		}

		err := p.runOnce(ctx)
		if err != nil {
			p.logger.Warn("plugin run ended", "activity", actSystem, "err", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		default:
		}

		// REQ-PLUG-05: open circuit after MaxCrashes in CrashWindow.
		now := p.mgr.opts.Clock.Now()
		p.recordCrash(now)
		if p.crashBudgetExhausted(now) {
			p.logger.Error("plugin crashed too many times, disabling",
				"activity", actSystem,
				"limit", p.effectiveMaxCrashes(),
				"window", p.effectiveCrashWindow())
			p.setState(StateDisabled)
			return
		}

		p.mu.Lock()
		p.restartCount++
		restartCount := p.restartCount
		p.mu.Unlock()

		delay := backoff.next()
		p.logger.Warn("plugin restart after crash",
			"activity", actSystem,
			"restart_count", restartCount,
			"delay", delay)
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-p.mgr.opts.Clock.After(delay):
		}
	}
}

// runOnce brings one child process up, runs handshake + configure, drives
// health probes, and returns when either the client read loop exits or Stop
// is signalled.
func (p *Plugin) runOnce(ctx context.Context) error {
	p.setState(StateStarting)
	cmd := exec.CommandContext(ctx, p.spec.Path, p.spec.Args...)
	if p.spec.WorkDir != "" {
		cmd.Dir = p.spec.WorkDir
	}
	if len(p.spec.Env) > 0 {
		cmd.Env = append(os.Environ(), p.spec.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("plugin stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("plugin stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("plugin stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("plugin start: %w", err)
	}
	p.pid.Store(int32(cmd.Process.Pid))
	defer p.pid.Store(0)

	p.logger.Info("plugin started",
		"activity", actSystem,
		"pid", cmd.Process.Pid,
		"path", p.spec.Path)

	// Pipe stderr into the slog stream with plugin=<name> field.
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			p.logger.Info("plugin stderr", "activity", actInternal, "line", sc.Text())
		}
	}()

	client := NewClient(stdout, stdin, ClientOptions{
		Name:          p.spec.Name,
		Logger:        p.logger,
		MaxConcurrent: 1,
		Notifications: newNotifSink(p.logger),
	})
	p.mu.Lock()
	p.cmd = cmd
	p.client = client
	p.mu.Unlock()

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run(ctx)
	}()

	// Handshake.
	p.setState(StateInitializing)
	handshakeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	var initRes InitializeResult
	err = client.Call(handshakeCtx, MethodInitialize, InitializeParams{
		ServerVersion: p.mgr.opts.ServerVersion,
		ABIVersion:    ABIVersion,
	}, &initRes)
	cancel()
	if err != nil {
		p.logger.Error("plugin initialize failed", "activity", actSystem, "err", err)
		p.teardown(cmd, client)
		<-stderrDone
		<-clientErr
		return err
	}
	if err := initRes.Manifest.Validate(); err != nil {
		p.logger.Error("plugin manifest invalid", "activity", actSystem, "err", err)
		p.teardown(cmd, client)
		<-stderrDone
		<-clientErr
		return err
	}
	if p.spec.Type != "" && initRes.Manifest.Type != p.spec.Type {
		err := fmt.Errorf("plugin: type mismatch want=%s got=%s", p.spec.Type, initRes.Manifest.Type)
		p.logger.Error(err.Error(), "activity", actSystem)
		p.teardown(cmd, client)
		<-stderrDone
		<-clientErr
		return err
	}
	p.manifest.Store(&initRes.Manifest)
	if initRes.Manifest.MaxConcurrentRequests > 0 {
		client.SetMaxConcurrent(int64(initRes.Manifest.MaxConcurrentRequests))
	}

	// Configure.
	p.setState(StateConfiguring)
	configCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = client.Call(configCtx, MethodConfigure, ConfigureParams{Options: p.spec.Options}, nil)
	cancel()
	if err != nil {
		p.logger.Error("plugin configure failed", "activity", actSystem, "err", err)
		p.teardown(cmd, client)
		<-stderrDone
		<-clientErr
		return err
	}

	p.setState(StateHealthy)

	// Drive health probes until stop or client exit.
	probeErr := p.drivePlugin(ctx, client, clientErr, &initRes.Manifest)

	p.setState(StateStopping)
	p.teardown(cmd, client)
	<-stderrDone
	// clientErr is buffered size 1; drivePlugin may already have consumed
	// the single value sent by client.Run. Drain non-blockingly.
	select {
	case <-clientErr:
	default:
	}
	p.setState(StateExited)
	return probeErr
}

func (p *Plugin) drivePlugin(ctx context.Context, client *Client, clientErr <-chan error, mf *Manifest) error {
	interval := p.spec.HealthInterval
	if interval <= 0 {
		if mf.HealthIntervalSec > 0 {
			interval = time.Duration(mf.HealthIntervalSec) * time.Second
		} else {
			interval = 30 * time.Second
		}
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.stopCh:
			p.gracefulShutdown(client, mf)
			return nil
		case err := <-clientErr:
			return err
		case <-t.C:
			hctx, cancel := context.WithTimeout(ctx, interval)
			var hr HealthResult
			err := client.Call(hctx, MethodHealth, nil, &hr)
			cancel()
			if err != nil {
				p.logger.Warn("plugin health probe failed", "activity", actSystem, "err", err)
				p.setState(StateUnhealthy)
				return err
			}
			if !hr.OK {
				p.logger.Warn("plugin reports unhealthy", "activity", actSystem, "reason", hr.Reason)
				p.setState(StateUnhealthy)
				return fmt.Errorf("plugin unhealthy: %s", hr.Reason)
			}
			if p.State() != StateHealthy {
				p.setState(StateHealthy)
			}
		}
	}
}

func (p *Plugin) gracefulShutdown(client *Client, mf *Manifest) {
	grace := p.spec.ShutdownGrace
	if grace <= 0 {
		if mf != nil && mf.ShutdownGraceSec > 0 {
			grace = time.Duration(mf.ShutdownGraceSec) * time.Second
		} else {
			grace = 10 * time.Second
		}
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := client.Call(shutCtx, MethodShutdown, ShutdownParams{GraceSec: int(grace / time.Second)}, nil); err != nil {
		p.logger.Warn("plugin shutdown rpc failed", "activity", actSystem, "err", err)
	}
}

func (p *Plugin) teardown(cmd *exec.Cmd, client *Client) {
	if client != nil {
		client.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return
	}
	mf := p.manifest.Load()
	grace := p.spec.ShutdownGrace
	if grace <= 0 {
		if mf != nil && mf.ShutdownGraceSec > 0 {
			grace = time.Duration(mf.ShutdownGraceSec) * time.Second
		} else {
			grace = 10 * time.Second
		}
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// Clean exit within grace window.
		p.logger.Info("plugin exited cleanly",
			"activity", actSystem,
			"pid", cmd.Process.Pid)
		return
	case <-time.After(grace):
	}
	// Grace window elapsed — send SIGTERM (supervisor-initiated, security-relevant).
	p.logger.Warn("plugin did not exit within grace window, sending SIGTERM",
		"activity", actAudit,
		"pid", cmd.Process.Pid,
		"grace", grace)
	_ = cmd.Process.Signal(sigterm)
	select {
	case <-done:
		return
	case <-time.After(grace):
	}
	// SIGTERM ignored — force kill (supervisor-initiated, security-relevant).
	p.logger.Warn("plugin did not respond to SIGTERM, killing",
		"activity", actAudit,
		"pid", cmd.Process.Pid)
	_ = cmd.Process.Kill()
	<-done
}

// invokeOnDemand spawns a fresh child, feeds a single request on stdin,
// reads a single response on stdout, and waits for exit.
func (p *Plugin) invokeOnDemand(ctx context.Context, method string, params, result any) error {
	cmd := exec.CommandContext(ctx, p.spec.Path, p.spec.Args...)
	if p.spec.WorkDir != "" {
		cmd.Dir = p.spec.WorkDir
	}
	if len(p.spec.Env) > 0 {
		cmd.Env = append(os.Environ(), p.spec.Env...)
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("plugin stdin: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("plugin stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("plugin stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("plugin start: %w", err)
	}
	p.pid.Store(int32(cmd.Process.Pid))
	defer p.pid.Store(0)
	go func() {
		sc := bufio.NewScanner(stderrPipe)
		for sc.Scan() {
			p.logger.Info("plugin stderr", "activity", actInternal, "line", sc.Text())
		}
	}()

	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	req := Request{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage("1"),
		Method:  method,
		Params:  raw,
	}
	fw := NewFrameWriter(stdinPipe)
	if err := fw.WriteFrame(req); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	_ = stdinPipe.Close()

	fr := NewFrameReader(stdoutPipe, DefaultMaxFrameBytes)
	line, err := fr.ReadFrame()
	if err != nil && !errors.Is(err, io.EOF) {
		_ = cmd.Process.Kill()
		return err
	}
	var resp Response
	if len(line) > 0 {
		if err := json.Unmarshal(line, &resp); err != nil {
			_ = cmd.Process.Kill()
			return fmt.Errorf("plugin: decode on-demand response: %w", err)
		}
	}
	_ = cmd.Wait()
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, result)
	}
	return nil
}

func (p *Plugin) recordCrash(t time.Time) {
	window := p.effectiveCrashWindow()
	cutoff := t.Add(-window)
	kept := p.crashes[:0]
	for _, c := range p.crashes {
		if c.After(cutoff) {
			kept = append(kept, c)
		}
	}
	p.crashes = append(kept, t)
}

func (p *Plugin) crashBudgetExhausted(now time.Time) bool {
	return len(p.crashes) > p.effectiveMaxCrashes()
}

func (p *Plugin) effectiveMaxCrashes() int {
	if p.spec.MaxCrashes > 0 {
		return p.spec.MaxCrashes
	}
	return 5
}

func (p *Plugin) effectiveCrashWindow() time.Duration {
	if p.spec.CrashWindow > 0 {
		return p.spec.CrashWindow
	}
	return 5 * time.Minute
}

// notifSink is the default NotificationHandler: log/metric/notify all feed
// into the plugin's slog logger. Metrics wiring lands when internal/observe
// publishes its collector; the log path is the full surface for now.
type notifSink struct{ logger *slog.Logger }

func newNotifSink(l *slog.Logger) NotificationHandler { return &notifSink{logger: l} }

func (n *notifSink) OnLog(p LogParams) {
	lvl := slog.LevelInfo
	switch p.Level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	attrs := []any{"activity", actInternal, "source", "plugin"}
	for k, v := range p.Fields {
		attrs = append(attrs, k, v)
	}
	n.logger.Log(context.Background(), lvl, p.Msg, attrs...)
}

func (n *notifSink) OnMetric(p MetricParams) {
	n.logger.Debug("plugin metric",
		"activity", actInternal,
		"name", p.Name,
		"kind", p.Kind,
		"value", p.Value,
		"labels", p.Labels)
}

func (n *notifSink) OnNotify(p NotifyParams) {
	n.logger.Info("plugin notify",
		"activity", actInternal,
		"type", p.Type,
		"payload", p.Payload)
}

func (n *notifSink) OnUnknown(method string, _ json.RawMessage) {
	n.logger.Warn("plugin notification with unknown method",
		"activity", actInternal,
		"method", method)
}
