package push

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// verificationCodeBytes is the random byte length the server mints
// at create time and embeds in the outbound verification ping per
// RFC 8620 §7.2. 24 bytes -> 32 base64url characters; comfortably
// resistant to brute force without bloating wire frames.
const verificationCodeBytes = 24

// requirePrincipal pulls the authenticated principal id out of ctx.
// Returns a MethodError if the request reached the handler without
// authentication; mirrors the helper in mailbox/. The check is
// defensive — the dispatcher's requireAuth middleware already
// enforces auth — but a future dispatcher rewrite cannot silently
// leak privileges past it.
func requirePrincipal(ctx context.Context) (store.PrincipalID, *protojmap.MethodError) {
	p, ok := principalFromTestCtx(ctx)
	if !ok || p.ID == 0 {
		return 0, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	return p.ID, nil
}

// serverFail wraps an internal Go error into a JMAP method-error
// envelope.
func serverFail(err error) *protojmap.MethodError {
	if err == nil {
		return nil
	}
	return protojmap.NewMethodError("serverFail", err.Error())
}

// renderSubscription projects a store row to the wire-form object.
// verificationCode is exposed only when the row is unverified; once
// the client has confirmed the handshake the field disappears from
// /get responses (the spec treats the verification code as a one-
// time secret).
func renderSubscription(ps store.PushSubscription) jmapPushSubscription {
	out := jmapPushSubscription{
		ID:             jmapIDFromPush(ps.ID),
		DeviceClientID: ps.DeviceClientID,
		Kind:           string(ps.Transport.Normalized()),
		URL:            ps.URL,
		Keys: jmapKeys{
			P256DH: base64.RawURLEncoding.EncodeToString(ps.P256DH),
			Auth:   base64.RawURLEncoding.EncodeToString(ps.Auth),
		},
		FCMToken:               ps.FCMToken,
		Types:                  ps.Types,
		VAPIDKeyAtRegistration: ps.VAPIDKeyAtRegistration,
	}
	if !ps.Verified && ps.VerificationCode != "" {
		v := ps.VerificationCode
		out.VerificationCode = &v
	}
	if ps.Expires != nil {
		s := ps.Expires.UTC().Format(time.RFC3339)
		out.Expires = &s
	}
	if len(ps.NotificationRulesJSON) > 0 {
		var rules any
		if err := json.Unmarshal(ps.NotificationRulesJSON, &rules); err == nil {
			out.NotificationRules = rules
		} else {
			// Persisted JSON failed to parse — surface the raw bytes
			// as a JSON RawMessage rather than dropping the field
			// entirely. Should never happen on a row we wrote, but
			// keeps the response well-formed if some operator hand-
			// edited the database.
			out.NotificationRules = json.RawMessage(ps.NotificationRulesJSON)
		}
	}
	if ps.QuietHoursStartLocal != nil || ps.QuietHoursEndLocal != nil || ps.QuietHoursTZ != "" {
		qh := jmapQuietHours{TZ: ps.QuietHoursTZ}
		if ps.QuietHoursStartLocal != nil {
			qh.StartHourLocal = *ps.QuietHoursStartLocal
		}
		if ps.QuietHoursEndLocal != nil {
			qh.EndHourLocal = *ps.QuietHoursEndLocal
		}
		out.QuietHours = &qh
	}
	return out
}

// -- PushSubscription/get --------------------------------------------

// getRequest is the wire-form PushSubscription/get request.
// RFC 8620 §7.2 explicitly states that PushSubscription is not
// account-scoped; accountId is absent from this method's contract.
type getRequest struct {
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type getResponse struct {
	State    string                 `json:"state"`
	List     []jmapPushSubscription `json:"list"`
	NotFound []jmapID               `json:"notFound"`
}

type getHandler struct{ h *handlerSet }

func (g *getHandler) Method() string { return "PushSubscription/get" }

func (g *getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req getRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	state, err := currentState(ctx, g.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	rows, err := g.h.store.Meta().ListPushSubscriptionsByPrincipal(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp := getResponse{
		State:    state,
		List:     []jmapPushSubscription{},
		NotFound: []jmapID{},
	}
	if req.IDs == nil {
		for _, ps := range rows {
			resp.List = append(resp.List, renderSubscription(ps))
		}
		return resp, nil
	}
	byID := make(map[store.PushSubscriptionID]store.PushSubscription, len(rows))
	for _, ps := range rows {
		byID[ps.ID] = ps
	}
	for _, raw := range *req.IDs {
		id, ok := pushIDFromJMAP(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		ps, ok := byID[id]
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		resp.List = append(resp.List, renderSubscription(ps))
	}
	return resp, nil
}

// -- PushSubscription/set --------------------------------------------

// setRequest is the wire-form PushSubscription/set request.
// RFC 8620 §7.2: PushSubscription is session-scoped; no accountId.
type setRequest struct {
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[jmapID]json.RawMessage `json:"update"`
	Destroy   []jmapID                   `json:"destroy"`
}

type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

type setResponse struct {
	OldState     string                          `json:"oldState"`
	NewState     string                          `json:"newState"`
	Created      map[string]jmapPushSubscription `json:"created"`
	Updated      map[jmapID]any                  `json:"updated"`
	Destroyed    []jmapID                        `json:"destroyed"`
	NotCreated   map[string]setError             `json:"notCreated"`
	NotUpdated   map[jmapID]setError             `json:"notUpdated"`
	NotDestroyed map[jmapID]setError             `json:"notDestroyed"`
}

// pushCreateInput is the wire-form per-create object. Per RFC 8620
// §7.2 every field except verificationCode is set at create time
// (verificationCode is server-minted and returned in the response).
type pushCreateInput struct {
	DeviceClientID         string          `json:"deviceClientId"`
	Kind                   string          `json:"kind,omitempty"`
	URL                    string          `json:"url"`
	Keys                   jmapKeys        `json:"keys"`
	FCMToken               string          `json:"fcmToken,omitempty"`
	Expires                *string         `json:"expires"`
	Types                  []string        `json:"types"`
	NotificationRules      json.RawMessage `json:"notificationRules,omitempty"`
	QuietHours             *jmapQuietHours `json:"quietHours,omitempty"`
	VAPIDKeyAtRegistration string          `json:"vapidKeyAtRegistration,omitempty"`
}

// pushUpdateInput uses raw JSON for the optional / nullable fields so
// the patch can distinguish "absent" (no change) from explicit null
// (clear).
type pushUpdateInput struct {
	Expires           json.RawMessage `json:"expires"`
	Types             *[]string       `json:"types"`
	VerificationCode  *string         `json:"verificationCode"`
	NotificationRules json.RawMessage `json:"notificationRules"`
	QuietHours        json.RawMessage `json:"quietHours"`
}

type setHandler struct{ h *handlerSet }

func (s *setHandler) Method() string { return "PushSubscription/set" }

func (s *setHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req setRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	state, err := currentState(ctx, s.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch",
			"ifInState does not match current state")
	}
	resp := setResponse{
		OldState:     state,
		NewState:     state,
		Created:      map[string]jmapPushSubscription{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}

	for key, raw := range req.Create {
		var in pushCreateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				resp.NotCreated[key] = setError{
					Type: "invalidProperties", Description: err.Error(),
				}
				continue
			}
		}
		ps, serr, err := s.h.createSubscription(ctx, pid, in)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		resp.Created[key] = renderSubscription(ps)
	}

	for raw, payload := range req.Update {
		id, ok := pushIDFromJMAP(raw)
		if !ok {
			resp.NotUpdated[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := s.h.updateSubscription(ctx, pid, id, payload)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotUpdated[raw] = *serr
			continue
		}
		resp.Updated[raw] = nil
	}

	for _, raw := range req.Destroy {
		id, ok := pushIDFromJMAP(raw)
		if !ok {
			resp.NotDestroyed[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := s.h.destroySubscription(ctx, pid, id)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotDestroyed[raw] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	newState, err := currentState(ctx, s.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

// parseTransportKind validates the wire-form "kind" property (re
// #200). Empty defaults to Web Push (the pre-existing shape); any
// other value must be exactly "webpush" or "fcm".
func parseTransportKind(kind string) (store.PushTransport, *setError) {
	switch kind {
	case "", string(store.PushTransportWebPush):
		return store.PushTransportWebPush, nil
	case string(store.PushTransportFCM):
		return store.PushTransportFCM, nil
	default:
		return "", &setError{
			Type: "invalidProperties", Properties: []string{"kind"},
			Description: `kind must be "webpush" or "fcm"`,
		}
	}
}

// createSubscription validates a /set { create } payload, allocates
// the verification code, persists the row, and returns the freshly
// loaded store.PushSubscription so the caller can render it. Branches
// on the wire "kind" property (re #200): a Web Push row requires
// url + optional keys per RFC 8620 §7.2; an FCM row requires
// fcmToken and leaves url/keys empty.
func (h *handlerSet) createSubscription(ctx context.Context, pid store.PrincipalID, in pushCreateInput) (store.PushSubscription, *setError, error) {
	transport, serr := parseTransportKind(in.Kind)
	if serr != nil {
		return store.PushSubscription{}, serr, nil
	}
	var row store.PushSubscription
	if transport == store.PushTransportFCM {
		row, serr = buildFCMCreateRow(pid, in)
	} else {
		row, serr = h.buildWebPushCreateRow(ctx, pid, in)
	}
	if serr != nil {
		return store.PushSubscription{}, serr, nil
	}
	return h.finishCreate(ctx, pid, row, in)
}

// buildFCMCreateRow validates and builds the store row for a kind="fcm"
// /set { create } payload (re #200). url/keys are not accepted for
// this transport — the mobile client registers a device token instead
// of a Web Push endpoint.
func buildFCMCreateRow(pid store.PrincipalID, in pushCreateInput) (store.PushSubscription, *setError) {
	if strings.TrimSpace(in.FCMToken) == "" {
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"fcmToken"},
			Description: "fcmToken is required when kind is \"fcm\"",
		}
	}
	if in.URL != "" {
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"url"},
			Description: "url must not be supplied when kind is \"fcm\"",
		}
	}
	if in.Keys.P256DH != "" || in.Keys.Auth != "" {
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"keys"},
			Description: "keys must not be supplied when kind is \"fcm\"",
		}
	}
	return store.PushSubscription{
		PrincipalID:    pid,
		DeviceClientID: in.DeviceClientID,
		Transport:      store.PushTransportFCM,
		FCMToken:       in.FCMToken,
		Types:          in.Types,
	}, nil
}

// buildWebPushCreateRow validates and builds the store row for a
// kind="webpush" (or omitted-kind) /set { create } payload — the
// pre-existing RFC 8620 §7.2 shape.
//
// Endpoint egress policy (re #211): the URL is a caller-supplied
// target the server will POST to on every delivery, so it is run
// through h.endpointGuard's scheme/port validation and a DNS
// resolution check before the row is ever persisted. This is a
// fail-fast convenience, not the security boundary -- the DNS answer
// can legitimately change between now and the next delivery attempt,
// so the dispatcher's outbound HTTP client applies the identical
// policy again at dial time (internal/netguard.Guard.DialContext),
// which is the check that cannot be raced.
func (h *handlerSet) buildWebPushCreateRow(ctx context.Context, pid store.PrincipalID, in pushCreateInput) (store.PushSubscription, *setError) {
	if strings.TrimSpace(in.URL) == "" {
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"url"},
			Description: "url is required",
		}
	}
	if h.endpointGuard != nil {
		u, err := h.endpointGuard.ValidateURL(in.URL)
		if err != nil {
			return store.PushSubscription{}, &setError{
				Type: "invalidProperties", Properties: []string{"url"},
				Description: err.Error(),
			}
		}
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := h.endpointGuard.CheckHost(checkCtx, u.Hostname()); err != nil {
			return store.PushSubscription{}, &setError{
				Type: "invalidProperties", Properties: []string{"url"},
				Description: "url endpoint is not permitted: " + err.Error(),
			}
		}
	} else if !strings.HasPrefix(in.URL, "https://") {
		// No guard wired (test-only fixtures that do not exercise the
		// network policy; production always supplies one) -- fall back
		// to the scheme-only check this path always enforced.
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"url"},
			Description: "url must use https",
		}
	}
	// RFC 8620 §7.2: keys is optional. When provided (Web Push encrypted
	// delivery) both p256dh and auth must be present and well-formed.
	// When absent the server stores empty key material; the dispatcher
	// will send unencrypted notifications to such subscriptions or skip
	// them — that is a 3.8b concern. The test suite exercises keyless
	// create/get/destroy to verify the basic subscription lifecycle
	// without standing up a Web Push gateway.
	var p256dh, authBytes []byte
	if strings.TrimSpace(in.Keys.P256DH) != "" || strings.TrimSpace(in.Keys.Auth) != "" {
		// At least one key field supplied — require both.
		if strings.TrimSpace(in.Keys.P256DH) == "" || strings.TrimSpace(in.Keys.Auth) == "" {
			return store.PushSubscription{}, &setError{
				Type: "invalidProperties", Properties: []string{"keys"},
				Description: "keys.p256dh and keys.auth must both be supplied when keys is present",
			}
		}
		var err error
		p256dh, err = decodeBase64URL(in.Keys.P256DH)
		if err != nil {
			return store.PushSubscription{}, &setError{
				Type: "invalidProperties", Properties: []string{"keys"},
				Description: "keys.p256dh: " + err.Error(),
			}
		}
		if len(p256dh) != 65 || p256dh[0] != 0x04 {
			return store.PushSubscription{}, &setError{
				Type: "invalidProperties", Properties: []string{"keys"},
				Description: "keys.p256dh must be the 65-byte uncompressed P-256 form",
			}
		}
		authBytes, err = decodeBase64URL(in.Keys.Auth)
		if err != nil {
			return store.PushSubscription{}, &setError{
				Type: "invalidProperties", Properties: []string{"keys"},
				Description: "keys.auth: " + err.Error(),
			}
		}
		if len(authBytes) != 16 {
			return store.PushSubscription{}, &setError{
				Type: "invalidProperties", Properties: []string{"keys"},
				Description: "keys.auth must be 16 bytes",
			}
		}
	}
	return store.PushSubscription{
		PrincipalID:            pid,
		DeviceClientID:         in.DeviceClientID,
		Transport:              store.PushTransportWebPush,
		URL:                    in.URL,
		P256DH:                 p256dh,
		Auth:                   authBytes,
		Types:                  in.Types,
		VAPIDKeyAtRegistration: in.VAPIDKeyAtRegistration,
	}, nil
}

