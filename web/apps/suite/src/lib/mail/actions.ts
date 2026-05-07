/**
 * Canonical action registry for the thread reading pane.
 *
 * Per re #98 the per-message action toolbar was removed: actions now live
 * exclusively at thread scope (in ThreadToolbar). Reactions moved into the
 * message header; reply / reply-all / forward are the fixed-bar CTAs at the
 * bottom of the reader.
 *
 * Every entry carries:
 *   - id: stable string key
 *   - labelKey: i18n key for the button label
 *   - iconName: SVG component name (resolved by the toolbar renderer)
 *   - shortcut: optional keyboard shortcut hint shown in the overflow menu
 *
 * State-conditional actions (Archive only when in Inbox, Delete only when
 * NOT in Trash, Restore only when in Trash) retain their state guards in
 * ThreadToolbar; this registry just sets ORDER. A state-hidden action does
 * not render and the next preferred action takes its slot.
 */

export interface ActionDef {
  id: string;
  labelKey: string;
  iconName: string;
  /** Optional keyboard shortcut hint shown in the overflow menu. */
  shortcut?: string;
}

// ── Thread-scope actions ───────────────────────────────────────────────────
// These operate on the whole thread. Shown in the persistent ThreadToolbar.

export const THREAD_ACTIONS: ActionDef[] = [
  { id: 'archive',       labelKey: 'thread.archive',        iconName: 'ArchiveIcon',     shortcut: 'e' },
  { id: 'deleteThread',  labelKey: 'thread.delete',         iconName: 'TrashIcon',       shortcut: '#' },
  { id: 'restoreThread', labelKey: 'thread.restore',        iconName: 'RestoreIcon' },
  { id: 'markUnread',    labelKey: 'thread.markUnread',     iconName: 'MarkUnreadIcon',  shortcut: 'u' },
  { id: 'snoozeThread',  labelKey: 'thread.snooze',         iconName: 'SnoozeIcon' },
  { id: 'moveThread',    labelKey: 'thread.move',           iconName: 'MoveIcon' },
  { id: 'labelThread',   labelKey: 'thread.label',          iconName: 'LabelIcon' },
  { id: 'muteThread',    labelKey: 'msg.muteThread',        iconName: 'MuteIcon' },
  { id: 'reportSpam',    labelKey: 'msg.reportSpam',        iconName: 'SpamIcon' },
  { id: 'reportPhishing',labelKey: 'msg.reportPhishing',    iconName: 'PhishingIcon' },
  { id: 'blockSender',   labelKey: 'msg.blockSender',       iconName: 'BlockIcon' },
  { id: 'print',         labelKey: 'thread.print',          iconName: 'PrintIcon' },
];

// Default visible-in-toolbar count. Chosen to keep the toolbar readable
// without configuration: Archive / Delete / Mark-unread / Snooze as the
// primary group; the rest sit in the "More actions" overflow menu.
export const DEFAULT_THREAD_VISIBLE = 4;
