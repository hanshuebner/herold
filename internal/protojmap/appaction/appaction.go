// Package appaction implements the JMAP apps capability and the
// AppAction/invoke method (docs/design/server/requirements/29-apps.md
// REQ-APP-40..45, REQ-APP-50..51). The capability lists the app actions
// offered to the requesting principal; AppAction/invoke exports the
// invoked thread, POSTs it to the app as one signed request and returns
// the app's validated answer.
package appaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/apps"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// Capability is the apps capability URI (REQ-APP-50).
const Capability protojmap.CapabilityID = "https://netzhansa.com/jmap/apps"

// deadlineSlack is the time AppAction/invoke gets beyond the client's
// invoke timeout for the thread lookup and the second body pass.
const deadlineSlack = 5 * time.Second

// Options configures Register.
type Options struct {
	Store store.Store
	// Exporter resolves and streams threads (REQ-TEXP-01).
	Exporter *protojmap.ThreadExporter
	// Client reaches the app origin (REQ-APP-60).
	Client *apps.Client
	// DataKey opens the sealed signing secrets (REQ-APP-30).
	DataKey []byte
	// MaxPayloadBytes bounds the exported message bytes of one
	// invocation (REQ-APP-42).
	MaxPayloadBytes int64
	// InvokeTimeout is the client's invocation timeout; the method
	// deadline is derived from it.
	InvokeTimeout time.Duration
	Logger        *slog.Logger
	Clock         clock.Clock
}

// Register installs the apps capability and AppAction/invoke on reg.
// Callers register only when apps are enabled and the data key is
// present (REQ-APP-14, REQ-APP-42).
func Register(reg *protojmap.CapabilityRegistry, opts Options) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = clock.NewReal()
	}
	if opts.Exporter == nil {
		opts.Exporter = protojmap.NewThreadExporter(opts.Store)
	}
	if opts.InvokeTimeout <= 0 {
		opts.InvokeTimeout = apps.DefaultTimeout
	}
	h := &handler{opts: opts, log: opts.Logger.With("subsystem", "jmap-apps")}
	reg.RegisterPrincipalCapability(Capability, capabilityProvider{h: h})
	reg.Register(Capability, invokeHandler{h: h})
}

type handler struct {
	opts Options
	log  *slog.Logger
}

// action is one entry of the capability's actions list (REQ-APP-50).
type action struct {
	AppID       string             `json:"appId"`
	ExtensionID string             `json:"extensionId"`
	Point       string             `json:"point"`
	AppName     apps.LocalizedText `json:"appName"`
	Label       apps.LocalizedText `json:"label"`
	Icon        string             `json:"icon"`
}

// capabilityValue is the account-level descriptor of Capability.
type capabilityValue struct {
	Actions []action `json:"actions"`
}

// appIDString renders an app id on the wire.
func appIDString(id store.AppID) string { return strconv.FormatUint(uint64(id), 10) }

// offeredActions returns the actions offered to pid and the apps they
// come from.
func (h *handler) offeredActions(ctx context.Context, pid store.PrincipalID) ([]action, []store.App, error) {
	rows, err := h.opts.Store.Meta().ListAppsForPrincipal(ctx, pid)
	if err != nil {
		return nil, nil, err
	}
	var out []action
	for _, a := range rows {
		m, err := apps.DecodeStoredManifest(a.ManifestJSON)
		if err != nil || m == nil {
			continue
		}
		for _, e := range m.Extensions {
			if e.Point != apps.PointThreadAction {
				continue
			}
			out = append(out, action{
				AppID:       appIDString(a.ID),
				ExtensionID: e.ID,
				Point:       e.Point,
				AppName:     m.Name,
				Label:       e.Label,
				Icon:        e.Icon,
			})
		}
	}
	return out, rows, nil
}

type capabilityProvider struct{ h *handler }

// PrincipalCapability advertises the capability only when the
// principal has at least one offered action; the token digests the
// offered apps and their approved manifests so any change re-keys the
// session state (REQ-APP-50).
func (c capabilityProvider) PrincipalCapability(ctx context.Context, p store.Principal) (any, string, bool) {
	actions, rows, err := c.h.offeredActions(ctx, p.ID)
	if err != nil {
		c.h.log.Warn("apps.capability_failed", "activity", observe.ActivityInternal, "err", err)
		return nil, "", false
	}
	if len(actions) == 0 {
		return nil, "", false
	}
	sum := sha256.New()
	for _, a := range rows {
		fmt.Fprintf(sum, "%d:%s;", a.ID, a.ManifestDigest)
	}
	return capabilityValue{Actions: actions}, hex.EncodeToString(sum.Sum(nil))[:16], true
}

type invokeHandler struct{ h *handler }

func (invokeHandler) Method() string { return "AppAction/invoke" }

