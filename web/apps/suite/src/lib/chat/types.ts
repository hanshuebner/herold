/**
 * Chat JMAP datatypes per docs/design/web/architecture/07-chat-protocol.md.
 *
 * Conversation, Message, and Membership are new JMAP entity kinds registered
 * by herold under the https://netzhansa.com/jmap/chat capability.
 *
 * These are wire shapes only. Derived views (sorted conversation lists,
 * display names, etc.) live in the chat store.
 */

/** A DM (1:1) or Space (group) conversation. */
export interface Conversation {
  id: string;
  /**
   * Wire-level discriminator from herold's JMAP chat handler. The server
   * field is `kind` (not `type`); aliasing it locally as `type` previously
   * meant `conv.type === 'dm'` was always false, breaking presence dots,
   * space icons, and DM-only labels.
   */
  kind: 'dm' | 'space';
  /**
   * For spaces: the display name. For DMs: server projects the OTHER
   * member's display name per viewer.
   */
  name: string;
  description?: string;
  members: Membership[];
  createdAt: string; // UTCDate
  lastMessageAt?: string; // UTCDate
  lastMessagePreview?: string;
  pinned: boolean;
  muted: boolean;
  /** Server-computed unread count for the requesting user. */
  unreadCount: number;
  /**
   * Requester's own Membership row, projected by the server on every
   * Conversation/get. Carries the Membership id needed for
   * Membership/set update calls (mark-read, mute, role) without a
   * separate Membership/get round-trip.
   */
  myMembership?: Membership;
}

/**
 * A server-fetched link preview card. Corresponds to jmapLinkPreview in
 * internal/protojmap/chat/types.go. All fields except url are optional
 * because the server omits unavailable fields rather than sending empty
 * strings.
 */
export interface LinkPreview {
  url: string;
  canonicalUrl?: string;
  title?: string;
  description?: string;
  /** Absolute URL, resolved server-side. Public resource; no auth headers. */
  imageUrl?: string;
  siteName?: string;
}

/** A message within a conversation. */
export interface Message {
  id: string;
  conversationId: string;
  /**
   * Server wire field is `senderPrincipalId`; aliasing it locally as
   * `senderId` previously meant every reload-served message had an
   * undefined sender, which broke `isMine` and pinned every bubble to
   * the other-party visual.
   */
  senderPrincipalId: string;
  type: 'text' | 'image' | 'system';
  body: { html: string; text: string };
  inlineImages: string[]; // BlobIds
  inReplyTo?: string; // Message id
  /** Sparse map of emoji -> list of principalIds who reacted. */
  reactions: Record<string, string[]>;
  /**
   * Server-fetched open-graph preview cards for URLs in the message body.
   * Omitted (never an empty array on the wire) when no previews were
   * successfully fetched. Max 3 per message. Matches jmapLinkPreview in
   * internal/protojmap/chat/types.go.
   */
  linkPreviews?: LinkPreview[];
  createdAt: string; // UTCDate
  editedAt?: string; // UTCDate
  deleted: boolean;
}

/** Per-conversation participation record. */
export interface Membership {
  id: string;
  conversationId: string;
  principalId: string;
  role: 'member' | 'admin' | 'owner';
  /**
   * Server-side projection of the principal's display name. Present on
   * Conversation.members[] (so the UI can label messages by sender without
   * a per-sender Principal/get round-trip). Absent on standalone
   * Membership records returned by Membership/get.
   */
  displayName?: string;
  joinedAt: string; // UTCDate
  /**
   * The last Message id this member has read. Wire field name matches
   * the server's memUpdateInput / jmapMembership; previously aliased
   * locally as `readThrough`, which silently failed Membership/set
   * because the server ignored the unknown property.
   */
  lastReadMessageId?: string;
  isMuted?: boolean;
  notificationsSetting?: 'all' | 'mentions' | 'none';
}

/**
 * Ephemeral WebSocket wire envelope.
 *
 * Both directions use this envelope shape, per internal/protochat/protocol.go:
 *   { "type": "<token>", "payload": {...}, "ack": "...", "error": {...} }
 *
 * Inbound (server -> client) and outbound (client -> server) use SEPARATE
 * discriminated unions because their token sets differ.
 */

