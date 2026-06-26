/**
 * Tests for the truncated-HTML-body recovery helpers (Forgejo #48).
 *
 * Covers:
 *   - htmlBodyIsTruncated: truncation detection from bodyValues
 *   - htmlBodyFullDownloadUrl: URL building with truncation guard
 *   - fetchFullHtmlBody: same-origin authenticated fetch, throws on error
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
  htmlBodyIsTruncated,
  htmlBodyFullDownloadUrl,
  fetchFullHtmlBody,
} from './html-body-full';
import type { Email } from './types';

// ── helpers ───────────────────────────────────────────────────────────────────

function makeEmail(overrides: Partial<Email> = {}): Email {
  return {
    id: 'e1',
    threadId: 't1',
    mailboxIds: {},
    keywords: {},
    from: null,
    to: null,
    subject: null,
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: 'root-blob',
    ...overrides,
  } as Email;
}

/** Make an email with one html body part and a matching bodyValue. */
function makeEmailWithHtmlPart(opts: {
  isTruncated?: boolean;
  blobId?: string | null;
  partId?: string;
  hasBodyValue?: boolean;
  /** Override Part.size explicitly (bytes). Default: 1048577 when isTruncated, 512 otherwise. */
  partSize?: number;
}): Email {
  const partId = opts.partId ?? 'p1';
  // Use explicit undefined check so a deliberate null is preserved.
  const blobId = opts.blobId !== undefined ? opts.blobId : 'html-blob-id';
  const isTruncated = opts.isTruncated ?? false;
  const hasBodyValue = opts.hasBodyValue ?? true;
  // Default part size: one byte over the 1 MiB cap for truncated parts so
  // the size-based fallback fires; well under the cap for non-truncated parts.
  const partSize = opts.partSize ?? (isTruncated ? 1_048_577 : 512);
  return makeEmail({
    htmlBody: [
      {
        partId,
        blobId,
        size: partSize,
        type: 'text/html',
        charset: 'utf-8',
        disposition: null,
        name: null,
        cid: null,
      },
    ],
    bodyValues: hasBodyValue
      ? {
          [partId]: {
            value: '<p>hello</p>',
            isEncodingProblem: false,
            isTruncated,
          },
        }
      : undefined,
  });
}

/** Minimal mock for jmap.downloadUrl. */
function makeDownloadUrl(
  opts: { returnNull?: boolean } = {},
): (args: { accountId: string; blobId: string; type: string; name: string }) => string | null {
  return (args) => {
    if (opts.returnNull) return null;
    return `/jmap/download/${args.accountId}/${args.blobId}/${args.type}/${args.name}`;
  };
}

// ── htmlBodyIsTruncated ───────────────────────────────────────────────────────

describe('htmlBodyIsTruncated', () => {
  it('returns false when email has no htmlBody', () => {
    const email = makeEmail({ htmlBody: undefined, bodyValues: undefined });
    expect(htmlBodyIsTruncated(email)).toBe(false);
  });

  it('returns false when htmlBody is empty', () => {
    const email = makeEmail({ htmlBody: [], bodyValues: {} });
    expect(htmlBodyIsTruncated(email)).toBe(false);
  });

  it('returns false when the html part has no partId', () => {
    const email = makeEmail({
      htmlBody: [
        {
          partId: null,
          blobId: 'b1',
          size: 100,
          type: 'text/html',
          charset: null,
          disposition: null,
          name: null,
          cid: null,
        },
      ],
      bodyValues: {},
    });
    expect(htmlBodyIsTruncated(email)).toBe(false);
  });

  it('returns false when bodyValues is absent', () => {
    const email = makeEmailWithHtmlPart({ hasBodyValue: false });
    expect(htmlBodyIsTruncated(email)).toBe(false);
  });

  it('returns false when bodyValue.isTruncated is false', () => {
    const email = makeEmailWithHtmlPart({ isTruncated: false });
    expect(htmlBodyIsTruncated(email)).toBe(false);
  });

  it('returns true when bodyValue.isTruncated is true', () => {
    const email = makeEmailWithHtmlPart({ isTruncated: true });
    expect(htmlBodyIsTruncated(email)).toBe(true);
  });

  it('returns true when part.size exceeds the server cap even if isTruncated is false', () => {
    // The server may omit isTruncated when the truncation comes from the
    // internal parser cap rather than a client-specified maxBodyValueBytes.
    const email = makeEmailWithHtmlPart({ isTruncated: false, partSize: 6_402_993 });
    expect(htmlBodyIsTruncated(email)).toBe(true);
  });

  it('returns true when part.size is one byte over the server cap', () => {
    const email = makeEmailWithHtmlPart({ isTruncated: false, partSize: 1_048_577 });
    expect(htmlBodyIsTruncated(email)).toBe(true);
  });

  it('returns false when part.size equals the server cap exactly', () => {
    // A part exactly at the cap is not truncated (the cap is exclusive).
    const email = makeEmailWithHtmlPart({ isTruncated: false, partSize: 1_048_576 });
    expect(htmlBodyIsTruncated(email)).toBe(false);
  });

  it('returns false when part.size is small even with isTruncated false', () => {
    const email = makeEmailWithHtmlPart({ isTruncated: false, partSize: 512 });
    expect(htmlBodyIsTruncated(email)).toBe(false);
  });
});