// MethodDeadline gives the method the invocation timeout plus slack in
// place of the default JMAP method deadline.
func (i invokeHandler) MethodDeadline() time.Duration {
	return i.h.opts.InvokeTimeout + deadlineSlack
}

type invokeArgs struct {
	AccountID   protojmap.Id `json:"accountId"`
	AppID       string       `json:"appId"`
	ExtensionID string       `json:"extensionId"`
	ThreadID    protojmap.Id `json:"threadId"`
}

// invokeResponse is the AppAction/invoke result (REQ-APP-51).
type invokeResponse struct {
	Type string `json:"type"`
	URL  string `json:"url,omitempty"`
	Text string `json:"text,omitempty"`
}

// outcome is what one invocation recorded for audit and logs.
type outcome struct {
	app          store.App
	extensionID  string
	threadID     string
	messageCount int
	requestBytes int64
	status       int
	result       string
	start        time.Time
}

func (i invokeHandler) Execute(ctx context.Context, raw json.RawMessage) (any, *protojmap.MethodError) {
	h := i.h
	p, ok := protojmap.PrincipalFromContext(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	if !protojmap.InteractiveSessionFromContext(ctx) {
		// REQ-APP-40: only a user's click in an interactive session may
		// release mail to an app.
		return nil, protojmap.NewMethodError("forbidden", "AppAction/invoke requires an interactive session")
	}
	var args invokeArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	if args.AccountID == "" || args.AppID == "" || args.ExtensionID == "" || args.ThreadID == "" {
		return nil, protojmap.NewMethodError("invalidArguments",
			"accountId, appId, extensionId and threadId are required")
	}
	ownerPID, merr := protojmap.ResolveAccount(ctx, h.opts.Store.Meta(), p.ID, args.AccountID)
	if merr != nil {
		return nil, merr
	}

	// Step 1: the app must be active and offered to the principal, and
	// the extension a thread action (REQ-APP-13, REQ-APP-40).
	offered, err := h.opts.Store.Meta().ListAppsForPrincipal(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", "list apps")
	}
	var app store.App
	for _, a := range offered {
		if appIDString(a.ID) == args.AppID {
			app = a
		}
	}
	if app.ID == 0 {
		return nil, protojmap.NewMethodError("notFound", "no such app action")
	}
	manifest, err := apps.DecodeStoredManifest(app.ManifestJSON)
	if err != nil || manifest == nil {
		return nil, protojmap.NewMethodError("notFound", "no such app action")
	}
	ext, ok := manifest.Extension(args.ExtensionID)
	if !ok || ext.Point != apps.PointThreadAction {
		return nil, protojmap.NewMethodError("notFound", "no such app action")
	}

	// Step 2: the thread, under the thread-export access rules.
	exp, err := h.opts.Exporter.Resolve(ctx, p.ID, ownerPID, args.ThreadID)
	if errors.Is(err, protojmap.ErrThreadNotFound) {
		return nil, protojmap.NewMethodError("notFound", "thread not found")
	}
	if err != nil {
		h.log.Warn("apps.invoke.resolve_failed", "activity", observe.ActivityInternal, "err", err)
		return nil, protojmap.NewMethodError("serverFail", "thread lookup failed")
	}
	oc := outcome{
		app:          app,
		extensionID:  ext.ID,
		threadID:     fmt.Sprintf("t%d", exp.ThreadKey),
		messageCount: len(exp.Messages),
		start:        h.opts.Clock.Now(),
	}
	withRaw := ext.HasScope(apps.ScopeThreadRaw)
	if withRaw && h.opts.MaxPayloadBytes > 0 && exp.TotalSize() > h.opts.MaxPayloadBytes {
		// REQ-APP-42: refuse before any byte reaches the app.
		oc.result = "tooLarge"
		h.audit(ctx, p, oc, store.OutcomeFailure)
		return nil, protojmap.NewMethodError("tooLarge",
			fmt.Sprintf("the thread's %d bytes exceed the %d-byte limit", exp.TotalSize(), h.opts.MaxPayloadBytes))
	}

	key, err := secrets.Open(h.opts.DataKey, app.SecretSealed)
	if err != nil {
		h.log.Error("apps.invoke.secret_open_failed", "activity", observe.ActivityInternal,
			"app_id", uint64(app.ID), "err", err)
		return nil, protojmap.NewMethodError("serverFail", "app secret unavailable")
	}
	owner := p
	if ownerPID != p.ID {
		if owner, err = h.opts.Store.Meta().GetPrincipalByID(ctx, ownerPID); err != nil {
			return nil, protojmap.NewMethodError("serverFail", "account lookup failed")
		}
	}
	inv := apps.Invocation{
		Origin:   app.Origin,
		Endpoint: ext.Endpoint,
		Key:      key,
		Now:      h.opts.Clock.Now(),
		Context: apps.InvocationContext{
			InvocationID: apps.NewInvocationID(),
			AppKey:       app.AppKey,
			ExtensionID:  ext.ID,
			User:         apps.Party{Email: p.CanonicalEmail, DisplayName: p.DisplayName},
			Account:      apps.Party{Email: owner.CanonicalEmail},
			Thread:       apps.ThreadInfo{ID: oc.threadID, MessageCount: len(exp.Messages)},
		},
	}
	if withRaw {
		inv.Context.Thread.Subject = exp.Messages[0].Subject
		for _, m := range exp.Messages {
			inv.Context.Messages = append(inv.Context.Messages, apps.MessageInfo{
				Index:      m.Index,
				ReceivedAt: m.ReceivedAt.UTC().Format(time.RFC3339),
				Size:       m.Blob.Size,
			})
		}
		limit := h.opts.MaxPayloadBytes
		inv.Messages = func(ctx context.Context, fn func(int, io.Reader) error) error {
			return h.opts.Exporter.Stream(ctx, exp, limit, func(m protojmap.ThreadExportMessage, r io.Reader) error {
				return fn(m.Index, r)
			})
		}
	}

	res, err := h.opts.Client.Invoke(ctx, inv)
	if err != nil {
		var ie *apps.InvokeError
		switch {
		case errors.Is(err, protojmap.ErrExportTooLarge):
			oc.result = "tooLarge"
			h.audit(ctx, p, oc, store.OutcomeFailure)
			return nil, protojmap.NewMethodError("tooLarge", "the thread exceeds the payload limit")
		case errors.As(err, &ie):
			oc.requestBytes, oc.status, oc.result = ie.RequestBytes, ie.Status, string(ie.Kind)
			h.log.Warn("apps.invoke.failed", "activity", observe.ActivityUser,
				"app_id", uint64(app.ID), "extension_id", ext.ID, "kind", string(ie.Kind),
				"status", ie.Status, "timeout", ie.Timeout, "reason", ie.Reason)
			h.audit(ctx, p, oc, store.OutcomeFailure)
			if ie.Kind == apps.ErrorRejected {
				return nil, protojmap.NewMethodError("appRejected", ie.Text)
			}
			return nil, protojmap.NewMethodError("appUnavailable", "the app is not reachable")
		default:
			h.log.Warn("apps.invoke.build_failed", "activity", observe.ActivityInternal,
				"app_id", uint64(app.ID), "err", err)
			oc.result = "serverFail"
			h.audit(ctx, p, oc, store.OutcomeFailure)
			return nil, protojmap.NewMethodError("serverFail", "the request could not be built")
		}
	}
	oc.requestBytes, oc.status, oc.result = res.RequestBytes, res.Status, res.Type
	if err := h.opts.Store.Meta().RecordAppInvoked(ctx, app.ID, h.opts.Clock.Now()); err != nil {
		h.log.Warn("apps.invoke.record_failed", "activity", observe.ActivityInternal, "err", err)
	}
	h.audit(ctx, p, oc, store.OutcomeSuccess)
	if res.Type == apps.ResponseOpenURL {
		return invokeResponse{Type: "openUrl", URL: res.URL}, nil
	}
	return invokeResponse{Type: "notice", Text: res.Text}, nil
}

// audit writes the app.invoke audit row and its log line (REQ-APP-45).
// Subjects, addresses and bodies are never recorded.
func (h *handler) audit(ctx context.Context, p store.Principal, oc outcome, result store.AuditOutcome) {
	dur := h.opts.Clock.Now().Sub(oc.start)
	meta := map[string]string{
		"thread_id":     oc.threadID,
		"message_count": strconv.Itoa(oc.messageCount),
		"request_bytes": strconv.FormatInt(oc.requestBytes, 10),
		"status":        strconv.Itoa(oc.status),
		"duration_ms":   strconv.FormatInt(dur.Milliseconds(), 10),
		"outcome":       oc.result,
	}
	subject := "app:" + oc.app.AppKey + "/" + oc.extensionID
	if err := h.opts.Store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:        h.opts.Clock.Now(),
		ActorKind: store.ActorPrincipal,
		ActorID:   strconv.FormatUint(uint64(p.ID), 10),
		Action:    "app.invoke",
		Subject:   subject,
		Outcome:   result,
		Metadata:  meta,
	}); err != nil {
		h.log.Warn("apps.invoke.audit_failed", "activity", observe.ActivityInternal, "err", err)
	}
	h.log.Info("app.invoke", "activity", observe.ActivityAudit,
		"principal_id", uint64(p.ID), "subject", subject, "thread_id", oc.threadID,
		"message_count", oc.messageCount, "request_bytes", oc.requestBytes,
		"status", oc.status, "duration_ms", dur.Milliseconds(), "outcome", oc.result)
}
