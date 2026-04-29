<script lang="ts">
  /**
   * Chat view — full implementation per docs/design/web/requirements/08-chat.md
   * and docs/design/web/architecture/07-chat-protocol.md.
   *
   * Layout: conversation list (left column) + message pane (right).
   * On narrow viewports the list hides when a conversation is open.
   *
   * WebSocket is started here (after auth is ready) and remains connected
   * across route changes because the Shell mounts ChatView persistently.
   * Note: the Shell renders all routes; ChatView is only mounted when the
   * 'chat' app is active. The WS is started from App.svelte's auth-ready
   * effect alongside the EventSource subscription.
   */

  import { untrack } from 'svelte';
  import { router } from '../lib/router/router.svelte';
  import { chat } from '../lib/chat/store.svelte';
  import { auth } from '../lib/auth/auth.svelte';
  import { chatWs } from '../lib/chat/chat-ws.svelte';
  import { toast } from '../lib/toast/toast.svelte';
  import { Capability } from '../lib/jmap/types';
  import { sounds } from '../lib/notifications/sounds.svelte';
  import ConversationList from '../lib/chat/ConversationList.svelte';
  import MessageList from '../lib/chat/MessageList.svelte';
  import ChatCompose from '../lib/chat/ChatCompose.svelte';
  import VideoCall from '../lib/chat/VideoCall.svelte';
  import IncomingCall from '../lib/chat/IncomingCall.svelte';
  import type { Conversation } from '../lib/chat/types';

  // Derive active conversation from route: /chat/conversation/<id>
  let routeConversationId = $derived(
    router.parts[1] === 'conversation' ? router.parts[2] : undefined,
  );

  let activeConversation = $derived<Conversation | null>(
    routeConversationId
      ? (chat.conversations.get(routeConversationId) ?? null)
      : null,
  );

  // Check whether chat capability is available.
  let chatAvailable = $derived(
    auth.session
      ? Capability.HeroldChat in (auth.session.capabilities ?? {})
      : false,
  );

  // Load conversations once auth is ready and the chat capability exists.
  $effect(() => {
    if (auth.status === 'ready' && chatAvailable) {
      untrack(() => {
        if (chat.conversationsStatus === 'idle') {
          void chat.loadConversations();
        }
      });
    }
  });

  // When the route names a conversation, open it.
  $effect(() => {
    const id = routeConversationId;
    if (id && auth.status === 'ready') {
      untrack(() => {
        if (chat.openConversationId !== id) {
          void chat.openConversation(id);
        }
      });
    }
  });

  function selectConversation(id: string): void {
    router.navigate(`/chat/conversation/${encodeURIComponent(id)}`);
  }

  // ------------------------------------------------------------------
  // Video call state machine
  // ------------------------------------------------------------------

  type CallPhase =
    | 'idle'
    | 'calling'    // we initiated, waiting for accept
    | 'active'     // call connected
    | 'incoming';  // receiving an invite

  let callPhase = $state<CallPhase>('idle');
  let callId = $state<string | null>(null);
  let callConversationId = $state<string | null>(null);
  let callRole = $state<'caller' | 'callee'>('caller');
  let incomingCallerName = $state('');
  let incomingRemoteSdp = $state('');

  function startCall(conversationId: string): void {
    callId = `call-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    callConversationId = conversationId;
    callRole = 'caller';
    callPhase = 'active'; // VideoCall.svelte manages the full flow
  }

  function handleCallHangup(): void {
    callPhase = 'idle';
    callId = null;
    callConversationId = null;
  }

  function acceptIncoming(inboundCallId: string, remoteSdp: string): void {
    sounds.stop('call');
    callId = inboundCallId;
    callRole = 'callee';
    incomingRemoteSdp = remoteSdp;
    callPhase = 'active';
  }

  function declineIncoming(): void {
    sounds.stop('call');
    callPhase = 'idle';
    callId = null;
  }

  // Listen for incoming call invites over the WS.
  const offInvite = chatWs.on('call.invite', (frame) => {
    if (callPhase !== 'idle') {
      // Already in a call — auto-decline.
      chatWs.send({ op: 'call.decline', callId: frame.callId });
      return;
    }
    callConversationId = frame.conversationId;
    incomingRemoteSdp = frame.sdp;

    // Resolve caller display name from the conversation.
    // For a DM, conv.name is the other participant's display name (server-computed).
    const conv = chat.conversations.get(frame.conversationId);
    incomingCallerName = conv?.name ?? 'Unknown caller';

    callId = frame.callId;
    callPhase = 'incoming';
    // Calls bypass the focus / muted-conversation gates per REQ-PUSH-96.
    sounds.play('call');
  });

  $effect(() => {
    return () => offInvite();
  });
</script>

<div class="chat-view">
  {#if !chatAvailable}
    <div class="unavailable">
      <p>Chat is not configured on this server</p>
    </div>
  {:else}
    <div class="conv-panel">
      <ConversationList
        onSelect={selectConversation}
        activeId={routeConversationId}
      />
    </div>

    <div class="message-panel">
      {#if activeConversation}
        <header class="conv-header">
          <div class="conv-title">
            <span class="conv-icon" aria-hidden="true">
              {activeConversation.type === 'dm' ? '@' : '#'}
            </span>
            <h1>{activeConversation.name}</h1>
          </div>

          {#if activeConversation.type === 'dm'}
            <button
              type="button"
              class="call-btn"
              aria-label="Start video call"
              title="Start video call"
              onclick={() => startCall(activeConversation!.id)}
              disabled={callPhase !== 'idle'}
            >
              Call
            </button>
          {/if}
        </header>

        <MessageList
          conversationId={activeConversation.id}
          conversation={activeConversation}
        />

        <ChatCompose
          conversationId={activeConversation.id}
          autofocus={true}
        />
      {:else}
        <div class="no-selection">
          <p>Select a conversation to start chatting</p>
        </div>
      {/if}
    </div>
  {/if}
</div>

<!-- Video call overlay (outside the layout flow) -->
{#if callPhase === 'active' && callId && callConversationId}
  <VideoCall
    conversationId={callConversationId}
    {callId}
    role={callRole}
    remoteSdp={callRole === 'callee' ? incomingRemoteSdp : undefined}
    onHangup={handleCallHangup}
  />
{/if}

<!-- Incoming call modal -->
{#if callPhase === 'incoming' && callId}
  <IncomingCall
    callId={callId}
    callerName={incomingCallerName}
    remoteSdp={incomingRemoteSdp}
    onAccept={acceptIncoming}
    onDecline={declineIncoming}
  />
{/if}

<style>
  .chat-view {
    display: flex;
    height: 100%;
    overflow: hidden;
  }

  .unavailable {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 100%;
    color: var(--text-helper);
    font-style: italic;
  }

  .conv-panel {
    width: 260px;
    flex-shrink: 0;
    border-right: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
    overflow-y: auto;
  }

  .message-panel {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-width: 0;
    overflow: hidden;
  }

  .conv-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: var(--spacing-04) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--background);
    flex-shrink: 0;
  }

  .conv-title {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    min-width: 0;
  }

  .conv-icon {
    font-size: var(--type-heading-compact-02-size);
    color: var(--text-helper);
    flex-shrink: 0;
  }

  h1 {
    font-size: var(--type-heading-compact-02-size);
    font-weight: var(--type-heading-compact-02-weight);
    line-height: var(--type-heading-compact-02-line);
    margin: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .call-btn {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    flex-shrink: 0;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
  }

  .call-btn:hover:not(:disabled) {
    filter: brightness(1.1);
  }

  .call-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .no-selection {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--text-helper);
    font-style: italic;
  }

  /* Narrow viewport: show only the message panel when a conversation is open */
  @media (max-width: 767px) {
    .conv-panel {
      display: none;
    }
  }
</style>
