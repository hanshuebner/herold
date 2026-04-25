package protochat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// pidHeader is the X-Test-PID header the test session resolver
// reads to authenticate a request as principal id N. Mirrors the
// pattern internal/protoimg uses in its own server_test.go.
const pidHeader = "X-Test-PID"

func testResolver(r *http.Request) (store.PrincipalID, bool) {
	v := r.Header.Get(pidHeader)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return store.PrincipalID(n), true
}

// fakeMembership is a deterministic MembershipResolver / members
// resolver pair used by the protocol tests. Members are keyed on
// conversation id; lookups consult both maps.
type fakeMembership struct {
	mu      sync.RWMutex
	members map[string]map[store.PrincipalID]struct{}
}

func newFakeMembership() *fakeMembership {
	return &fakeMembership{members: make(map[string]map[store.PrincipalID]struct{})}
}

func (f *fakeMembership) addMember(conv string, pid store.PrincipalID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.members[conv]; !ok {
		f.members[conv] = make(map[store.PrincipalID]struct{})
	}
	f.members[conv][pid] = struct{}{}
}

func (f *fakeMembership) IsMember(_ context.Context, conv string, pid store.PrincipalID) (bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	m, ok := f.members[conv]
	if !ok {
		return false, nil
	}
	_, has := m[pid]
	return has, nil
}

func (f *fakeMembership) ListMembers(_ context.Context, conv string) ([]store.PrincipalID, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	m, ok := f.members[conv]
	if !ok {
		return nil, nil
	}
	out := make([]store.PrincipalID, 0, len(m))
	for pid := range m {
		out = append(out, pid)
	}
	return out, nil
}

// harness wires a Server up against a real httptest listener so the
// tests speak the actual TCP/upgrade path. Each test gets its own
// FakeClock anchored at a deterministic instant.
type harness struct {
	t       *testing.T
	srv     *Server
	bcast   *Broadcaster
	mem     *fakeMembership
	httpSrv *httptest.Server
	clk     *clock.FakeClock
}

type harnessOpt func(o *Options)

func newHarness(t *testing.T, opts ...harnessOpt) *harness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	mem := newFakeMembership()
	bcast := NewBroadcaster(slog.New(slog.NewTextHandler(io.Discard, nil)), mem.ListMembers)
	o := Options{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:           clk,
		SessionResolver: testResolver,
		Broadcaster:     bcast,
		Membership:      mem.IsMember,
		PingInterval:    24 * time.Hour,
		PongTimeout:     48 * time.Hour,
		MaxFrameBytes:   4096,
		PerPrincipalCap: 4,
		MaxConnections:  64,
		WriteQueueSize:  16,
		TypingAutoStop:  10 * time.Second,
		PresenceGrace:   30 * time.Second,
	}
	for _, fn := range opts {
		fn(&o)
	}
	srv := New(o)
	mux := http.NewServeMux()
	mux.Handle("/chat/ws", srv.Handler())
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return &harness{
		t:       t,
		srv:     srv,
		bcast:   bcast,
		mem:     mem,
		httpSrv: httpSrv,
		clk:     clk,
	}
}

// addr returns the host:port form of the test server's bound
// listener. Used to dial a raw net.Conn for the WebSocket upgrade.
func (h *harness) addr() string {
	u := h.httpSrv.URL
	u = strings.TrimPrefix(u, "http://")
	return u
}

// connect opens a WebSocket as principal pid. Fails the test on
// connection / upgrade errors.
func (h *harness) connect(pid store.PrincipalID) *testClient {
	h.t.Helper()
	c, _, err := dialTestClient(h.addr(), map[string]string{
		pidHeader: strconv.FormatUint(uint64(pid), 10),
	})
	if err != nil {
		h.t.Fatalf("dialTestClient: %v", err)
	}
	if c == nil {
		h.t.Fatalf("dialTestClient: handshake rejected")
	}
	h.t.Cleanup(c.Close)
	return c
}

// ----- upgrade tests -----

func TestUpgrade_NoSession_401(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.httpSrv.URL + "/chat/ws")
	if err != nil {
		t.Fatalf("http.Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", resp.StatusCode)
	}
}

