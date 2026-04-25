// Gmail Interaction Logger — content.js
// Runs inside mail.google.com, captures structured interaction events.

const SESSION_ID = Date.now().toString(36);
const MAX_EVENTS = 5000;

// ── Taxonomy ─────────────────────────────────────────────────────────────────
// Gmail's DOM is heavily obfuscated. We identify elements by stable heuristics:
// ARIA labels, data-tooltip attributes, and known URL hash patterns.

const ACTION_MAP = [
  // Toolbar / global actions
  { selector: '[data-tooltip="Compose"]',           action: 'compose_new' },
  { selector: '[aria-label="Compose"]',              action: 'compose_new' },
  { selector: '[data-tooltip="Search mail"]',        action: 'search_focus' },
  { selector: 'input[aria-label="Search mail"]',     action: 'search_focus' },

  // Thread list actions
  { selector: '[data-tooltip="Archive"]',            action: 'archive' },
  { selector: '[data-tooltip="Delete"]',             action: 'delete' },
  { selector: '[data-tooltip="Mark as read"]',       action: 'mark_read' },
  { selector: '[data-tooltip="Mark as unread"]',     action: 'mark_unread' },
  { selector: '[data-tooltip="Snooze"]',             action: 'snooze' },
  { selector: '[data-tooltip="Move to"]',            action: 'move_to' },
  { selector: '[data-tooltip="Label as"]',           action: 'label_apply' },
  { selector: '[data-tooltip="More"]',               action: 'more_actions' },

  // Compose window
  { selector: '[data-tooltip="Send"]',               action: 'send' },
  { selector: '[aria-label="Send"]',                 action: 'send' },
  { selector: '[data-tooltip="Discard draft"]',      action: 'discard_draft' },
  { selector: '[data-tooltip="Formatting options"]', action: 'formatting_toggle' },
  { selector: '[data-tooltip="Attach files"]',       action: 'attach_file' },

  // Navigation / sidebar
  { selector: '[data-tooltip="Inbox"]',              action: 'nav_inbox' },
  { selector: '[data-tooltip="Starred"]',            action: 'nav_starred' },
  { selector: '[data-tooltip="Snoozed"]',            action: 'nav_snoozed' },
  { selector: '[data-tooltip="Sent"]',               action: 'nav_sent' },
  { selector: '[data-tooltip="Drafts"]',             action: 'nav_drafts' },
  { selector: '[data-tooltip="All Mail"]',           action: 'nav_all_mail' },
  { selector: '[data-tooltip="Spam"]',               action: 'nav_spam' },
  { selector: '[data-tooltip="Trash"]',              action: 'nav_trash' },

  // Thread reading
  { selector: '[data-tooltip="Reply"]',              action: 'reply' },
  { selector: '[data-tooltip="Reply all"]',          action: 'reply_all' },
  { selector: '[data-tooltip="Forward"]',            action: 'forward' },
  { selector: '[data-tooltip="Star"]',               action: 'star_toggle' },
  { selector: '[data-tooltip="Print all"]',          action: 'print' },
  { selector: '[data-tooltip="Open in new window"]', action: 'open_new_window' },
];

// Keyboard shortcut taxonomy
// Full list: https://support.google.com/mail/answer/6594
const KEY_MAP = {
  'c':        'compose_new',
  'C':        'compose_new_window',
  '/':        'search_focus',
  'r':        'reply',
  'a':        'reply_all',
  'f':        'forward',
  'e':        'archive',
  '#':        'delete',
  's':        'star_toggle',
  'x':        'select_thread',
  'n':        'next_message',
  'p':        'prev_message',
  'j':        'next_thread',
  'k':        'prev_thread',
  'o':        'open_thread',
  'Enter':    'open_thread',
  'u':        'back_to_list',
  'y':        'remove_label',
  'l':        'label_apply',
  'v':        'move_to',
  'Escape':   'dismiss',
  'I':        'mark_read',
  'U':        'mark_unread',
  'b':        'snooze',
  '!':        'report_spam',
  'Shift+r':  'reply_in_new_window',
  'Shift+a':  'reply_all_in_new_window',
  'Shift+f':  'forward_new_window',
  'Shift+n':  'update_conversation',
  'gi':       'nav_inbox',
  'gs':       'nav_starred',
  'gb':       'nav_snoozed',
  'gt':       'nav_sent',
  'gd':       'nav_drafts',
  'ga':       'nav_all_mail',
  'gl':       'nav_label',          // then type label name
  'gk':       'nav_tasks',
  '?':        'shortcut_help',
};

