package protocall

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// fakeBroadcaster captures every emitted envelope. Per-test setup
// initialises one and inspects events after HandleSignal returns.
type fakeBroadcaster struct {
	mu     sync.Mutex
	events []emittedEvent
	// failNext, when true, makes the next Emit return an error. Used
	// to confirm protocall swallows broadcaster failures rather than
	// crashing.
	failNext bool
}

type emittedEvent struct {
	To  store.PrincipalID
	Env ServerEnvelope
}

func (b *fakeBroadcaster) Emit(_ context.Context, to store.PrincipalID, env ServerEnvelope) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failNext {
		b.failNext = false
		return errors.New("fake: emit failure")
	}
	b.events = append(b.events, emittedEvent{To: to, Env: env})
	return nil
}

func (b *fakeBroadcaster) snapshot() []emittedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]emittedEvent, len(b.events))
	copy(out, b.events)
	return out
}

// fakeMembers / fakePresence / fakeSysmsgs satisfy the dependency
// interfaces with simple in-memory state.
type fakeMembers struct {
	byConv map[string][]store.PrincipalID
}

func (f *fakeMembers) ConversationMembers(_ context.Context, conv string) ([]store.PrincipalID, error) {
	m, ok := f.byConv[conv]
	if !ok {
		return nil, errors.New("conversation not found")
	}
	out := make([]store.PrincipalID, len(m))
	copy(out, m)
	return out, nil
}

type fakePresence struct {
	online map[store.PrincipalID]bool
}

func (f *fakePresence) IsOnline(p store.PrincipalID) bool { return f.online[p] }

type fakeSysmsgs struct {
	mu       sync.Mutex
	inserted []insertedSysmsg
	failOn   string
}

type insertedSysmsg struct {
	ConversationID string
	Sender         store.PrincipalID
	Payload        SystemMessagePayload
}

func (f *fakeSysmsgs) InsertChatSystemMessage(_ context.Context, conv string, sender store.PrincipalID, payload []byte) error {
	var p SystemMessagePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	if f.failOn != "" && p.Kind == f.failOn {
		return errors.New("fake: sysmsg insert failure")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserted = append(f.inserted, insertedSysmsg{
		ConversationID: conv,
		Sender:         sender,
		Payload:        p,
	})
	return nil
}

func (f *fakeSysmsgs) snapshot() []insertedSysmsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]insertedSysmsg, len(f.inserted))
	copy(out, f.inserted)
	return out
}

// newFixture sets up a server wired against fakes for a 1:1 DM
// between principals 10 and 20 (both online).
func newFixture(t *testing.T) (*Server, *fakeBroadcaster, *fakeMembers, *fakePresence, *fakeSysmsgs, *clock.FakeClock) {
	t.Helper()
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	bc := &fakeBroadcaster{}
	mb := &fakeMembers{byConv: map[string][]store.PrincipalID{
		"conv-dm":    {10, 20},
		"conv-group": {10, 20, 30},
		"conv-self":  {10},
	}}
	pr := &fakePresence{online: map[store.PrincipalID]bool{10: true, 20: true, 30: true}}
	sm := &fakeSysmsgs{}
	s := New(Options{
		Clock:          clk,
		Broadcaster:    bc,
		Members:        mb,
		Presence:       pr,
		SystemMessages: sm,
	})
	t.Cleanup(func() { _ = s.Close() })
	return s, bc, mb, pr, sm, clk
}

func encodePayload(t *testing.T, p SignalPayload) ClientFrame {
	t.Helper()
	buf, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return ClientFrame{Type: "call.signal", Payload: buf}
}

func TestSignal_Offer_CreatesSystemMessageAndForwards(t *testing.T) {
	s, bc, _, _, sm, _ := newFixture(t)

	frame := encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
		SDP:            "v=0\r\n...",
	})
	s.HandleSignal(context.Background(), 10, frame)

	events := bc.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	got := events[0]
	if got.To != 20 {
		t.Fatalf("forwarded to %d, want 20", got.To)
	}
	if got.Env.Type != "call.signal" {
		t.Fatalf("envelope type = %q, want call.signal", got.Env.Type)
	}
	fwd, ok := got.Env.Payload.(SignalPayload)
	if !ok {
		t.Fatalf("payload type = %T, want SignalPayload", got.Env.Payload)
	}
	if fwd.CallID == "" {
		t.Fatalf("server did not mint a CallID on the offer")
	}
	if fwd.SDP != "v=0\r\n..." {
		t.Fatalf("forwarded SDP altered: %q", fwd.SDP)
	}
	// One system message recorded.
	inserted := sm.snapshot()
	if len(inserted) != 1 {
		t.Fatalf("system messages = %d, want 1", len(inserted))
	}
	if inserted[0].Payload.Kind != SystemMessageCallStarted {
		t.Fatalf("system kind = %q, want %q", inserted[0].Payload.Kind, SystemMessageCallStarted)
	}
	if inserted[0].Payload.CallID != fwd.CallID {
		t.Fatalf("system call_id %q != forwarded %q", inserted[0].Payload.CallID, fwd.CallID)
	}
	if inserted[0].Payload.CallerPrincipal != 10 {
		t.Fatalf("system caller = %d, want 10", inserted[0].Payload.CallerPrincipal)
	}
	// In-flight session count = 1.
	if got := s.SessionCount(); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
}