func TestUpgrade_BadHeaders_400(t *testing.T) {
	h := newHarness(t)
	// Use a normal http.Get (no Upgrade header) but supply a session.
	req, _ := http.NewRequest("GET", h.httpSrv.URL+"/chat/ws", nil)
	req.Header.Set(pidHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestUpgrade_HappyPath_AcceptHashCorrect(t *testing.T) {
	h := newHarness(t)
	// dialTestClient verifies the Sec-WebSocket-Accept hash itself
	// against computeAcceptKey on the client side; reaching this
	// line without a fatal means the hash matched.
	c := h.connect(1)
	c.Close()
}

// ----- frame codec tests -----

func TestFrames_TextRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.mem.addMember("c1", 1)
	h.mem.addMember("c1", 2)
	c := h.connect(1)

	// Send presence.set; the server should ack it (presence.set is
	// the simplest round-trippable op because it requires no
	// peer).
	body := mustJSON(t, ClientFrame{
		Type:     clientTypePresenceSet,
		Payload:  mustJSON(t, presencePayload{State: "online"}),
		ClientID: "x1",
	})
	if err := c.writeText(body); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	deadlineUnblock(t, c)
	sf, err := c.readServerFrame()
	if err != nil {
		t.Fatalf("readServerFrame: %v", err)
	}
	if sf.Type != ServerTypeAck {
		t.Fatalf("type: got %q want %q", sf.Type, ServerTypeAck)
	}
	if sf.Ack != "x1" {
		t.Fatalf("ack: got %q want x1", sf.Ack)
	}
}

func TestFrames_LargePayload_Rejected(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.MaxFrameBytes = 256 })
	c := h.connect(1)
	// 512 bytes — over the 256 cap.
	big := strings.Repeat("a", 512)
	if err := c.writeText([]byte(`{"type":"presence.set","payload":{"state":"` + big + `"}}`)); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	deadlineUnblock(t, c)
	if _, err := c.readUntilClose(); err != nil {
		t.Fatalf("readUntilClose: %v", err)
	}
}

func TestFrames_UnmaskedClientFrame_ProtocolError(t *testing.T) {
	h := newHarness(t)
	c := h.connect(1)
	// Send an unmasked text frame. RFC 6455 §5.1: server MUST
	// close.
	if err := c.writeUnmasked(opText, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("writeUnmasked: %v", err)
	}
	deadlineUnblock(t, c)
	code, err := c.readUntilClose()
	if err != nil {
		t.Fatalf("readUntilClose: %v", err)
	}
	if code != closeProtocolError {
		t.Fatalf("close code: got %d want %d", code, closeProtocolError)
	}
}

// ----- protocol tests -----

func TestProtocol_TypingStart_FansOutToMembers(t *testing.T) {
	h := newHarness(t)
	h.mem.addMember("c1", 1)
	h.mem.addMember("c1", 2)
	a := h.connect(1)
	b := h.connect(2)

	body := mustJSON(t, ClientFrame{
		Type:    clientTypeTypingStart,
		Payload: mustJSON(t, typingPayload{ConversationID: "c1"}),
	})
	if err := a.writeText(body); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	sf, err := b.readServerFrame()
	if err != nil {
		t.Fatalf("readServerFrame: %v", err)
	}
	if sf.Type != ServerTypeTyping {
		t.Fatalf("type: got %q want %q", sf.Type, ServerTypeTyping)
	}
	var ot outboundTyping
	if err := json.Unmarshal(sf.Payload, &ot); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ot.ConversationID != "c1" || ot.SenderPrincipalID != 1 || ot.State != "start" {
		t.Fatalf("payload: got %+v", ot)
	}
}

func TestProtocol_TypingAutoStop_After10s(t *testing.T) {
	h := newHarness(t)
	h.mem.addMember("c1", 1)
	h.mem.addMember("c1", 2)
	a := h.connect(1)
	b := h.connect(2)

	body := mustJSON(t, ClientFrame{
		Type:    clientTypeTypingStart,
		Payload: mustJSON(t, typingPayload{ConversationID: "c1"}),
	})
	if err := a.writeText(body); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	// First frame on b is the typing.start fanout.
	sf, err := b.readServerFrame()
	if err != nil {
		t.Fatalf("readServerFrame: %v", err)
	}
	if sf.Type != ServerTypeTyping {
		t.Fatalf("first type: got %q", sf.Type)
	}
	var first outboundTyping
	_ = json.Unmarshal(sf.Payload, &first)
	if first.State != "start" {
		t.Fatalf("first state: got %q", first.State)
	}
	// Advance the FakeClock past 10s. The auto-stop goroutine is
	// blocked on the FakeClock.After channel so it fires
	// deterministically here.
	h.clk.Advance(11 * time.Second)
	sf, err = b.readServerFrame()
	if err != nil {
		t.Fatalf("readServerFrame: %v", err)
	}
	var second outboundTyping
	_ = json.Unmarshal(sf.Payload, &second)
	if second.State != "stop" {
		t.Fatalf("auto-stop state: got %q", second.State)
	}
}

