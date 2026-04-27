# 15 — Video calls (1:1)

1:1 video calls between herold users, initiated from a chat DM. WebRTC peer-to-peer; herold acts as the signaling channel and as the TURN-credential issuer. Self-hosted coturn (or equivalent) handles relay.

Phase 2 — runs alongside the chat (`14-chat.md`) work. Group calls are NOT in scope: they require an SFU (mediasoup, Pion, LiveKit, Janus), which is a substantial new operational surface.

The suite's client surface for calls lives in `docs/design/web/requirements/21-video-calls.md`.

## In scope

- 1:1 video calls — caller invites callee from a DM; callee accepts / declines.
- Mute mic, mute camera (audio-only is "video call with camera off"), hangup.
- Per-call short-lived TURN credentials (~5 min TTL).
- Standard WebRTC codecs: VP8/VP9 video, Opus audio.
- Call records as system messages in the conversation.

## Out of scope (calls v1)

- Group calls (≥3 participants) — require SFU.
- Screen sharing — `getDisplayMedia` integration deferred (small, but cut for v1).
- Recording calls — privacy + storage + transcription = too much for v1.
- Voice-only "audio call" as a distinct mode (video call with camera off covers it).
- Browser-level "incoming call" notifications when the tab is closed (would require service worker).
- Call quality detail (jitter, packet loss, bitrate) beyond basic connection state.
- Background blur / virtual backgrounds.
- Calls from a Space (group call) — cut.

## Signaling (REQ-CALL-SIG)

Signaling messages flow over the chat ephemeral WebSocket (`14-chat.md` REQ-CHAT-40) using the JMAP-style `{type, payload}` envelope (REQ-CHAT-41). All call-signaling frames share a single frame type, `call.signal`; the inner `payload.kind` discriminates the call-signaling vocabulary: `offer | answer | decline | ice-candidate | hangup | busy | timeout`. Signaling frames are NOT persisted to chat history; only the call lifecycle (start time, end time, duration, participants, disposition) is recorded as a system message.

- **REQ-CALL-01** Call invite: caller emits `{"type":"call.signal","payload":{"kind":"offer","conversationId":"...","callId":"...","sdp":"..."}}`. The conversation MUST be a DM the caller is in. Server validates and fans out to the callee's WebSocket connection(s).
- **REQ-CALL-02** Call accept: callee emits `{"type":"call.signal","payload":{"kind":"answer","callId":"...","sdp":"..."}}`. Server fans out to the caller. Both peers begin ICE exchange.
- **REQ-CALL-03** Call decline: callee emits `{"type":"call.signal","payload":{"kind":"decline","callId":"..."}}`. Server fans out to caller. System message emitted in the DM: "Call declined".
- **REQ-CALL-04** ICE candidates: either peer emits `{"type":"call.signal","payload":{"kind":"ice-candidate","callId":"...","candidate":"..."}}`. Server fans out to the other peer.
- **REQ-CALL-05** Hangup: either peer emits `{"type":"call.signal","payload":{"kind":"hangup","callId":"..."}}`. Server fans out to the other peer. System message: "Video call ended -- <duration>".
- **REQ-CALL-06** Ring timeout: if the callee doesn't accept or decline within 30 s of the invite, server emits a synthetic `{"type":"call.signal","payload":{"kind":"timeout","callId":"..."}}` to both peers and writes a "Missed call from <caller>" system message.
- **REQ-CALL-07** Stale call cleanup: a call with no signaling activity for 5 minutes (no candidates, no hangup) is auto-terminated server-side and a "Call ended (timeout)" system message is recorded.

## TURN credential mint (REQ-CALL-TURN)

