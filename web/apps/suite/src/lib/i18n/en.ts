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
  'sidebar.newMailbox': 'New label',
  'sidebar.noCustom': 'No custom labels.',
  'sidebar.rename': 'Rename',
  'sidebar.delete': 'Delete',
  'sidebar.chats': 'Chats',
  'sidebar.newChat': 'New chat',
  'sidebar.createFolder.title': 'New label',
  'sidebar.createFolder.label': 'Label name',
  'sidebar.createFolder.confirm': 'Create',
  'sidebar.renameFolder.title': 'Rename label',
  'sidebar.renameFolder.label': 'New name',
  'sidebar.renameFolder.confirm': 'Rename',
  'sidebar.editFolder.title': 'Edit label',
  'sidebar.editFolder.confirm': 'Change',
  'sidebar.editFolder.toastChanged': 'Changed',
  'sidebar.deleteFolder.title': 'Delete label "{name}"?',
  'sidebar.deleteFolder.message':
    "Messages it contains will remain in any other labels they're in (otherwise they go to Trash on the server).",
  'sidebar.deleteFolder.confirm': 'Delete',

  // ── Mail list ───────────────────────────────────────────────────────
  'list.refresh': 'Refresh',
  'list.emptyTrash': 'Empty trash',
  'list.loading': 'Loading...',
  // Slow-load warning shown after 1 s of loading (re #30).
  'list.loadSlow': "Sorry we're slow. There may be a system problem.",
  'list.retry': 'Retry',
  'list.empty.inbox': 'Inbox is empty.',
  'list.empty.allMail': 'No mail.',
  'list.empty.folder': '{name} is empty.',
  'list.couldNotLoad': "Couldn't load {name}.",
  // Supplementary hint in the hard-error state (re #30).
  'list.couldNotLoad.systemHint': 'This may be a capacity or system problem. Please try again.',
  'list.dragMessageCount': 'Move {count} messages',
  // External-image internalize-pending badge (REQ-EXTIMG-BG-30).
  'mail.list.internalizePending.tooltip':
    'Images are being processed in the background. Refresh in a moment to see them.',
  // Thread-reader internalize-pending banner (REQ-EXTIMG-BG-31).
  'mail.threadReader.internalizePending.heading': 'Images are being processed',
  'mail.threadReader.internalizePending.body':
    'External images in this message are still being internalized in the background. They appear as placeholders for now; refresh in a moment to see them.',

  // Thread-reader new-reply banner (issue #118): inline non-modal banner
  // shown above a freshly-arrived reply so the user is not blindsided by
  // a new message while composing a reply themselves. Persists until
  // explicitly dismissed.
  'mail.threadReader.newReply.aria': 'New reply notification',
  'mail.threadReader.newReply.one.heading': 'New reply from {sender}',
  'mail.threadReader.newReply.many.heading': '{count} new replies',
  'mail.threadReader.newReply.show': 'Show new reply',
  'mail.threadReader.newReply.dismiss': 'Got it',
  'mail.threadReader.newReply.more': '+{count} more',

  // ── Tagged-address banner (REQ-MAIL-12c) ─────────────────────────────
  // Banner above the message body offering four actions on first-seen
  // sub-addressed mail. The headline interpolates the +suffix so the
  // user sees exactly which tag drives the prompt.
  'mail.taggedBanner.heading': 'Mail to +{suffix}.',
  'mail.taggedBanner.lead': 'Set up auto-sorting for future messages?',
  'mail.taggedBanner.action.label': 'Label',
  'mail.taggedBanner.action.labelArchive': 'Label + archive',
  'mail.taggedBanner.action.labelArchiveRead': 'Label + archive + mark read',
  'mail.taggedBanner.action.dismiss': 'Ignore future mail to +{suffix}',
  // Label-name editor pre-populated with the suffix capitalised. Title
  // changes per action so the user knows what they are about to commit.
  'mail.taggedBanner.dialog.title.label': 'Label mail to +{suffix}',
  'mail.taggedBanner.dialog.title.labelArchive': 'Label and archive mail to +{suffix}',
  'mail.taggedBanner.dialog.title.labelArchiveRead':
    'Label, archive, and mark read mail to +{suffix}',
  'mail.taggedBanner.dialog.confirm': 'Create rule',
  // Toast messages on action completion (REQ-MAIL-12c).
  'mail.taggedBanner.toast.filterCreated': 'Rule created for +{suffix}',
  'mail.taggedBanner.toast.dismissed': 'Future mail to +{suffix} will not prompt again',
  'mail.taggedBanner.toast.filterFailed': 'Could not create rule',
  'mail.taggedBanner.toast.dismissFailed': 'Could not save dismissal',

  // ── MailView surfaces (re #97) ──────────────────────────────────────
  // Search route: heading + empty-query echo, back link, ARIA labels for
  // the chip-recogniser strip / history strip / results listbox, and the
  // "no matches" empty state.
  'mail.search.heading': 'Search:',
  'mail.search.empty': '(empty)',
  'mail.search.backToInbox': '← Back to inbox',
  'mail.search.recognisedQueryAria': 'Recognised query',
  'mail.search.recentSearchesAria': 'Recent searches',
  'mail.search.resultsAria': 'Search results',
  'mail.search.noMatches': 'No matches.',
  // List-toolbar / category-tab strip / threads listbox ARIA labels and
  // their associated count badges.
  'mail.list.actionsAria': 'List actions',
  'mail.list.threadsAria': '{name} threads',
  'mail.list.tabsAria': 'Inbox categories',
  'mail.list.tabUnreadAria': '{count} unread',
  'mail.list.tabPrimary': 'Primary',
  'mail.list.emptyTab': 'No messages in {name}.',
  // Per-row controls: select checkbox, star toggle, thread-count badge,
  // and the inline attachment indicator (paperclip glyph next to the
  // subject). The thread-count badge is pluralised via the .other key.
  'mail.row.selectAria': 'Select message',
  'mail.row.starAria': 'Star',
  'mail.row.unstarAria': 'Unstar',
  'mail.row.threadCountAria': '{count} message',
  'mail.row.threadCountAria.other': '{count} messages',
  // Label-route placeholder (label-view querying lands after inbox).
  'mail.label.heading': 'Label: {name}',
  'mail.label.lead': 'Label-view querying arrives after inbox.',

  // ── Search (re #97) ──────────────────────────────────────────────────
  'search.history.label': 'Recent:',
  'search.history.rerun': 'Re-run search',
  'search.history.clear': 'Clear',
  'search.history.clearTitle': 'Clear history',
  'search.history.clearAria': 'Clear search history',
  'search.searching': 'Searching…',
  'search.failed': 'Search failed.',
  'search.advanced.from.placeholder': 'Sender name or address',
  'search.advanced.to.placeholder': 'Recipient name or address',
  'search.advanced.subject.placeholder': 'Subject contains',
  'search.advanced.body.placeholder': 'Body contains',

  // ── Mailbox picker ───────────────────────────────────────────────────
  'mailbox.filter.placeholder': 'Filter mailboxes…',
  'mailbox.filter.aria': 'Filter mailboxes',

  // ── Mail-route fallback ──────────────────────────────────────────────
  'mail.notFound.title': 'Not found',
  'mail.notFound.lead': 'No mail folder at that URL.',

  // ── JMAP setError types (issue #17) ──────────────────────────────────
  // Surfaced when an Email/set update lands in `notUpdated`. Without
  // these, the raw type code (`notFound`, `forbidden`, ...) leaks into
  // the toast as a user-visible English string.
  'mail.setError.notFound':           'That message no longer exists.',
  'mail.setError.forbidden':          'You do not have permission for that action.',
  'mail.setError.invalidProperties':  'The change was rejected as invalid.',
  'mail.setError.overQuota':          'Your mailbox is over quota.',
  'mail.setError.tooManyMailboxes':   'Too many mailboxes for one message.',
  'mail.setError.mailboxHasChild':    'That mailbox has sub-mailboxes; remove them first.',
  'mail.setError.serverFail':         'The server returned an error.',
  'mail.setError.unknown':            'Something went wrong.',

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
  'thread.print': 'Print',
  'thread.subject.none': '(no subject)',
  'thread.back': 'Back to list',
  // Thread-scope toolbar actions (re #60, re #98). Labels intentionally do
  // NOT repeat "thread" — the toolbar is the thread toolbar, the prefix is
  // redundant and noisy in the UI.
  'thread.archive': 'Archive',
  'thread.delete': 'Delete',
  'thread.restore': 'Restore from trash',
  'thread.markUnread': 'Mark unread',
  'thread.snooze': 'Snooze',
  'thread.move': 'Move',
  'thread.label': 'Label',
  'thread.moreActions': 'More actions',

  // ── Thread reader: self-authored card treatment ──────────────────────
  // Displayed in place of the sender name when the From address belongs to
  // one of the user's own identities (Change A / Change B in thread UX).
  'mail.thread.fromYou': 'You',

  // ── Message-scope strings ────────────────────────────────────────────
  // Per re #98 the per-message action surface was removed entirely:
  // reply / reply-all / forward live in the fixed reply bar; thread verbs
  // (muteThread, reportSpam, reportPhishing, blockSender, restore, archive,
  // mark-unread, snooze, move, label, print) live in the ThreadToolbar;
  // reactions live inline with the message title row.
  'msg.reply': 'Reply',
  'msg.replyAll': 'Reply all',
  'msg.forward': 'Forward',
  'msg.react': 'React',
  'msg.muteThread': 'Mute thread',
  'msg.unmuteThread': 'Unmute thread',
  'msg.reportSpam': 'Report spam',
  'msg.reportPhishing': 'Report phishing',
  'msg.blockSender': 'Block sender',
  'msg.imagesBlocked': 'External images are blocked.',
  'msg.loadImages': 'Load images',
  'msg.alwaysFrom': 'Always from {sender}',
  'msg.noBody': '(no body)',
  'msg.noSender': '(no sender)',
  'msg.recipientsTo': 'to {first}',
  'msg.recipientsToMany': 'to {first} and {others} other',
  'msg.recipientsToManyOther': 'to {first} and {others} others',

  // ── Per-message kebab menu (REQ-UI-21b, REQ-MAIL-130..142) ──────────
  'msg.kebab.openLabel': 'More actions',
  'msg.kebab.markUnread': 'Mark as unread',
  'msg.kebab.markUnreadFromHere': 'Mark unread from here',
  'msg.kebab.delete': 'Delete this message',
  'msg.kebab.download': 'Download message',
  'msg.kebab.showOriginal': 'Show original',
  'msg.kebab.print': 'Print this message',
  'msg.rawSource.title': 'Original message source',
  'msg.rawSource.copy': 'Copy',
  'msg.rawSource.copied': 'Copied',
  'msg.rawSource.close': 'Close',
  'msg.rawSource.loading': 'Loading raw source…',
  'msg.rawSource.error': 'Failed to load raw source',

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
  'compose.from.picker.aria': 'From identity',
  'compose.from.picker.open': 'Change From identity',
  'compose.from.chip.verifying': 'Verification pending',
  'compose.from.chip.unverified': 'Unverified',
  'compose.from.chip.external': 'External SMTP missing',
  'compose.from.disabled.unverified': 'Verify this address before you can send from it.',
  'compose.from.disabled.verifying': 'Waiting for verification of this address.',
  'compose.from.disabled.external': 'Set up external SMTP submission before you can send from this address.',
  'compose.from.sendDisabled.unverified': 'Verify this identity before sending.',
  'compose.from.sendDisabled.verifying': 'This identity is awaiting verification — cannot send yet.',
  'compose.from.sendDisabled.external': 'External SMTP submission is not configured for this identity.',
  'compose.from.sendDisabled.noIdentity': 'No identity available to send from.',

  // ── Compose formatting toolbar (re #97) ─────────────────────────────
  'composeToolbar.aria': 'Formatting',
  'composeToolbar.bold': 'Bold',
  'composeToolbar.italic': 'Italic',
  'composeToolbar.underline': 'Underline',
  'composeToolbar.bulletList': 'Bulleted list',
  'composeToolbar.orderedList': 'Numbered list',
  'composeToolbar.blockquote': 'Blockquote',
  'composeToolbar.linkAdd': 'Add link',
  'composeToolbar.linkRemove': 'Remove link',
  'composeToolbar.linkPrompt.title': 'Insert link',
  'composeToolbar.linkPrompt.confirm': 'Insert',
  'composeToolbar.image': 'Insert image',
  'composeToolbar.imageBadType': 'Pick image files (PNG, JPEG, GIF, WebP).',
  'composeToolbar.imageUploadFailed': 'Image upload failed: {name}',

  // ── Attachment list ──────────────────────────────────────────────────
  'att.attachments': '{count} attachment',
  'att.attachments.other': '{count} attachments',
  'att.downloadAll': 'Download all ({count})',
  'att.attachmentsOnly': 'Attachments only',
  'att.download': 'Download',
  'att.noUrl': 'No URL',
  'att.view': 'View',
  'att.close': 'Close',
  'att.headerIcon.label': 'Has attachment',
  'att.aria.open': 'Open {name}',
  'att.aria.download': 'Download {name}',
  'mail.trimmed.hide': 'Hide trimmed content',

  // ── Pickers (move / label / etc.) ────────────────────────────────────
  'picker.close': 'Close',
  'movePicker.title.single': 'Move to label',
  'movePicker.title.bulk': 'Move {count} message to',
  'movePicker.title.bulk.other': 'Move {count} messages to',
  'movePicker.filter': 'Filter labels...',
  'movePicker.empty': 'No other labels available.',
  'movePicker.empty.filter': 'No labels match "{filter}".',
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
  'cat.recategorise.runTitle': 'Re-categorise recent inbox',
  'cat.recategorise.disabledTitle': 'Bulk re-categorisation is not enabled on this server',
  'cat.disclosure.defaultNotLoaded': '(Default prompt -- not yet loaded.)',
  'cat.prompt.resetTitle': 'Revert to the shipped default prompt',

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
  'settings.displayName.label': 'Display name',
  'settings.displayName.helper':
    "Used in outbound mail's From header as 'Name' <address>.",
  'settings.save': 'Save',
  'settings.saved': 'Settings saved',
  'settings.saveFailed': 'Could not save settings',
  'settings.avatar.title': 'Profile picture',
  'settings.avatar.change': 'Change…',
  'settings.avatar.remove': 'Remove',
  'settings.avatar.pickNew': 'Pick new file',
  'settings.avatar.applyToAll.title': 'Use this avatar for all identities?',
  'settings.avatar.applyToAll.message':
    'You have {count} identities. Apply this picture to all of them?',
  'settings.avatar.applyToAll.confirm': 'Yes, apply to all',
  'settings.avatar.applyToAll.cancel': 'Just this identity',
  'settings.avatar.xface.title': 'Include avatar in outbound mail?',
  'settings.avatar.xface.message':
    'When enabled, your avatar is attached as an X-Face / Face header on every message you send from this identity. Recipients with email clients that support these headers will see your picture next to your name.',
  'settings.avatar.xface.confirm': 'Yes, enable',
  'settings.avatar.xface.cancel': 'No thanks',
  'settings.avatar.upload.failed': 'Upload failed: {reason}',
  'settings.avatar.upload.tooLarge':
    'Image is too large after compression. Try a smaller picture.',
  // Avatar capture / crop dialog (re #97)
  'settings.avatar.capture.title': 'Choose profile picture',
  'settings.avatar.capture.dropPrompt': 'Drag an image here, or pick a source:',
  'settings.avatar.capture.chooseFile': 'Choose file',
  'settings.avatar.capture.takePhoto': 'Take photo',
  'settings.avatar.capture.cameraUnavailable': 'Camera unavailable',
  'settings.avatar.capture.snapshotFailedNoCtx': 'Snapshot failed: no 2D context',
  'settings.avatar.capture.snapshotFailedEmpty': 'Snapshot failed: empty canvas',
  'settings.avatar.capture.decodeFailed': 'Could not decode image',
  'settings.avatar.capture.decodeFailedReason': 'Could not decode image: {reason}',
  'settings.avatar.capture.emptyFile': 'Could not decode image: the file is empty',
  'settings.avatar.capture.takePhotoTitle': 'Take photo',
  'settings.avatar.capture.back': 'Back',
  'settings.avatar.capture.snapshot': 'Snapshot',
  'settings.avatar.capture.cropTitle': 'Crop your picture',
  'settings.avatar.capture.reset': 'Reset',
  'settings.avatar.capture.useThis': 'Use this',
  'settings.avatar.capture.sourceAlt': 'Source',

  // Identity edit dialog (re #97)
  'settings.identityEdit.title': 'Edit identity',
  'settings.identityEdit.discardConfirm': 'Discard unsaved changes?',
  'settings.identityEdit.saveAll': 'Save',
  'settings.identityEdit.cancel': 'Cancel',
  'settings.identityEdit.replyTo': 'Reply-To address',
  'settings.identityEdit.replyToHelper':
    'Optional. When set, replies are directed here instead of the From address.',
  'settings.identityEdit.bcc': 'Bcc address (auto-bcc on send)',
  'settings.identityEdit.bccHelper':
    'Optional. Every message sent from this identity is silently copied to this address.',
  'settings.identityEdit.signaturePlain': 'Signature (plain text)',
  'settings.identityEdit.signatureHtml': 'Signature (HTML)',
  'settings.identityEdit.signatureHtmlHelper':
    'Raw HTML appended to outbound HTML messages. Leave blank to fall back to the plain-text signature.',
  'settings.identityEdit.invalidEmail': 'Enter a valid email address or leave blank.',
  'settings.identityEdit.back': 'Back to identities',
  'settings.identityEdit.notFound': 'That identity could not be found.',
  'settings.identityEdit.saving': 'Saving…',
  'settings.identityEdit.saved': 'Saved',
  'settings.identityEdit.saveFailed': 'Could not save',
  'settings.identityEdit.replyToHeading': 'Reply-To and Bcc',

  // Identity list (REQ-SET-IDENT-01..08, re #20)
  'settings.identityList.heading': 'Identities',
  'settings.identityList.addBtn': 'Add identity',
  'settings.identityList.addTooltip': 'Add a new sending identity',
  'settings.identityList.defaultRadioAria': 'Set {email} as default',
  'settings.identityList.defaultRadioDisabledTitle':
    'Only verified identities can be the default.',
  'settings.identityList.editRowAria': 'Edit {email}',
  'settings.identityList.editBtn': 'Edit',
  'settings.identityList.defaultColumn': 'Default',
  'settings.identityList.identityColumn': 'Identity',
  'settings.identityList.defaultBadge': 'Default',
  'settings.identityList.chip.verified': 'Verified',
  'settings.identityList.chip.verifying': 'Verification pending',
  'settings.identityList.chip.unverified': 'Unverified',
  'settings.identityList.verifyBtn': 'Verify',
  'settings.identityList.verifyTooltip': 'Enter the verification code',
  'settings.identityList.resendBtn': 'Resend',
  'settings.identityList.resendTooltip': 'Send the verification email again',
  'settings.identityList.externalDisabledTitle':
    'External SMTP submission is not configured for this identity.',
  'settings.identityList.defaultChanged': 'Default identity updated',
  'settings.identityList.defaultChangeFailed': 'Could not change default identity',
  'settings.identityList.rowMenuAria': 'Actions for {email}',
  'settings.identityList.deleteBtn': 'Delete',
  'settings.identityList.deleteConfirmTitle': 'Delete this identity?',
  'settings.identityList.deleteConfirmMessage':
    'Delete the identity {email}? You will no longer be able to send from this address. This cannot be undone.',
  'settings.identityList.deleteConfirm': 'Delete identity',
  'settings.identityList.deleted': 'Identity {email} deleted',
  'settings.identityList.deleteFailed': 'Could not delete the identity',

  // Add-identity wizard (REQ-SET-IDENT-30..33, re #21)
  'settings.identityWizard.title': 'Add identity',
  'settings.identityWizard.close': 'Close',
  'settings.identityWizard.cancel': 'Cancel',
  'settings.identityWizard.next': 'Next',
  'settings.identityWizard.back': 'Back',
  'settings.identityWizard.done': 'Done',
  'settings.identityWizard.skip': 'Skip',
  'settings.identityWizard.create': 'Create',
  'settings.identityWizard.creating': 'Creating…',
  'settings.identityWizard.step1Title': 'Address',
  'settings.identityWizard.step1Intro':
    'Tell us which email address this identity is for. We will send a confirmation message and ask you to enter the 6-digit code or click the link in the message.',
  'settings.identityWizard.emailLabel': 'Email address',
  'settings.identityWizard.emailHelper':
    'The address you want to be able to send from.',
  'settings.identityWizard.emailInvalid': 'Enter a valid email address.',
  'settings.identityWizard.displayNameLabel': 'Display name (optional)',
  'settings.identityWizard.displayNameHelper':
    'Shown to recipients alongside the email address.',
  'settings.identityWizard.domainBlocked':
    'This server does not allow identities for {domain} — contact your administrator.',
  'settings.identityWizard.emailExists':
    'An identity with this email address already exists.',
  'settings.identityWizard.createFailed':
    'Could not create the identity. Please try again.',
  'settings.identityWizard.step2Title': 'Confirm your address',
  'settings.identityWizard.step2Intro':
    'We sent a confirmation email to {email}. Either click the link in the email, or enter the 6-digit code below.',
  'settings.identityWizard.codeLabel': 'Verification code',
  'settings.identityWizard.codeHelper':
    '6 digits — leading zeros included. Codes expire after 24 hours.',
  'settings.identityWizard.codeInvalid': 'Enter exactly 6 digits.',
  'settings.identityWizard.codeWrong': 'That code did not match. Check the email and try again.',
  'settings.identityWizard.verify': 'Verify',
  'settings.identityWizard.verifying': 'Verifying…',
  'settings.identityWizard.resend': 'Resend email',
  'settings.identityWizard.resending': 'Resending…',
  'settings.identityWizard.resendOk': 'Verification email sent again.',
  'settings.identityWizard.resendRateLimited': 'Try again in {seconds} s.',
  'settings.identityWizard.resendRateLimitedShort': 'Wait before resending.',
  'settings.identityWizard.cancelPendingNotice':
    'Closing keeps this identity in the "Verification pending" state — you can verify later from Settings.',
  'settings.identityWizard.discardPending': 'Discard',
  'settings.identityWizard.keepPending': 'Keep pending',
  'settings.identityWizard.step3Title': 'Configure external SMTP',
  'settings.identityWizard.step3Intro':
    'This identity uses an external domain. Configure SMTP submission credentials so outbound mail is sent through {domain}.',
  'settings.identityWizard.step3SkipNote':
    'You can configure SMTP submission later from the identity edit dialog.',
  'settings.identityWizard.smtpHost': 'SMTP host',
  'settings.identityWizard.smtpPort': 'Port',
  'settings.identityWizard.smtpUser': 'Username',
  'settings.identityWizard.smtpPassword': 'Password',
  'settings.identityWizard.smtpSecurity': 'Security',
  'settings.identityWizard.smtpSecurityStartTLS': 'STARTTLS',
  'settings.identityWizard.smtpSecurityImplicit': 'Implicit TLS',
  'settings.identityWizard.smtpSecurityNone': 'None (plain)',
  'settings.identityWizard.smtpHostRequired': 'Host is required.',
  'settings.identityWizard.smtpUserRequired': 'Username is required.',
  'settings.identityWizard.smtpPasswordRequired': 'Password is required.',
  'settings.identityWizard.smtpPortInvalid': 'Port must be between 1 and 65535.',
  'settings.identityWizard.smtpSaveError': 'Could not save SMTP settings: {message}',
  'settings.identityWizard.successToast': 'Verified {email}',
  'settings.identityWizard.createdToast': 'Verification email sent to {email}',

  // Verify dialog (REQ-SET-IDENT-20..21, re #21)
  'settings.identityVerify.title': 'Verify identity',
  'settings.identityVerify.close': 'Close',
  'settings.identityVerify.intro':
    'We sent a verification message to {email}. Click the link in the message, or enter the 6-digit code below.',
  'settings.identityVerify.codeLabel': 'Verification code',
  'settings.identityVerify.verify': 'Verify',
  'settings.identityVerify.verifying': 'Verifying…',
  'settings.identityVerify.resend': 'Resend email',
  'settings.identityVerify.resending': 'Resending…',
  'settings.identityVerify.resendOk': 'Verification email sent again.',
  'settings.identityVerify.resendRateLimited': 'Try again in {seconds} s.',
  'settings.identityVerify.resendRateLimitedShort': 'Wait before resending.',
  'settings.identityVerify.successToast': 'Verified {email}',
  'settings.identityVerify.cancel': 'Cancel',

  // Filters form (re #97)
  'settings.filters.loading': 'Loading filters…',
  'settings.filters.heading2': 'Filters',
  'settings.filters.create': 'Create filter',
  'settings.filters.edit': 'Edit filter',
  'settings.filters.save': 'Save filter',
  'settings.filters.created': 'Filter created',
  'settings.filters.updated': 'Filter updated',
  'settings.filters.intro':
    'Filters run on incoming mail in order (lowest order first). AND combines multiple conditions. The raw Sieve editor in "Mail" settings works alongside these structured filters; both sources coexist without interfering.',
  'settings.filters.empty': 'No filters yet.',
  'settings.filters.unnamed': '(unnamed)',
  'settings.filters.summaryIf': 'If {conditions} → {actions}',
  'settings.filters.moveUp': 'Move filter up',
  'settings.filters.moveDown': 'Move filter down',
  'settings.filters.enabled': 'Enabled',
  'settings.filters.disabled': 'Disabled',
  'settings.filters.editBtn': 'Edit',
  'settings.filters.deleteBtn': 'Delete',
  'settings.filters.deleteAria': 'Delete filter',
  'settings.filters.confirmDelete': 'Delete this filter?',
  'settings.filters.nameLabel': 'Name',
  'settings.filters.nameOptional': '(optional)',
  'settings.filters.namePlaceholder': 'My filter',
  'settings.filters.enabledCheckbox': 'Enabled',
  'settings.filters.conditionsHeading': 'Conditions (AND combined)',
  'settings.filters.addCondition': 'Add condition',
  'settings.filters.noConditions': 'No conditions — matches all mail.',
  'settings.filters.actionsHeading': 'Actions',
  'settings.filters.addAction': 'Add action',
  'settings.filters.noActions': 'No actions — filter matches but does nothing.',
  'settings.filters.removeCondition': 'Remove condition',
  'settings.filters.removeAction': 'Remove action',
  'settings.filters.conditionField': 'Condition field',
  'settings.filters.conditionOperator': 'Condition operator',
  'settings.filters.conditionValue': 'Condition value',
  'settings.filters.actionKind': 'Action kind',
  'settings.filters.labelName': 'Label name',
  'settings.filters.forwardAddress': 'Forward address',
  'settings.filters.forwardPlaceholder': 'forward@example.com',
  'settings.filters.fromPlaceholder': 'user@example.com or *@example.com',
  'settings.filters.boolHasAttachment': '(boolean — matches messages with attachments)',
  'settings.filters.test': 'Test against existing mail',
  'settings.filters.testing': 'Testing…',
  'settings.filters.testResult': 'Matches {count} threads in your mailbox.',
  'settings.filters.testResultOne': 'Matches {count} thread in your mailbox.',
  'settings.filters.errMinCondition': 'At least one condition is required.',
  'settings.filters.errMinAction': 'At least one action is required.',
  'settings.filters.errDeleteApplyLabel':
    '"Move to Trash" and "Apply label" cannot be combined: the delete action short-circuits and the label is never applied.',
  'settings.filters.errEmptyValue': 'All condition values must be non-empty.',
  'settings.filters.errForwardRequired': 'Forward address is required.',
  'settings.filters.errLabelRequired': 'Label name is required for Apply label action.',
  'settings.filters.summaryHasAttachment': 'has attachment',
  'settings.filters.summaryNoConditions': '(no conditions)',
  'settings.filters.summaryNoActions': '(no actions)',
  'settings.filters.summaryApplyLabel': 'Apply label: {label}',
  'settings.filters.summaryForward': 'Forward to: {to}',
  'settings.filters.blockedHeading': 'Blocked senders',
  'settings.filters.blockedHint':
    'Messages from blocked senders are moved to Trash on arrival. To block a sender, use the "Block sender" option in the per-message menu.',
  'settings.filters.blockedEmpty': 'No senders blocked.',
  'settings.filters.unblock': 'Unblock',
  'settings.filters.field.from': 'From',
  'settings.filters.field.to': 'To',
  'settings.filters.field.subject': 'Subject',
  'settings.filters.field.fromDomain': 'From domain',
  'settings.filters.field.hasAttachment': 'Has attachment',
  'settings.filters.field.threadId': 'Thread ID',
  'settings.filters.op.contains': 'contains',
  'settings.filters.op.equals': 'equals',
  'settings.filters.op.wildcard': 'matches wildcard',
  'settings.filters.action.skipInbox': 'Skip inbox (archive on arrival)',
  'settings.filters.action.markRead': 'Mark as read',
  'settings.filters.action.applyLabel': 'Apply label',
  'settings.filters.action.delete': 'Move to Trash',
  'settings.filters.action.forward': 'Forward to address',

  // Sieve form (re #97)
  'settings.sieve.noAccount': 'No Mail account on this session',
  'settings.sieve.loadFailed': 'Failed to load Sieve script',
  'settings.sieve.saveFailedReason': 'Save failed: {reason}',
  'settings.sieve.saveFailed': 'Save failed',
  'settings.sieve.saved': 'Sieve script saved',
  'settings.sieve.intro':
    'Sieve scripts run server-side on incoming mail. Phase 1 ships a single active script per account; the editor below is the raw RFC 5228 / 9007 source. See {rfcLink} for the language.',
  'settings.sieve.rfcLinkLabel': 'RFC 5228',
  'settings.sieve.name': 'Name',
  'settings.sieve.namePlaceholder': 'default',
  'settings.sieve.script': 'Script',
  'settings.sieve.validationHeading': 'Sieve validation errors:',

  // ── Diagnostics settings (REQ-CLOG-06) ───────────────────────────────
  'settings.diagnostics.heading': 'Diagnostics',
  'settings.diagnostics.telemetry.label':
    'Send anonymous diagnostic logs to my mail-server operator',

  // ── Privacy settings ─────────────────────────────────────────────────
  'settings.privacy.avatarLookup.label':
    'Look up sender avatars from email metadata (Gravatar / X-Face / Face)',
  'settings.privacy.avatarLookup.hint':
    'When enabled, the suite contacts Gravatar with a one-way hash of each sender\'s email address to fetch their picture. Disable to keep all sender lookups local-only.',
  'settings.privacy.avatarLookup.confirmTitle': 'Look up sender pictures from the public web?',
  'settings.privacy.avatarLookup.confirmBody':
    "When enabled, the suite contacts Gravatar with a one-way hash of each sender's email address to fetch their picture. The sender does not see this lookup, but Gravatar's logs do. The sender's email never leaves your device in plaintext. You can turn this off any time.",
  'settings.privacy.avatarLookup.confirmEnable': 'Enable lookups',
  'settings.privacy.avatarLookup.confirmCancel': 'Keep local-only',

  // ── Recipient hover card (REQ-MAIL-46) ──────────────────────────────
  'contact.card.add': 'Add contact',
  'contact.card.edit': 'Edit contact',
  'contact.card.copy': 'Copy email address',
  'contact.card.copied': 'Copied',
  'contact.card.sendEmail': 'Send email',
  'contact.card.chat': 'Chat',
  'contact.card.video': 'Start video call',
  'contact.card.calendar': 'Schedule event',
  'contact.card.viewDetail': 'View detailed view',
  'contact.view.edit': 'Edit',
  'contact.view.save': 'Save',
  'contact.view.cancel': 'Cancel',
  'contact.view.name': 'Name',
  'contact.view.email': 'Email',
  'contact.view.saveSuccess': 'Contact saved',
  'contact.view.saveError': 'Could not save contact',
  'contact.phone.mobile': 'Mobile',
  'contact.phone.work': 'Work',
  'contact.phone.home': 'Home',
  'contact.phone.fax': 'Fax',
  'contact.phone.other': 'Other',

  // ── App switcher ────────────────────────────────────────────────────
  'app.mail': 'Mail',
  'app.calendar': 'Calendar',
  'app.contacts': 'Contacts',
  'app.chat': 'Chat',
  'app.admin': 'Server admin',
  'app.switch': 'Switch suite component',
  'shell.profile.menu': 'Account menu',

  // ── Time ────────────────────────────────────────────────────────────
  'time.justNow': 'just now',

  // ── Manual viewer (re #58) ───────────────────────────────────────────
  'sidebar.help': 'Help',
  'manual.loading': 'Loading help...',
  'manual.loadError': 'Could not load the manual.',
  'manual.empty': 'No help content available.',
  'manual.toc.label': 'Table of contents',
  'manual.onthispage.label': 'On this page',
  'manual.search.placeholder': 'Search topics...',
  'manual.search.label': 'Search manual',
  'manual.search.noResults': 'No matching topics.',
  'manual.callout.info': 'Note',
  'manual.callout.warning': 'Warning',
  'manual.callout.caution': 'Caution',
  'manual.req.label': 'Requirement',
  'manual.code.copy': 'Copy',
  'manual.code.copied': 'Copied',

  // ── Action toolbar overflow trigger ─────────────────────────────────
  // Used by ActionOverflowMenu in both the thread toolbar (re #60) and
  // the per-message header kebab (re #98).
  'actions.moreActions': 'More actions',

  // ── Common ──────────────────────────────────────────────────────────
  'common.cancel': 'Cancel',
  'common.confirm': 'Confirm',
  'common.save': 'Save',
  'common.saving': 'Saving...',
  'common.loading': 'Loading...',
  'common.retry': 'Retry',
  'common.revert': 'Revert',
  'common.remove': 'Remove',
  'common.create': 'Create',
  'common.copy': 'Copy',
  'common.copied': 'Copied',
  'common.close': 'Close',
  'common.back': 'Back',

  // ── Settings: section headings (re #97) ──────────────────────────────
  'settings.about': 'About',
  'settings.notifications': 'Notifications',
  'settings.apiKeys': 'API keys',
  'settings.privacy.heading': 'Privacy',
  'settings.categories.heading': 'Categories',
  'settings.filters.heading': 'Filters',

  // ── Settings: account section (re #97) ───────────────────────────────
  'settings.account.signedInAs': 'Signed in as',
  'settings.account.signOut': 'Sign out',
  'settings.account.identitiesHeading': 'Identities & signatures',
  'settings.account.noIdentities': 'No identities loaded yet.',
  'settings.account.authFailedTitle': 'Authentication failed — click to re-authenticate',
  'settings.account.unreachableTitle': 'External server unreachable — click to review config',
  'settings.account.authFailedBadge': 'Auth failed',
  'settings.account.unreachableBadge': 'Unreachable',
  'settings.account.externalBadge': 'External',
  'settings.account.externalBadgeTitle': 'External SMTP configured — click to edit',
  'settings.account.configureExternal': 'Configure external SMTP',
  'settings.account.configureExternalTitle': 'Configure external SMTP submission',
  'settings.account.extSubHint':
    'External SMTP submission (e.g. Gmail or Microsoft 365) is not enabled on this server. To allow routing outbound mail through an external provider, an operator can enable it in {systemToml} — see {docPath}.',

  // ── Settings: appearance section (re #97) ────────────────────────────
  'settings.appearance.themeHint':
    'System follows your OS-level preference and updates live when you toggle it.',

  // ── Settings: mail section (re #97) ──────────────────────────────────
  'settings.mail.undoSendWindow': 'Undo-send window',
  'settings.mail.undoSendOff': 'Off (sends immediately)',
  'settings.mail.undoSendValue': '{seconds}s',
  'settings.mail.undoSendAria': 'Seconds before send',
  'settings.mail.undoSendHint':
    "When set, sends are held server-side; the toast's Undo cancels delivery.",
  'settings.mail.swipeLeft': 'Swipe-left action',
  'settings.mail.swipeRight': 'Swipe-right action',
  'settings.mail.swipeTouch': '(touch)',
  'settings.mail.swipe.archive': 'Archive',
  'settings.mail.swipe.snooze': 'Snooze',
  'settings.mail.swipe.delete': 'Delete',
  'settings.mail.swipe.markRead': 'Mark read',
  'settings.mail.swipe.label': 'Label…',
  'settings.mail.swipe.none': 'None',
  'settings.mail.vacationHeading': 'Vacation auto-reply',
  'settings.mail.sieveHeading': 'Sieve filtering',
  'settings.mail.spamHeading': 'Spam classifier',
  'settings.mail.spamHint':
    "The prompt used when classifying your inbound mail as spam. Your messages are sent to herold's configured classifier endpoint along with this prompt.",
  'settings.mail.spamLoading': 'Loading…',
  'settings.mail.spamLoadError': 'Could not load',
  'settings.mail.spamModelLabel': 'Model',
  'settings.mail.spamNoPrompt': 'No spam prompt configured.',
  'settings.mail.coachHeading': 'Shortcut coach',
  'settings.mail.coachLabel': 'Show coach hints',

  // ── Settings: notifications section (re #97) ─────────────────────────
  'settings.notifications.soundsLabel': 'Notification sounds',
  'settings.notifications.soundsHint':
    'Play a sound when a new message or call arrives while this tab is open.',
  'settings.notifications.soundsAria': 'Notification sounds',
  // Desktop notification toggle (re #23).
  'settings.notifications.desktopLabel': 'Desktop notifications',
  'settings.notifications.desktopHint':
    'Show a desktop notification for new inbox messages when this tab is open and permission is granted.',
  'settings.notifications.desktopAria': 'Desktop notifications',
  'settings.notifications.pushLabel': 'Push notifications',
  'settings.notifications.pushDeniedHint':
    'Notifications are off. You can re-enable them in your browser settings.',
  'settings.notifications.pushForget': 'Forget my decision',
  'settings.notifications.pushOnHint': 'Notifications are on.',
  'settings.notifications.pushUpdating': 'Updating…',
  'settings.notifications.pushDisable': 'Disable notifications',
  'settings.notifications.pushOffHint':
    'Get notified about new mail and messages when this tab is closed.',
  // Clarification of what "push subscription" means (re #23c).
  'settings.notifications.pushSubscriptionHint':
    'Push notifications are delivered by your browser even when this tab is closed.',
  'settings.notifications.pushEnabling': 'Enabling…',
  'settings.notifications.pushEnable': 'Enable notifications',
  'settings.notifications.forgetAllLabel': 'Forget all subscriptions',
  'settings.notifications.forgetAllHint':
    'Removes all notification subscriptions for your account. Useful when decommissioning a device.',
  'settings.notifications.forgetAllButton': 'Forget all notification subscriptions',
  'settings.notifications.unavailable': 'Push notifications are not available on this server.',

  // ── Settings: privacy section (re #97) ───────────────────────────────
  'settings.privacy.externalImagesLabel': 'External images',
  'settings.privacy.externalImagesAria': 'External-image loading',
  'settings.privacy.externalImages.never': 'Never',
  'settings.privacy.externalImages.perSender': 'Per sender',
  'settings.privacy.externalImages.always': 'Always',
  'settings.privacy.externalImagesHint':
    "External images can act as read receipts. {neverEm} blocks them by default; {perSenderEm} only loads from senders you've allowed.",
  'settings.privacy.allowedSendersHeading': 'Allowed senders',
  'settings.privacy.allowedSendersEmpty':
    'No senders allowed yet. Use "Always from <sender>" in the reading pane to add one.',
  'settings.privacy.autocompleteHeading': 'Autocomplete history',
  'settings.privacy.seenAddressesLabel': 'Remember recently-used addresses',
  'settings.privacy.seenAddressesHint':
    'Herold keeps a per-account history of addresses you have corresponded with to supplement the recipient autocomplete. Turning this off purges the history immediately and stops new entries from being added.',
  // Image processing backlog status (REQ-EXTIMG-BG-32).
  'settings.privacy.imageProcessing.heading': 'Image processing',
  'settings.privacy.imageProcessing.pending':
    '{count} message is waiting for external images to be internalized.',
  'settings.privacy.imageProcessing.pending.other':
    '{count} messages are waiting for external images to be internalized.',
  'settings.privacy.imageProcessing.asOf': 'Counted {time}.',

  // ── Settings: about section (re #97) ─────────────────────────────────
  'settings.about.heroldVersion': 'Herold version',
  'settings.about.jmapApiUrl': 'JMAP API URL',
  'settings.about.eventSourceUrl': 'EventSource URL',
  'settings.about.sessionState': 'Session state',
  'settings.about.capabilitiesHeading': 'Server capabilities',
  'settings.about.noSession': 'No session.',

  // ── Settings: nav aria + body (re #97) ───────────────────────────────
  'settings.sectionsAria': 'Settings sections',

  // ── Identity edit forms (re #97) ─────────────────────────────────────
  'settings.identity.signatureLabel': 'Signature (plain text)',
  'settings.identity.signatureSaved': 'Signature saved',
  'settings.identity.signatureNoAccount': 'No Mail account on this session',

  // ── Vacation form (re #97) ──────────────────────────────────────────
  'settings.vacation.loadFailed': 'Failed to load vacation response',
  'settings.vacation.noAccount': 'No Mail account on this session',
  'settings.vacation.saveFailed': 'Save failed',
  'settings.vacation.saveFailedReason': 'Save failed: {reason}',
  'settings.vacation.enabled': 'Vacation auto-reply enabled',
  'settings.vacation.disabled': 'Vacation auto-reply disabled',
  'settings.vacation.autoReply': 'Auto-reply',
  'settings.vacation.activeFrom': 'Active from',
  'settings.vacation.activeFromHint': 'Leave blank to start immediately when enabled.',
  'settings.vacation.activeUntil': 'Active until',
  'settings.vacation.activeUntilHint': 'Leave blank for no end date.',
  'settings.vacation.subject': 'Subject',
  'settings.vacation.subjectPlaceholder': 'Out of office',
  'settings.vacation.body': 'Body',

  // ── Security form (re #97) ──────────────────────────────────────────
  'settings.security.loadingSession': 'Loading session...',
  'settings.security.loadingAria': 'Loading security settings',
  'settings.security.intro':
    'This page lets you manage the credentials and second-factor settings for your account. Use it to change your password or to enrol a time-based one-time password (TOTP) authenticator app for two-factor authentication.',
  'settings.security.introHint':
    'Two-factor authentication adds a second verification step at sign-in. Once enabled, your authenticator app will be required in addition to your password. Disabling it requires your current password to confirm.',
  'settings.security.changePassword': 'Change password',
  'settings.security.currentPassword': 'Current password',
  'settings.security.newPassword': 'New password',
  'settings.security.confirmNewPassword': 'Confirm new password',
  'settings.security.changePwSubmit': 'Change password',
  'settings.security.passwordMismatch': 'New passwords do not match.',
  'settings.security.passwordTooShort': 'New password must be at least 12 characters.',
  'settings.security.sessionNotReady': 'Session not ready. Please reload.',
  'settings.security.passwordChanged': 'Password changed.',
  'settings.security.currentPasswordWrong': 'Current password is wrong.',
  'settings.security.twoFactorHeading': 'Two-factor authentication',
  'settings.security.twoFactorEnabled': 'Two-factor authentication is enabled.',
  'settings.security.twoFactorDisabled': 'Two-factor authentication is not enabled.',
  'settings.security.disable2faLabel': 'Current password to disable 2FA',
  'settings.security.disabling': 'Disabling...',
  'settings.security.disable2fa': 'Disable 2FA',
  'settings.security.disable2faTitle': 'Disable two-factor authentication?',
  'settings.security.disable2faMessage': 'This reduces your account security.',
  'settings.security.disable2faConfirm': 'Disable',
  'settings.security.twoFactorDisabledToast': 'Two-factor authentication disabled.',
  'settings.security.starting': 'Starting...',
  'settings.security.enable2fa': 'Enable two-factor authentication',
  'settings.security.scanHint':
    'Scan the QR code with your authenticator app, then enter the 6-digit code to confirm.',
  'settings.security.qrAriaLabel': 'TOTP QR code',
  'settings.security.manualEntryKey': 'Manual entry key',
  'settings.security.totpSecretAria': 'TOTP secret key',
  'settings.security.provisioningUri': 'Provisioning URI',
  'settings.security.provisioningUriAria': 'TOTP provisioning URI',
  'settings.security.codePlaceholder': '6-digit code',
  'settings.security.codeAria': 'Authenticator code',
  'settings.security.confirming': 'Confirming...',
  'settings.security.confirmEnroll': 'Confirm',
  'settings.security.twoFactorEnabledToast': 'Two-factor authentication enabled.',

  // ── API keys form (re #97) ──────────────────────────────────────────
  'settings.apiKeys.intro1':
    'API keys let scripts and external programs authenticate against this account using {bearer}. They are optional -- nothing in the web suite needs one. Create a key only if you want to drive JMAP or the REST API from outside the browser.',
  'settings.apiKeys.intro2':
    'Each key carries a fixed scope: tighten it to the smallest set of permissions the script actually needs (for example, {mailSend} for an outbound bot). Keys appear only once at creation time -- copy them then; they cannot be retrieved later.',
  'settings.apiKeys.copyNow': 'Copy this key now. It will not be shown again.',
  'settings.apiKeys.newKeyAria': 'New API key',
  'settings.apiKeys.savedKey': 'I have saved this key',
  'settings.apiKeys.heading.new': 'New API key',
  'settings.apiKeys.label': 'Label',
  'settings.apiKeys.labelPlaceholder': 'e.g. my-script',
  'settings.apiKeys.scopes': 'Scopes',
  'settings.apiKeys.scopesHint':
    'Select the permissions this key grants. Leave empty for mail.send only (default).',
  'settings.apiKeys.creating': 'Creating...',
  'settings.apiKeys.create': 'Create key',
  'settings.apiKeys.createNew': 'Create new key',
  'settings.apiKeys.loadingAria': 'Loading API keys',
  'settings.apiKeys.empty': 'No API keys yet.',
  'settings.apiKeys.createdAt': 'Created {date}',
  'settings.apiKeys.lastUsed': 'Last used {date}',
  'settings.apiKeys.revoke': 'Revoke',
  'settings.apiKeys.revokeTitle': 'Revoke this API key?',
  'settings.apiKeys.revokeMessage': 'Any applications using it will stop working immediately.',
  'settings.apiKeys.revoked': 'API key revoked.',
  'settings.apiKeys.scope.endUser': 'End-user',
  'settings.apiKeys.scope.mailSend': 'Mail: send',
  'settings.apiKeys.scope.mailReceive': 'Mail: receive',
  'settings.apiKeys.scope.chatRead': 'Chat: read',
  'settings.apiKeys.scope.chatWrite': 'Chat: write',
  'settings.apiKeys.scope.calRead': 'Calendar: read',
  'settings.apiKeys.scope.calWrite': 'Calendar: write',
  'settings.apiKeys.scope.contactsRead': 'Contacts: read',
  'settings.apiKeys.scope.contactsWrite': 'Contacts: write',

  // ── Diagnostics form (re #97) ───────────────────────────────────────
  'settings.diagnostics.saveError': 'Could not save setting.',

  // ── Login (re #97) ──────────────────────────────────────────────────
  'login.email': 'Email address',
  'login.password': 'Password',
  'login.totpCode': 'Authenticator code',
  'login.totpPlaceholder': '6-digit code',
  'login.signingIn': 'Signing in...',
  'login.signIn': 'Sign in',
  'login.signInFailed': 'Sign-in failed.',

  // ── Chat view (re #97) ──────────────────────────────────────────────
  'chat.unavailable': 'Chat is not configured on this server',
  'chat.startVideoCall': 'Start video call',
  'chat.callButton': 'Call',
  'chat.unknownCaller': 'Unknown caller',
  'chat.selectConversation': 'Select a conversation to start chatting',

  // ── Chat surface (re #97) ───────────────────────────────────────────
  // ChatCompose
  'chat.composeAria': 'Chat compose',
  'chat.composeEditorAria': 'Message compose area',
  'chat.send': 'Send message',
  // MessageList
  'chat.reactions': 'Reactions',
  'chat.addReaction': 'Add reaction',
  'chat.newMessages': 'New messages',
  // VideoCall
  'chat.videoCall': 'Video call',
  'chat.remoteVideo': 'Remote video',
  'chat.yourCamera': 'Your camera',
  'chat.callControls': 'Call controls',
  'chat.hangUp': 'Hang up',
  // IncomingCall
  'chat.incomingVideoCall': 'Incoming video call',
  'chat.acceptCall': 'Accept call',
  'chat.declineCall': 'Decline call',
  // ChatOverlayHost
  'chat.windows': 'Chat windows',
  // SidebarChats
  'chat.loadingConversations': 'Loading conversations',
  'chat.discardChatWith': 'Discard chat with {name}',

  // ── Contacts view (re #97) ──────────────────────────────────────────
  'contact.view.loading': 'Loading…',
  'contact.view.couldNotLoad': 'Could not load contact.',
  'contact.view.title': 'Contact',
  'contact.view.emailHeading': 'Email',
  'contact.view.phoneHeading': 'Phone',

  // ── Settings → Tagged addresses (re #30, REQ-SET-TAG-01..21) ────────
  'settings.taggedAddresses': 'Tagged addresses',
  'settings.taggedAddresses.heading': 'Tagged addresses',
  'settings.taggedAddresses.intro':
    'Tagged addresses let you give out variants of your email — like alice+amazon@example.local — and auto-sort future replies into folders. When you open a message addressed to a tagged variant, the suite will offer to set up a filter.',
  'settings.taggedAddresses.unavailable':
    'This server does not advertise the tagged-addresses capability.',
  'settings.taggedAddresses.loading': 'Loading filters…',
  'settings.taggedAddresses.refresh': 'Refresh',
  'settings.taggedAddresses.filtersHeading': 'Filters',
  'settings.taggedAddresses.empty':
    'No tagged-address filters yet. Open a message sent to a tagged variant of your address to get started.',
  'settings.taggedAddresses.addFilter': 'Add filter',
  'settings.taggedAddresses.col.address': 'Address',
  'settings.taggedAddresses.col.action': 'Action',
  'settings.taggedAddresses.col.label': 'Label',
  'settings.taggedAddresses.col.created': 'Created',
  'settings.taggedAddresses.col.dismissedAt': 'Dismissed',
  'settings.taggedAddresses.col.actions': 'Actions',
  'settings.taggedAddresses.action.label': 'Label as {label}',
  'settings.taggedAddresses.action.labelArchive': 'Label as {label} + archive',
  'settings.taggedAddresses.action.labelArchiveRead': 'Label as {label} + archive + mark read',
  'settings.taggedAddresses.row.edit': 'Edit',
  'settings.taggedAddresses.row.delete': 'Delete',
  'settings.taggedAddresses.row.convert': 'Convert to Sieve',
  'settings.taggedAddresses.row.editAria': 'Edit filter for {address}',
  'settings.taggedAddresses.row.deleteAria': 'Delete filter for {address}',
  'settings.taggedAddresses.row.convertAria': 'Convert filter for {address} to Sieve',
  'settings.taggedAddresses.capNotice': '{used} of {cap} filter slots used',
  'settings.taggedAddresses.capReached':
    'You have reached the per-account limit of {cap} filters. Delete or convert an existing filter to add a new one.',
  'settings.taggedAddresses.dismissalsHeading': 'Dismissed suffixes',
  'settings.taggedAddresses.dismissalsIntro':
    'Tagged variants you chose to ignore from the per-message banner. Re-enable a row to be offered the filter prompt again.',
  'settings.taggedAddresses.dismissalsEmpty': 'No dismissed suffixes.',
  'settings.taggedAddresses.dismissalsCount': '{count} dismissed',
  'settings.taggedAddresses.dismissalsToggle': 'Show / hide dismissed suffixes',
  'settings.taggedAddresses.dismissalsCapNotice': '{used} of {cap} dismissal slots used',
  'settings.taggedAddresses.row.undismiss': 'Allow prompts again',
  'settings.taggedAddresses.row.undismissAria': 'Re-enable banner for {address}',
  'settings.taggedAddresses.dialog.titleAdd': 'Add tagged-address filter',
  'settings.taggedAddresses.dialog.titleEdit': 'Edit tagged-address filter',
  'settings.taggedAddresses.dialog.identity': 'Base identity',
  'settings.taggedAddresses.dialog.identityHint':
    'Mail flowing to {address} with a +suffix will be intercepted by this filter.',
  'settings.taggedAddresses.dialog.suffix': 'Suffix (after the +)',
  'settings.taggedAddresses.dialog.suffixHint':
    'Lower-case only, no @ or spaces. The server normalises the casing on save.',
  'settings.taggedAddresses.dialog.suffixPlaceholder': 'amazon',
  'settings.taggedAddresses.dialog.action': 'Action',
  'settings.taggedAddresses.dialog.actionLabel': 'Apply label',
  'settings.taggedAddresses.dialog.actionLabelArchive': 'Apply label and archive',
  'settings.taggedAddresses.dialog.actionLabelArchiveRead': 'Apply label, archive, and mark read',
  'settings.taggedAddresses.dialog.labelName': 'Label name',
  'settings.taggedAddresses.dialog.labelNameHint':
    'Mail matching this filter is filed into the given label.',
  'settings.taggedAddresses.dialog.labelPlaceholder': 'Shopping',
  'settings.taggedAddresses.dialog.cancel': 'Cancel',
  'settings.taggedAddresses.dialog.save': 'Save filter',
  'settings.taggedAddresses.dialog.create': 'Create filter',
  'settings.taggedAddresses.dialog.saving': 'Saving…',
  'settings.taggedAddresses.dialog.noVerifiedIdentities':
    'You have no verified identities. Verify one in the Account section before creating a tagged-address filter.',
  'settings.taggedAddresses.confirmDelete.title': 'Delete filter',
  'settings.taggedAddresses.confirmDelete.body':
    'Stop auto-sorting mail to {address}? Future mail will go to your inbox as normal. Mail already sorted is unaffected.',
  'settings.taggedAddresses.confirmDelete.confirm': 'Delete filter',
  'settings.taggedAddresses.confirmDelete.cancel': 'Keep filter',
  'settings.taggedAddresses.confirmConvert.title': 'Convert to Sieve',
  'settings.taggedAddresses.confirmConvert.body':
    'Convert the filter for {address} into a Sieve script fragment? This is one-way: the managed filter row disappears and an equivalent Sieve rule is added to your user script.',
  'settings.taggedAddresses.confirmConvert.confirm': 'Convert to Sieve',
  'settings.taggedAddresses.confirmConvert.cancel': 'Keep managed',
  'settings.taggedAddresses.toast.created': 'Filter created.',
  'settings.taggedAddresses.toast.updated': 'Filter updated.',
  'settings.taggedAddresses.toast.deleted': 'Filter deleted.',
  'settings.taggedAddresses.toast.converted':
    'Filter copied to your Sieve script. Edit it in Settings → Mail → Sieve.',
  'settings.taggedAddresses.toast.undismissed': 'Banner re-enabled for {address}.',
  'settings.taggedAddresses.toast.failed': 'Could not complete the action: {error}',
  'settings.taggedAddresses.error.suffixEmpty': 'Suffix must not be empty.',
  'settings.taggedAddresses.error.suffixTooLong': 'Suffix must be 64 bytes or fewer.',
  'settings.taggedAddresses.error.suffixWhitespace': 'Suffix must not contain whitespace.',
  'settings.taggedAddresses.error.suffixForbidden':
    'Suffix may not contain control bytes or @.',
  'settings.taggedAddresses.error.labelEmpty': 'Label name must not be empty.',
  'settings.taggedAddresses.error.labelTooLong': 'Label name must be 255 bytes or fewer.',
} as const;
