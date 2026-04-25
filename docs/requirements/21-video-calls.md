# 21 — Video calls

1:1 video calls between tabard users, initiated from a chat DM (`08-chat.md` REQ-CHAT-100). WebRTC peer-to-peer with herold acting as the signaling channel and as the TURN-credential issuer.

Group calls are out of scope for v1 — they require an SFU (Selective Forwarding Unit), which is a substantial new operational surface. See § Out of scope.

## Initiation and ringing

| ID | Requirement |
|----|-------------|
| REQ-CALL-01 | A DM exposes a "Start video call" button (camera icon) in the conversation header. Click initiates the call flow. |
| REQ-CALL-02 | On initiation, the caller's browser requests `getUserMedia` (camera + microphone). On grant, an SDP offer is built and sent over the chat ephemeral WebSocket channel as a `call.invite` message. On deny, a toast: "Camera/microphone access required for video calls" with a "Try again" affordance. |
| REQ-CALL-03 | The callee's tab(s) ring: an in-app modal with caller name, accept / decline buttons, and audible ring tone (default 30s timeout). The browser tab title shows "📞 Incoming call from <caller>". |
| REQ-CALL-04 | Ring timeout: 30 seconds with no accept/decline → caller sees "No answer"; system message in the DM: "Missed call from <caller>"; callee sees a missed-call notification on next active. |
| REQ-CALL-05 | Decline → caller's UI ends, system message: "Call declined". |
| REQ-CALL-06 | Accept → `getUserMedia` on the callee, build SDP answer, send `call.accept` back. Both peers exchange ICE candidates and the peer connection establishes. |

## In-call experience

| ID | Requirement |
|----|-------------|
| REQ-CALL-20 | The in-call UI is a full-window modal (not the chat panel) showing both video tiles (local in a small overlay, remote large) plus controls. The modal traps focus; Escape does NOT dismiss (would be a foot-gun mid-call). Hangup is the only way out. |
| REQ-CALL-21 | Controls: mute microphone, mute camera, hangup, fullscreen toggle, switch camera (if multiple cameras). Each is a keyboard binding visible in the help overlay (`?` while in-call): `m` mic, `v` video, `h` hangup, `f` fullscreen. |
| REQ-CALL-22 | Hangup either side → both peers tear down the peer connection. System message in the DM: "Video call ended — 12:34" with duration. |
| REQ-CALL-23 | If the network drops during a call: the peer connection enters `disconnected` state. After 10 seconds in that state, tabard auto-hangs up and shows "Call dropped due to connection loss" toast. |
| REQ-CALL-24 | Window/tab close while in a call: same as hangup — peer connection torn down, system message logged. |

## WebRTC details

| ID | Requirement |
|----|-------------|
| REQ-CALL-30 | Tabard uses the browser's native `RTCPeerConnection` API. No third-party WebRTC library on the client. |
| REQ-CALL-31 | Codec preferences: VP8 / VP9 for video (let the browser pick), Opus for audio. No H.264 negotiation in v1. |
| REQ-CALL-32 | Tabard requests TURN credentials from herold per call via the chat WebSocket (`../architecture/07-chat-protocol.md` § TURN credentials). Credentials are short-lived (~5 minute TTL) — long-lived creds in the client are a leak vector. |
| REQ-CALL-33 | ICE configuration uses STUN (a public server, e.g. `stun:stun.l.google.com:19302`) plus the call-scoped TURN credentials. STUN handles ~90% of NAT traversal; TURN relays the rest. |
| REQ-CALL-34 | The signaling messages (`call.invite`, `call.accept`, `call.decline`, `call.candidate`, `call.hangup`) are JSON over the chat ephemeral WebSocket. They are not stored in chat history beyond the system messages. |

## Recording (out)

| ID | Requirement |
|----|-------------|
| REQ-CALL-40 | Tabard does NOT record video calls in v1. Neither side. The browser's native MediaRecorder is not invoked. |

## Failure modes

| Symptom | Cause | UX |
|---------|-------|----|
| Camera/mic access denied | OS or browser permission revoked | Toast: "Camera/microphone access required" with "Try again" |
| TURN credential mint failed | herold returned 5xx | Toast: "Couldn't start call — try again". No call-attempt logged. |
| ICE never connects (STUN fails AND TURN fails) | Strict NAT on both sides + TURN unreachable | After 30s of "Connecting…": auto-hangup + toast: "Couldn't establish a connection — check your network" |
| Camera or mic missing | No hardware | "Try again" affords with "audio only" option (camera off, mic on) |
| Other side's browser doesn't support a codec | Edge case; modern browsers all support VP8 + Opus | Negotiate down to lowest common; if none, fail with "Browser incompatible" |

## Out of scope (v1)

- **Group calls (3+ participants).** Requires an SFU; substantial new server. Cut.
- **Screen sharing.** Requires `getDisplayMedia` plus track-replacement during the call. Cut for v1; trivial to add later.
- **Call recording.** Privacy implications, consent flow, storage, transcription — too much for v1.
- **Audio-only calls** as a distinct call type. The user can "start video call" and turn camera off via REQ-CALL-21; that's audio-only without a separate flow.
- **Call from a Space (group call).** Requires SFU; cut.
- **Call quality indicators** beyond the basic connection state. Detailed per-track stats (jitter, packet loss, bitrate) deferred.
- **Background blur / virtual backgrounds.** Cut.
- **Browser-level "incoming call" notifications** when the tab is closed. Requires a service worker (NG2); cut.

## TURN deployment

For production, the operator runs **coturn** (or equivalent) at the same origin or a closely-coordinated origin, configured with a shared secret that herold uses to mint per-call credentials. Defaults: ports 3478/UDP and 5349/TCP, fingerprinted, both IPv4 and IPv6.

Configuration shape and operational guidance live in herold's deploy / operations docs (resolution Q-call-5 — coturn). Tabard's contract is: herold returns a TURN config payload `{ urls, username, credential, ttl }` from the call-credential mint endpoint; tabard plugs it into the `RTCPeerConnection` configuration unchanged.
