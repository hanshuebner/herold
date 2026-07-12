package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Scope is the OAuth2 scope FCM HTTP v1 requires on the service-
// account bearer token.
const Scope = "https://www.googleapis.com/auth/firebase.messaging"

// DefaultBaseURLTemplate is the production FCM HTTP v1 send endpoint;
// "%s" is the Firebase project id. Options.BaseURL overrides this —
// tests point it at an in-tree httptest.Server per the fakes-before-
// fixes rule (re #200); no test ever talks to the real endpoint.
const DefaultBaseURLTemplate = "https://fcm.googleapis.com/v1/projects/%s/messages:send"

// HTTPDoer is the minimal Do interface Sender consumes. The stdlib
// *http.Client implements it.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// TokenSource returns a bearer token for the FCM HTTP v1 Authorization
// header. The production implementation wraps golang.org/x/oauth2's
// service-account JWT flow (which caches and refreshes internally);
// tests inject a stub returning a fixed token so the fake-FCM e2e
// never depends on reaching Google's real OAuth token endpoint.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// oauth2TokenSource adapts an oauth2.TokenSource, as returned by
// golang.org/x/oauth2/google's service-account JWT config, to the
// narrower TokenSource interface Sender consumes.
type oauth2TokenSource struct{ ts oauth2.TokenSource }

func (o oauth2TokenSource) Token(_ context.Context) (string, error) {
	tok, err := o.ts.Token()
	if err != nil {
		return "", fmt.Errorf("fcm: mint token: %w", err)
	}
	return tok.AccessToken, nil
}

// StaticTokenSource returns a TokenSource that always yields token
// unchanged. Used by tests and by any deployment that fronts FCM with
// its own token-minting sidecar rather than an in-process
// service-account credential.
func StaticTokenSource(token string) TokenSource {
	return staticTokenSource(token)
}

type staticTokenSource string

func (s staticTokenSource) Token(context.Context) (string, error) { return string(s), nil }

// Options configures a Sender.
type Options struct {
	// ServiceAccountJSON is the Firebase/GCP service-account key JSON
	// (type "service_account"), as downloaded from the Firebase console
	// or `gcloud iam service-accounts keys create`. Required unless
	// Tokens is supplied directly.
	ServiceAccountJSON []byte
	// ProjectID overrides the project id embedded in
	// ServiceAccountJSON's "project_id" field. Only needed when Tokens
	// is supplied directly (no JSON to read the id from) and BaseURL
	// is left empty.
	ProjectID string
	// Tokens overrides the token source entirely. Production callers
	// leave this nil so New derives a service-account JWT token
	// source from ServiceAccountJSON; tests set it to
	// StaticTokenSource("fake-token") so no OAuth round-trip to Google
	// occurs.
	Tokens TokenSource
	// HTTPDoer performs the FCM send request. Defaults to
	// http.DefaultClient.
	HTTPDoer HTTPDoer
	// BaseURL overrides the FCM messages:send endpoint. Empty uses
	// DefaultBaseURLTemplate with the resolved project id. Tests point
	// this at an httptest.Server so the whole flow — event -> gateway
	// -> fake FCM — runs deterministically in-tree.
	BaseURL string
}

// serviceAccountProbe reads just the field New needs to derive the
// default BaseURL, without depending on golang.org/x/oauth2/google's
// unexported credentialsFile shape.
type serviceAccountProbe struct {
	ProjectID string `json:"project_id"`
}

// Sender is the outbound FCM HTTP v1 client. Construct via New; Send
// is safe for concurrent use (the underlying oauth2.TokenSource caches
// and refreshes its own token; HTTPDoer implementations used in
// production, *http.Client, are also concurrency-safe).
type Sender struct {
	doer    HTTPDoer
	tokens  TokenSource
	baseURL string
}

