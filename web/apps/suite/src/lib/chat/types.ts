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
 * Ephemeral WebSocket frame shapes.
 *
 * Outbound (client -> server): TypingFrame, PresenceFrame, CallFrame variants.
 * Inbound (server -> client): mirrors plus presence-update, call.credentials.response.
 */

export interface TypingFrame {
  op: 'typing';
  conversationId: string;
  /** Present on inbound frames (added by server when fanning out). */
  principalId?: string;
}

export interface TypingStoppedFrame {
  op: 'typing-stopped';
  conversationId: string;
  /** Present on inbound frames (added by server when fanning out). */
  principalId?: string;
}

export interface PresenceFrame {
  op: 'presence';
  state: 'online' | 'away';
}

export interface PongFrame {
  op: 'pong';
}

export interface CallInviteFrame {
  op: 'call.invite';
  conversationId: string;
  sdp: string;
  callId: string;
}

export interface CallAcceptFrame {
  op: 'call.accept';
  callId: string;
  sdp: string;
}

export interface CallDeclineFrame {
  op: 'call.decline';
  callId: string;
}

export interface CallCandidateFrame {
  op: 'call.candidate';
  callId: string;
  candidate: string;
}

export interface CallHangupFrame {
  op: 'call.hangup';
  callId: string;
}

export interface CallCredentialsRequestFrame {
  op: 'call.credentials';
  callId: string;
}

// Inbound-only frames
export interface PingFrame {
  op: 'ping';
}

export interface PresenceUpdateFrame {
  op: 'presence-update';
  principalId: string;
  state: 'online' | 'away' | 'offline';
}

export interface CallCredentialsResponseFrame {
  op: 'call.credentials.response';
  callId: string;
  config: TurnConfig;
}

export interface TurnConfig {
  urls: string[];
  username: string;
  credential: string;
  ttl: number;
}

export type OutboundFrame =
  | TypingFrame
  | TypingStoppedFrame
  | PresenceFrame
  | PongFrame
  | CallInviteFrame
  | CallAcceptFrame
  | CallDeclineFrame
  | CallCandidateFrame
  | CallHangupFrame
  | CallCredentialsRequestFrame;

export type InboundFrame =
  | PingFrame
  | TypingFrame
  | TypingStoppedFrame
  | PresenceUpdateFrame
  | CallInviteFrame
  | CallAcceptFrame
  | CallDeclineFrame
  | CallCandidateFrame
  | CallHangupFrame
  | CallCredentialsResponseFrame;

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
