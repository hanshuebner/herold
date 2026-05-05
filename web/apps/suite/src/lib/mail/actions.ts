/**
 * Canonical action registry for the mail reading pane.
 *
 * Every entry carries:
 *   - id: stable string key used in prefs storage
 *   - scope: whether this action targets one message or the whole thread
 *   - labelKey: i18n key for the button label / settings UI
 *   - iconName: SVG component name (resolved by the toolbar renderer)
 *   - shortcut: optional keyboard shortcut hint shown in the overflow menu
 *
 * Thread-scope actions live in the ThreadToolbar; message-scope actions
 * live in the per-message row inside MessageAccordion. The split solves
 * the UX confusion where "Mute thread" appeared under every message.
 *
 * State-conditional actions (Restore-in-Trash, Reply-All with >1 recipient,
 * Snooze-vs-Unsnooze) retain their state guards — prefs set ORDER only.
 * A state-hidden action simply does not render and the next preferred action
 * fills its slot.
 *
 * re #60
 */

export type ActionScope = 'message' | 'thread';

export interface ActionDef {
  id: string;
  scope: ActionScope;
  labelKey: string;
  iconName: string;
  /** Optional keyboard shortcut hint shown in the overflow menu. */
  shortcut?: string;
}

// ── Message-scope actions ──────────────────────────────────────────────────
// These operate on a single message. Shown in the per-message action row.

export const MESSAGE_ACTIONS: ActionDef[] = [
  { id: 'reply',         scope: 'message', labelKey: 'msg.reply',           iconName: 'ReplyIcon',       shortcut: 'r' },
  { id: 'replyAll',      scope: 'message', labelKey: 'msg.replyAll',         iconName: 'ReplyAllIcon' },
  { id: 'forward',       scope: 'message', labelKey: 'msg.forward',          iconName: 'ForwardIcon',     shortcut: 'f' },
  { id: 'react',         scope: 'message', labelKey: 'msg.react',            iconName: 'ReactIcon',       shortcut: '+' },
  { id: 'moveMsg',       scope: 'message', labelKey: 'msg.move',             iconName: 'MoveIcon' },
  { id: 'labelMsg',      scope: 'message', labelKey: 'msg.label',            iconName: 'LabelIcon' },
  { id: 'markRead',      scope: 'message', labelKey: 'msg.markUnread',       iconName: 'MarkUnreadIcon' },
  { id: 'markImportant', scope: 'message', labelKey: 'msg.markImportant',    iconName: 'ImportantIcon' },
  { id: 'snoozeMsg',     scope: 'message', labelKey: 'msg.snooze',           iconName: 'SnoozeIcon' },
  { id: 'restore',       scope: 'message', labelKey: 'msg.restore',          iconName: 'RestoreIcon' },
  { id: 'filterLike',    scope: 'message', labelKey: 'msg.filterLike',       iconName: 'FilterIcon' },
  // Re "Filter messages like this": handleFilterLike in MessageAccordion builds
  // Sieve conditions from the *message* sender/subject/list-id — it is decidedly
  // per-message (each message can have a different sender), so it stays here.
  { id: 'viewOriginal',  scope: 'message', labelKey: 'msg.viewOriginal',     iconName: 'ViewOriginalIcon' },
];

// ── Thread-scope actions ───────────────────────────────────────────────────
// These operate on the whole thread. Shown in the persistent ThreadToolbar.

export const THREAD_ACTIONS: ActionDef[] = [
  { id: 'archive',       scope: 'thread', labelKey: 'thread.archive',        iconName: 'ArchiveIcon',     shortcut: 'e' },
  { id: 'deleteThread',  scope: 'thread', labelKey: 'thread.delete',         iconName: 'TrashIcon',       shortcut: '#' },
  { id: 'markUnread',    scope: 'thread', labelKey: 'thread.markUnread',     iconName: 'MarkUnreadIcon',  shortcut: 'u' },
  { id: 'snoozeThread',  scope: 'thread', labelKey: 'thread.snooze',         iconName: 'SnoozeIcon' },
  { id: 'moveThread',    scope: 'thread', labelKey: 'thread.move',           iconName: 'MoveIcon' },
  { id: 'labelThread',   scope: 'thread', labelKey: 'thread.label',          iconName: 'LabelIcon' },
  { id: 'muteThread',    scope: 'thread', labelKey: 'msg.muteThread',        iconName: 'MuteIcon' },
  { id: 'reportSpam',    scope: 'thread', labelKey: 'msg.reportSpam',        iconName: 'SpamIcon' },
  { id: 'reportPhishing',scope: 'thread', labelKey: 'msg.reportPhishing',    iconName: 'PhishingIcon' },
  { id: 'blockSender',   scope: 'thread', labelKey: 'msg.blockSender',       iconName: 'BlockIcon' },
  { id: 'print',         scope: 'thread', labelKey: 'thread.print',          iconName: 'PrintIcon' },
];

// Default visible-in-toolbar counts. Chosen to keep the toolbar readable
// without extra configuration:
//   Message: Reply / Forward / React / Move-to-label in the pill row.
//   Thread:  Archive / Delete / Mark-unread / Snooze as the primary group.
export const DEFAULT_MSG_VISIBLE = 4;
export const DEFAULT_THREAD_VISIBLE = 4;