// finishCreate applies the transport-agnostic optional fields
// (expires, notificationRules, quietHours), mints the verification
// code, persists row, bumps the JMAP state, and fires the
// asynchronous verification ping. Shared by both the Web Push and
// FCM create paths (re #200).
func (h *handlerSet) finishCreate(ctx context.Context, pid store.PrincipalID, row store.PushSubscription, in pushCreateInput) (store.PushSubscription, *setError, error) {
	if in.Expires != nil && *in.Expires != "" {
		t, err := time.Parse(time.RFC3339, *in.Expires)
		if err != nil {
			return store.PushSubscription{}, &setError{
				Type: "invalidProperties", Properties: []string{"expires"},
				Description: "expires must be RFC 3339 / ISO 8601",
			}, nil
		}
		row.Expires = &t
	}
	if len(in.NotificationRules) > 0 {
		// Validate it parses as JSON; the rules-engine in 3.8c does the
		// real shape-check. Storing parse-failed bytes would force the
		// dispatcher to defend in 3.8b too.
		var probe any
		if err := json.Unmarshal(in.NotificationRules, &probe); err != nil {
			return store.PushSubscription{}, &setError{
				Type: "invalidProperties", Properties: []string{"notificationRules"},
				Description: "notificationRules must be valid JSON",
			}, nil
		}
		row.NotificationRulesJSON = append([]byte(nil), in.NotificationRules...)
	}
	if in.QuietHours != nil {
		if serr := validateQuietHours(*in.QuietHours); serr != nil {
			return store.PushSubscription{}, serr, nil
		}
		start := in.QuietHours.StartHourLocal
		end := in.QuietHours.EndHourLocal
		row.QuietHoursStartLocal = &start
		row.QuietHoursEndLocal = &end
		row.QuietHoursTZ = in.QuietHours.TZ
	}
	code, err := mintVerificationCode()
	if err != nil {
		return store.PushSubscription{}, nil, fmt.Errorf("push: mint verification code: %w", err)
	}
	row.VerificationCode = code
	row.Verified = false

	id, err := h.store.Meta().InsertPushSubscription(ctx, row)
	if err != nil {
		return store.PushSubscription{}, nil, fmt.Errorf("push: insert: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindPushSubscription); err != nil {
		return store.PushSubscription{}, nil, fmt.Errorf("push: bump state: %w", err)
	}
	persisted, err := h.store.Meta().GetPushSubscription(ctx, id)
	if err != nil {
		return store.PushSubscription{}, nil, fmt.Errorf("push: reload after insert: %w", err)
	}
	// Fire the RFC 8620 §7.2 verification ping asynchronously so the
	// JMAP response is not blocked on the gateway round-trip. The
	// dispatcher handles outcome accounting (delete on 410/404,
	// metric increment on success, log otherwise) and the row stays
	// Verified=false until the client echoes verificationCode via
	// /set update on the next JMAP request.
	if h.verifier != nil {
		go func(sub store.PushSubscription) {
			pingCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := h.verifier.SendVerificationPing(pingCtx, sub); err != nil {
				h.logger.LogAttrs(pingCtx, slog.LevelInfo,
					"push: verification ping failed",
					slog.Uint64("subscription_id", uint64(sub.ID)),
					slog.String("err", err.Error()))
			}
		}(persisted)
	}
	return persisted, nil, nil
}

// updateSubscription applies the wire-form patch to the row owned by
// pid. Per RFC 8620 §7.2 most fields are immutable post-create; the
// permitted mutables are expires, types, verificationCode (the
// handshake), notificationRules, and quietHours (suite extension).
func (h *handlerSet) updateSubscription(ctx context.Context, pid store.PrincipalID, id store.PushSubscriptionID, raw json.RawMessage) (*setError, error) {
	cur, err := h.store.Meta().GetPushSubscription(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("push: get: %w", err)
	}
	if cur.PrincipalID != pid {
		// Cross-principal access denied — surface as notFound per
		// RFC 8620 §5.3 so the existence of foreign rows does not
		// leak.
		return &setError{Type: "notFound"}, nil
	}
	var in pushUpdateInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return &setError{
				Type: "invalidProperties", Description: err.Error(),
			}, nil
		}
	}
	// Reject any attempt to mutate immutable fields. Decoding the raw
	// JSON twice — once into the typed shape above, once into a map
	// here — keeps the immutable check uniform across all attribute
	// names without expanding pushUpdateInput.
	var rawMap map[string]json.RawMessage
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &rawMap)
	}
	for _, k := range []string{"id", "deviceClientId", "kind", "url", "keys", "fcmToken", "vapidKeyAtRegistration"} {
		if _, present := rawMap[k]; present {
			return &setError{
				Type: "invalidProperties", Properties: []string{k},
				Description: "field is immutable post-create",
			}, nil
		}
	}

	// Apply patches.
	if len(in.Expires) > 0 {
		switch string(in.Expires) {
		case "null":
			cur.Expires = nil
		default:
			var s string
			if err := json.Unmarshal(in.Expires, &s); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"expires"},
					Description: "expires must be a string or null",
				}, nil
			}
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"expires"},
					Description: "expires must be RFC 3339",
				}, nil
			}
			cur.Expires = &t
		}
	}
	if in.Types != nil {
		cur.Types = *in.Types
	}
	if in.VerificationCode != nil {
		// RFC 8620 §7.2: matching the server-minted verificationCode
		// transitions Verified to true. A non-matching value clears
		// Verified back to false (defensive — a client that re-
		// registers under a different code starts fresh).
		if *in.VerificationCode == cur.VerificationCode && cur.VerificationCode != "" {
			cur.Verified = true
		} else {
			return &setError{
				Type: "invalidProperties", Properties: []string{"verificationCode"},
				Description: "verificationCode does not match server-issued value",
			}, nil
		}
	}
	if len(in.NotificationRules) > 0 {
		switch string(in.NotificationRules) {
		case "null":
			cur.NotificationRulesJSON = nil
		default:
			var probe any
			if err := json.Unmarshal(in.NotificationRules, &probe); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"notificationRules"},
					Description: "notificationRules must be valid JSON",
				}, nil
			}
			cur.NotificationRulesJSON = append([]byte(nil), in.NotificationRules...)
		}
	}
	if len(in.QuietHours) > 0 {
		switch string(in.QuietHours) {
		case "null":
			cur.QuietHoursStartLocal = nil
			cur.QuietHoursEndLocal = nil
			cur.QuietHoursTZ = ""
		default:
			var qh jmapQuietHours
			if err := json.Unmarshal(in.QuietHours, &qh); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"quietHours"},
					Description: "quietHours object malformed",
				}, nil
			}
			if serr := validateQuietHours(qh); serr != nil {
				return serr, nil
			}
			start := qh.StartHourLocal
			end := qh.EndHourLocal
			cur.QuietHoursStartLocal = &start
			cur.QuietHoursEndLocal = &end
			cur.QuietHoursTZ = qh.TZ
		}
	}

	if err := h.store.Meta().UpdatePushSubscription(ctx, cur); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("push: update: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindPushSubscription); err != nil {
		return nil, fmt.Errorf("push: bump state: %w", err)
	}
	return nil, nil
}

