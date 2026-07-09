/**
 * Unit tests for ThreadReplyBar's Reply-All visibility gate (re #163).
 *
 * hasMultipleRecipients must filter To/Cc through the user's registered
 * identities before testing whether any recipients remain — mirroring
 * the own-email filter computeReplyAllCc applies in compose.svelte.ts.
 * Without the filter, Reply All appears even when every extra To/Cc
 * entry is the user's own identity, and computeReplyAllCc then strips
 * them all, making Reply All produce an empty Cc identical to Reply.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import ThreadReplyBar from './ThreadReplyBar.svelte';
import type { Email, Identity } from './types';

// ── hoisted fixtures ───────────────────────────────────────────────────────────

const { mailMock, composeMock } = vi.hoisted(() => {
  const mailMock = {
    identities: new Map<string, Identity>(),
  };
  const composeMock = {
    isOpen: false,
    inlineMode: false,
    openReply: vi.fn().mockResolvedValue(undefined),
    openReplyAll: vi.fn(),
    openForward: vi.fn(),
  };
  return { mailMock, composeMock };
});

vi.mock('./store.svelte', () => ({ mail: mailMock }));
vi.mock('../compose/compose.svelte', () => ({ compose: composeMock }));
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));
vi.mock('../icons/ReplyIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/ReplyAllIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/ForwardIcon.svelte', () => ({ default: () => null }));

// ── helpers ────────────────────────────────────────────────────────────────────

function makeIdentity(email: string): Identity {
  return {
    id: `id-${email}`,
    name: '',
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
  };
}

function makeEmail(overrides: Partial<Email>): Email {
  return {
    id: 'e-1',
    threadId: 'tid-1',
    mailboxIds: { 'mbx-inbox': true },
    keywords: {},
    from: [{ name: 'Sender', email: 'sender@example.test' }],
    to: null,
    cc: null,
    subject: 'Test',
    preview: 'preview',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    blobId: 'blob-1',
    'header:List-ID:asText': null,
    ...overrides,
  };
}

describe('ThreadReplyBar Reply-All visibility (re #163)', () => {
  beforeEach(() => {
    mailMock.identities = new Map([
      ['id-alice@example.local', makeIdentity('alice@example.local')],
    ]);
    composeMock.isOpen = false;
    composeMock.inlineMode = false;
  });

  it('suppresses Reply All when every extra To/Cc entry is the user\'s own identity', () => {
    const email = makeEmail({
      to: [{ name: 'Alice', email: 'alice@example.local' }],
      cc: [{ name: 'Alice', email: 'alice@example.local' }],
    });

    render(ThreadReplyBar, { props: { target: email } });

    expect(screen.getByRole('button', { name: 'msg.reply' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'msg.replyAll' })).not.toBeInTheDocument();
  });

  it('shows Reply All when at least one To/Cc entry is outside the identity set', () => {
    const email = makeEmail({
      to: [{ name: 'Alice', email: 'alice@example.local' }],
      cc: [{ name: 'External', email: 'external@example.test' }],
    });

    render(ThreadReplyBar, { props: { target: email } });

    expect(screen.getByRole('button', { name: 'msg.replyAll' })).toBeInTheDocument();
  });

  it('shows Reply All when To has an external second recipient and no Cc', () => {
    const email = makeEmail({
      to: [
        { name: 'Alice', email: 'alice@example.local' },
        { name: 'External', email: 'external@example.test' },
      ],
      cc: null,
    });

    render(ThreadReplyBar, { props: { target: email } });

    expect(screen.getByRole('button', { name: 'msg.replyAll' })).toBeInTheDocument();
  });

  it('suppresses Reply All when To has only the single own-identity recipient and no Cc', () => {
    const email = makeEmail({
      to: [{ name: 'Alice', email: 'alice@example.local' }],
      cc: null,
    });

    render(ThreadReplyBar, { props: { target: email } });

    expect(screen.queryByRole('button', { name: 'msg.replyAll' })).not.toBeInTheDocument();
  });
});
