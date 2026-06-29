/**
 * Unit tests for the service worker notificationclick path resolver.
 *
 * sw.js lives in public/ and is not a module, so we cannot import it
 * directly.  We evaluate its source in a controlled context with the SW
 * global mocked to silence event registrations, then extract and call the
 * pure resolveNotificationPath function, which has no dependency on SW APIs.
 *
 * REQ-PUSH-70..73, REQ-MOB-74.
 */

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// Resolve the path to sw.js relative to this test file.
// test: src/lib/push/  →  ../../../public/sw.js
const __dir = dirname(fileURLToPath(import.meta.url));
const swSource = readFileSync(resolve(__dir, '../../../public/sw.js'), 'utf-8');

// Minimal SW global mock.  resolveNotificationPath itself makes no SW calls,
// but the module-level self.addEventListener() calls at the top of sw.js need
// something callable so evaluation does not throw.
const selfMock = {
  addEventListener: () => {},
  clients: {},
  registration: { scope: 'http://localhost/' },
  skipWaiting: () => {},
};

// Evaluate sw.js and return its internal functions under test.
// eslint-disable-next-line no-new-func
const factory = new Function(
  'self',
  `${swSource}\nreturn { resolveNotificationPath };`,
);
const { resolveNotificationPath } = factory(selfMock) as {
  resolveNotificationPath: (
    data: Record<string, unknown>,
    action: string,
  ) => string | null;
};

// ── Background JMAP actions (must return null — no window to open) ──────────

describe('resolveNotificationPath — background actions', () => {
  it('returns null for archive action on mail', () => {
    expect(
      resolveNotificationPath(
        { kind: 'mail', emailId: 'e1', inboxMailboxId: 'm1' },
        'archive',
      ),
    ).toBeNull();
  });

  it('returns null for mark_read action on mail', () => {
    expect(
      resolveNotificationPath({ kind: 'mail', emailId: 'e1' }, 'mark_read'),
    ).toBeNull();
  });

  it('returns null only for mail archive, not for other kinds', () => {
    // archive is a JMAP-mail-only background action; other kinds fall through
    expect(resolveNotificationPath({ kind: 'mail' }, 'archive')).toBeNull();
    // chat has no server-side archive — its click opens the conversation
    expect(resolveNotificationPath({ kind: 'chat', conversationId: 'c1' }, 'archive')).toBe(
      '/#/mail?openChat=c1',
    );
  });
});

// ── Mail body click ──────────────────────────────────────────────────────────

describe('resolveNotificationPath — mail body click', () => {
  it('returns /mail/thread/<threadId> when threadId is present', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', threadId: 'T1234', emailId: 'e1' },
      '',
    );
    expect(path).toBe('/mail/thread/T1234');
  });

  it('encodes special characters in threadId', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', threadId: 'T abc/+=' },
      '',
    );
    expect(path).toBe('/mail/thread/T%20abc%2F%2B%3D');
  });

  it('falls back to /mail when threadId is absent', () => {
    expect(resolveNotificationPath({ kind: 'mail', emailId: 'e1' }, '')).toBe(
      '/mail',
    );
  });
});

// ── Mail action buttons that open the compose window ────────────────────────

describe('resolveNotificationPath — mail open-window actions', () => {
  it('returns compose path for reply action', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', emailId: 'e42' },
      'reply',
    );
    expect(path).toBe('/mail/compose?inReplyTo=e42&quick=1');
  });

  it('returns compose path for retry_archive action', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', emailId: 'e42' },
      'retry_archive',
    );
    expect(path).toBe('/mail/compose?inReplyTo=e42&quick=1');
  });

  it('returns compose path for retry_read action', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', emailId: 'e42' },
      'retry_read',
    );
    expect(path).toBe('/mail/compose?inReplyTo=e42&quick=1');
  });

  it('encodes emailId in compose path', () => {
    const path = resolveNotificationPath(
      { kind: 'mail', emailId: 'e a+b' },
      'reply',
    );
    expect(path).toContain('inReplyTo=e%20a%2Bb');
  });

  it('uses empty string for missing emailId in compose path', () => {
    const path = resolveNotificationPath({ kind: 'mail' }, 'reply');
    expect(path).toBe('/mail/compose?inReplyTo=&quick=1');
  });
});

// ── Chat ─────────────────────────────────────────────────────────────────────

describe('resolveNotificationPath — chat', () => {
  it('returns openChat deep-link when conversationId is present', () => {
    const path = resolveNotificationPath(
      { kind: 'chat', conversationId: 'conv-1' },
      '',
    );
    expect(path).toBe('/#/mail?openChat=conv-1');
  });

  it('encodes special characters in conversationId', () => {
    const path = resolveNotificationPath(
      { kind: 'chat', conversationId: 'conv a+b' },
      '',
    );
    expect(path).toBe('/#/mail?openChat=conv%20a%2Bb');
  });

  it('falls back to /#/mail when conversationId is absent', () => {
    expect(resolveNotificationPath({ kind: 'chat' }, '')).toBe('/#/mail');
  });

  it('returns openChat deep-link for mark_read action on chat', () => {
    // mark_read on chat is not a background action — chat has no JMAP mark_read
    // handler, so the click opens the conversation overlay.
    const path = resolveNotificationPath(
      { kind: 'chat', conversationId: 'conv-1' },
      'mark_read',
    );
    expect(path).toBe('/#/mail?openChat=conv-1');
  });
});

// ── Calendar invite ──────────────────────────────────────────────────────────

describe('resolveNotificationPath — calendar-invite', () => {
  it('returns mail thread path for emailId', () => {
    const path = resolveNotificationPath(
      { kind: 'calendar-invite', emailId: 'e99' },
      '',
    );
    expect(path).toBe('/mail/thread/e99');
  });

  it('uses empty string for missing emailId', () => {
    const path = resolveNotificationPath({ kind: 'calendar-invite' }, '');
    expect(path).toBe('/mail/thread/');
  });

  it('returns mail thread path for accept action (handled in-app)', () => {
    const path = resolveNotificationPath(
      { kind: 'calendar-invite', emailId: 'e99' },
      'accept',
    );
    expect(path).toBe('/mail/thread/e99');
  });
});

// ── Call ─────────────────────────────────────────────────────────────────────

describe('resolveNotificationPath — call', () => {
  it('returns /chat/<conversationId> when conversationId is present', () => {
    const path = resolveNotificationPath(
      { kind: 'call', conversationId: 'room-7' },
      '',
    );
    expect(path).toBe('/chat/room-7');
  });

  it('falls back to / when conversationId is absent', () => {
    expect(resolveNotificationPath({ kind: 'call' }, '')).toBe('/');
  });
});

// ── Unknown / reaction ───────────────────────────────────────────────────────

describe('resolveNotificationPath — unknown kind', () => {
  it('returns / for unrecognised kind', () => {
    expect(resolveNotificationPath({ kind: 'reaction' }, '')).toBe('/');
  });

  it('returns / when data is empty', () => {
    expect(resolveNotificationPath({}, '')).toBe('/');
  });

  it('returns / for archive action on unknown kind (not a mail background action)', () => {
    // The background-action gate is mail-specific; unknown kind falls to default.
    expect(resolveNotificationPath({ kind: 'unknown' }, 'archive')).toBe('/');
  });
});
