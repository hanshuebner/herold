/**
 * Message research spam-verdict rendering (re #326).
 *
 * The spam classification record's confidence is absent/out-of-range for
 * verdict "unclassified" (classifier-failure rows written since 5f960d0a).
 * The received-entry spam chip must never compute a raw percentage from
 * such a value ("-100%" from the historical -1 sentinel, "NaN%" once the
 * server omits the field) -- it renders the verdict chip plus the failure
 * reason instead. A genuine verdict with an in-range confidence still
 * renders its percentage.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, cleanup } from '@testing-library/svelte';

import MessageResearchView from './MessageResearchView.svelte';
import { messageResearch, type ReceivedHit } from '../lib/message-research/message-research.svelte';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function baseHit(overrides: Partial<ReceivedHit>): ReceivedHit {
  return {
    source: 'received',
    at: '2026-09-01T12:00:00Z',
    principal_id: 1,
    principal_email: 'alice@example.local',
    disposition: 'delivered_inbox',
    ingest_source: 'smtp',
    ingest_source_ref: '',
    mailboxes: [],
    mailbox_name: 'INBOX',
    is_junk: false,
    envelope: {
      from: 'sender@example.com',
      to: 'alice@example.local',
      cc: '',
      bcc: '',
      reply_to: '',
      message_id: '<abc@example.com>',
      in_reply_to: '',
      references: '',
    },
    ...overrides,
  };
}

function resetState(): void {
  messageResearch.status = 'idle';
  messageResearch.items = [];
  messageResearch.errorMessage = null;
  messageResearch.cursor = null;
  messageResearch.hasMore = false;
}

function stubLoad(items: ReceivedHit[]): void {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockImplementation((url: string) => {
      if ((url as string).includes('/api/v1/admin/message-research')) {
        return Promise.resolve(jsonResponse({ items, next: null }));
      }
      return Promise.resolve(new Response(null, { status: 404 }));
    }),
  );
}

describe('MessageResearchView spam verdict rendering (re #326)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    cleanup();
  });

  it('shows the verdict chip and reason, no percentage, for unclassified with a reason', async () => {
    resetState();
    stubLoad([
      baseHit({
        spam_verdict: 'unclassified',
        spam_confidence: undefined,
        spam_reason: 'plugin timed out after 5s',
      }),
    ]);

    render(MessageResearchView);

    expect(await screen.findByText('unclassified')).toBeInTheDocument();
    expect(screen.getByText('plugin timed out after 5s')).toBeInTheDocument();
    expect(screen.queryByText(/%/)).not.toBeInTheDocument();
  });

  it('shows the percentage for a ham verdict with a 0.05 confidence', async () => {
    resetState();
    stubLoad([
      baseHit({
        spam_verdict: 'ham',
        spam_confidence: 0.05,
      }),
    ]);

    render(MessageResearchView);

    expect(await screen.findByText('ham')).toBeInTheDocument();
    expect(screen.getByText('(5%)')).toBeInTheDocument();
  });

  it('suppresses the percentage when confidence is missing on a genuine verdict', async () => {
    resetState();
    stubLoad([
      baseHit({
        spam_verdict: 'spam',
        spam_confidence: undefined,
      }),
    ]);

    render(MessageResearchView);

    expect(await screen.findByText('spam')).toBeInTheDocument();
    expect(screen.queryByText(/%/)).not.toBeInTheDocument();
  });
});