/** Raw wire envelope — both directions. */
export interface WireEnvelope {
  type: string;
  payload?: unknown;
  ack?: string;
  error?: { code: string; message?: string };
}

// ------------------------------------------------------------------
// Outbound payload interfaces (client -> server)
// ------------------------------------------------------------------

export interface TypingStartPayload {
  conversationId: string;
}

export interface TypingStopPayload {
  conversationId: string;
}

export interface PresenceSetPayload {
  state: PresenceState;
}

export interface SubscribePayload {
  conversationIds: string[];
}

export interface UnsubscribePayload {
  conversationIds: string[];
}

/**
 * Outbound call.signal payload. Kind selects the WebRTC verb;
 * payload is the verb-specific body forwarded to the peer unchanged.
 * targetPrincipalId identifies the call recipient (uint64 on wire).
 */
export interface CallSignalOutPayload {
  conversationId: string;
  targetPrincipalId?: number;
  kind: string;
  payload: unknown;
}

// Discriminated union of typed outbound frames.
export type OutboundFrame =
  | { type: 'typing.start'; payload: TypingStartPayload }
  | { type: 'typing.stop'; payload: TypingStopPayload }
  | { type: 'presence.set'; payload: PresenceSetPayload }
  | { type: 'subscribe'; payload: SubscribePayload }
  | { type: 'unsubscribe'; payload: UnsubscribePayload }
  | { type: 'call.signal'; payload: CallSignalOutPayload }
  | { type: 'ping' };

// ------------------------------------------------------------------
// Inbound payload interfaces (server -> client)
// ------------------------------------------------------------------

/**
 * Inbound typing payload. PrincipalID is uint64 on the wire (a JSON
 * number); coerce to string at the WS boundary so Map<string, ...> keys work.
 */
export interface TypingPayload {
  conversationId: string;
  principalId: string; // coerced from uint64 at WS boundary
  state: 'start' | 'stop';
}

/**
 * Inbound presence payload. PrincipalID is uint64 on the wire (a JSON
 * number); coerce to string at the WS boundary.
 */
export interface PresencePayload {
  principalId: string; // coerced from uint64 at WS boundary
  state: PresenceState;
  lastSeenAt: number; // unix seconds
}

/** Inbound read-receipt payload. */
export interface ReadPayload {
  conversationId: string;
  principalId: string; // coerced from uint64 at WS boundary
  lastReadMessageId: string;
}

/**
 * Inbound call.signal payload. fromPrincipalId identifies the sender.
 * Kind selects the WebRTC verb; payload is opaque and forwarded to the
 * peer's RTCPeerConnection unchanged.
 */
export interface CallSignalInPayload {
  conversationId: string;
  kind: string;
  payload: unknown;
  fromPrincipalId: number; // uint64 on wire; coerce at call-signal handler boundary
}

export interface AckPayload {
  clientId: string;
}

export interface ErrorPayload {
  code: string;
  message?: string;
}

// Discriminated union of typed inbound frames (by server token).
export type InboundFrame =
  | { type: 'typing'; payload: TypingPayload }
  | { type: 'presence'; payload: PresencePayload }
  | { type: 'read'; payload: ReadPayload }
  | { type: 'call.signal'; payload: CallSignalInPayload }
  | { type: 'error'; payload: ErrorPayload }
  | { type: 'ack'; payload: AckPayload }
  | { type: 'pong' };

/** Presence state for a principal. */
export type PresenceState = 'online' | 'away' | 'offline';

/**
 * A Herold Principal as returned by Principal/get.
 * Only the three fields the suite is allowed to see (REQ-CHAT-15).
 * The id is opaque — used only as a foreign key; never rendered.
 */
export interface Principal {
  id: string;
  email: string;
  displayName: string;
}

/**
 * TURN credential returned by POST /api/v1/call/credentials.
 * Wire shape matches internal/protocall/turn.go Credential struct.
 */
export interface TurnCredential {
  username: string;
  password: string;
  uris: string[];
  expiresAt: string; // RFC3339
  ttlSeconds: number;
}
