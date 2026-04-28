<script lang="ts">
  /**
   * 1:1 video call UI per docs/design/web/requirements/21-video-calls.md.
   *
   * Full-window modal (REQ-CALL-20). No Escape-to-dismiss (REQ-CALL-20).
   * Controls: mute mic, mute camera, hangup, fullscreen (REQ-CALL-21).
   * Keyboard: m=mic, v=video, h=hangup, f=fullscreen.
   *
   * WebRTC signaling flows over the chat WS (REQ-CALL-30, REQ-CALL-34).
   * TURN credentials fetched per call from herold (REQ-CALL-32).
   *
   * call.invite is sent by the *caller*; call.accept by the *callee*.
   * ICE candidates exchanged as they arrive.
   */

  import { untrack } from 'svelte';
  import { chatWs } from './chat-ws.svelte';
  import { toast } from '../toast/toast.svelte';
  import type { TurnConfig } from './types';

  interface Props {
    conversationId: string;
    callId: string;
    /** 'caller' = we initiated; 'callee' = we received an invite. */
    role: 'caller' | 'callee';
    /** Only set on callee — the SDP offer from the caller. */
    remoteSdp?: string;
    onHangup: () => void;
  }
  let {
    conversationId,
    callId,
    role,
    remoteSdp = undefined,
    onHangup,
  }: Props = $props();

  let localVideo = $state<HTMLVideoElement | null>(null);
  let remoteVideo = $state<HTMLVideoElement | null>(null);

  let micMuted = $state(false);
  let cameraMuted = $state(false);
  let isFullscreen = $state(false);
  let connectionState = $state<RTCPeerConnectionState>('new');
  let callDuration = $state(0);

  let pc: RTCPeerConnection | null = null;
  let localStream: MediaStream | null = null;
  let durationTimer: ReturnType<typeof setInterval> | null = null;
  let disconnectTimer: ReturnType<typeof setTimeout> | null = null;

  // ------------------------------------------------------------------
  // Keyboard bindings while in call
  // ------------------------------------------------------------------

  function handleKeydown(ev: KeyboardEvent): void {
    switch (ev.key) {
      case 'm':
        ev.preventDefault();
        toggleMic();
        break;
      case 'v':
        ev.preventDefault();
        toggleCamera();
        break;
      case 'h':
        ev.preventDefault();
        void hangup();
        break;
      case 'f':
        ev.preventDefault();
        void toggleFullscreen();
        break;
    }
  }

  function toggleMic(): void {
    if (!localStream) return;
    for (const t of localStream.getAudioTracks()) {
      t.enabled = micMuted;
    }
    micMuted = !micMuted;
  }

  function toggleCamera(): void {
    if (!localStream) return;
    for (const t of localStream.getVideoTracks()) {
      t.enabled = cameraMuted;
    }
    cameraMuted = !cameraMuted;
  }

  async function toggleFullscreen(): Promise<void> {
    const el = document.documentElement;
    if (!document.fullscreenElement) {
      await el.requestFullscreen?.();
      isFullscreen = true;
    } else {
      await document.exitFullscreen?.();
      isFullscreen = false;
    }
  }

  async function hangup(): Promise<void> {
    chatWs.send({ op: 'call.hangup', callId });
    teardown();
    onHangup();
  }

  function teardown(): void {
    if (durationTimer) clearInterval(durationTimer);
    if (disconnectTimer) clearTimeout(disconnectTimer);
    localStream?.getTracks().forEach((t) => t.stop());
    localStream = null;
    pc?.close();
    pc = null;
    if (document.fullscreenElement) void document.exitFullscreen();
  }

  // ------------------------------------------------------------------
  // TURN credentials
  // ------------------------------------------------------------------

  function fetchTurnCredentials(): Promise<TurnConfig> {
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('TURN credential timeout')), 10000);
      const off = chatWs.on('call.credentials.response', (frame) => {
        if (frame.callId !== callId) return;
        off();
        clearTimeout(timeout);
        resolve(frame.config);
      });
      chatWs.send({ op: 'call.credentials', callId });
    });
  }

  // ------------------------------------------------------------------
  // Setup peer connection
  // ------------------------------------------------------------------

  async function setup(): Promise<void> {
    let turn: TurnConfig | null = null;
    try {
      turn = await fetchTurnCredentials();
    } catch {
      toast.show({
        message: 'Could not get TURN credentials — trying without relay',
        kind: 'error',
        timeoutMs: 5000,
      });
    }

    const iceServers: RTCIceServer[] = [
      { urls: 'stun:stun.l.google.com:19302' },
    ];
    if (turn) {
      iceServers.push({
        urls: turn.urls,
        username: turn.username,
        credential: turn.credential,
      });
    }

    pc = new RTCPeerConnection({ iceServers });

    pc.addEventListener('icecandidate', (event) => {
      if (event.candidate) {
        chatWs.send({
          op: 'call.candidate',
          callId,
          candidate: JSON.stringify(event.candidate),
        });
      }
    });

    pc.addEventListener('connectionstatechange', () => {
      if (!pc) return;
      connectionState = pc.connectionState;

      if (connectionState === 'connected') {
        // Start duration counter.
        durationTimer = setInterval(() => callDuration++, 1000);
        if (disconnectTimer) {
          clearTimeout(disconnectTimer);
          disconnectTimer = null;
        }
      }

      if (connectionState === 'disconnected') {
        // Auto-hangup after 10s of disconnected state (REQ-CALL-23).
        disconnectTimer = setTimeout(() => {
          toast.show({
            message: 'Call dropped due to connection loss',
            kind: 'error',
          });
          teardown();
          onHangup();
        }, 10000);
      }

      if (
        connectionState === 'failed' ||
        connectionState === 'closed'
      ) {
        if (disconnectTimer) {
          clearTimeout(disconnectTimer);
          disconnectTimer = null;
        }
      }
    });

    pc.addEventListener('track', (event) => {
      if (remoteVideo && event.streams[0]) {
        remoteVideo.srcObject = event.streams[0];
      }
    });

    // Get local media.
    try {
      localStream = await navigator.mediaDevices.getUserMedia({
        video: true,
        audio: true,
      });
    } catch {
      toast.show({
        message: 'Camera/microphone access required for video calls',
        kind: 'error',
        timeoutMs: 0,
      });
      pc.close();
      pc = null;
      onHangup();
      return;
    }

    for (const track of localStream.getTracks()) {
      pc.addTrack(track, localStream);
    }

    if (localVideo) {
      localVideo.srcObject = localStream;
    }

    if (role === 'caller') {
      await startCall();
    } else if (remoteSdp) {
      await answerCall(remoteSdp);
    }
  }

  // ------------------------------------------------------------------
  // Caller side: create offer
  // ------------------------------------------------------------------

  async function startCall(): Promise<void> {
    if (!pc) return;
    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    chatWs.send({
      op: 'call.invite',
      conversationId,
      sdp: offer.sdp ?? '',
      callId,
    });
  }

  // ------------------------------------------------------------------
  // Callee side: answer
  // ------------------------------------------------------------------

  async function answerCall(sdp: string): Promise<void> {
    if (!pc) return;
    await pc.setRemoteDescription({ type: 'offer', sdp });
    const answer = await pc.createAnswer();
    await pc.setLocalDescription(answer);
    chatWs.send({
      op: 'call.accept',
      callId,
      sdp: answer.sdp ?? '',
    });
  }

  // ------------------------------------------------------------------
  // Inbound ICE candidates and answer (caller receives call.accept)
  // ------------------------------------------------------------------

  const offAccept = chatWs.on('call.accept', async (frame) => {
    if (frame.callId !== callId || !pc) return;
    await pc.setRemoteDescription({ type: 'answer', sdp: frame.sdp });
  });

  const offCandidate = chatWs.on('call.candidate', async (frame) => {
    if (frame.callId !== callId || !pc) return;
    try {
      const candidate = JSON.parse(frame.candidate) as RTCIceCandidateInit;
      await pc.addIceCandidate(candidate);
    } catch {
      // Stale or invalid candidate; ignore.
    }
  });

  const offHangup = chatWs.on('call.hangup', (frame) => {
    if (frame.callId !== callId) return;
    teardown();
    onHangup();
  });

  // ------------------------------------------------------------------
  // Lifecycle
  // ------------------------------------------------------------------

  $effect(() => {
    untrack(() => {
      void setup();
    });
    return () => {
      offAccept();
      offCandidate();
      offHangup();
      teardown();
    };
  });

  function formatDuration(seconds: number): string {
    const m = Math.floor(seconds / 60);
    const s = seconds % 60;
    return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  }
