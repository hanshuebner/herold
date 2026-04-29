<script lang="ts">
  /**
   * Incoming call modal per REQ-CALL-03..06.
   *
   * Shown when a call.invite frame arrives. Auto-declines after 30 seconds
   * (REQ-CALL-04). Emits accept (with SDP) or decline.
   */

  import { chatWs } from './chat-ws.svelte';
  import { toast } from '../toast/toast.svelte';
  import { sounds } from '../notifications/sounds.svelte';

  interface Props {
    callId: string;
    callerName: string;
    remoteSdp: string;
    conversationId: string;
    onAccept: (callId: string, remoteSdp: string) => void;
    onDecline: () => void;
  }
  let { callId, callerName, remoteSdp, conversationId, onAccept, onDecline }: Props = $props();

  let timeLeft = $state(30);
  const timer = setInterval(() => {
    timeLeft--;
    if (timeLeft <= 0) {
      clearInterval(timer);
      chatWs.send({
        type: 'call.signal',
        payload: { conversationId, kind: 'decline', payload: { callId } },
      });
      onDecline();
    }
  }, 1000);

  function accept(): void {
    clearInterval(timer);
    onAccept(callId, remoteSdp);
  }

  function decline(): void {
    clearInterval(timer);
    chatWs.send({
      type: 'call.signal',
      payload: { conversationId, kind: 'decline', payload: { callId } },
    });
    onDecline();
  }

  // Clear timer and stop the call ringtone on unmount.
  $effect(() => {
    return () => {
      clearInterval(timer);
      sounds.stop('call');
    };
  });
</script>

<div
  class="incoming-call"
  role="dialog"
  aria-modal="true"
  aria-label="Incoming video call"
>
  <div class="card">
    <p class="caller">{callerName}</p>
    <p class="label">Incoming video call</p>
    <p class="timeout" aria-live="polite">
      Auto-decline in {timeLeft}s
    </p>
    <div class="actions">
      <button
        type="button"
        class="accept-btn"
        onclick={accept}
        aria-label="Accept call"
      >
        Accept
      </button>
      <button
        type="button"
        class="decline-btn"
        onclick={decline}
        aria-label="Decline call"
      >
        Decline
      </button>
    </div>
  </div>
</div>

<style>
  .incoming-call {
    position: fixed;
    top: var(--spacing-07);
    right: var(--spacing-07);
    z-index: 900;
  }

  .card {
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-05);
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.4);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    min-width: 240px;
  }

  .caller {
    font-size: var(--type-heading-compact-02-size);
    font-weight: var(--type-heading-compact-02-weight);
    color: var(--text-primary);
    margin: 0;
  }

  .label {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0;
  }

  .timeout {
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
    margin: 0;
  }

  .actions {
    display: flex;
    gap: var(--spacing-03);
  }

  .accept-btn {
    flex: 1;
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--support-success);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-weight: 600;
    min-height: var(--touch-min);
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
  }

  .accept-btn:hover {
    filter: brightness(1.1);
  }

  .decline-btn {
    flex: 1;
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--support-error);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-weight: 600;
    min-height: var(--touch-min);
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
  }

  .decline-btn:hover {
    filter: brightness(1.1);
  }
</style>
