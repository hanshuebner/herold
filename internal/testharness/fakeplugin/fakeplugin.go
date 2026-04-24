// Package fakeplugin implements an in-process stand-in for the out-of-process
// plugin system (docs/architecture/07-plugin-architecture.md). In production
// plugins are child processes talking JSON-RPC over stdio; in tests the
// harness wires a FakePlugin directly into the server's plugin registry so
// protocol code can exercise delivery-hook / spam / DNS / events semantics
// without spawning a child or shaping JSON-RPC frames.
//
// Scope is deliberately narrow: the tests need a dispatchable Call entry
// point and notifications; the JSON-RPC framing, supervisor lifecycle, and
// capability handshake all live in internal/plugin, which this package does
// NOT import (that package is in flux in Wave 0).
package fakeplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// ErrUnknownMethod is returned by Call when a plugin has no handler
// registered for the requested method. Tests check for this with errors.Is.
var ErrUnknownMethod = errors.New("fakeplugin: unknown method")

// ErrUnknownPlugin is returned by Registry.Call when no plugin is
// registered under the requested name.
var ErrUnknownPlugin = errors.New("fakeplugin: unknown plugin")

// Handler handles a single plugin method call. Params is the JSON-encoded
// request body; the handler returns the JSON-encoded response body.
// Handlers must honour ctx cancellation.
type Handler func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

// FakePlugin is the minimal shape the harness treats as a plugin. Kind
// matches the PluginType enum used in production ("dns", "spam",
// "event-publisher", "delivery-hook", "directory", "echo"); handlers
// dispatch Call by method name.
//
// Construct one via New; direct struct literal construction is allowed
// when tests want to inject a stub with pre-populated fields.
type FakePlugin struct {
	// Name is the registered name (same shape as the operator-supplied
	// plugin name in system.toml).
	Name string
	// Kind declares the plugin type.
	Kind string

	mu            sync.RWMutex
	handlers      map[string]Handler
	notifications []Notification
	healthy       bool
}

// Notification is a recorded notify/log/metric emission from the plugin
// back to the server. Tests inspect these after exercising a path.
type Notification struct {
	// Method is the notify method name (e.g. "log", "metric", "notify").
	Method string
	// Params is the JSON-encoded payload.
	Params json.RawMessage
}

// New returns a FakePlugin with no handlers registered. The plugin is
// marked healthy; call SetHealthy(false) to simulate unhealth.
func New(name, kind string) *FakePlugin {
	return &FakePlugin{
		Name:     name,
		Kind:     kind,
		handlers: make(map[string]Handler),
		healthy:  true,
	}
}

// Handle registers h as the handler for method. Replacing an existing
// handler is allowed (tests may rewire partway through).
func (p *FakePlugin) Handle(method string, h Handler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handlers[method] = h
}

// Call invokes the handler for method with params. Returns ErrUnknownMethod
// if no handler is registered.
func (p *FakePlugin) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	p.mu.RLock()
	h, ok := p.handlers[method]
	p.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s.%s", ErrUnknownMethod, p.Name, method)
	}
	return h(ctx, params)
}

// EmitNotification records a plugin -> server notification. Tests use
// Notifications() to read them back.
func (p *FakePlugin) EmitNotification(method string, params json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notifications = append(p.notifications, Notification{Method: method, Params: params})
}

// Notifications returns a copy of the recorded notifications in order.
func (p *FakePlugin) Notifications() []Notification {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Notification, len(p.notifications))
	copy(out, p.notifications)
	return out
}

// SetHealthy toggles the plugin's reported health state. The harness's
// health probe reports this value.
func (p *FakePlugin) SetHealthy(ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = ok
}

// Healthy returns the plugin's current health state.
func (p *FakePlugin) Healthy() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.healthy
}

// Registry is the collection of registered fake plugins. Keyed by Name.
// Concurrency-safe.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]*FakePlugin
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{plugins: make(map[string]*FakePlugin)}
}

// Register adds p under its Name, replacing any existing plugin with the
// same name.
func (r *Registry) Register(p *FakePlugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plugins[p.Name] = p
}

// Get returns the named plugin and true, or nil and false.
func (r *Registry) Get(name string) (*FakePlugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[name]
	return p, ok
}

// Names returns the registered plugin names in unspecified order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.plugins))
	for n := range r.plugins {
		out = append(out, n)
	}
	return out
}

// Call dispatches to the named plugin's method. Returns ErrUnknownPlugin
// if the plugin is not registered, or ErrUnknownMethod if the method is
// not handled.
func (r *Registry) Call(ctx context.Context, plugin, method string, params json.RawMessage) (json.RawMessage, error) {
	p, ok := r.Get(plugin)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownPlugin, plugin)
	}
	return p.Call(ctx, method, params)
}
