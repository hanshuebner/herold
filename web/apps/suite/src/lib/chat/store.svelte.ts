/**
 * Chat store — JMAP-backed conversation, message, and membership cache.
 *
 * Two transports per docs/design/web/architecture/07-chat-protocol.md:
 *   1. JMAP over HTTPS for durable state (Conversation, Message, Membership).
 *   2. The ephemeral WebSocket (chatWs) for presence + typing indicators.
 *
 * State-change push events arrive on the shared EventSource (sync.on())
 * and trigger Foo/changes incremental syncs, keeping the in-memory
 * cache fresh without polling.
 *
 * The store exposes:
 *   - conversations: sorted list (pinned first, then by lastMessageAt desc)
 *   - openConversationId / activeConversation
 *   - messages(conversationId): ordered message list for the open pane
 *   - presence: principalId -> PresenceState
 *   - typing: conversationId -> principalId[]
 *   - sendMessage(conversationId, html, text): optimistic send with rollback
 *   - loadConversations(): initial load
 *   - openConversation(id): load messages, mark read
 *   - markRead(conversationId, messageId): advance Membership.readThrough
 */

import { jmap } from '../jmap/client';
import { auth } from '../auth/auth.svelte';
import { sync } from '../jmap/sync.svelte';
import { toast } from '../toast/toast.svelte';
import { Capability } from '../jmap/types';
import { chatWs } from './chat-ws.svelte';
import { sounds } from '../notifications/sounds.svelte';
import { shouldPlayChatCue } from '../notifications/cue-gates';
import { chatOverlay } from './overlay-store.svelte';
import { router } from '../router/router.svelte';
import type {
  Conversation,
  Message,
  Membership,
  PresenceState,
  Principal,
} from './types';

const CHAT_CAPABILITY = Capability.HeroldChat;
const USING = [Capability.Core, CHAT_CAPABILITY];

/** How many messages to fetch per page when opening a conversation. */
const PAGE_SIZE = 50;

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

class ChatStore {
  /** All known conversations keyed by id. */
  conversations = $state(new Map<string, Conversation>());
  /** Ordered conversation ids (pinned first, then by activity). */
  conversationIds = $state<string[]>([]);
  conversationsStatus = $state<LoadStatus>('idle');

  /** Which conversation the message pane shows. */
  openConversationId = $state<string | null>(null);
  /** Messages for the open conversation, oldest first. */
  messages = $state<Message[]>([]);
  messagesStatus = $state<LoadStatus>('idle');
  /** true when there are older messages available to paginate backwards. */
  hasMoreMessages = $state(false);

  /** Memberships keyed by conversation id. */
  memberships = $state(new Map<string, Membership[]>());

  /**
   * Per-conversation message caches for overlay windows.
   * Keyed by conversationId; each entry mirrors the shape used by the
   * main message pane so the same MessageList component can read from it.
   */
  overlayMessages = $state(
    new Map<string, { messages: Message[]; status: LoadStatus; hasMore: boolean }>(),
  );

  /** principalId -> presence state, populated from WS presence-update frames. */
  presence = $state(new Map<string, PresenceState>());

  /**
   * conversationId -> Set of principalIds currently typing.
   * Entries auto-expire after 7 seconds per REQ-CHAT-52.
   */
  typing = $state(new Map<string, Set<string>>());

  /** State strings for incremental sync. */
  #conversationState: string | null = null;
  #messageState: string | null = null;
  #membershipState: string | null = null;

  #typingTimers = new Map<string, ReturnType<typeof setTimeout>>();
  /** Timer keys: `${conversationId}:${principalId}` */
  #presenceTimers = new Map<string, ReturnType<typeof setTimeout>>();