// New builds a Sender from opts. When opts.Tokens is nil,
// ServiceAccountJSON is parsed via golang.org/x/oauth2/google's
// service-account JWT flow scoped to Scope.
func New(opts Options) (*Sender, error) {
	doer := opts.HTTPDoer
	if doer == nil {
		doer = http.DefaultClient
	}
	tokens := opts.Tokens
	projectID := opts.ProjectID
	if tokens == nil {
		if len(opts.ServiceAccountJSON) == 0 {
			return nil, errors.New("fcm: Options.ServiceAccountJSON or Options.Tokens is required")
		}
		cfg, err := google.JWTConfigFromJSON(opts.ServiceAccountJSON, Scope)
		if err != nil {
			return nil, fmt.Errorf("fcm: parse service account JSON: %w", err)
		}
		tokens = oauth2TokenSource{ts: cfg.TokenSource(context.Background())}
		if projectID == "" {
			var probe serviceAccountProbe
			if err := json.Unmarshal(opts.ServiceAccountJSON, &probe); err == nil {
				projectID = probe.ProjectID
			}
		}
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		if projectID == "" {
			return nil, errors.New("fcm: project id unresolved; set Options.ProjectID, Options.BaseURL, or a service_account JSON with a project_id field")
		}
		baseURL = fmt.Sprintf(DefaultBaseURLTemplate, projectID)
	}
	return &Sender{doer: doer, tokens: tokens, baseURL: baseURL}, nil
}

// Message is the payload Send delivers to one FCM registration token.
// Data-only: FCM's "data" fields must be string-valued, so the caller
// (webpush.Dispatcher) carries the already-built JSON payload as a
// single field rather than flattening it — the Android client parses
// it the same way it would parse the Web Push plaintext envelope.
type Message struct {
	Token string
	Data  map[string]string
	// AndroidPriority sets FCM HTTP v1's AndroidConfig.priority (the
	// AndroidMessagePriority enum: PriorityHigh or PriorityNormal).
	// Empty leaves the field unset on the wire, which FCM treats as
	// NORMAL for a data-only message — callers that need prompt,
	// Doze-piercing delivery (re #200 priority-mapping follow-up) set
	// this to PriorityHigh explicitly rather than relying on that
	// default.
	AndroidPriority string
}

// AndroidMessagePriority values for Message.AndroidPriority, matching
// FCM HTTP v1's wire enum verbatim (re #200 priority-mapping
// follow-up): https://firebase.google.com/docs/reference/fcm/rest/v1/projects.messages#AndroidMessagePriority
const (
	PriorityHigh   = "HIGH"
	PriorityNormal = "NORMAL"
)

// wireMessage / wireEnvelope / wireAndroidConfig mirror the FCM HTTP
// v1 request body:
// https://firebase.google.com/docs/reference/fcm/rest/v1/projects.messages
type wireAndroidConfig struct {
	Priority string `json:"priority,omitempty"`
}

type wireMessage struct {
	Token   string             `json:"token"`
	Data    map[string]string  `json:"data,omitempty"`
	Android *wireAndroidConfig `json:"android,omitempty"`
}

type wireEnvelope struct {
	Message wireMessage `json:"message"`
}

// Send POSTs msg to the FCM HTTP v1 endpoint. status is the raw HTTP
// status code FCM returned (200 success; 404 UNREGISTERED — the token
// is stale; 5xx transient; other 4xx — INVALID_ARGUMENT,
// SENDER_ID_MISMATCH, QUOTA_EXCEEDED). body is the raw response body,
// returned for logging on unexpected statuses. A non-nil err means the
// request never produced an FCM response (token mint failure, network
// error, ...) — the caller treats that as transient the same way a
// Web Push POST's transport error is treated.
func (s *Sender) Send(ctx context.Context, msg Message) (status int, body []byte, err error) {
	tok, err := s.tokens.Token(ctx)
	if err != nil {
		return 0, nil, err
	}
	wire := wireMessage{Token: msg.Token, Data: msg.Data}
	if msg.AndroidPriority != "" {
		wire.Android = &wireAndroidConfig{Priority: msg.AndroidPriority}
	}
	payload, err := json.Marshal(wireEnvelope{Message: wire})
	if err != nil {
		return 0, nil, fmt.Errorf("fcm: marshal message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("fcm: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := s.doer.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("fcm: post: %w", err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp.StatusCode, nil, fmt.Errorf("fcm: read response body: %w", readErr)
	}
	return resp.StatusCode, respBody, nil
}