// destroySubscription removes the row owned by pid. Cross-principal
// destroys surface as notFound (the existence of the foreign row is
// not visible to the caller).
func (h *handlerSet) destroySubscription(ctx context.Context, pid store.PrincipalID, id store.PushSubscriptionID) (*setError, error) {
	cur, err := h.store.Meta().GetPushSubscription(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("push: get: %w", err)
	}
	if cur.PrincipalID != pid {
		return &setError{Type: "notFound"}, nil
	}
	if err := h.store.Meta().DeletePushSubscription(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("push: delete: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindPushSubscription); err != nil {
		return nil, fmt.Errorf("push: bump state: %w", err)
	}
	return nil, nil
}

// validateQuietHours enforces the REQ-PROTO-121 shape: 0..23 hours,
// non-empty IANA timezone (validated via time.LoadLocation so a
// typo is loud at create time, not on the first push attempt).
func validateQuietHours(qh jmapQuietHours) *setError {
	if qh.StartHourLocal < 0 || qh.StartHourLocal > 23 {
		return &setError{
			Type: "invalidProperties", Properties: []string{"quietHours"},
			Description: "quietHours.startHourLocal must be 0..23",
		}
	}
	if qh.EndHourLocal < 0 || qh.EndHourLocal > 23 {
		return &setError{
			Type: "invalidProperties", Properties: []string{"quietHours"},
			Description: "quietHours.endHourLocal must be 0..23",
		}
	}
	if qh.TZ == "" {
		return &setError{
			Type: "invalidProperties", Properties: []string{"quietHours"},
			Description: "quietHours.tz is required",
		}
	}
	if _, err := time.LoadLocation(qh.TZ); err != nil {
		return &setError{
			Type: "invalidProperties", Properties: []string{"quietHours"},
			Description: "quietHours.tz must be a valid IANA timezone",
		}
	}
	return nil
}

// decodeBase64URL accepts both padded and unpadded base64url strings
// (browsers emit unpadded; some JMAP libraries pad). The standard
// library has separate decoders for each form.
func decodeBase64URL(s string) ([]byte, error) {
	if strings.HasSuffix(s, "=") {
		return base64.URLEncoding.DecodeString(s)
	}
	return base64.RawURLEncoding.DecodeString(s)
}

// mintVerificationCode returns a fresh server-minted verification
// code per RFC 8620 §7.2. 24 random bytes -> 32 base64url characters.
func mintVerificationCode() (string, error) {
	buf := make([]byte, verificationCodeBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
