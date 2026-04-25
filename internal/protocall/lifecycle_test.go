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

	// Recipient answers — without this the hangup is treated as a
	// missed call (ring window was never crossed). We want the
	// "completed" disposition path here.
	s.HandleSignal(context.Background(), 20, encodePayload(t, SignalPayload{
		Kind:           SignalKindAnswer,
		CallID:         callID,
		ConversationID: "conv-dm",
		SDP:            "answer-sdp",
	}))

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
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (offer-fwd, answer-fwd, hangup-fwd)", len(events))
	}
	hangupEv := events[2]
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
	if endedMsg.Disposition != DispositionCompleted {
		t.Fatalf("disposition = %q, want %q", endedMsg.Disposition, DispositionCompleted)
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
	s, bc, _, _, sm, clk := newFixture(t)

	// Start a call.
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
	}))
	if got := s.SessionCount(); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
	startEvents := bc.snapshot()
	if len(startEvents) != 1 {
		t.Fatalf("events after offer = %d, want 1", len(startEvents))
	}
	callID := startEvents[0].Env.Payload.(SignalPayload).CallID

	// Recipient answers so the ring timer is cancelled and cannot
	// pre-empt the reaper's sysmsg.
	s.HandleSignal(context.Background(), 20, encodePayload(t, SignalPayload{
		Kind:           SignalKindAnswer,
		CallID:         callID,
		ConversationID: "conv-dm",
		SDP:            "answer-sdp",
	}))

	// Now simulate the "stuck call" case: never receive a hangup,
	// just advance past callSessionTTL. reapOnce drops the session
	// AND writes a call.ended sysmsg with disposition="timeout".
	clk.Advance(callSessionTTL + time.Minute)
	s.reapOnce()
	if got := s.SessionCount(); got != 0 {
		t.Fatalf("sessions after reap = %d, want 0", got)
	}
	inserted := sm.snapshot()
	var sawTimeout bool
	for _, ins := range inserted {
		if ins.Payload.Kind == SystemMessageCallEnded &&
			ins.Payload.Disposition == DispositionTimeout &&
			ins.Payload.CallID == callID &&
			ins.Payload.HangupReason == "stale" {
			sawTimeout = true
		}
	}
	if !sawTimeout {
		t.Fatalf("reaper did not write a call.ended sysmsg with disposition=timeout; got %+v", inserted)
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

// TestSignal_RingTimeout_FiresAndEmitsTimeoutAndMissed pins
// REQ-CALL-06: a fresh offer that is not answered inside the ring
// window produces (a) a synthetic call.signal kind="timeout" to the
// offerer and (b) a call.ended sysmsg with disposition="missed".
func TestSignal_RingTimeout_FiresAndEmitsTimeoutAndMissed(t *testing.T) {
	s, bc, _, _, sm, clk := newFixture(t)
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
	}))
	// Confirm the offer registered a session.
	if got := s.SessionCount(); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
	startEvents := bc.snapshot()
	callID := startEvents[0].Env.Payload.(SignalPayload).CallID

	// Fire the ring timer by advancing past the default ring window.
	clk.Advance(DefaultRingTimeout + time.Second)

	// Session is gone.
	if got := s.SessionCount(); got != 0 {
		t.Fatalf("sessions after ring timeout = %d, want 0", got)
	}
	// Ring-timeout fanout: original offer-forward to recipient (20)
	// plus synthetic timeout signal to caller (10).
	events := bc.snapshot()
	if len(events) < 2 {
		t.Fatalf("events = %d, want >=2", len(events))
	}
	var timeoutEv *emittedEvent
	for i := range events {
		ev := events[i]
		sp, ok := ev.Env.Payload.(SignalPayload)
		if !ok {
			continue
		}
		if sp.Kind == SignalKindTimeout && sp.CallID == callID {
			timeoutEv = &events[i]
			break
		}
	}
	if timeoutEv == nil {
		t.Fatalf("no kind=timeout signal observed; events=%+v", events)
	}
	if timeoutEv.To != 10 {
		t.Fatalf("timeout sent to %d, want 10 (offerer)", timeoutEv.To)
	}
	if timeoutEv.Env.Type != "call.signal" {
		t.Fatalf("timeout envelope type = %q, want call.signal", timeoutEv.Env.Type)
	}
	// Sysmsg side: call.started + call.ended(missed).
	inserted := sm.snapshot()
	if len(inserted) != 2 {
		t.Fatalf("sysmsgs = %d, want 2 (started+ended-missed); got %+v", len(inserted), inserted)
	}
	if inserted[1].Payload.Kind != SystemMessageCallEnded {
		t.Fatalf("second sysmsg kind = %q, want call.ended", inserted[1].Payload.Kind)
	}
	if inserted[1].Payload.Disposition != DispositionMissed {
		t.Fatalf("disposition = %q, want missed", inserted[1].Payload.Disposition)
	}
	if inserted[1].Payload.HangupReason != "ring_timeout" {
		t.Fatalf("hangup_reason = %q, want ring_timeout", inserted[1].Payload.HangupReason)
	}
}