func TestProtocol_PresenceSet_BroadcastsToSubscribers(t *testing.T) {
	h := newHarness(t)
	h.mem.addMember("c1", 1)
	h.mem.addMember("c1", 2)
	a := h.connect(1)
	b := h.connect(2)

	// b subscribes to c1; a's presence.set should fan out to b.
	if err := b.writeText(mustJSON(t, ClientFrame{
		Type:     clientTypeSubscribe,
		Payload:  mustJSON(t, subscribePayload{ConversationIDs: []string{"c1"}}),
		ClientID: "sub1",
	})); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// Drain the ack so subsequent reads see only the presence
	// fanout.
	if sf, err := b.readServerFrame(); err != nil {
		t.Fatalf("subscribe ack: %v", err)
	} else if sf.Type != ServerTypeAck {
		t.Fatalf("subscribe ack: type %q want %q", sf.Type, ServerTypeAck)
	}

	if err := a.writeText(mustJSON(t, ClientFrame{
		Type:    clientTypePresenceSet,
		Payload: mustJSON(t, presencePayload{State: "online"}),
	})); err != nil {
		t.Fatalf("presence: %v", err)
	}
	sf, err := b.readServerFrame()
	if err != nil {
		t.Fatalf("readServerFrame: %v", err)
	}
	if sf.Type != ServerTypePresence {
		t.Fatalf("type: got %q want %q", sf.Type, ServerTypePresence)
	}
	var op outboundPresence
	if err := json.Unmarshal(sf.Payload, &op); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if op.PrincipalID != 1 || op.State != "online" {
		t.Fatalf("payload: got %+v", op)
	}
}

func TestProtocol_Subscribe_ValidatesMembership(t *testing.T) {
	h := newHarness(t)
	// pid=1 is a member of c1 only; subscribe with [c1, c2] should
	// allow c1 and reject c2.
	h.mem.addMember("c1", 1)
	c := h.connect(1)
	if err := c.writeText(mustJSON(t, ClientFrame{
		Type:     clientTypeSubscribe,
		Payload:  mustJSON(t, subscribePayload{ConversationIDs: []string{"c1", "c2"}}),
		ClientID: "s1",
	})); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	sf, err := c.readServerFrame()
	if err != nil {
		t.Fatalf("readServerFrame: %v", err)
	}
	if sf.Type != ServerTypeError {
		t.Fatalf("type: got %q want %q", sf.Type, ServerTypeError)
	}
	if sf.Error == nil || sf.Error.Code != ErrCodeNotMember {
		t.Fatalf("error: got %+v", sf.Error)
	}
	if !strings.Contains(sf.Error.Message, "c2") {
		t.Fatalf("error message: %q should mention c2", sf.Error.Message)
	}
}