func TestSignal_Offer_RecipientOffline_RejectsWithError(t *testing.T) {
	s, bc, _, pr, sm, _ := newFixture(t)
	pr.online[20] = false

	frame := encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
		SDP:            "v=0\r\n...",
	})
	s.HandleSignal(context.Background(), 10, frame)

	events := bc.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (error to caller)", len(events))
	}
	if events[0].To != 10 {
		t.Fatalf("error sent to %d, want 10 (caller)", events[0].To)
	}
	if events[0].Env.Type != "call.error" {
		t.Fatalf("envelope type = %q, want call.error", events[0].Env.Type)
	}
	ep, ok := events[0].Env.Payload.(ErrorPayload)
	if !ok || ep.Code != ErrCodeRecipientOffline {
		t.Fatalf("payload = %#v, want ErrCodeRecipientOffline", events[0].Env.Payload)
	}
	if len(sm.snapshot()) != 0 {
		t.Fatalf("system message inserted on rejected offer")
	}
	if s.SessionCount() != 0 {
		t.Fatalf("session created for rejected offer")
	}
}

func TestSignal_Offer_GroupConversation_RejectsWithGroupCallsUnsupported(t *testing.T) {
	s, bc, _, _, _, _ := newFixture(t)

	frame := encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-group",
	})
	s.HandleSignal(context.Background(), 10, frame)

	events := bc.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ep, ok := events[0].Env.Payload.(ErrorPayload)
	if !ok || ep.Code != ErrCodeGroupCallsUnsupported {
		t.Fatalf("payload = %#v, want ErrCodeGroupCallsUnsupported", events[0].Env.Payload)
	}
	if events[0].To != 10 {
		t.Fatalf("error to %d, want 10", events[0].To)
	}
}

func TestSignal_Answer_ForwardsToCallerOnly(t *testing.T) {
	s, bc, _, _, _, _ := newFixture(t)
	// Set up a call with caller=10, recipient=20.
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
		SDP:            "offer-sdp",
	}))
	startEvents := bc.snapshot()
	callID := startEvents[0].Env.Payload.(SignalPayload).CallID

	// Recipient answers.
	s.HandleSignal(context.Background(), 20, encodePayload(t, SignalPayload{
		Kind:           SignalKindAnswer,
		CallID:         callID,
		ConversationID: "conv-dm",
		SDP:            "answer-sdp",
	}))

	allEvents := bc.snapshot()
	if len(allEvents) != 2 {
		t.Fatalf("events = %d, want 2", len(allEvents))
	}
	last := allEvents[1]
	if last.To != 10 {
		t.Fatalf("answer forwarded to %d, want 10", last.To)
	}
	fwd, ok := last.Env.Payload.(SignalPayload)
	if !ok {
		t.Fatalf("payload type = %T, want SignalPayload", last.Env.Payload)
	}
	if fwd.SDP != "answer-sdp" || fwd.Kind != SignalKindAnswer {
		t.Fatalf("forwarded payload = %#v, want answer", fwd)
	}
	// ICE-candidate from the caller side flows the other way.
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindIceCandidate,
		CallID:         callID,
		ConversationID: "conv-dm",
		Candidate:      "candidate:1 1 udp ...",
		SDPMid:         "0",
		SDPMLineIndex:  0,
	}))
	allEvents = bc.snapshot()
	if len(allEvents) != 3 {
		t.Fatalf("events = %d, want 3", len(allEvents))
	}
	if allEvents[2].To != 20 {
		t.Fatalf("ice forwarded to %d, want 20", allEvents[2].To)
	}
}