</script>

<svelte:window onkeydown={handleKeydown} />

<div
  class="call-overlay"
  role="dialog"
  aria-modal="true"
  aria-label="Video call"
>
  <!-- Remote video (full area) -->
  <video
    bind:this={remoteVideo}
    class="remote-video"
    autoplay
    playsinline
    aria-label="Remote video"
  ></video>

  <!-- Local video (small overlay) -->
  <video
    bind:this={localVideo}
    class="local-video"
    autoplay
    muted
    playsinline
    aria-label="Your camera"
  ></video>

  <!-- Status bar -->
  <div class="status-bar">
    {#if connectionState === 'connecting' || connectionState === 'new'}
      <span>Connecting…</span>
    {:else if connectionState === 'connected'}
      <span>{formatDuration(callDuration)}</span>
    {:else if connectionState === 'disconnected'}
      <span>Connection lost — reconnecting…</span>
    {:else if connectionState === 'failed'}
      <span>Connection failed</span>
    {/if}
  </div>

  <!-- Controls -->
  <div class="controls" aria-label="Call controls">
    <button
      type="button"
      class="ctrl-btn"
      class:active={micMuted}
      aria-pressed={micMuted}
      aria-label={micMuted ? 'Unmute microphone (m)' : 'Mute microphone (m)'}
      onclick={toggleMic}
    >
      {#if micMuted}
        Mic Off
      {:else}
        Mic
      {/if}
    </button>

    <button
      type="button"
      class="ctrl-btn"
      class:active={cameraMuted}
      aria-pressed={cameraMuted}
      aria-label={cameraMuted ? 'Turn camera on (v)' : 'Turn camera off (v)'}
      onclick={toggleCamera}
    >
      {#if cameraMuted}
        Cam Off
      {:else}
        Cam
      {/if}
    </button>

    <button
      type="button"
      class="ctrl-btn hangup"
      aria-label="Hang up (h)"
      onclick={() => void hangup()}
    >
      Hang Up
    </button>

    <button
      type="button"
      class="ctrl-btn"
      aria-pressed={isFullscreen}
      aria-label={isFullscreen ? 'Exit fullscreen (f)' : 'Fullscreen (f)'}
      onclick={() => void toggleFullscreen()}
    >
      {isFullscreen ? 'Exit FS' : 'Fullscreen'}
    </button>
  </div>
</div>

<style>
  .call-overlay {
    position: fixed;
    inset: 0;
    z-index: 1000;
    background: #000;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  .remote-video {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }

  .local-video {
    position: absolute;
    bottom: var(--spacing-07);
    right: var(--spacing-07);
    width: 180px;
    height: 120px;
    border-radius: var(--radius-lg);
    object-fit: cover;
    border: 2px solid rgba(255, 255, 255, 0.3);
    background: #222;
  }

  .status-bar {
    position: absolute;
    top: var(--spacing-05);
    left: 50%;
    transform: translateX(-50%);
    color: #fff;
    font-size: var(--type-body-compact-01-size);
    background: rgba(0, 0, 0, 0.5);
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-pill);
  }

  .controls {
    position: absolute;
    bottom: var(--spacing-07);
    left: 50%;
    transform: translateX(-50%);
    display: flex;
    gap: var(--spacing-04);
    align-items: center;
  }

  .ctrl-btn {
    padding: var(--spacing-03) var(--spacing-05);
    border-radius: var(--radius-pill);
    background: rgba(255, 255, 255, 0.15);
    color: #fff;
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    backdrop-filter: blur(8px);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    min-height: var(--touch-min);
  }

  .ctrl-btn:hover {
    background: rgba(255, 255, 255, 0.3);
  }

  .ctrl-btn.active {
    background: rgba(255, 80, 80, 0.5);
  }

  .ctrl-btn.hangup {
    background: #c62828;
  }

  .ctrl-btn.hangup:hover {
    background: #e53935;
  }
</style>