// ── URL / View Detection ──────────────────────────────────────────────────────
function parseGmailView(hash) {
  if (!hash) return { view: 'unknown' };
  const h = hash.replace(/^#/, '');
  if (h === 'inbox')             return { view: 'inbox' };
  if (h === 'starred')           return { view: 'starred' };
  if (h === 'snoozed')           return { view: 'snoozed' };
  if (h === 'sent')              return { view: 'sent' };
  if (h === 'drafts')            return { view: 'drafts' };
  if (h === 'all')               return { view: 'all_mail' };
  if (h === 'spam')              return { view: 'spam' };
  if (h === 'trash')             return { view: 'trash' };
  if (h.startsWith('search/'))   return { view: 'search', query: decodeURIComponent(h.slice(7)) };
  if (h.startsWith('label/'))    return { view: 'label', label: decodeURIComponent(h.slice(6)) };
  if (h.startsWith('thread/'))   return { view: 'thread', threadId: h.slice(7) };
  if (h.startsWith('compose/') || h === 'compose')
                                  return { view: 'compose' };
  if (h.startsWith('chat/'))     return { view: 'chat' };
  if (h.startsWith('dm/'))       return { view: 'chat_dm' };
  if (h.startsWith('space/'))    return { view: 'chat_space' };
  return { view: 'other', hash: h };
}

function currentView() {
  return parseGmailView(window.location.hash);
}

// ── Event Storage ─────────────────────────────────────────────────────────────
let buffer = [];
let flushTimer = null;

function pushEvent(evt) {
  buffer.push(evt);
  if (!flushTimer) {
    flushTimer = setTimeout(flush, 2000);
  }
}

function flush() {
  flushTimer = null;
  if (buffer.length === 0) return;
  const batch = buffer.splice(0);
  chrome.storage.local.get(['events'], (res) => {
    const existing = res.events || [];
    const merged = [...existing, ...batch].slice(-MAX_EVENTS);
    chrome.storage.local.set({ events: merged });
  });
}

// ── Logging Helpers ───────────────────────────────────────────────────────────
function log(action, extra = {}) {
  const evt = {
    ts: Date.now(),
    session: SESSION_ID,
    action,
    view: currentView(),
    ...extra,
  };
  pushEvent(evt);
  console.debug('[gmail-logger]', JSON.stringify(evt));
}

// ── Click Interception ────────────────────────────────────────────────────────
document.addEventListener('click', (e) => {
  const target = e.target;

  // Check action map
  for (const { selector, action } of ACTION_MAP) {
    if (target.closest(selector)) {
      log(action, { method: 'click' });
      return;
    }
  }

  // Detect label clicks in sidebar
  const labelLink = target.closest('a[href*="#label/"]');
  if (labelLink) {
    const m = labelLink.href.match(/#label\/([^&]+)/);
    log('nav_label', { label: m ? decodeURIComponent(m[1]) : null });
    return;
  }

  // Detect thread open (click on a row in the thread list)
  const threadRow = target.closest('tr.zA');
  if (threadRow) {
    const subjectEl = threadRow.querySelector('span.bog') || threadRow.querySelector('[data-thread-id]');
    log('open_thread', {
      method: 'click',
      hasSubject: !!subjectEl,
    });
    return;
  }

  // Detect snooze picker selections
  const snoozePicker = target.closest('[aria-label*="nooz"]') || target.closest('[data-snooze-datetime]');
  if (snoozePicker) {
    log('snooze_pick', {
      label: snoozePicker.getAttribute('aria-label') || snoozePicker.textContent.trim().slice(0, 40),
    });
    return;
  }

  // Detect label creation / apply in label dropdown
  const labelApply = target.closest('[aria-label*="label"]');
  if (labelApply) {
    log('label_interact', {
      label: labelApply.getAttribute('aria-label'),
      text: labelApply.textContent.trim().slice(0, 40),
    });
  }
}, true);

// ── Keyboard Interception ─────────────────────────────────────────────────────
let keySeq = '';        // for two-key sequences like 'gi', 'gs', 'gd'
let keySeqTimer = null;

document.addEventListener('keydown', (e) => {
  // Ignore if typing in an input / contenteditable
  const tag = document.activeElement?.tagName;
  const editable = document.activeElement?.isContentEditable;
  if (tag === 'INPUT' || tag === 'TEXTAREA' || editable) {
    if (e.key === 'Enter' && tag !== 'INPUT') {
      // Could be sending compose — handled by click listener on Send button
    }
    return;
  }

  const key = (e.shiftKey && e.key.length === 1 ? 'Shift+' : '') + e.key;

  // Two-key sequence
  if (keySeq) {
    const seq = keySeq + e.key;
    clearTimeout(keySeqTimer);
    keySeq = '';
    if (KEY_MAP[seq]) {
      log(KEY_MAP[seq], { method: 'keyboard', key: seq });
      return;
    }
  }

  if (e.key === 'g') {
    keySeq = 'g';
    keySeqTimer = setTimeout(() => { keySeq = ''; }, 1000);
    return;
  }

  if (KEY_MAP[key]) {
    log(KEY_MAP[key], { method: 'keyboard', key });
  }
}, true);

// ── URL / View Change Detection ───────────────────────────────────────────────
let lastHash = window.location.hash;

function onHashChange() {
  const newView = parseGmailView(window.location.hash);
  const oldView = parseGmailView(lastHash);
  lastHash = window.location.hash;
  if (newView.view !== oldView.view) {
    log('view_change', { from: oldView, to: newView });
  }
}

window.addEventListener('hashchange', onHashChange);

// Gmail is a SPA — also observe the history API
const origPushState = history.pushState.bind(history);
history.pushState = (...args) => {
  origPushState(...args);
  setTimeout(onHashChange, 100);
};

// ── Search Capture ────────────────────────────────────────────────────────────
const searchInput = document.querySelector('input[aria-label="Search mail"]');
if (searchInput) {
  searchInput.addEventListener('change', (e) => {
    log('search_submit', { query_length: e.target.value.length });
  });
}

// Observe DOM for dynamically added search inputs
const bodyObserver = new MutationObserver(() => {
  const si = document.querySelector('input[aria-label="Search mail"]');
  if (si && !si._loggerBound) {
    si._loggerBound = true;
    si.addEventListener('change', (e) => {
      log('search_submit', { query_length: e.target.value.length });
    });
  }
});
bodyObserver.observe(document.body, { childList: true, subtree: true });

// ── Session Start ─────────────────────────────────────────────────────────────
log('session_start', { url: location.href });
window.addEventListener('beforeunload', () => {
  log('session_end');
  flush();
});

// Flush on visibility change (tab switch)
document.addEventListener('visibilitychange', () => {
  if (document.hidden) flush();
});
