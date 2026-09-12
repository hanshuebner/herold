// Package fakefcm is a stand-in for Firebase Cloud Messaging's HTTP v1
// messages:send endpoint, for tests and development. It stands in for FCM
// so herold's FCM push transport (internal/fcm) can be exercised end to end
// without a real Firebase project (re #334, re #200).
//
// The server accepts POST requests on its messages:send path, records the
// bearer token, registration token, data payload, and Android priority of
// every accepted send, and returns FCM's normal 200 response shape. A
// registration token can be marked "unregistered" so the server returns
// FCM's 404 UNREGISTERED response for it instead, exercising the
// subscription-destroy path.
//
// For tests, use New which registers cleanup on testing.TB. For standalone
// dev-tooling outside a test, use NewServer and call Close when done.
package fakefcm

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

// Message is one accepted messages:send call recorded by the server.
type Message struct {
	// Token is the FCM registration token the message targeted.
	Token string `json:"token"`
	// Data is the data-only payload's string-valued fields.
	Data map[string]string `json:"data,omitempty"`
	// AndroidPriority is the wire value of message.android.priority
	// ("HIGH", "NORMAL", or "" when unset).
	AndroidPriority string `json:"android_priority,omitempty"`
	// AuthHeader is the raw Authorization header value presented
	// ("Bearer <token>").
	AuthHeader string `json:"auth_header,omitempty"`
}

// wireAndroidConfig / wireMessage / wireEnvelope mirror the FCM HTTP v1
// request body internal/fcm.Sender sends; duplicated here (rather than
// imported) so the fake parses the actual wire shape, not the sender's
// internal type.
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

// Options configures a Server. The zero value is usable.
type Options struct {
	// ProjectID names the Firebase project embedded in the send path
	// and in the response's "name" field. Defaults to "devfake".
	ProjectID string
}

// Server is a running fake FCM HTTP v1 endpoint.
type Server struct {
	ln        net.Listener
	srv       *http.Server
	projectID string
	seq       atomic.Uint64

	mu           sync.Mutex
	messages     []Message
	unregistered map[string]bool
}

// NewServer starts a fake FCM server bound to 127.0.0.1 on a kernel-picked
// port and returns it. Call Close when done. For test code that wants
// automatic cleanup, use New instead.
func NewServer(opts Options) (*Server, error) {
	if opts.ProjectID == "" {
		opts.ProjectID = "devfake"
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fakefcm: listen: %w", err)
	}
	s := &Server{
		ln:           ln,
		projectID:    opts.ProjectID,
		unregistered: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc(s.sendPath(), s.handleSend)
	mux.HandleFunc("/messages", s.handleMessages)
	mux.HandleFunc("/unregister", s.handleUnregister)
	s.srv = &http.Server{Handler: mux}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// New starts a fake FCM server bound to 127.0.0.1 on a kernel-picked port
// and registers cleanup on t.
func New(t testing.TB, opts Options) *Server {
	t.Helper()
	s, err := NewServer(opts)
	if err != nil {
		t.Fatalf("fakefcm.New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// sendPath is the path component of FCM HTTP v1's messages:send endpoint
// for the server's configured project id.
func (s *Server) sendPath() string {
	return fmt.Sprintf("/v1/projects/%s/messages:send", s.projectID)
}

// SendURL is the full messages:send endpoint URL, suitable for
// [server.push] fcm_base_url or internal/fcm.Options.BaseURL. It is used
// verbatim by internal/fcm.Sender (no further project-id templating).
func (s *Server) SendURL() string {
	return fmt.Sprintf("http://%s%s", s.ln.Addr().String(), s.sendPath())
}

// Addr is the "host:port" the server is listening on.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Port is the listening TCP port, for callers that need to allowlist it
// against a [server.push.network] SSRF guard (internal/netguard).
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var env wireEnvelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"status": "INVALID_ARGUMENT", "message": err.Error()},
		})
		return
	}
	msg := Message{
		Token:      env.Message.Token,
		Data:       env.Message.Data,
		AuthHeader: r.Header.Get("Authorization"),
	}
	if env.Message.Android != nil {
		msg.AndroidPriority = env.Message.Android.Priority
	}

	s.mu.Lock()
	unregistered := s.unregistered[msg.Token]
	s.messages = append(s.messages, msg)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if unregistered {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"status": "UNREGISTERED", "message": "requested entity was not found"},
		})
		return
	}
	name := fmt.Sprintf("projects/%s/messages/%d", s.projectID, s.seq.Add(1))
	_ = json.NewEncoder(w).Encode(map[string]string{"name": name})
}

// handleMessages serves the recorded messages for test assertions.
//
//	GET    /messages — JSON array of Message.
//	DELETE /messages — clears recorded messages; 204 on success.
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Messages())
	case http.MethodDelete:
		s.mu.Lock()
		s.messages = nil
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUnregister marks or clears a registration token's "unregistered"
// status so the next messages:send for it returns 404 UNREGISTERED.
//
//	POST   /unregister {"token": "..."} — marks the token unregistered.
//	DELETE /unregister                  — clears every marked token.
func (s *Server) handleUnregister(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
			http.Error(w, "invalid body: {\"token\": \"...\"} required", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.unregistered[body.Token] = true
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		s.mu.Lock()
		s.unregistered = map[string]bool{}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Messages returns a snapshot of every accepted messages:send call.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, len(s.messages))
	copy(out, s.messages)
	return out
}

// SetUnregistered marks (or clears, when unregistered is false) token as
// FCM's UNREGISTERED response target for the next messages:send call.
func (s *Server) SetUnregistered(token string, unregistered bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if unregistered {
		s.unregistered[token] = true
	} else {
		delete(s.unregistered, token)
	}
}

// Close stops the server.
func (s *Server) Close() {
	_ = s.srv.Close()
}
