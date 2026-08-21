/**
 * Unit tests for share-link body insertion and removal helpers.
 * (REQ-ATT-63 / REQ-ATT-64)
 */

import { describe, it, expect } from 'vitest';
import {
  insertShareLinkIntoBody,
  removeShareLinkFromBody,
  _internals_forTest,
} from './compose.svelte';
import type { ComposeShare } from './compose.svelte';

const { escapeHtml } = _internals_forTest;

function makeShare(overrides?: Partial<ComposeShare>): ComposeShare {
  const url = overrides?.url ?? 'https://example.com/share/abc123';
  return {
    key: 'att-1',
    shareId: 'srv-share-1',
    name: 'report.zip',
    size: 5 * 1024 * 1024,
    type: 'application/zip',
    url,
    // pending_ttl-based; the block label is driven by expiresIn, not
    // this field (see the retention-labeling tests below).
    expiresAt: new Date(Date.now() + 48 * 3600 * 1000).toISOString(),
    // 30-day retention chosen at create.
    expiresIn: 30 * 24 * 3600,
    maxDownloads: null,
    hasPassword: false,
    ...overrides,
  };
}

describe('insertShareLinkIntoBody', () => {
  it('appends a share link block to an empty body', () => {
    const result = insertShareLinkIntoBody('', makeShare());
    expect(result).toContain('data-herold-share=');
    expect(result).toContain('report.zip');
    expect(result).toContain('https://example.com/share/abc123');
  });

  it('inserts before the signature delimiter when present', () => {
    const body = '<p>Hello world</p><p>-- </p><p>My Signature</p>';
    const result = insertShareLinkIntoBody(body, makeShare());
    // The link block should appear before the sig delimiter
    const sigPos = result.indexOf('<p>-- </p>');
    const linkPos = result.indexOf('data-herold-share=');
    expect(linkPos).toBeGreaterThanOrEqual(0);
    expect(linkPos).toBeLessThan(sigPos);
  });

  it('appends after body content when no sig delimiter', () => {
    const body = '<p>Hello world</p>';
    const result = insertShareLinkIntoBody(body, makeShare());
    expect(result.startsWith('<p>Hello world</p>')).toBe(true);
    expect(result).toContain('data-herold-share=');
  });

  it('HTML-escapes the filename in the block', () => {
    const share = makeShare({ name: '<script>alert(1)</script>.zip' });
    const result = insertShareLinkIntoBody('', share);
    expect(result).not.toContain('<script>');
    expect(result).toContain('&lt;script&gt;');
  });

  it('HTML-escapes the URL in the anchor', () => {
    const share = makeShare({ url: 'https://example.com/share/a"b' });
    const result = insertShareLinkIntoBody('', share);
    expect(result).not.toContain('"b"');
    expect(result).toContain('&quot;b');
  });

  it('labels the block with the chosen retention, not the pending expiry (re #290)', () => {
    // 30-day retention chosen at create, but the share is still pending
    // (expiresAt reflects pending_ttl, here 48h). The block must report
    // "30 days", not a value derived from the ~48h pending expiresAt.
    const share = makeShare({ expiresIn: 30 * 24 * 3600 });
    const result = insertShareLinkIntoBody('', share);
    expect(result).toContain('Expires in 30 days');
  });

  it('rounds a 48-hour retention to "2 days" (re #290)', () => {
    const share = makeShare({ expiresIn: 48 * 3600 });
    const result = insertShareLinkIntoBody('', share);
    expect(result).toContain('Expires in 2 days');
  });
});

describe('removeShareLinkFromBody', () => {
  it('removes a share link block that was inserted', () => {
    const share = makeShare();
    const withLink = insertShareLinkIntoBody('<p>Hello</p>', share);
    expect(withLink).toContain('data-herold-share=');

    const without = removeShareLinkFromBody(withLink, share.url);
    expect(without).not.toContain('data-herold-share=');
    expect(without).toContain('<p>Hello</p>');
  });

  it('returns the body unchanged when the URL is not present', () => {
    const body = '<p>Hello world</p>';
    const result = removeShareLinkFromBody(body, 'https://example.com/share/nothere');
    expect(result).toBe(body);
  });

  it('removes the correct block when multiple shares are present', () => {
    const shareA = makeShare({ key: 'att-1', shareId: 's1', url: 'https://example.com/share/aaa', name: 'a.zip' });
    const shareB = makeShare({ key: 'att-2', shareId: 's2', url: 'https://example.com/share/bbb', name: 'b.zip' });

    let body = '<p>Hello</p>';
    body = insertShareLinkIntoBody(body, shareA);
    body = insertShareLinkIntoBody(body, shareB);

    expect(body).toContain(shareA.url);
    expect(body).toContain(shareB.url);

    const afterRemoveA = removeShareLinkFromBody(body, shareA.url);
    expect(afterRemoveA).not.toContain(shareA.url);
    expect(afterRemoveA).toContain(shareB.url);
  });

  it('preserves the signature after removing a share link', () => {
    const body = '<p>Hello</p><p>-- </p><p>My Sig</p>';
    const share = makeShare();
    const withLink = insertShareLinkIntoBody(body, share);
    const without = removeShareLinkFromBody(withLink, share.url);
    expect(without).toContain('<p>-- </p>');
    expect(without).toContain('<p>My Sig</p>');
  });
});