func TestProtocol_CallSignal_ForwardsToTargetMember(t *testing.T) {
	h := newHarness(t)
	h.mem.addMember("c1", 1)
	h.mem.addMember("c1", 2)
	a := h.connect(1)
	b := h.connect(2)
	body := mustJSON(t, ClientFrame{
		Type: clientTypeCallSignal,
		Payload: mustJSON(t, callSignalPayload{
			ConversationID: "c1",
			TargetID:       2,
			Kind:           "offer",
			Payload:        json.RawMessage(`{"sdp":"..."}`),
		}),
	})
	if err := a.writeText(body); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	sf, err := b.readServerFrame()
	if err != nil {
		t.Fatalf("readServerFrame: %v", err)
	}
	if sf.Type != ServerTypeCallSignal {
		t.Fatalf("type: got %q want %q", sf.Type, ServerTypeCallSignal)
	}
	var ocs outboundCallSignal
	if err := json.Unmarshal(sf.Payload, &ocs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ocs.FromPrincipalID != 1 || ocs.Kind != "offer" {
		t.Fatalf("payload: got %+v", ocs)
	}
}

func TestProtocol_RateLimit_DropsAndEmitsError(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		// Override the limiter knobs by replacing the limiter
		// directly after construction; the test's harness path
		// has access to the unexported field via the same
		// package.
	})
	// Replace the rate limiter with a strict version: 1 token
	// burst, never replenishing within the test window. The
	// second frame must be dropped with ErrCodeRateLimited.
	h.srv.rateLimiter = newRateLimiter(h.clk, 0.0001, 1)
	c := h.connect(1)
	if err := c.writeText([]byte(`{"type":"ping","clientId":"a"}`)); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	first, err := c.readServerFrame()
	if err != nil || first.Type != ServerTypePong {
		t.Fatalf("first: type=%q err=%v", first.Type, err)
	}
	if err := c.writeText([]byte(`{"type":"ping","clientId":"b"}`)); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	second, err := c.readServerFrame()
	if err != nil {
		t.Fatalf("readServerFrame: %v", err)
	}
	if second.Type != ServerTypeError || second.Error == nil ||
		second.Error.Code != ErrCodeRateLimited {
		t.Fatalf("second: %+v", second)
	}
}

// ----- heartbeat / disconnect / backpressure -----

func TestHeartbeat_NoPongWithinTimeout_ClosesConnection(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.PingInterval = 50 * time.Millisecond
		o.PongTimeout = 100 * time.Millisecond
	})
	c := h.connect(1)
	// Refuse to respond to pings — drain raw frames and discard.
	// After PongTimeout elapses with no pong, the server closes.
	gotClose := make(chan closeCode, 1)
	go func() {
		for {
			op, payload, err := c.readNext()
			if err != nil {
				return
			}
			if op == opClose {
				code, _ := decodeCloseFrame(payload)
				gotClose <- code
				return
			}
			// Ignore pings — do NOT respond.
		}
	}()
	// Drive the FakeClock so that ping fires and pong-timeout
	// elapses. Each ping resets — we need the gap between the
	// last pong and now to exceed pongTimeout.
	h.clk.Advance(50 * time.Millisecond)  // first ping
	time.Sleep(20 * time.Millisecond)     // let read pump observe
	h.clk.Advance(200 * time.Millisecond) // exceed timeout

	select {
	case code := <-gotClose:
		if code != closePolicyViolation {
			t.Fatalf("close code: got %d want %d", code, closePolicyViolation)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected close within 2s")
	}
}

func TestPresence_DisconnectGracePeriod_Offline_After30s(t *testing.T) {
	h := newHarness(t)
	c := h.connect(1)
	// Set presence online so the tracker has something to record.
	if err := c.writeText(mustJSON(t, ClientFrame{
		Type:    clientTypePresenceSet,
		Payload: mustJSON(t, presencePayload{State: "online"}),
	})); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	// Drain whatever frames arrive (we don't care about order).
	go func() {
		for {
			if _, _, err := c.readNext(); err != nil {
				return
			}
		}
	}()
	// Disconnect.
	c.Close()
	// Wait for the disconnect to register on the broadcaster.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !h.bcast.HasConnection(1) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h.bcast.HasConnection(1) {
		t.Fatalf("connection not removed from broadcaster")
	}
	// Before grace expires the state should still be "online".
	if pr, ok := h.srv.presence.Get(1); !ok || pr.State != "online" {
		t.Fatalf("pre-grace presence: %+v ok=%v", pr, ok)
	}
	// Advance past the 30s grace.
	h.clk.Advance(31 * time.Second)
	// Poll for the offline transition.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pr, ok := h.srv.presence.Get(1); ok && pr.State == "offline" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pr, ok := h.srv.presence.Get(1); !ok || pr.State != "offline" {
		t.Fatalf("post-grace presence: %+v ok=%v", pr, ok)
	}
}

// blockingSender is a Sender whose Send blocks until released. Used
// to simulate a slow consumer for the backpressure test.
type blockingSender struct {
	pid     store.PrincipalID
	sent    int
	cap     int
	mu      sync.Mutex
	dropped int
}