// ── htmlBodyFullDownloadUrl ───────────────────────────────────────────────────

describe('htmlBodyFullDownloadUrl', () => {
  it('returns null when the body is not truncated', () => {
    const email = makeEmailWithHtmlPart({ isTruncated: false });
    const url = htmlBodyFullDownloadUrl(email, 'acct1', makeDownloadUrl());
    expect(url).toBeNull();
  });

  it('returns null when email has no htmlBody', () => {
    const email = makeEmail({ htmlBody: undefined });
    const url = htmlBodyFullDownloadUrl(email, 'acct1', makeDownloadUrl());
    expect(url).toBeNull();
  });

  it('returns null when the html part has no blobId', () => {
    const email = makeEmailWithHtmlPart({ isTruncated: true, blobId: null });
    const url = htmlBodyFullDownloadUrl(email, 'acct1', makeDownloadUrl());
    expect(url).toBeNull();
  });

  it('returns null when the downloadUrl factory returns null (no session)', () => {
    const email = makeEmailWithHtmlPart({ isTruncated: true });
    const url = htmlBodyFullDownloadUrl(email, 'acct1', makeDownloadUrl({ returnNull: true }));
    expect(url).toBeNull();
  });

  it('returns the download URL with the correct blobId and type for a truncated body', () => {
    const email = makeEmailWithHtmlPart({ isTruncated: true, blobId: 'html-blob-42' });
    const url = htmlBodyFullDownloadUrl(email, 'acct1', makeDownloadUrl());
    expect(url).toBe('/jmap/download/acct1/html-blob-42/text/html/body.html');
  });

  it('passes accountId to the downloadUrl factory', () => {
    const email = makeEmailWithHtmlPart({ isTruncated: true, blobId: 'b99' });
    const captured: { accountId: string }[] = [];
    const factory = (args: { accountId: string; blobId: string; type: string; name: string }) => {
      captured.push(args);
      return `/jmap/download/${args.accountId}/${args.blobId}`;
    };
    htmlBodyFullDownloadUrl(email, 'acct-xyz', factory);
    expect(captured[0]?.accountId).toBe('acct-xyz');
  });
});

// ── fetchFullHtmlBody ─────────────────────────────────────────────────────────

describe('fetchFullHtmlBody', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('returns the response text on a 200 OK response', async () => {
    const body = '<html><body><p>Full content</p></body></html>';
    vi.mocked(fetch).mockResolvedValue({
      ok: true,
      status: 200,
      text: () => Promise.resolve(body),
    } as unknown as Response);

    const result = await fetchFullHtmlBody('/jmap/download/acct1/b1/text%2Fhtml/body.html');
    expect(result).toBe(body);
  });

  it('passes credentials: include on the fetch request', async () => {
    vi.mocked(fetch).mockResolvedValue({
      ok: true,
      status: 200,
      text: () => Promise.resolve(''),
    } as unknown as Response);

    await fetchFullHtmlBody('/jmap/download/acct1/b1/text%2Fhtml/body.html');
    expect(vi.mocked(fetch)).toHaveBeenCalledWith(
      '/jmap/download/acct1/b1/text%2Fhtml/body.html',
      { credentials: 'include' },
    );
  });

  it('throws on an HTTP 404 response', async () => {
    vi.mocked(fetch).mockResolvedValue({
      ok: false,
      status: 404,
    } as unknown as Response);

    await expect(
      fetchFullHtmlBody('/jmap/download/acct1/missing/text%2Fhtml/body.html'),
    ).rejects.toThrow('HTTP 404');
  });

  it('throws on an HTTP 500 response', async () => {
    vi.mocked(fetch).mockResolvedValue({
      ok: false,
      status: 500,
    } as unknown as Response);

    await expect(
      fetchFullHtmlBody('/jmap/download/acct1/b1/text%2Fhtml/body.html'),
    ).rejects.toThrow('HTTP 500');
  });

  it('propagates network-level fetch rejections', async () => {
    vi.mocked(fetch).mockRejectedValue(new TypeError('network failure'));

    await expect(
      fetchFullHtmlBody('/jmap/download/acct1/b1/text%2Fhtml/body.html'),
    ).rejects.toThrow('network failure');
  });
});