// TestSignal_RingTimeout_AnswerCancelsTimer pins that the ring timer
// is cancelled when the recipient answers inside the ring window: a
// later Advance MUST NOT produce a kind=timeout signal.
func TestSignal_RingTimeout_AnswerCancelsTimer(t *testing.T) {
	s, bc, _, _, sm, clk := newFixture(t)
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
	}))
	startEvents := bc.snapshot()
	callID := startEvents[0].Env.Payload.(SignalPayload).CallID

	// Recipient answers well before the ring window expires.
	clk.Advance(5 * time.Second)
	s.HandleSignal(context.Background(), 20, encodePayload(t, SignalPayload{
		Kind:           SignalKindAnswer,
		CallID:         callID,
		ConversationID: "conv-dm",
		SDP:            "answer-sdp",
	}))

	// Advance past the ring window. The timer must not fire.
	clk.Advance(DefaultRingTimeout + time.Second)

	// No call.ended sysmsg should exist.
	for _, ins := range sm.snapshot() {
		if ins.Payload.Kind == SystemMessageCallEnded {
			t.Fatalf("call.ended sysmsg written despite answer cancelling ring timer: %+v", ins.Payload)
		}
	}
	// No kind=timeout signal should be in the broadcaster log.
	for _, ev := range bc.snapshot() {
		if sp, ok := ev.Env.Payload.(SignalPayload); ok && sp.Kind == SignalKindTimeout {
			t.Fatalf("kind=timeout fired despite answer; ev=%+v", ev)
		}
	}
}

// TestSignal_Decline_EmitsDeclinedSysmsgAndForwards pins REQ-CALL-30:
// a recipient decline tears down the session, writes a call.ended
// sysmsg with disposition="declined", and forwards the decline to
// the offerer.
func TestSignal_Decline_EmitsDeclinedSysmsgAndForwards(t *testing.T) {
	s, bc, _, _, sm, _ := newFixture(t)
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
	}))
	startEvents := bc.snapshot()
	callID := startEvents[0].Env.Payload.(SignalPayload).CallID

	s.HandleSignal(context.Background(), 20, encodePayload(t, SignalPayload{
		Kind:           SignalKindDecline,
		CallID:         callID,
		ConversationID: "conv-dm",
		Reason:         "no thanks",
	}))

	if s.SessionCount() != 0 {
		t.Fatalf("session not dropped after decline")
	}
	events := bc.snapshot()
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (offer-fwd, decline-fwd)", len(events))
	}
	declineEv := events[1]
	if declineEv.To != 10 {
		t.Fatalf("decline forwarded to %d, want 10 (offerer)", declineEv.To)
	}
	dp, ok := declineEv.Env.Payload.(SignalPayload)
	if !ok || dp.Kind != SignalKindDecline {
		t.Fatalf("decline payload = %#v", declineEv.Env.Payload)
	}
	inserted := sm.snapshot()
	if len(inserted) != 2 {
		t.Fatalf("sysmsgs = %d, want 2 (started+ended-declined); got %+v", len(inserted), inserted)
	}
	endedMsg := inserted[1].Payload
	if endedMsg.Kind != SystemMessageCallEnded {
		t.Fatalf("ended sysmsg kind = %q, want %q", endedMsg.Kind, SystemMessageCallEnded)
	}
	if endedMsg.Disposition != DispositionDeclined {
		t.Fatalf("disposition = %q, want %q", endedMsg.Disposition, DispositionDeclined)
	}
	if endedMsg.HangupPrincipal != 20 {
		t.Fatalf("hangup_principal = %d, want 20", endedMsg.HangupPrincipal)
	}
}

