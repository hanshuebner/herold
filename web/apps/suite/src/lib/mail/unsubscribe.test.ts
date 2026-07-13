/**
 * Unit tests for the RFC 8058 one-click POST helper (REQ-UNS-20).
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { postOneClickUnsubscribe } from './unsubscribe';

describe('postOneClickUnsubscribe', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('POSTs the exact RFC 8058 body with the right content type, no credentials, no referrer', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true });
    vi.stubGlobal('fetch', fetchMock);

    const result = await postOneClickUnsubscribe('https://example.com/unsub?id=1');

    expect(result).toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledWith('https://example.com/unsub?id=1', {
      method: 'POST',
      credentials: 'omit',
      referrerPolicy: 'no-referrer',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: 'List-Unsubscribe=One-Click',
    });
  });

  it('reports ok:false on a non-2xx response', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false }));
    expect(await postOneClickUnsubscribe('https://example.com/unsub')).toEqual({ ok: false });
  });

  it('reports ok:false on a network error rather than throwing', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network down')));
    expect(await postOneClickUnsubscribe('https://example.com/unsub')).toEqual({ ok: false });
  });
});
