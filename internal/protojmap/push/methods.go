package push

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

// requireAccount validates the JMAP accountId against the
// authenticated principal. Empty values are accepted as "the calling
// principal".
func requireAccount(req jmapID, pid store.PrincipalID) *protojmap.MethodError {
	if req == "" {
		return nil
	}
	if req != protojmap.AccountIDForPrincipal(pid) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
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
		URL:            ps.URL,
		Keys: jmapKeys{
			P256DH: base64.RawURLEncoding.EncodeToString(ps.P256DH),
			Auth:   base64.RawURLEncoding.EncodeToString(ps.Auth),
		},
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

type getRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type getResponse struct {
	AccountID jmapID                 `json:"accountId"`
	State     string                 `json:"state"`
	List      []jmapPushSubscription `json:"list"`
	NotFound  []jmapID               `json:"notFound"`
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
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
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
		AccountID: req.AccountID,
		State:     state,
		List:      []jmapPushSubscription{},
		NotFound:  []jmapID{},
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

type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
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
	AccountID    jmapID                          `json:"accountId"`
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
	URL                    string          `json:"url"`
	Keys                   jmapKeys        `json:"keys"`
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
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
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
		AccountID:    req.AccountID,
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

// createSubscription validates a /set { create } payload, allocates
// the verification code, persists the row, and returns the freshly
// loaded store.PushSubscription so the caller can render it.
func (h *handlerSet) createSubscription(ctx context.Context, pid store.PrincipalID, in pushCreateInput) (store.PushSubscription, *setError, error) {
	if strings.TrimSpace(in.URL) == "" {
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"url"},
			Description: "url is required",
		}, nil
	}
	if !strings.HasPrefix(in.URL, "https://") {
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"url"},
			Description: "url must use https",
		}, nil
	}
	if strings.TrimSpace(in.Keys.P256DH) == "" || strings.TrimSpace(in.Keys.Auth) == "" {
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"keys"},
			Description: "keys.p256dh and keys.auth are required",
		}, nil
	}
	p256dh, err := decodeBase64URL(in.Keys.P256DH)
	if err != nil {
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"keys"},
			Description: "keys.p256dh: " + err.Error(),
		}, nil
	}
	if len(p256dh) != 65 || p256dh[0] != 0x04 {
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"keys"},
			Description: "keys.p256dh must be the 65-byte uncompressed P-256 form",
		}, nil
	}
	authBytes, err := decodeBase64URL(in.Keys.Auth)
	if err != nil {
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"keys"},
			Description: "keys.auth: " + err.Error(),
		}, nil
	}
	if len(authBytes) != 16 {
		return store.PushSubscription{}, &setError{
			Type: "invalidProperties", Properties: []string{"keys"},
			Description: "keys.auth must be 16 bytes",
		}, nil
	}
	row := store.PushSubscription{
		PrincipalID:            pid,
		DeviceClientID:         in.DeviceClientID,
		URL:                    in.URL,
		P256DH:                 p256dh,
		Auth:                   authBytes,
		Types:                  in.Types,
		VAPIDKeyAtRegistration: in.VAPIDKeyAtRegistration,
	}
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
	// TODO(3.8b-coord): send verification ping via outbound push
	// dispatcher. For 3.8a the row is created with Verified=false and
	// stays that way until the client echoes the verificationCode via
	// /set update.
	return persisted, nil, nil
}

// updateSubscription applies the wire-form patch to the row owned by
// pid. Per RFC 8620 §7.2 most fields are immutable post-create; the
// permitted mutables are expires, types, verificationCode (the
// handshake), notificationRules, and quietHours (tabard extension).
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
	for _, k := range []string{"id", "deviceClientId", "url", "keys", "vapidKeyAtRegistration"} {
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
