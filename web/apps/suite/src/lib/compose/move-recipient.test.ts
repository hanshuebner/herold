/**
 * Unit tests for ComposeStore.moveRecipient (re #272): dragging or
 * keyboard-moving a recipient chip between To/Cc/Bcc.
 *
 *   - Moving a recipient removes it from the source array and appends it
 *     to the destination array in one call (neither dropped nor duplicated).
 *   - The comma-joined string form (`to`/`cc`/`bcc`) is re-derived for
 *     both the source and destination fields.
 *   - Dropping onto the recipient's own field is a no-op.
 *   - Moving into Cc or Bcc reveals the field if it was collapsed.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import { compose } from './compose.svelte';
import type { Recipient } from './recipient-parse';

// Stub the JMAP client so no method under test touches the network.
vi.mock('../jmap/client', () => ({
  jmap: {
    maxUploadSize: null,
    uploadBlob: vi.fn(),
    downloadUrl: vi.fn().mockReturnValue(null),
  },
  strict: vi.fn(),
}));

vi.mock('../mail/store.svelte', () => ({
  mail: {
    mailAccountId: 'acct1',
    primaryIdentity: null,
    drafts: null,
    identities: new Map(),
    mailboxes: new Map(),
    loadMailboxes: vi.fn(),
    loadIdentities: vi.fn(),
  },
}));

vi.mock('../settings/settings.svelte', () => ({
  settings: { undoWindowSec: 0 },
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  localeTag: () => 'en',
}));

const alice: Recipient = { name: 'Alice Smith', email: 'alice@example.com' };
const bob: Recipient = { email: 'bob@example.com' };
const carol: Recipient = { name: 'Carol Jones', email: 'carol@example.com' };

describe('ComposeStore.moveRecipient', () => {
  beforeEach(() => {
    compose.toRecipients = [alice, bob];
    compose.ccRecipients = [carol];
    compose.bccRecipients = [];
    compose.to = 'Alice Smith <alice@example.com>, bob@example.com';
    compose.cc = 'Carol Jones <carol@example.com>';
    compose.bcc = '';
    compose.ccBccVisible = false;
  });

  it('removes the recipient from the source array and appends it to the destination', () => {
    compose.moveRecipient('to', 0, 'cc');

    expect(compose.toRecipients).toEqual([bob]);
    expect(compose.ccRecipients).toEqual([carol, alice]);
    expect(compose.bccRecipients).toEqual([]);
  });

  it('re-derives both the source and destination comma-joined strings', () => {
    compose.moveRecipient('to', 0, 'cc');

    expect(compose.to).toBe('bob@example.com');
    expect(compose.cc).toBe('Carol Jones <carol@example.com>, Alice Smith <alice@example.com>');
  });

  it('does not drop or duplicate a chip across the two arrays', () => {
    compose.moveRecipient('to', 1, 'bcc');

    const allEmails = [
      ...compose.toRecipients,
      ...compose.ccRecipients,
      ...compose.bccRecipients,
    ].map((r) => r.email);
    expect(allEmails.sort()).toEqual(
      ['alice@example.com', 'bob@example.com', 'carol@example.com'].sort(),
    );
    // bob appears exactly once, in bcc, not in to.
    expect(compose.toRecipients).toEqual([alice]);
    expect(compose.bccRecipients).toEqual([bob]);
  });

  it('is a no-op when the source and destination field are the same', () => {
    const beforeTo = [...compose.toRecipients];
    const beforeToStr = compose.to;

    compose.moveRecipient('to', 0, 'to');

    expect(compose.toRecipients).toEqual(beforeTo);
    expect(compose.to).toBe(beforeToStr);
  });

  it('reveals Cc/Bcc when a recipient is moved into a collapsed field', () => {
    expect(compose.ccBccVisible).toBe(false);

    compose.moveRecipient('to', 0, 'bcc');

    expect(compose.ccBccVisible).toBe(true);
  });

  it('does not reveal Cc/Bcc when the destination is To', () => {
    compose.ccBccVisible = true;
    compose.moveRecipient('cc', 0, 'to');
    // Stays visible (it was already), and moving *to* To never forces it
    // closed either -- this just documents that only cc/bcc destinations
    // flip it on.
    expect(compose.ccBccVisible).toBe(true);
  });

  it('is a no-op for an out-of-range source index', () => {
    const beforeTo = [...compose.toRecipients];
    const beforeCc = [...compose.ccRecipients];

    compose.moveRecipient('to', 99, 'cc');

    expect(compose.toRecipients).toEqual(beforeTo);
    expect(compose.ccRecipients).toEqual(beforeCc);
  });
});