// TestSignal_Decline_FromCaller_Rejected: only the recipient may
// decline a call. A caller-side decline is an invalid payload.
func TestSignal_Decline_FromCaller_Rejected(t *testing.T) {
	s, bc, _, _, _, _ := newFixture(t)
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
	}))
	startEvents := bc.snapshot()
	callID := startEvents[0].Env.Payload.(SignalPayload).CallID
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindDecline,
		CallID:         callID,
		ConversationID: "conv-dm",
	}))
	events := bc.snapshot()
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (offer-fwd, error)", len(events))
	}
	if events[1].To != 10 {
		t.Fatalf("error sent to %d, want 10 (caller)", events[1].To)
	}
	ep, ok := events[1].Env.Payload.(ErrorPayload)
	if !ok || ep.Code != ErrCodeInvalidPayload {
		t.Fatalf("payload = %#v, want ErrCodeInvalidPayload", events[1].Env.Payload)
	}
	// Session still in flight (declining as caller is invalid).
	if s.SessionCount() != 1 {
		t.Fatalf("session count = %d, want 1", s.SessionCount())
	}
}

// TestSignal_BusyAndTimeout_FromClient_Rejected: server-emitted
// kinds (busy, timeout) are not permitted on the inbound side. A
// client sending one gets an invalid_payload error.
func TestSignal_BusyAndTimeout_FromClient_Rejected(t *testing.T) {
	for _, kind := range []string{SignalKindBusy, SignalKindTimeout} {
		t.Run(kind, func(t *testing.T) {
			s, bc, _, _, _, _ := newFixture(t)
			s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
				Kind:           kind,
				CallID:         "anything",
				ConversationID: "conv-dm",
			}))
			events := bc.snapshot()
			if len(events) != 1 {
				t.Fatalf("events = %d, want 1", len(events))
			}
			ep, ok := events[0].Env.Payload.(ErrorPayload)
			if !ok || ep.Code != ErrCodeInvalidPayload {
				t.Fatalf("payload = %#v, want ErrCodeInvalidPayload", events[0].Env.Payload)
			}
		})
	}
}

// TestSignal_ConcurrentCallLimit_OffererBusy pins REQ-CALL-43 from
// the caller side: a second offer from a principal already in a
// call gets a synthetic kind=busy back AND no second session is
// registered.
func TestSignal_ConcurrentCallLimit_OffererBusy(t *testing.T) {
	s, bc, mb, pr, _, _ := newFixture(t)
	// Extend the membership so caller (10) has a second DM with a
	// third principal (40) — needed so the busy-on-caller branch
	// can be exercised without colliding with the recipient-busy
	// branch.
	mb.byConv["conv-dm-2"] = []store.PrincipalID{10, 40}
	pr.online[40] = true

	// First offer succeeds.
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
	}))
	if s.SessionCount() != 1 {
		t.Fatalf("first offer did not register; sessions=%d", s.SessionCount())
	}
	bc.mu.Lock()
	bc.events = nil
	bc.mu.Unlock()

	// Second offer to a different DM by the same caller: refused with busy.
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm-2",
	}))
	if s.SessionCount() != 1 {
		t.Fatalf("second offer registered despite busy; sessions=%d", s.SessionCount())
	}
	events := bc.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (busy to offerer)", len(events))
	}
	if events[0].To != 10 {
		t.Fatalf("busy sent to %d, want 10 (offerer)", events[0].To)
	}
	bp, ok := events[0].Env.Payload.(SignalPayload)
	if !ok || bp.Kind != SignalKindBusy {
		t.Fatalf("payload = %#v, want kind=busy", events[0].Env.Payload)
	}
}