func TestSignal_Hangup_InsertsCallEndedSystemMessageAndForwards(t *testing.T) {
	s, bc, _, _, sm, clk := newFixture(t)
	// Start a call.
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
	}))
	startEvents := bc.snapshot()
	callID := startEvents[0].Env.Payload.(SignalPayload).CallID

	// Advance clock so duration > 0.
	clk.Advance(45 * time.Second)

	// Recipient hangs up.
	s.HandleSignal(context.Background(), 20, encodePayload(t, SignalPayload{
		Kind:           SignalKindHangup,
		CallID:         callID,
		ConversationID: "conv-dm",
		Reason:         "bye",
	}))

	events := bc.snapshot()
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	hangupEv := events[1]
	if hangupEv.To != 10 {
		t.Fatalf("hangup forwarded to %d, want 10 (caller)", hangupEv.To)
	}
	hp, ok := hangupEv.Env.Payload.(SignalPayload)
	if !ok || hp.Kind != SignalKindHangup || hp.Reason != "bye" {
		t.Fatalf("forwarded payload = %#v", hangupEv.Env.Payload)
	}
	// Two system messages: started + ended.
	inserted := sm.snapshot()
	if len(inserted) != 2 {
		t.Fatalf("system messages = %d, want 2 (started+ended)", len(inserted))
	}
	if inserted[0].Payload.Kind != SystemMessageCallStarted {
		t.Fatalf("first sysmsg kind = %q", inserted[0].Payload.Kind)
	}
	endedMsg := inserted[1].Payload
	if endedMsg.Kind != SystemMessageCallEnded {
		t.Fatalf("second sysmsg kind = %q, want call.ended", endedMsg.Kind)
	}
	if endedMsg.CallID != callID {
		t.Fatalf("ended call_id = %q, want %q", endedMsg.CallID, callID)
	}
	if endedMsg.DurationSeconds != 45 {
		t.Fatalf("duration = %d, want 45", endedMsg.DurationSeconds)
	}
	if endedMsg.HangupReason != "bye" {
		t.Fatalf("hangup_reason = %q, want bye", endedMsg.HangupReason)
	}
	if endedMsg.HangupPrincipal != 20 {
		t.Fatalf("hangup_principal = %d, want 20", endedMsg.HangupPrincipal)
	}
	// Session dropped.
	if s.SessionCount() != 0 {
		t.Fatalf("session not dropped after hangup; count = %d", s.SessionCount())
	}
}

func TestSignal_UnknownCallID_Rejected(t *testing.T) {
	s, bc, _, _, _, _ := newFixture(t)

	// Answer for a call that was never offered.
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindAnswer,
		CallID:         "doesnotexist",
		ConversationID: "conv-dm",
		SDP:            "...",
	}))

	events := bc.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ep, ok := events[0].Env.Payload.(ErrorPayload)
	if !ok || ep.Code != ErrCodeUnknownCall {
		t.Fatalf("payload = %#v, want ErrCodeUnknownCall", events[0].Env.Payload)
	}
}

func TestSignal_NotMember_Rejected(t *testing.T) {
	s, bc, _, _, _, _ := newFixture(t)

	// Principal 99 is not a member of conv-dm.
	s.HandleSignal(context.Background(), 99, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
	}))

	events := bc.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ep, ok := events[0].Env.Payload.(ErrorPayload)
	if !ok || ep.Code != ErrCodeNotMember {
		t.Fatalf("payload = %#v, want ErrCodeNotMember", events[0].Env.Payload)
	}
}

func TestSignal_InvalidPayload_Rejected(t *testing.T) {
	s, bc, _, _, _, _ := newFixture(t)

	bad := ClientFrame{Type: "call.signal", Payload: []byte("not-json")}
	s.HandleSignal(context.Background(), 10, bad)

	events := bc.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ep, ok := events[0].Env.Payload.(ErrorPayload)
	if !ok || ep.Code != ErrCodeInvalidPayload {
		t.Fatalf("payload = %#v, want ErrCodeInvalidPayload", events[0].Env.Payload)
	}
}

func TestCallSession_TimeoutReap(t *testing.T) {
	s, bc, _, _, _, clk := newFixture(t)

	// Start a call.
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
	}))
	if got := s.SessionCount(); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
	_ = bc.snapshot()

	// Advance past callSessionTTL and run reapOnce directly.
	clk.Advance(callSessionTTL + time.Minute)
	s.reapOnce()
	if got := s.SessionCount(); got != 0 {
		t.Fatalf("sessions after reap = %d, want 0", got)
	}
}

func TestSignal_HangupOnUnknownCall_DoesNotPanic(t *testing.T) {
	s, bc, _, _, sm, _ := newFixture(t)

	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindHangup,
		CallID:         "missing",
		ConversationID: "conv-dm",
	}))

	events := bc.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (error)", len(events))
	}
	ep, ok := events[0].Env.Payload.(ErrorPayload)
	if !ok || ep.Code != ErrCodeUnknownCall {
		t.Fatalf("payload = %#v, want ErrCodeUnknownCall", events[0].Env.Payload)
	}
	if len(sm.snapshot()) != 0 {
		t.Fatalf("sysmsg inserted on unknown-call hangup")
	}
}

func TestSignal_OfferIdempotent_OnRetransmit(t *testing.T) {
	s, bc, _, _, sm, _ := newFixture(t)

	// Caller transmits an offer with an explicit callID.
	frame1 := encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		CallID:         "client-minted-call-1",
		ConversationID: "conv-dm",
		SDP:            "v=0\r\n...",
	})
	s.HandleSignal(context.Background(), 10, frame1)
	// Retransmits the same offer (e.g. on reconnect).
	s.HandleSignal(context.Background(), 10, frame1)

	if got := s.SessionCount(); got != 1 {
		t.Fatalf("sessions = %d, want 1 (idempotent)", got)
	}
	// Two forwards — both legitimate, the recipient handles dedupe.
	if got := len(bc.snapshot()); got != 2 {
		t.Fatalf("events = %d, want 2", got)
	}
	// Only one system message: call.started.
	if got := len(sm.snapshot()); got != 1 {
		t.Fatalf("sysmsgs = %d, want 1 (no duplicate call.started)", got)
	}
}
