/**
 * Unit tests for testSubmission() in identity-submission.ts.
 *
 * re #113, re #115: on-demand SMTP probe endpoint.
 *
 * The function always resolves (never throws):
 *  - HTTP 200 {ok:true} -> TestSubmissionResult with ok=true
 *  - HTTP 200 {ok:false, detail} -> TestSubmissionResult with ok=false
 *  - HTTP 4xx/5xx problem+json -> TestSubmissionResult with ok=false + detail
 *  - Network error -> TestSubmissionResult with ok=false + message
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { testSubmission } from './identity-submission';
import { readCsrfToken } from './client';

// Prevent the CSRF-token reader from blowing up in jsdom (no document.cookie).
vi.mock('./client', async (importOriginal) => {
  const mod = await importOriginal<typeof import('./client')>();
  return {
    ...mod,
    readCsrfToken: vi.fn(() => 'test-csrf'),
  };
});

// Silence the unused-variable lint hint — readCsrfToken is mocked but
// referenced only to satisfy the mock import.
void readCsrfToken;

beforeEach(() => {
  vi.unstubAllGlobals();
});

describe('testSubmission', () => {
  it('returns {ok:true} when the endpoint responds 200 ok=true', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ ok: true, detail: 'message queued' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    );

    const result = await testSubmission('ident-1');
    expect(result.ok).toBe(true);
    expect(result.detail).toBe('message queued');
  });

  it('returns {ok:false} with detail when the endpoint responds 200 ok=false', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ ok: false, detail: '535 Bad credentials' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    );

    const result = await testSubmission('ident-2');
    expect(result.ok).toBe(false);
    expect(result.detail).toBe('535 Bad credentials');
  });

  it('converts a 404 ApiError to {ok:false} with the problem detail field', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({ detail: 'identity not found', type: 'not_found' }),
          {
            status: 404,
            headers: { 'Content-Type': 'application/json' },
          },
        ),
      ),
    );

    const result = await testSubmission('missing-id');
    expect(result.ok).toBe(false);
    expect(result.detail).toBe('identity not found');
  });

  it('converts a 500 ApiError with a message field to {ok:false}', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({ message: 'internal server error' }),
          {
            status: 500,
            headers: { 'Content-Type': 'application/json' },
          },
        ),
      ),
    );

    const result = await testSubmission('ident-3');
    expect(result.ok).toBe(false);
    // Falls back to the message field when detail is absent.
    expect(result.detail).toBe('internal server error');
  });

  it('converts a network error to {ok:false} without throwing', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockRejectedValue(new TypeError('Failed to fetch')),
    );

    const result = await testSubmission('ident-4');
    expect(result.ok).toBe(false);
    expect(result.detail).toContain('Failed to fetch');
  });

  it('POSTs to the correct URL', async () => {
    const mockFetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ ok: true, detail: '' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    vi.stubGlobal('fetch', mockFetch);

    await testSubmission('abc-123');

    expect(mockFetch).toHaveBeenCalledOnce();
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/identities/abc-123/submission/test');
    expect(init.method).toBe('POST');
  });

  it('includes "to" in the request body when the option is provided', async () => {
    const mockFetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ ok: true, detail: '' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    vi.stubGlobal('fetch', mockFetch);

    await testSubmission('abc-456', { to: 'primary@example.local' });

    expect(mockFetch).toHaveBeenCalledOnce();
    const [, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string) as Record<string, unknown>;
    expect(body).toMatchObject({ to: 'primary@example.local' });
  });

  it('sends no body when no options are provided', async () => {
    const mockFetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ ok: true, detail: '' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    vi.stubGlobal('fetch', mockFetch);

    await testSubmission('abc-789');

    expect(mockFetch).toHaveBeenCalledOnce();
    const [, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    // body should be absent or empty when no options are passed
    expect(init.body).toBeFalsy();
  });
});