func (b *blockingSender) Send(_ ServerFrame) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sent >= b.cap {
		b.dropped++
		return ErrFull
	}
	b.sent++
	return nil
}

func (b *blockingSender) Principal() store.PrincipalID { return b.pid }

func TestBackpressure_SlowClient_DroppedWithWarn(t *testing.T) {
	bcast := NewBroadcaster(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	slow := &blockingSender{pid: 7, cap: 2}
	bcast.Register(7, slow)
	for i := 0; i < 10; i++ {
		bcast.Emit(7, ServerFrame{Type: ServerTypeTyping})
	}
	delivered, dropped := bcast.Stats()
	if delivered != 2 {
		t.Fatalf("delivered: got %d want 2", delivered)
	}
	if dropped != 8 {
		t.Fatalf("dropped: got %d want 8", dropped)
	}
}

// ----- external handler registration (track D hand-off) -----

func TestRegisterHandler_OverridesBuiltInDispatch(t *testing.T) {
	h := newHarness(t)
	h.mem.addMember("c1", 1)
	type captured struct {
		from  store.PrincipalID
		frame ClientFrame
	}
	gotCh := make(chan captured, 1)
	if err := h.srv.RegisterHandler(clientTypeCallSignal, func(_ context.Context, from store.PrincipalID, f ClientFrame) {
		gotCh <- captured{from: from, frame: f}
	}); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}
	c := h.connect(1)
	body := mustJSON(t, ClientFrame{
		Type:    clientTypeCallSignal,
		Payload: json.RawMessage(`{"conversationId":"c1","kind":"offer","payload":{}}`),
	})
	if err := c.writeText(body); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	select {
	case got := <-gotCh:
		if got.from != 1 {
			t.Fatalf("from: got %d want 1", got.from)
		}
		if got.frame.Type != clientTypeCallSignal {
			t.Fatalf("type: got %q want %q", got.frame.Type, clientTypeCallSignal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler not invoked within 2s")
	}
}

func TestRegisterHandler_RejectsEmptyTypeOrNilFn(t *testing.T) {
	h := newHarness(t)
	if err := h.srv.RegisterHandler("", func(context.Context, store.PrincipalID, ClientFrame) {}); err == nil {
		t.Fatal("expected error for empty frameType")
	}
	if err := h.srv.RegisterHandler("call.signal", nil); err == nil {
		t.Fatal("expected error for nil handler")
	}
}

// ----- frame codec unit tests (encoder / decoder symmetry, no socket) -----

func TestFrameCodec_RoundTrip(t *testing.T) {
	for _, plen := range []int{0, 1, 125, 126, 1024, 70000} {
		body := make([]byte, plen)
		for i := range body {
			body[i] = byte(i % 251)
		}
		var buf strings.Builder
		buf.Grow(plen + 16)
		// Server-to-client frame (unmasked).
		out := &writerSpy{}
		if err := writeFrame(out, frame{fin: true, opcode: opText, payload: body}); err != nil {
			t.Fatalf("plen=%d writeFrame: %v", plen, err)
		}
		// Decode as a server-emitted frame: clientToServer=false.
		f, err := readFrame(strings.NewReader(out.String()), false, plen+8)
		if err != nil {
			t.Fatalf("plen=%d readFrame: %v", plen, err)
		}
		if !f.fin || f.opcode != opText {
			t.Fatalf("plen=%d header: %+v", plen, f)
		}
		if len(f.payload) != plen {
			t.Fatalf("plen=%d payload len: got %d", plen, len(f.payload))
		}
		for i, b := range f.payload {
			if b != body[i] {
				t.Fatalf("plen=%d payload byte %d: got %x want %x", plen, i, b, body[i])
			}
		}
	}
}

// writerSpy adapts a bytes-only buffer to io.Writer for the codec
// test. We avoid bytes.Buffer here only to keep the test's import
// surface tight and to surface allocations clearly in the trace.
type writerSpy struct{ buf []byte }

func (w *writerSpy) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}
func (w *writerSpy) String() string { return string(w.buf) }

// ----- helpers -----

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return b
}

// deadlineUnblock sets a short read deadline on the underlying
// connection so a misbehaving server can't hang the test forever.
func deadlineUnblock(t *testing.T, c *testClient) {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
}