  constructor() {
    // Register EventSource state-change handlers so the store syncs
    // when herold pushes a state advance for chat types.
    sync.on('Conversation', (newState, accountId) => {
      this.#syncConversationChanges(newState, accountId).catch((err) => {
        console.error('chat Conversation/changes failed', err);
      });
    });
    sync.on('Message', (newState, accountId) => {
      this.#syncMessageChanges(newState, accountId).catch((err) => {
        console.error('chat Message/changes failed', err);
      });
    });
    sync.on('Membership', (newState, accountId) => {
      this.#syncMembershipChanges(newState, accountId).catch((err) => {
        console.error('chat Membership/changes failed', err);
      });
    });

    // Presence updates from the ephemeral channel.
    chatWs.on('presence-update', (frame) => {
      this.presence.set(frame.principalId, frame.state);
    });

    // Typing indicators from the ephemeral channel.
    // The server fans out typing frames with principalId added; fall back
    // to a placeholder when the field is absent (should not happen in practice).
    chatWs.on('typing', (frame) => {
      const pid = (frame as { principalId?: string }).principalId ?? 'unknown';
      this.#setTyping(frame.conversationId, pid, true);
    });
    chatWs.on('typing-stopped', (frame) => {
      const pid = (frame as { principalId?: string }).principalId ?? 'unknown';
      this.#setTyping(frame.conversationId, pid, false);
    });
  }

  // ------------------------------------------------------------------
  // Conversation list
  // ------------------------------------------------------------------

  async loadConversations(): Promise<void> {
    if (this.conversationsStatus === 'loading') return;
    const accountId = this.#accountId();
    if (!accountId) return;

    this.conversationsStatus = 'loading';
    try {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Conversation/query',
          { accountId, sort: [{ property: 'lastMessageAt', isAscending: false }] },
          USING,
        );
        b.call(
          'Conversation/get',
          { accountId, '#ids': q.ref('/ids') },
          USING,
        );
      });

      const getResp = responses.find(
        ([name]) => name === 'Conversation/get',
      );
      if (!getResp || getResp[0] === 'error') {
        throw new Error('Conversation/get failed');
      }
      const result = getResp[1] as {
        state: string;
        list: Conversation[];
        notFound?: string[];
      };
      this.#conversationState = result.state;
      for (const c of result.list) {
        this.conversations.set(c.id, c);
      }
      this.#rebuildConversationOrder();
      this.conversationsStatus = 'ready';
    } catch (err) {
      this.conversationsStatus = 'error';
      console.error('loadConversations failed', err);
    }
  }

  // ------------------------------------------------------------------
  // Open a conversation + load messages
  // ------------------------------------------------------------------

  async openConversation(id: string): Promise<void> {
    this.openConversationId = id;
    await this.loadMessages(id);
  }

  async loadMessages(conversationId: string): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) return;
    if (this.messagesStatus === 'loading') return;

    this.messagesStatus = 'loading';
    this.messages = [];
    this.hasMoreMessages = false;

    try {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Message/query',
          {
            accountId,
            filter: { conversationId },
            sort: [{ property: 'createdAt', isAscending: false }],
            limit: PAGE_SIZE,
          },
          USING,
        );
        b.call(
          'Message/get',
          { accountId, '#ids': q.ref('/ids') },
          USING,
        );
      });

      const queryResp = responses.find(([name]) => name === 'Message/query');
      const getResp = responses.find(([name]) => name === 'Message/get');
      if (!getResp || getResp[0] === 'error') {
        throw new Error('Message/get failed');
      }

      if (queryResp && queryResp[0] === 'Message/query') {
        const qResult = queryResp[1] as { total: number; ids: string[] };
        this.hasMoreMessages = qResult.total > PAGE_SIZE;
      }

      const result = getResp[1] as { state: string; list: Message[] };
      this.#messageState = result.state;
      // Query returns newest-first; reverse for display (oldest first).
      this.messages = result.list.slice().reverse();
      this.messagesStatus = 'ready';
    } catch (err) {
      this.messagesStatus = 'error';
      console.error('loadMessages failed', err);
    }
  }

  /**
   * Load the next page of older messages and prepend them.
   */
  async loadMoreMessages(conversationId: string): Promise<void> {
    if (!this.hasMoreMessages) return;
    const accountId = this.#accountId();
    if (!accountId) return;

    const oldest = this.messages[0];
    const position = oldest ? this.messages.length : 0;

    try {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Message/query',
          {
            accountId,
            filter: { conversationId },
            sort: [{ property: 'createdAt', isAscending: false }],
            limit: PAGE_SIZE,
            position,
          },
          USING,
        );
        b.call(
          'Message/get',
          { accountId, '#ids': q.ref('/ids') },
          USING,
        );
      });

      const queryResp = responses.find(([name]) => name === 'Message/query');
      const getResp = responses.find(([name]) => name === 'Message/get');
      if (!getResp || getResp[0] === 'error') return;

      if (queryResp && queryResp[0] === 'Message/query') {
        const qResult = queryResp[1] as { total: number };
        this.hasMoreMessages = qResult.total > position + PAGE_SIZE;
      }

      const result = getResp[1] as { list: Message[] };
      // Older messages come back newest-first; reverse then prepend.
      this.messages = [...result.list.slice().reverse(), ...this.messages];
    } catch (err) {
      console.error('loadMoreMessages failed', err);
    }
  }

  // ------------------------------------------------------------------
  // Send a message (optimistic)
  // ------------------------------------------------------------------

  async sendMessage(
    conversationId: string,
    html: string,
    text: string,
  ): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) return;

    const tempId = `tmp-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    const now = new Date().toISOString();
    const optimisticMsg: Message = {
      id: tempId,
      conversationId,
      senderPrincipalId: auth.principalId ?? '',
      type: 'text',
      body: { html, text },
      inlineImages: [],
      reactions: {},
      createdAt: now,
      deleted: false,
    };

    // Optimistic insert into both the main message pane and the overlay
    // cache for this conversation, so the message renders immediately
    // wherever the user is composing — main pane or floating overlay.
    this.messages = [...this.messages, optimisticMsg];
    this.#appendOverlayMessage(conversationId, optimisticMsg);

    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Message/set',
          {
            accountId,
            create: {
              [tempId]: {
                conversationId,
                type: 'text',
                body: { html, text },
              },
            },
          },
          USING,
        );
      });

      const setResp = responses.find(([name]) => name === 'Message/set');
      if (!setResp || setResp[0] === 'error') {
        this.#rollbackOptimistic(conversationId, tempId);
        toast.show({ message: 'Failed to send message', kind: 'error' });
        return;
      }

      const result = setResp[1] as {
        created?: Record<string, { id: string }>;
        notCreated?: Record<string, { type: string; description?: string }>;
      };

      if (result.notCreated?.[tempId]) {
        this.#rollbackOptimistic(conversationId, tempId);
        const errType = result.notCreated[tempId]?.type ?? 'unknown';
        toast.show({ message: `Failed to send: ${errType}`, kind: 'error' });
        return;
      }

      const realId = result.created?.[tempId]?.id;
      if (realId) {
        // Replace optimistic record with real id in both caches.
        this.messages = this.messages.map((m) =>
          m.id === tempId ? { ...m, id: realId } : m,
        );
        this.#replaceOverlayMessageId(conversationId, tempId, realId);
      }
    } catch (err) {
      this.#rollbackOptimistic(conversationId, tempId);
      const msg = err instanceof Error ? err.message : 'Send failed';
      toast.show({ message: msg, kind: 'error' });
    }
  }

  #rollbackOptimistic(conversationId: string, tempId: string): void {
    this.messages = this.messages.filter((m) => m.id !== tempId);
    const entry = this.overlayMessages.get(conversationId);
    if (!entry) return;
    this.overlayMessages.set(conversationId, {
      ...entry,
      messages: entry.messages.filter((m) => m.id !== tempId),
    });
    this.overlayMessages = new Map(this.overlayMessages);
  }

  #appendOverlayMessage(conversationId: string, msg: Message): void {
    const entry = this.overlayMessages.get(conversationId);
    if (!entry) return;
    this.overlayMessages.set(conversationId, {
      ...entry,
      messages: [...entry.messages, msg],
    });
    this.overlayMessages = new Map(this.overlayMessages);
  }

  #replaceOverlayMessageId(conversationId: string, oldId: string, newId: string): void {
    const entry = this.overlayMessages.get(conversationId);
    if (!entry) return;
    this.overlayMessages.set(conversationId, {
      ...entry,
      messages: entry.messages.map((m) => (m.id === oldId ? { ...m, id: newId } : m)),
    });
    this.overlayMessages = new Map(this.overlayMessages);
  }

  // ------------------------------------------------------------------
  // Read receipts
  // ------------------------------------------------------------------

  async markRead(conversationId: string, messageId: string): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId || !auth.principalId) return;

    // The requester's Membership row is delivered with every
    // Conversation/get under `myMembership`. The previous implementation
    // tried to look it up in `this.memberships` (only ever populated by
    // the EventSource Membership/changes handler), so the lookup failed
    // silently on initial load and the unread badge never reset.
    const conv = this.conversations.get(conversationId);
    const myMembership = conv?.myMembership;
    if (!myMembership) return;

    // Skip the round-trip if we have already advanced past this message.
    if (myMembership.lastReadMessageId === messageId) return;

    try {
      // Wire field is `lastReadMessageId` per memUpdateInput in
      // internal/protojmap/chat/membership.go. Sending `readThrough`
      // (the suite's local field name) is silently ignored by the
      // server.
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Membership/set',
          {
            accountId,
            update: {
              [myMembership.id]: { lastReadMessageId: messageId },
            },
          },
          USING,
        );
      });
      const setResp = responses.find(([name]) => name === 'Membership/set');
      if (!setResp || setResp[0] === 'error') {
        console.error('markRead Membership/set failed', setResp?.[1]);
        return;
      }
      const result = setResp[1] as {
        notUpdated?: Record<string, { type: string; description?: string }>;
      };
      if (result.notUpdated?.[myMembership.id]) {
        console.error(
          'markRead notUpdated',
          result.notUpdated[myMembership.id],
        );
        return;
      }

      // Reflect the new read pointer locally so the unread badge
      // updates immediately, before the next state-advance round-trip.
      const updatedMembership: Membership = {
        ...myMembership,
        lastReadMessageId: messageId,
      };
      const updatedConv: Conversation = {
        ...conv!,
        myMembership: updatedMembership,
        unreadCount: 0,
      };
      this.conversations.set(conversationId, updatedConv);
      this.#rebuildConversationOrder();
    } catch (err) {
      console.error('markRead failed', err);
    }
  }

  // ------------------------------------------------------------------
  // Reactions
  // ------------------------------------------------------------------

  async toggleReaction(
    messageId: string,
    emoji: string,
    principalId: string,
  ): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) return;

    const msg = this.messages.find((m) => m.id === messageId);
    if (!msg) return;

    const reactors = msg.reactions[emoji] ?? [];
    const alreadyReacted = reactors.includes(principalId);

    // Optimistic update.
    this.messages = this.messages.map((m) => {
      if (m.id !== messageId) return m;
      const updated = { ...m.reactions };
      if (alreadyReacted) {
        updated[emoji] = (updated[emoji] ?? []).filter(
          (id) => id !== principalId,
        );
        if (updated[emoji]!.length === 0) delete updated[emoji];
      } else {
        updated[emoji] = [...(updated[emoji] ?? []), principalId];
      }
      return { ...m, reactions: updated };
    });

    try {
      const patch: Record<string, boolean | null> = {};
      patch[`reactions/${emoji}/${principalId}`] = alreadyReacted ? null : true;

      await jmap.batch((b) => {
        b.call(
          'Message/set',
          {
            accountId,
            update: { [messageId]: patch },
          },
          USING,
        );
      });
    } catch (err) {
      // Rollback optimistic change.
      this.messages = this.messages.map((m) => {
        if (m.id !== messageId) return m;
        return msg; // restore original
      });
      const errMsg = err instanceof Error ? err.message : 'Reaction failed';
      toast.show({ message: errMsg, kind: 'error' });
    }
  }

  // ------------------------------------------------------------------
  // Typing indicator (outbound)
  // ------------------------------------------------------------------

  private _lastTypingSent = 0;
  private _typingStopTimer: ReturnType<typeof setTimeout> | null = null;

  /** Call on every keystroke in the chat compose. Debounced per REQ-CHAT-51. */
  notifyTyping(conversationId: string): void {
    const now = Date.now();
    if (now - this._lastTypingSent >= 3000) {
      this._lastTypingSent = now;
      chatWs.send({ op: 'typing', conversationId });
    }

    // Schedule a "stopped" frame 5s after the last keystroke.
    if (this._typingStopTimer !== null) {
      clearTimeout(this._typingStopTimer);
    }
    this._typingStopTimer = setTimeout(() => {
      this._typingStopTimer = null;
      chatWs.send({ op: 'typing-stopped', conversationId });
    }, 5000);
  }

  stopTyping(conversationId: string): void {
    if (this._typingStopTimer !== null) {
      clearTimeout(this._typingStopTimer);
      this._typingStopTimer = null;
    }
    chatWs.send({ op: 'typing-stopped', conversationId });
  }

  // ------------------------------------------------------------------
  // Typing indicator (inbound)
  // ------------------------------------------------------------------

  #setTyping(conversationId: string, principalId: string, isTyping: boolean): void {
    // Re-key the per-conversation set to trigger Svelte reactivity.
    const current = new Set(this.typing.get(conversationId) ?? []);
    const timerKey = `${conversationId}:${principalId}`;

    if (isTyping) {
      current.add(principalId);
      this.typing.set(conversationId, current);

      // Auto-clear after 7s (REQ-CHAT-52).
      const existing = this.#typingTimers.get(timerKey);
      if (existing) clearTimeout(existing);
      this.#typingTimers.set(
        timerKey,
        setTimeout(() => {
          this.#typingTimers.delete(timerKey);
          this.#setTyping(conversationId, principalId, false);
        }, 7000),
      );
    } else {
      current.delete(principalId);
      if (current.size === 0) {
        this.typing.delete(conversationId);
      } else {
        this.typing.set(conversationId, current);
      }
      const existing = this.#typingTimers.get(timerKey);
      if (existing) {
        clearTimeout(existing);
        this.#typingTimers.delete(timerKey);
      }
    }
  }

  // ------------------------------------------------------------------
  // Incremental sync handlers (EventSource push)
  // ------------------------------------------------------------------

  async #syncConversationChanges(
    _newState: string,
    accountId: string,
  ): Promise<void> {
    if (!this.#conversationState) {
      // No prior state; do a full reload.
      await this.loadConversations();
      return;
    }
    try {
      const { responses } = await jmap.batch((b) => {
        const changes = b.call(
          'Conversation/changes',
          {
            accountId,
            sinceState: this.#conversationState,
          },
          USING,
        );
        b.call(
          'Conversation/get',
          {
            accountId,
            '#ids': changes.ref('/changed'),
          },
          USING,
        );
      });

      const changesResp = responses.find(
        ([name]) => name === 'Conversation/changes',
      );
      const getResp = responses.find(
        ([name]) => name === 'Conversation/get',
      );

      if (
        changesResp &&
        changesResp[0] === 'Conversation/changes'
      ) {
        const cr = changesResp[1] as {
          newState: string;
          destroyed: string[];
        };
        this.#conversationState = cr.newState;
        for (const destroyedId of cr.destroyed) {
          this.conversations.delete(destroyedId);
        }
      }

      if (getResp && getResp[0] === 'Conversation/get') {
        const gr = getResp[1] as { list: Conversation[] };
        for (const c of gr.list) {
          this.conversations.set(c.id, c);
        }
      }

      this.#rebuildConversationOrder();
    } catch (err) {
      console.error('Conversation/changes sync failed', err);
      // Fall back to full reload.
      this.#conversationState = null;
      await this.loadConversations();
    }
  }

  async #syncMessageChanges(
    _newState: string,
    accountId: string,
  ): Promise<void> {
    // The handler must run for any session that has chat open, even if
    // the user is only looking at floating overlay windows (and so
    // openConversationId is null). Without #messageState we have no
    // valid sinceState anchor — that case is bridged by loadMessages /
    // loadOverlayMessages which seed the field on first fetch.
    const openId = this.openConversationId;
    if (!this.#messageState) return;

    try {
      const { responses } = await jmap.batch((b) => {
        const changes = b.call(
          'Message/changes',
          {
            accountId,
            sinceState: this.#messageState,
          },
          USING,
        );
        b.call(
          'Message/get',
          {
            accountId,
            '#ids': changes.ref('/changed'),
          },
          USING,
        );
      });

      const changesResp = responses.find(([name]) => name === 'Message/changes');
      const getResp = responses.find(([name]) => name === 'Message/get');

      if (changesResp && changesResp[0] === 'Message/changes') {
        const cr = changesResp[1] as {
          newState: string;
          destroyed: string[];
        };
        this.#messageState = cr.newState;
        const destroyedSet = new Set(cr.destroyed);
        if (destroyedSet.size > 0) {
          this.messages = this.messages.filter(
            (m) => !destroyedSet.has(m.id),
          );
        }
      }

      if (getResp && getResp[0] === 'Message/get') {
        const gr = getResp[1] as { list: Message[] };
        for (const incoming of gr.list) {
          // Update main message pane (if this is the open conversation).
          if (incoming.conversationId === openId) {
            const idx = this.messages.findIndex((m) => m.id === incoming.id);
            if (idx >= 0) {
              // Update existing (edited, reaction toggle, etc.).
              this.messages = this.messages.map((m) =>
                m.id === incoming.id ? incoming : m,
              );
            } else {
              // New message — append and maybe play an audio cue.
              this.messages = [...this.messages, incoming];
              this.#maybeChatCue(incoming);
            }
          } else {
            // Message for a conversation that is not the open pane.
            // It may still be new (not in overlay cache) — check and cue.
            const overlayEntry = this.overlayMessages.get(incoming.conversationId);
            const alreadyInOverlay =
              overlayEntry?.messages.some((m) => m.id === incoming.id) ?? false;
            if (!alreadyInOverlay) {
              this.#maybeChatCue(incoming);
            }
          }

          // Also update the overlay cache for this conversation if loaded.
          const overlayEntry = this.overlayMessages.get(incoming.conversationId);
          if (overlayEntry) {
            const idx = overlayEntry.messages.findIndex((m) => m.id === incoming.id);
            let updated: Message[];
            if (idx >= 0) {
              updated = overlayEntry.messages.map((m) =>
                m.id === incoming.id ? incoming : m,
              );
            } else {
              updated = [...overlayEntry.messages, incoming];
            }
            this.overlayMessages.set(incoming.conversationId, {
              ...overlayEntry,
              messages: updated,
            });
            this.overlayMessages = new Map(this.overlayMessages);
          }
        }
      }
    } catch (err) {
      console.error('Message/changes sync failed', err);
    }
  }

  async #syncMembershipChanges(
    _newState: string,
    accountId: string,
  ): Promise<void> {
    if (!this.#membershipState) return;
    try {
      const { responses } = await jmap.batch((b) => {
        const changes = b.call(
          'Membership/changes',
          {
            accountId,
            sinceState: this.#membershipState,
          },
          USING,
        );
        b.call(
          'Membership/get',
          {
            accountId,
            '#ids': changes.ref('/changed'),
          },
          USING,
        );
      });

      const changesResp = responses.find(
        ([name]) => name === 'Membership/changes',
      );
      const getResp = responses.find(
        ([name]) => name === 'Membership/get',
      );

      if (changesResp && changesResp[0] === 'Membership/changes') {
        const cr = changesResp[1] as {
          newState: string;
          destroyed: string[];
        };
        this.#membershipState = cr.newState;
        for (const destroyedId of cr.destroyed) {
          for (const [convId, mems] of this.memberships.entries()) {
            const filtered = mems.filter((m) => m.id !== destroyedId);
            if (filtered.length !== mems.length) {
              this.memberships.set(convId, filtered);
            }
          }
        }
      }

      if (getResp && getResp[0] === 'Membership/get') {
        const gr = getResp[1] as { list: Membership[] };
        for (const mem of gr.list) {
          const existing = this.memberships.get(mem.conversationId) ?? [];
          const idx = existing.findIndex((m) => m.id === mem.id);
          const updated =
            idx >= 0
              ? existing.map((m) => (m.id === mem.id ? mem : m))
              : [...existing, mem];
          this.memberships.set(mem.conversationId, updated);
        }
      }
    } catch (err) {
      console.error('Membership/changes sync failed', err);
    }
  }

  // ------------------------------------------------------------------
  // Overlay window message management
  // ------------------------------------------------------------------

  /**
   * Load messages for an overlay window.  Stores results in
   * overlayMessages keyed by conversationId so multiple windows can
   * each have their own cache independently of the main message pane.
   */
  async loadOverlayMessages(conversationId: string): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) return;

    const existing = this.overlayMessages.get(conversationId);
    if (existing?.status === 'loading') return;

    // Seed the entry so the overlay shows a loading state immediately.
    this.overlayMessages.set(conversationId, {
      messages: existing?.messages ?? [],
      status: 'loading',
      hasMore: existing?.hasMore ?? false,
    });
    // Trigger reactivity.
    this.overlayMessages = new Map(this.overlayMessages);

    try {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Message/query',
          {
            accountId,
            filter: { conversationId },
            sort: [{ property: 'createdAt', isAscending: false }],
            limit: PAGE_SIZE,
          },
          USING,
        );
        b.call(
          'Message/get',
          { accountId, '#ids': q.ref('/ids') },
          USING,
        );
      });

      const queryResp = responses.find(([name]) => name === 'Message/query');
      const getResp = responses.find(([name]) => name === 'Message/get');
      if (!getResp || getResp[0] === 'error') {
        throw new Error('Message/get failed');
      }

      let hasMore = false;
      if (queryResp && queryResp[0] === 'Message/query') {
        const qResult = queryResp[1] as { total: number };
        hasMore = qResult.total > PAGE_SIZE;
      }

      const result = getResp[1] as { state: string; list: Message[] };
      // Query returns newest-first; reverse for display (oldest first).
      const msgs = result.list.slice().reverse();

      // Seed #messageState if it is not already known so the
      // EventSource-driven Message/changes path has a valid sinceState
      // anchor. Without this an overlay-only session never picks up
      // pushed messages until a full reload.
      if (!this.#messageState && result.state) {
        this.#messageState = result.state;
      }

      this.overlayMessages.set(conversationId, {
        messages: msgs,
        status: 'ready',
        hasMore,
      });
      this.overlayMessages = new Map(this.overlayMessages);
    } catch (err) {
      const cur = this.overlayMessages.get(conversationId);
      this.overlayMessages.set(conversationId, {
        messages: cur?.messages ?? [],
        status: 'error',
        hasMore: false,
      });
      this.overlayMessages = new Map(this.overlayMessages);
      console.error('loadOverlayMessages failed', err);
    }
  }

  /**
   * Load the next page of older messages for an overlay window.
   */
  async loadMoreOverlayMessages(conversationId: string): Promise<void> {
    const entry = this.overlayMessages.get(conversationId);
    if (!entry || !entry.hasMore) return;
    const accountId = this.#accountId();
    if (!accountId) return;

    const position = entry.messages.length;

    try {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Message/query',
          {
            accountId,
            filter: { conversationId },
            sort: [{ property: 'createdAt', isAscending: false }],
            limit: PAGE_SIZE,
            position,
          },
          USING,
        );
        b.call(
          'Message/get',
          { accountId, '#ids': q.ref('/ids') },
          USING,
        );
      });

      const queryResp = responses.find(([name]) => name === 'Message/query');
      const getResp = responses.find(([name]) => name === 'Message/get');
      if (!getResp || getResp[0] === 'error') return;

      let hasMore = false;
      if (queryResp && queryResp[0] === 'Message/query') {
        const qResult = queryResp[1] as { total: number };
        hasMore = qResult.total > position + PAGE_SIZE;
      }

      const result = getResp[1] as { list: Message[] };
      const older = result.list.slice().reverse();

      const cur = this.overlayMessages.get(conversationId)!;
      this.overlayMessages.set(conversationId, {
        messages: [...older, ...cur.messages],
        status: 'ready',
        hasMore,
      });
      this.overlayMessages = new Map(this.overlayMessages);
    } catch (err) {
      console.error('loadMoreOverlayMessages failed', err);
    }
  }

  /**
   * Drop the cached overlay messages for a conversation (called when its
   * overlay window is closed to keep memory tidy).
   */
  closeOverlayMessages(conversationId: string): void {
    this.overlayMessages.delete(conversationId);
    this.overlayMessages = new Map(this.overlayMessages);
  }

  // ------------------------------------------------------------------
  // Principal directory (REQ-CHAT-01b..c)
  // ------------------------------------------------------------------

  /**
   * Search principals by display name / email local-part prefix.
   * Issues Principal/query with textPrefix then Principal/get for the ids
   * in a single batched call.
   */
  async searchPrincipals(prefix: string, limit = 25): Promise<Principal[]> {
    const accountId = this.#accountId();
    if (!accountId || prefix.length === 0) return [];

    try {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Principal/query',
          { accountId, filter: { textPrefix: prefix }, limit },
          USING,
        );
        b.call(
          'Principal/get',
          { accountId, '#ids': q.ref('/ids') },
          USING,
        );
      });

      const getResp = responses.find(([name]) => name === 'Principal/get');
      if (!getResp || getResp[0] === 'error') return [];
      const result = getResp[1] as { list: Principal[] };
      return result.list;
    } catch (err) {
      console.error('searchPrincipals failed', err);
      return [];
    }
  }

  /**
   * Look up a principal by exact email address.
   * Returns the principal if found, null if not a Herold user on this server.
   */
  async lookupPrincipalByEmail(email: string): Promise<Principal | null> {
    const accountId = this.#accountId();
    if (!accountId) return null;

    try {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Principal/query',
          { accountId, filter: { emailExact: email } },
          USING,
        );
        b.call(
          'Principal/get',
          { accountId, '#ids': q.ref('/ids') },
          USING,
        );
      });

      const queryResp = responses.find(([name]) => name === 'Principal/query');
      if (!queryResp || queryResp[0] !== 'Principal/query') return null;
      const qResult = queryResp[1] as { ids: string[] };
      if (qResult.ids.length === 0) return null;

      const getResp = responses.find(([name]) => name === 'Principal/get');
      if (!getResp || getResp[0] === 'error') return null;
      const result = getResp[1] as { list: Principal[] };
      return result.list[0] ?? null;
    } catch (err) {
      console.error('lookupPrincipalByEmail failed', err);
      return null;
    }
  }

  /**
   * Pure helper: find an existing DM with the given other principal id.
   * Scans the in-memory conversations map; no network call.
   */
  findExistingDM(otherPrincipalId: string): Conversation | null {
    const myId = auth.principalId;
    if (!myId) return null;
    for (const conv of this.conversations.values()) {
      if (conv.kind !== 'dm') continue;
      const memberIds = conv.members.map((m) => m.principalId);
      if (
        memberIds.length === 2 &&
        memberIds.includes(myId) &&
        memberIds.includes(otherPrincipalId)
      ) {
        return conv;
      }
    }
    return null;
  }

  /**
   * Create a new DM or Space via Conversation/set and seed the resulting
   * Conversation into the local cache so any UI that opens against the
   * returned id (e.g. the chat overlay window or the sidebar list)
   * renders with the full record without waiting for the server-pushed
   * state advance.
   *
   * Per RFC 8620 §5.3 the server returns the fully populated record in
   * `created` (not just the id), so no follow-up Conversation/get is
   * needed.
   */
  async createConversation(args: {
    kind: 'dm' | 'space';
    members: string[];
    name?: string;
    topic?: string;
  }): Promise<{ id: string }> {
    const accountId = this.#accountId();
    if (!accountId) throw new Error('Not authenticated');

    const tempId = `new-conv-${Date.now()}`;
    const createPayload: Record<string, unknown> = {
      kind: args.kind,
      members: args.members,
    };
    if (args.name) createPayload['name'] = args.name;
    if (args.topic) createPayload['topic'] = args.topic;

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Conversation/set',
        {
          accountId,
          create: { [tempId]: createPayload },
        },
        USING,
      );
    });

    const setResp = responses.find(([name]) => name === 'Conversation/set');
    if (!setResp || setResp[0] === 'error') {
      throw new Error('Conversation/set failed');
    }

    const result = setResp[1] as {
      created?: Record<string, Conversation & { id: string }>;
      notCreated?: Record<string, { type: string; description?: string }>;
    };

    if (result.notCreated?.[tempId]) {
      const err = result.notCreated[tempId]!;
      throw new Error(err.description ?? err.type);
    }

    const created = result.created?.[tempId];
    if (!created || !created.id) {
      throw new Error('Conversation/set returned no created record');
    }

    // Seed the cache with the full record from the set response so the
    // sidebar list and any open overlay window render immediately.
    this.conversations.set(created.id, created);
    this.#rebuildConversationOrder();

    return { id: created.id };
  }

  /**
   * Discard a conversation: Conversation/set { destroy: [id] }. The
   * server destroys the conversation, all memberships, and all messages
   * in one tx; the change feed is fanned to every member, so any other
   * open client sees the row disappear from their sidebar on the next
   * EventSource state advance.
   *
   * The cache is updated immediately so the sidebar reflects the
   * deletion before the EventSource round-trip completes.
   */
  async destroyConversation(conversationId: string): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) throw new Error('Not authenticated');

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Conversation/set',
        { accountId, destroy: [conversationId] },
        USING,
      );
    });

    const setResp = responses.find(([name]) => name === 'Conversation/set');
    if (!setResp || setResp[0] === 'error') {
      throw new Error('Conversation/set failed');
    }
    const result = setResp[1] as {
      destroyed?: string[];
      notDestroyed?: Record<string, { type: string; description?: string }>;
    };
    if (result.notDestroyed?.[conversationId]) {
      const err = result.notDestroyed[conversationId]!;
      throw new Error(err.description ?? err.type);
    }
    if (!result.destroyed?.includes(conversationId)) {
      throw new Error('Conversation/set destroy did not return the id');
    }

    // Update the local caches so the sidebar drops the row immediately.
    this.conversations.delete(conversationId);
    this.#rebuildConversationOrder();
    this.overlayMessages.delete(conversationId);
    this.overlayMessages = new Map(this.overlayMessages);
    this.memberships.delete(conversationId);
    if (this.openConversationId === conversationId) {
      this.openConversationId = null;
      this.messages = [];
    }
  }

  // ------------------------------------------------------------------
  // Derived / helpers
  // ------------------------------------------------------------------

  #rebuildConversationOrder(): void {
    // The server's Conversation wire shape does not always include
    // createdAt (and lastMessageAt is null for brand-new conversations
    // with no messages yet), so the comparator must tolerate missing
    // values without throwing — otherwise the rebuild silently aborts
    // and the sidebar never picks up the new row.
    const sorted = Array.from(this.conversations.values()).sort((a, b) => {
      if (a.pinned !== b.pinned) return a.pinned ? -1 : 1;
      const ta = a.lastMessageAt ?? a.createdAt ?? '';
      const tb = b.lastMessageAt ?? b.createdAt ?? '';
      return tb.localeCompare(ta);
    });
    this.conversationIds = sorted.map((c) => c.id);
  }

  #accountId(): string | null {
    const session = auth.session;
    if (!session) return null;
    return (
      session.primaryAccounts[CHAT_CAPABILITY] ??
      session.primaryAccounts[Capability.Core] ??
      null
    );
  }

  /** Total unread across all unmuted conversations (for the rail badge). */
  get totalUnread(): number {
    let n = 0;
    for (const c of this.conversations.values()) {
      if (!c.muted) n += c.unreadCount;
    }
    return n;
  }

  /**
   * Conditionally play the chat audio cue for an incoming message.
   * Evaluates all gates via shouldPlayChatCue:
   *   - not from self
   *   - conversation not muted
   *   - not focused: document visible AND the conversation is the
   *     active route OR an un-minimized overlay window is open for it.
   */
  #maybeChatCue(message: Message): void {
    const conv = this.conversations.get(message.conversationId);
    const conversationMuted = conv?.muted ?? false;

    // Focus gate: is this conversation in the foreground?
    const documentVisible =
      typeof document !== 'undefined' &&
      document.visibilityState === 'visible';
    const routeActive =
      router.parts[0] === 'chat' &&
      router.parts[1] === 'conversation' &&
      router.parts[2] === message.conversationId;
    const overlayExpanded =
      chatOverlay.windows.some(
        (w) =>
          w.conversationId === message.conversationId && !w.minimized,
      );
    const conversationFocused =
      documentVisible && (routeActive || overlayExpanded);

    if (
      shouldPlayChatCue({
        senderId: message.senderPrincipalId,
        myPrincipalId: auth.principalId,
        conversationMuted,
        conversationFocused,
      })
    ) {
      sounds.play('chat');
    }
  }
}

export const chat = new ChatStore();
