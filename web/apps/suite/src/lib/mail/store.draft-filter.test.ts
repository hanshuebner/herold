/**
 * Tests for the $draft keyword filter in threadEmails() (re #129).
 *
 * When the inline reply composer auto-saves a draft, the JMAP server puts
 * the draft's email ID in the thread's emailIds (via In-Reply-To). The store
 * then adds it to committedThreadEmailIds via #processFreshArrivals (the
 * draft is from the signed-in user so it is classified as self-sent and
 * committed immediately). Without the filter, ThreadReader would render the
 * draft as a MessageAccordion next to the open inline composer — duplicating
 * its content and confusing the user.
 *
 * The fix: threadEmails() filters out emails whose keywords.$draft is truthy
 * from the committed snapshot before passing to resolveDeduplicatedThreadEmails.
 * The filter is read-time only — the draft ID stays in committedThreadEmailIds
 * so that when the server removes $draft after submission, the now-sent email
 * appears immediately.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Email } from './types';

// Minimal mocks so the store module can be imported.
vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn() },
}));
vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn(), session: null, uploadBlob: vi.fn(), downloadUrl: vi.fn() },
  strict: (r: unknown[]) => r,
}));
vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'acc1',
        'urn:ietf:params:jmap:submission': 'acc1',
      },
    },
    principalId: 'p1',
  },
  registerAccountResetCallback: vi.fn(),
}));
vi.mock('../toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));
vi.mock('../notifications/sounds.svelte', () => ({ sounds: { play: vi.fn() } }));
vi.mock('../notifications/cue-gates', () => ({ shouldPlayMailCue: () => false }));
vi.mock('../router/router.svelte', () => ({ router: { parts: [], getParam: () => null } }));
vi.mock('../i18n/i18n.svelte', () => ({ i18n: { t: (k: string) => k }, localeTag: () => 'en' }));
vi.mock('../settings/settings.svelte', () => ({ settings: { desktopNotifEnabled: false } }));
vi.mock('../debug-ring/debug-ring', () => ({ appendEvent: vi.fn() }));

function makeEmail(id: string, opts: { draft?: boolean; mailboxIds?: Record<string, true> } = {}): Email {
  return {
    id,
    threadId: 'thread-1',
    mailboxIds: opts.mailboxIds ?? { 'mbx-inbox': true },
    keywords: opts.draft ? { $draft: true } : {},
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    subject: 'Test',
    preview: '',
    receivedAt: '2026-07-05T10:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    'header:List-ID:asText': null,
  } as unknown as Email;
}

describe('threadEmails() — draft filter (re #129)', () => {
  // Use ReturnType so we avoid referencing the non-exported MailStore class directly.
  let mail: Awaited<ReturnType<typeof import('./store.svelte')['mail']['reset']>> extends void
    ? (typeof import('./store.svelte'))['mail']
    : never;

  beforeEach(async () => {
    const mod = await import('./store.svelte');
    mail = mod.mail as typeof mail;
    mail.reset();
  });

  it('excludes a $draft email from the committed snapshot', () => {
    const real = makeEmail('e-sent');
    const draft = makeEmail('e-draft', { draft: true });

    mail.emails = new Map([
      ['e-sent', real],
      ['e-draft', draft],
    ]);
    mail.committedThreadEmailIds = new Map([
      ['thread-1', ['e-sent', 'e-draft']],
    ]);

    const result = mail.threadEmails('thread-1');
    expect(result.map((e: Email) => e.id)).toEqual(['e-sent']);
    expect(result.find((e: Email) => e.id === 'e-draft')).toBeUndefined();
  });

  it('returns all non-draft emails unchanged', () => {
    const e1 = makeEmail('e1');
    const e2 = makeEmail('e2');
    const e3 = makeEmail('e3');

    mail.emails = new Map([
      ['e1', e1],
      ['e2', e2],
      ['e3', e3],
    ]);
    mail.committedThreadEmailIds = new Map([['thread-1', ['e1', 'e2', 'e3']]]);

    const result = mail.threadEmails('thread-1');
    expect(result.map((e: Email) => e.id)).toEqual(['e1', 'e2', 'e3']);
  });

  it('passes through a formerly-draft email once $draft is removed (simulates send)', () => {
    // When the server confirms submission, it removes $draft from the email.
    // The now-sent email must appear in the thread immediately.
    const sent = makeEmail('e-sent', { draft: false });

    mail.emails = new Map([['e-sent', sent]]);
    mail.committedThreadEmailIds = new Map([['thread-1', ['e-sent']]]);

    const result = mail.threadEmails('thread-1');
    expect(result.map((e: Email) => e.id)).toEqual(['e-sent']);
  });

  it('omits unknown-cache IDs (conservative pass-through delegates to resolveDeduplicatedThreadEmails)', () => {
    // If an email is absent from the cache, the draft filter cannot know
    // whether it is a draft, so it passes the ID through. The downstream
    // resolveDeduplicatedThreadEmails() then skips IDs not in the cache,
    // so the result is empty — not undefined / error.
    mail.emails = new Map();
    mail.committedThreadEmailIds = new Map([['thread-1', ['e-unknown']]]);

    const result = mail.threadEmails('thread-1');
    expect(result).toHaveLength(0);
  });
});
