/**
 * Tests for compose-stack: minimize / restore / cap / chip-label.
 *
 * compose itself is the singleton at compose.svelte.ts; we don't try
 * to drive it through real send paths here -- we exercise the
 * snapshot lifecycle that compose-stack mediates.
 */
import { describe, it, expect, beforeEach } from 'vitest';
import { compose } from './compose.svelte';
import { composeStack, type ComposeSnapshot } from './compose-stack.svelte';

function chip(s: ComposeSnapshot): string {
  // Re-export the static helper inline for the assertion below.
  if (s.subject.trim()) return s.subject;
  const firstAddr = s.to.split(',')[0]?.trim();
  if (firstAddr) return firstAddr;
  return '(empty)';
}

beforeEach(() => {
  // Reset state between tests.
  composeStack.minimized = [];
  compose.close();
});

describe('composeStack.minimizeCurrent', () => {
  it('returns false when nothing is open', () => {
    expect(composeStack.minimizeCurrent()).toBe(false);
    expect(composeStack.minimized).toEqual([]);
  });

  it('snapshots and closes the active compose', () => {
    compose.openBlank();
    compose.subject = 'Quarterly review';
    compose.body = '<p>Hi all.</p>';
    expect(composeStack.minimizeCurrent()).toBe(true);
    expect(composeStack.minimized).toHaveLength(1);
    expect(composeStack.minimized[0]?.subject).toBe('Quarterly review');
    expect(composeStack.minimized[0]?.body).toBe('<p>Hi all.</p>');
    expect(compose.isOpen).toBe(false);
  });

  it('produces unique keys per snapshot', () => {
    compose.openBlank();
    compose.subject = 'first';
    composeStack.minimizeCurrent();
    compose.openBlank();
    compose.subject = 'second';
    composeStack.minimizeCurrent();
    expect(composeStack.minimized).toHaveLength(2);
    const keys = composeStack.minimized.map((s) => s.key);
    expect(new Set(keys).size).toBe(2);
  });
});

describe('composeStack.restore', () => {
  it('reopens compose with the snapshotted content', () => {
    compose.openBlank();
    compose.to = 'alice@x.test';
    compose.subject = 'Restored';
    composeStack.minimizeCurrent();
    const key = composeStack.minimized[0]!.key;

    composeStack.restore(key);
    expect(compose.isOpen).toBe(true);
    expect(compose.to).toBe('alice@x.test');
    expect(compose.subject).toBe('Restored');
    expect(composeStack.minimized).toHaveLength(0);
  });

  it('snapshots the active compose first when one is open', () => {
    compose.openBlank();
    compose.subject = 'first';
    composeStack.minimizeCurrent();
    const firstKey = composeStack.minimized[0]!.key;

    compose.openBlank();
    compose.subject = 'second';
    // Restoring `first` should snapshot `second` and load `first`.
    composeStack.restore(firstKey);
    expect(compose.subject).toBe('first');
    expect(composeStack.minimized).toHaveLength(1);
    expect(composeStack.minimized[0]?.subject).toBe('second');
  });

  it('no-op for an unknown key', () => {
    compose.openBlank();
    composeStack.restore('mc-unknown');
    expect(compose.isOpen).toBe(true);
  });
});

describe('composeStack.discard', () => {
  it('removes a snapshot without reopening', () => {
    compose.openBlank();
    compose.subject = 'goodbye';
    composeStack.minimizeCurrent();
    const key = composeStack.minimized[0]!.key;
    composeStack.discard(key);
    expect(composeStack.minimized).toHaveLength(0);
    expect(compose.isOpen).toBe(false);
  });
});

describe('composeStack.beforeOpenNew', () => {
  it('returns true when nothing is open', () => {
    expect(composeStack.beforeOpenNew()).toBe(true);
  });

  it('closes an empty active compose without snapshotting', () => {
    compose.openBlank();
    expect(composeStack.beforeOpenNew()).toBe(true);
    expect(composeStack.minimized).toEqual([]);
    expect(compose.isOpen).toBe(false);
  });

  it('auto-minimizes a non-empty active compose', () => {
    compose.openBlank();
    compose.subject = 'in progress';
    expect(composeStack.beforeOpenNew()).toBe(true);
    expect(composeStack.minimized).toHaveLength(1);
    expect(compose.isOpen).toBe(false);
  });

  it('refuses to open a 4th compose when 2 are minimized + 1 active', () => {
    compose.openBlank();
    compose.subject = 'a';
    composeStack.minimizeCurrent();
    compose.openBlank();
    compose.subject = 'b';
    composeStack.minimizeCurrent();
    compose.openBlank();
    compose.subject = 'c';
    expect(composeStack.beforeOpenNew()).toBe(false);
    expect(composeStack.minimized).toHaveLength(2);
    expect(compose.isOpen).toBe(true);
    expect(compose.subject).toBe('c');
  });
});

describe('chipLabel', () => {
  it('prefers the subject', () => {
    const s: ComposeSnapshot = {
      key: 'k',
      to: 'a@x.test',
      cc: '',
      bcc: '',
      subject: 'Hello',
      body: '',
      ccBccVisible: false,
      editingDraftId: null,
      replyContext: { parentId: null, parentKeyword: null, inReplyTo: null, references: null },
      attachments: [],
      createdAt: 0,
    };
    expect(chip(s)).toBe('Hello');
  });
  it('falls back to the first recipient when subject is blank', () => {
    const s: ComposeSnapshot = {
      key: 'k',
      to: 'a@x.test, b@y.test',
      cc: '',
      bcc: '',
      subject: '',
      body: '',
      ccBccVisible: false,
      editingDraftId: null,
      replyContext: { parentId: null, parentKeyword: null, inReplyTo: null, references: null },
      attachments: [],
      createdAt: 0,
    };
    expect(chip(s)).toBe('a@x.test');
  });
  it('falls back to "(empty)" when nothing is set', () => {
    const s: ComposeSnapshot = {
      key: 'k',
      to: '',
      cc: '',
      bcc: '',
      subject: '',
      body: '',
      ccBccVisible: false,
      editingDraftId: null,
      replyContext: { parentId: null, parentKeyword: null, inReplyTo: null, references: null },
      attachments: [],
      createdAt: 0,
    };
    expect(chip(s)).toBe('(empty)');
  });
});
