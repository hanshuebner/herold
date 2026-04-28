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
import type {
  Conversation,
  Message,
  Membership,
  PresenceState,
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
      senderId: auth.principalId ?? '',
      type: 'text',
      body: { html, text },
      inlineImages: [],
      reactions: {},
      createdAt: now,
      deleted: false,
    };

    // Optimistic insert.
    this.messages = [...this.messages, optimisticMsg];

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
        this.#rollbackOptimistic(tempId);
        toast.show({ message: 'Failed to send message', kind: 'error' });
        return;
      }

      const result = setResp[1] as {
        created?: Record<string, { id: string }>;
        notCreated?: Record<string, { type: string; description?: string }>;
      };

      if (result.notCreated?.[tempId]) {
        this.#rollbackOptimistic(tempId);
        const errType = result.notCreated[tempId]?.type ?? 'unknown';
        toast.show({ message: `Failed to send: ${errType}`, kind: 'error' });
        return;
      }

      const realId = result.created?.[tempId]?.id;
      if (realId) {
        // Replace optimistic record with real id.
        this.messages = this.messages.map((m) =>
          m.id === tempId ? { ...m, id: realId } : m,
        );
      }
    } catch (err) {
      this.#rollbackOptimistic(tempId);
      const msg = err instanceof Error ? err.message : 'Send failed';
      toast.show({ message: msg, kind: 'error' });
    }
  }

  #rollbackOptimistic(tempId: string): void {
    this.messages = this.messages.filter((m) => m.id !== tempId);
  }

  // ------------------------------------------------------------------
  // Read receipts
  // ------------------------------------------------------------------

  async markRead(conversationId: string, messageId: string): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId || !auth.principalId) return;

    try {
      // Find the user's Membership in this conversation.
      const mems = this.memberships.get(conversationId) ?? [];
      const myMembership = mems.find(
        (m) => m.principalId === auth.principalId,
      );
      if (!myMembership) return;

      await jmap.batch((b) => {
        b.call(
          'Membership/set',
          {
            accountId,
            update: {
              [myMembership.id]: { readThrough: messageId },
            },
          },
          USING,
        );
      });

      // Update local membership state.
      const updated = mems.map((m) =>
        m.id === myMembership.id ? { ...m, readThrough: messageId } : m,
      );
      this.memberships.set(conversationId, updated);
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
    const openId = this.openConversationId;
    if (!openId || !this.#messageState) return;

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
          if (incoming.conversationId !== openId) continue;
          const idx = this.messages.findIndex((m) => m.id === incoming.id);
          if (idx >= 0) {
            // Update existing (edited, reaction toggle, etc.).
            this.messages = this.messages.map((m) =>
              m.id === incoming.id ? incoming : m,
            );
          } else {
            // New message — append.
            this.messages = [...this.messages, incoming];
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
  // Derived / helpers
  // ------------------------------------------------------------------

  #rebuildConversationOrder(): void {
    const sorted = Array.from(this.conversations.values()).sort((a, b) => {
      if (a.pinned !== b.pinned) return a.pinned ? -1 : 1;
      const ta = a.lastMessageAt ?? a.createdAt;
      const tb = b.lastMessageAt ?? b.createdAt;
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
}

export const chat = new ChatStore();