- **REQ-CALL-20** Server MUST mint short-lived TURN credentials on demand. Path: client issues `POST /api/v1/call/credentials` with a JSON body `{"callId":"..."}`; server validates the requesting user is in the call's conversation and that the call is current (invite or in-progress, not declined or hung-up); server returns `{"urls":[...],"username":"...","credential":"...","ttl":300}`.
- **REQ-CALL-21** Credentials use the long-term-credential mechanism (RFC 5389 / RFC 8489) against a coturn shared secret. The username encodes a UTC expiry timestamp; the credential is HMAC-SHA1(username, shared-secret), base64'd. coturn validates inline without per-credential server state.
- **REQ-CALL-22** TTL default 300 seconds; maximum 12 hours (`Server.TURN.CredentialTTLSeconds` in system config caps the value). If a call lasts longer than the issued TTL, the client refreshes credentials approximately 30 seconds before expiry by re-posting `POST /api/v1/call/credentials` with the same `callId`. Server validates and re-mints.
- **REQ-CALL-23** Shared secret with coturn: stored in system config (`/etc/herold/system.toml` § coturn or similar). Rotated by the operator; herold reads at startup and on SIGHUP.
- **REQ-CALL-24** TURN credentials are minted via `POST /api/v1/call/credentials` on the admin HTTP surface, dual-authenticated by the suite session cookie OR by a `Bearer hk_...` API key. The endpoint is rate-limited per-principal. Mint MUST NOT happen during JMAP method calls -- keep the credential-issuance side channel separate from the JMAP request envelope.

## Call records (REQ-CALL-RECORD)

- **REQ-CALL-30** Each call lifecycle results in one system message in the DM: `{type: "system", body.text: "Video call ended — 12:34", senderId: <caller>, ...}` plus extension fields capturing start/end times, participant ids, and the missed/declined/completed disposition.
- **REQ-CALL-31** No media recording. The system message is the only persistent artifact of the call.
- **REQ-CALL-32** Call records contribute to conversation `lastMessageAt` and the unread count for the callee on missed calls.

## Operations (REQ-CALL-OPS)

- **REQ-CALL-40** Operator deploys coturn (or equivalent) at the same origin or a closely-coordinated origin (e.g. `turn.example.com` if `mail.example.com` hosts herold). Default ports: 3478/UDP and 5349/TCP/TLS. IPv4 and IPv6 both reachable.
- **REQ-CALL-41** coturn configuration: `use-auth-secret`, `static-auth-secret <secret>` matching herold's stored secret, `realm <origin>`, `fingerprint`. TLS certificate from the same ACME flow as herold's other listeners (REQ-OPS-50..) or operator-supplied. Detail in `09-operations.md` § coturn.
- **REQ-CALL-42** TURN traffic is opt-in by reachability — not all calls relay (most use STUN+P2P). Herold doesn't need to know whether a given call relayed; coturn's stats are operator-side.
- **REQ-CALL-43** Per-user concurrent call limit: 1 (you can't be in two calls at once). Attempted second call returns a synthetic `{"type":"call.signal","payload":{"kind":"busy","callId":"..."}}` to both peers; the new call is dropped.

## Failure modes

| Symptom | Cause | Server action |
|---------|-------|---------------|
| Caller's WebSocket dies during invite | Network drop on caller side | Server emits a synthetic hangup to the callee; cleans up call state. |
| Callee's WebSocket dies before accept | Callee tab closed | Caller's UI continues to ring until 30s timeout. After timeout, missed-call system message. |
| ICE never connects (STUN+TURN both fail for one side) | Strict NAT + TURN unreachable | Server is unaware (signaling completed); both clients time out and emit hangup. |
| coturn down | Infrastructure failure | TURN credential mint succeeds but the credentials are useless. STUN-only calls work; relayed calls fail. Log + alert. |
| Mint endpoint returns 5xx | herold transient failure | Client retries once with backoff; if persistent, call setup fails with "Couldn't start call". |

## See also

- `requirements/14-chat.md` — chat data model and ephemeral channel.
- `architecture/08-chat.md` — chat / call protocol architecture (WebSocket frame schema, signaling state machine).
- `requirements/09-operations.md` § coturn — operator deployment guidance.
- The suite side: `docs/design/web/requirements/21-video-calls.md`.
