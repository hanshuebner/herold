/**
 * English message catalogue. Source-of-truth -- every key in any other
 * locale must exist here. Keys are dot-grouped by surface to keep
 * future additions discoverable.
 */
export const en = {
  // ── Mail sidebar ─────────────────────────────────────────────────────
  'sidebar.compose': 'Compose',
  'sidebar.inbox': 'Inbox',
  'sidebar.snoozed': 'Snoozed',
  'sidebar.important': 'Important',
  'sidebar.sent': 'Sent',
  'sidebar.drafts': 'Drafts',
  'sidebar.trash': 'Trash',
  'sidebar.allMail': 'All Mail',
  'sidebar.more': 'More',
  'sidebar.labels': 'Labels',
  'sidebar.newMailbox': 'New mailbox',
  'sidebar.noCustom': 'No custom mailboxes.',
  'sidebar.rename': 'Rename',
  'sidebar.delete': 'Delete',
  'sidebar.chats': 'Chats',
  'sidebar.newChat': 'New chat',

  // ── Mail list ───────────────────────────────────────────────────────
  'list.refresh': 'Refresh',
  'list.emptyTrash': 'Empty trash',
  'list.loading': 'Loading...',
  'list.retry': 'Retry',
  'list.empty.inbox': 'Inbox is empty.',
  'list.empty.allMail': 'No mail.',
  'list.empty.folder': '{name} is empty.',
  'list.couldNotLoad': "Couldn't load {name}.",

  // ── Selection chooser (issue #10) ────────────────────────────────────
  'select.all': 'All',
  'select.none': 'None',
  'select.read': 'Read',
  'select.unread': 'Unread',
  'select.starred': 'Starred',
  'select.unstarred': 'Unstarred',
  'select.openMenu': 'Select...',
  'select.deselectAll': 'Deselect all',
  'select.clearSelection': 'Clear selection',
  'select.selectAll': 'Select all',
  'select.options': 'Select options',

  // ── Bulk actions ────────────────────────────────────────────────────
  'bulk.selected': '{count} selected',
  'bulk.archive': 'Archive',
  'bulk.markRead': 'Mark read',
  'bulk.markUnread': 'Mark unread',
  'bulk.move': 'Move...',
  'bulk.label': 'Label...',
  'bulk.category': 'Category...',
  'bulk.delete': 'Delete',

  // ── Thread reader ───────────────────────────────────────────────────
  'thread.loading': 'Loading thread...',
  'thread.couldNotLoad': "Couldn't load thread.",
  'thread.retry': 'Retry',
  'thread.empty': 'Thread has no messages.',
  'thread.print': 'Print thread',
  'thread.subject.none': '(no subject)',

  // ── Message actions (issue #17 tooltips) ─────────────────────────────
  'msg.reply': 'Reply',
  'msg.replyAll': 'Reply all',
  'msg.forward': 'Forward',
  'msg.react': 'React',
  'msg.move': 'Move to mailbox',
  'msg.label': 'Apply labels',
  'msg.markRead': 'Mark as read',
  'msg.markUnread': 'Mark as unread',
  'msg.markImportant': 'Mark important',
  'msg.unmarkImportant': 'Unmark important',
  'msg.snooze': 'Snooze',
  'msg.unsnooze': 'Wake up now',
  'msg.restore': 'Restore from trash',
  'msg.muteThread': 'Mute thread',
  'msg.unmuteThread': 'Unmute thread',
  'msg.reportSpam': 'Report spam',
  'msg.reportPhishing': 'Report phishing',
  'msg.blockSender': 'Block sender',
  'msg.filterLike': 'Filter messages like this',
  'msg.imagesBlocked': 'External images are blocked.',
  'msg.loadImages': 'Load images',
  'msg.alwaysFrom': 'Always from {sender}',
  'msg.noBody': '(no body)',
  'msg.noSender': '(no sender)',
  'msg.recipientsTo': 'to {first}',
  'msg.recipientsToMany': 'to {first} and {others} other',
  'msg.recipientsToManyOther': 'to {first} and {others} others',

  // ── Compose ─────────────────────────────────────────────────────────
  'compose.title.new': 'New message',
  'compose.title.reply': 'Reply',
  'compose.title.forward': 'Forward',
  'compose.minimize': 'Minimize',
  'compose.close': 'Close compose',
  'compose.from': 'From',
  'compose.to': 'To',
  'compose.cc': 'Cc',
  'compose.bcc': 'Bcc',
  'compose.subject': 'Subject',
  'compose.body': 'Body',
  'compose.toggleCcBcc': 'Cc / Bcc',
  'compose.send': 'Send',
  'compose.sending': 'Sending...',
  'compose.discard': 'Discard',
  'compose.attach': 'Attach',
  'compose.attached': 'Attached',
  'compose.dropToAttach': 'Drop to attach',
  'compose.dropInline': 'Drop image here to inline',
  'compose.dropAttach': 'Drop file here to attach',
  'compose.moveToAttachments': 'Move to attachments',
  'compose.discardConfirm.title': 'Discard this message?',
  'compose.discardConfirm.message': 'Your draft will be lost.',
  'compose.discardConfirm.confirm': 'Discard',
  'compose.discardConfirm.cancel': 'Keep editing',

  // ── Attachment list ──────────────────────────────────────────────────
  'att.attachments': '{count} attachment',
  'att.attachments.other': '{count} attachments',
  'att.inlineImages': 'Inline images',
  'att.downloadAll': 'Download all ({count})',
  'att.attachmentsOnly': 'Attachments only',
  'att.download': 'Download',
  'att.noUrl': 'No URL',

  // ── Pickers (move / label / etc.) ────────────────────────────────────
  'picker.close': 'Close',
  'movePicker.title.single': 'Move to mailbox',
  'movePicker.title.bulk': 'Move {count} message to',
  'movePicker.title.bulk.other': 'Move {count} messages to',
  'movePicker.filter': 'Filter mailboxes...',
  'movePicker.empty': 'No other mailboxes available.',
  'movePicker.empty.filter': 'No mailboxes match "{filter}".',
  'labelPicker.title.single': 'Apply labels',
  'labelPicker.title.bulk': 'Label {count} message',
  'labelPicker.title.bulk.other': 'Label {count} messages',
  'labelPicker.filter': 'Filter or create label...',
  'labelPicker.empty':
    'No labels yet. Type a name above to create one.',
  'labelPicker.empty.filter': 'No labels match "{filter}".',
  'labelPicker.create': 'Create label "{name}"',
  'labelPicker.done': 'Done',

  // ── Categories settings ──────────────────────────────────────────────
  'cat.currentCategories': 'Current categories',
  'cat.currentCategories.hint':
    'These are the categories the LLM is currently using, derived from the prompt above. Edit the prompt to change them.',
  'cat.currentCategories.empty':
    'No categories yet. Categories will appear here after the next message is classified.',
  'cat.prompt.heading': 'Classification prompt',
  'cat.prompt.hint':
    'The prompt used by the LLM to classify your mail into categories. Editing this changes how future mail (and re-categorised mail) is classified. Max 32 KB.',
  'cat.prompt.reset': 'Reset to default',
  'cat.prompt.save': 'Save prompt',
  'cat.disclosure.heading': 'How your mail is classified',
  'cat.disclosure.hint':
    'This is the prompt used to categorise your mail. Your messages are sent to herold\'s configured classifier endpoint along with this prompt.',
  'cat.recategorise.heading': 'Re-categorise inbox',
  'cat.recategorise.hint':
    'Run the classifier on your recent inbox (up to 1000 messages). Results appear as the job progresses in the background.',
  'cat.recategorise.run': 'Re-categorise inbox',
  'cat.recategorise.running': 'Running...',
  'cat.recategorise.notAvailable': 'Not available on this server.',
  'cat.recategorise.inProgress':
    'Re-categorisation in progress -- results will update automatically.',

  // ── Settings ────────────────────────────────────────────────────────
  'settings.title': 'Settings',
  'settings.account': 'Account',
  'settings.security': 'Security',
  'settings.appearance': 'Appearance',
  'settings.mail': 'Mail',
  'settings.theme': 'Theme',
  'settings.theme.system': 'System',
  'settings.theme.light': 'Light',
  'settings.theme.dark': 'Dark',
  'settings.language': 'Language',
  'settings.language.en': 'English',
  'settings.language.de': 'Deutsch',

  // ── App switcher ────────────────────────────────────────────────────
  'app.mail': 'Mail',
  'app.calendar': 'Calendar',
  'app.contacts': 'Contacts',
  'app.chat': 'Chat',
  'app.admin': 'Server admin',
  'app.switch': 'Switch suite component',

  // ── Common ──────────────────────────────────────────────────────────
  'common.cancel': 'Cancel',
  'common.confirm': 'Confirm',
  'common.save': 'Save',
} as const;