// TestSignal_ConcurrentCallLimit_RecipientBusy pins REQ-CALL-43 from
// the recipient side: an offer to a principal already in a call
// produces a synthetic kind=busy to the OFFERER (not the recipient,
// who is mid-call), refuses the new session, AND writes a missed
// sysmsg in the offerer's conversation.
func TestSignal_ConcurrentCallLimit_RecipientBusy(t *testing.T) {
	s, bc, mb, pr, sm, _ := newFixture(t)
	// Add a DM between principals 30 and 20 — principal 20 is the
	// recipient who will be busy when 30 tries to call them.
	mb.byConv["conv-dm-30-20"] = []store.PrincipalID{30, 20}
	pr.online[30] = true

	// 10 calls 20 first.
	s.HandleSignal(context.Background(), 10, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
	}))
	if s.SessionCount() != 1 {
		t.Fatalf("first offer did not register; sessions=%d", s.SessionCount())
	}
	bc.mu.Lock()
	bc.events = nil
	bc.mu.Unlock()
	sm.mu.Lock()
	sm.inserted = nil
	sm.mu.Unlock()

	// 30 now offers 20. 20 is busy.
	s.HandleSignal(context.Background(), 30, encodePayload(t, SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm-30-20",
	}))
	if s.SessionCount() != 1 {
		t.Fatalf("recipient-busy offer registered a session; sessions=%d", s.SessionCount())
	}
	events := bc.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (busy to offerer 30)", len(events))
	}
	if events[0].To != 30 {
		t.Fatalf("busy sent to %d, want 30 (offerer)", events[0].To)
	}
	bp, ok := events[0].Env.Payload.(SignalPayload)
	if !ok || bp.Kind != SignalKindBusy {
		t.Fatalf("payload = %#v, want kind=busy", events[0].Env.Payload)
	}
	// Missed sysmsg recorded in 30's conversation with no call.started.
	inserted := sm.snapshot()
	if len(inserted) != 1 {
		t.Fatalf("sysmsgs = %d, want 1 (missed); got %+v", len(inserted), inserted)
	}
	if inserted[0].Payload.Kind != SystemMessageCallEnded {
		t.Fatalf("kind = %q, want %q", inserted[0].Payload.Kind, SystemMessageCallEnded)
	}
	if inserted[0].Payload.Disposition != DispositionMissed {
		t.Fatalf("disposition = %q, want %q", inserted[0].Payload.Disposition, DispositionMissed)
	}
	if inserted[0].ConversationID != "conv-dm-30-20" {
		t.Fatalf("missed sysmsg conv = %q, want %q", inserted[0].ConversationID, "conv-dm-30-20")
	}
}

// TestServer_Close_Idempotent pins that a second Close is a safe no-op
// and that the call drains the reaper without blocking. The admin
// errgroup wiring in composeAdminAndUI relies on this contract:
// SIGTERM may run Close() concurrently with the test cleanup hook.
func TestServer_Close_Idempotent(t *testing.T) {
	s, _, _, _, _, _ := newFixture(t)
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second Close: must return without blocking and without panicking
	// on a closed reaperStop channel.
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("second Close blocked")
	}
}
