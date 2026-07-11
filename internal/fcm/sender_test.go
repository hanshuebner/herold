package fcm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureGateway records the last request Sender.Send made and
// replies with a fixed status + body.
type captureGateway struct {
	srv        *httptest.Server
	lastAuth   string
	lastBody   []byte
	lastMethod string
	lastPath   string
	status     int
	respBody   string
}

func newCaptureGateway(status int, respBody string) *captureGateway {
	g := &captureGateway{status: status, respBody: respBody}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.lastAuth = r.Header.Get("Authorization")
		g.lastMethod = r.Method
		g.lastPath = r.URL.Path
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		g.lastBody = body
		w.WriteHeader(g.status)
		_, _ = w.Write([]byte(g.respBody))
	}))
	return g
}

func (g *captureGateway) Close() { g.srv.Close() }

func TestSender_Send_Success(t *testing.T) {
	gw := newCaptureGateway(http.StatusOK, `{"name":"projects/p/messages/1"}`)
	defer gw.Close()

	s, err := New(Options{
		Tokens:   StaticTokenSource("tok-abc"),
		HTTPDoer: gw.srv.Client(),
		BaseURL:  gw.srv.URL + "/v1/projects/test/messages:send",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	status, body, err := s.Send(context.Background(), Message{
		Token: "device-token-1",
		Data:  map[string]string{"payload": `{"kind":"email"}`},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(string(body), "messages/1") {
		t.Fatalf("body = %q, want the fake response echoed back", body)
	}
	if gw.lastMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gw.lastMethod)
	}
	if gw.lastAuth != "Bearer tok-abc" {
		t.Fatalf("Authorization = %q, want Bearer tok-abc", gw.lastAuth)
	}
	var env wireEnvelope
	if err := json.Unmarshal(gw.lastBody, &env); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if env.Message.Token != "device-token-1" {
		t.Fatalf("message.token = %q, want device-token-1", env.Message.Token)
	}
	if env.Message.Data["payload"] != `{"kind":"email"}` {
		t.Fatalf("message.data.payload = %q", env.Message.Data["payload"])
	}
}

func TestSender_Send_PropagatesStatusOnError(t *testing.T) {
	gw := newCaptureGateway(http.StatusNotFound, `{"error":{"status":"UNREGISTERED"}}`)
	defer gw.Close()

	s, err := New(Options{
		Tokens:   StaticTokenSource("tok"),
		HTTPDoer: gw.srv.Client(),
		BaseURL:  gw.srv.URL + "/v1/projects/test/messages:send",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	status, body, err := s.Send(context.Background(), Message{Token: "stale-token"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if !strings.Contains(string(body), "UNREGISTERED") {
		t.Fatalf("body = %q, want the UNREGISTERED error surfaced", body)
	}
}

func TestSender_Send_TokenSourceErrorPropagates(t *testing.T) {
	s, err := New(Options{
		Tokens:  failingTokenSource{},
		BaseURL: "http://unused.invalid/messages:send",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, _, err = s.Send(context.Background(), Message{Token: "t"})
	if err == nil {
		t.Fatalf("expected an error when the token source fails")
	}
}

type failingTokenSource struct{}

func (failingTokenSource) Token(context.Context) (string, error) {
	return "", errTokenSourceFailed
}

var errTokenSourceFailed = errors.New("token source failed")

func TestNew_RequiresCredentialOrTokenSource(t *testing.T) {
	if _, err := New(Options{BaseURL: "http://unused.invalid"}); err == nil {
		t.Fatalf("expected an error when neither ServiceAccountJSON nor Tokens is set")
	}
}

func TestNew_DerivesBaseURLFromServiceAccountProjectID(t *testing.T) {
	// A minimal, syntactically valid service-account JSON. New must be
	// able to read project_id from it to build the default BaseURL even
	// though the JWT flow itself is never exercised here (Tokens is
	// supplied separately in the dispatcher e2e; this test only checks
	// project-id derivation errors do not surface for a well-formed key).
	// private_key is a placeholder string, not a PEM block: New only
	// stores it verbatim (google.JWTConfigFromJSON does not parse key
	// material at load time), and signing happens lazily on first
	// Token(), which this test never triggers — BaseURL derivation
	// completes before any network or crypto operation.
	saJSON := []byte(`{
		"type": "service_account",
		"project_id": "my-firebase-project",
		"private_key_id": "abc",
		"private_key": "not-a-real-key-material-placeholder",
		"client_email": "svc@my-firebase-project.iam.gserviceaccount.com",
		"token_uri": "https://oauth2.googleapis.com/token"
	}`)
	_, err := New(Options{ServiceAccountJSON: saJSON})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
}
