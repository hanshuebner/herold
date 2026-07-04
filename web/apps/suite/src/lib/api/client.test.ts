/**
 * Tests for the REST API client.
 *
 * Covers:
 *  - onUnauthenticated hook (re #57, re #77):
 *      1. invoke the registered onUnauthenticated callback on 401
 *      2. still throw UnauthenticatedError
 *      3. not invoke the callback for non-401 error responses
 *      4. pass the RFC 7807 `type` URI (REQ-AUTH-76, REQ-AS-10, re #77)
 *  - debug-ring logging (re #124):
 *      5. records failed API calls (422, 500, network error) to the ring
 *      6. does NOT log successful responses
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock the debug-ring module so appendEvent is a controllable spy.
// vi.mock is hoisted above imports so client.ts receives the mock binding.
vi.mock('../debug-ring/debug-ring', () => ({
  appendEvent: vi.fn(async () => undefined),
}));

import { setOnUnauthenticated, UnauthenticatedError, ApiError, get, put } from './client';
const { appendEvent } = await import('../debug-ring/debug-ring');

// Reset fetch mock before each test.
beforeEach(() => {
  vi.unstubAllGlobals();
  // Clear the registered callback between tests.
  setOnUnauthenticated(() => {});
});

describe('setOnUnauthenticated / REST client 401 hook', () => {
  it('calls the registered callback when the server returns 401', async () => {
    const onUnauthenticated = vi.fn();
    setOnUnauthenticated(onUnauthenticated);

    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ message: 'Session expired.' }), {
          status: 401,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    );

    await expect(get('/api/v1/test')).rejects.toThrow(UnauthenticatedError);
    expect(onUnauthenticated).toHaveBeenCalledOnce();
  });

  it('does not call the callback for non-401 error responses', async () => {
    const onUnauthenticated = vi.fn();
    setOnUnauthenticated(onUnauthenticated);

    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ message: 'Internal error.' }), {
          status: 500,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    );

    await expect(get('/api/v1/test')).rejects.toThrow(ApiError);
    expect(onUnauthenticated).not.toHaveBeenCalled();
  });

  it('still throws UnauthenticatedError even when no callback is registered', async () => {
    // No callback registered (cleared in beforeEach with a no-op).
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response('', { status: 401 }),
      ),
    );

    await expect(get('/api/v1/test')).rejects.toThrow(UnauthenticatedError);
  });

  it('passes the RFC 7807 type URI to the callback for a typed session_expired 401', async () => {
    // REQ-AUTH-76, REQ-AS-10, re #77: typed 401 bodies carry the problem type so
    // the auth state machine can set forcedLoginReason correctly.
    const onUnauthenticated = vi.fn();
    setOnUnauthenticated(onUnauthenticated);

    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            type: 'https://netzhansa.com/problems/session_expired',
            title: 'authentication required',
            status: 401,
            detail: 'session idle timeout exceeded',
          }),
          { status: 401, headers: { 'Content-Type': 'application/problem+json' } },
        ),
      ),
    );

    await expect(get('/api/v1/test')).rejects.toThrow(UnauthenticatedError);
    expect(onUnauthenticated).toHaveBeenCalledWith(
      'https://netzhansa.com/problems/session_expired',
    );
  });

  it('passes null to the callback when the 401 body has no RFC 7807 type', async () => {
    const onUnauthenticated = vi.fn();
    setOnUnauthenticated(onUnauthenticated);

    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({ message: 'Unauthorized.' }),
          { status: 401, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    );

    await expect(get('/api/v1/test')).rejects.toThrow(UnauthenticatedError);
    expect(onUnauthenticated).toHaveBeenCalledWith(null);
  });

  it('passes null to the callback when the 401 body is empty', async () => {
    const onUnauthenticated = vi.fn();
    setOnUnauthenticated(onUnauthenticated);

    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('', { status: 401 })),
    );

    await expect(get('/api/v1/test')).rejects.toThrow(UnauthenticatedError);
    expect(onUnauthenticated).toHaveBeenCalledWith(null);
  });

  it('passes null to the callback when type is the reserved "about:blank"', async () => {
    const onUnauthenticated = vi.fn();
    setOnUnauthenticated(onUnauthenticated);

    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({ type: 'about:blank', title: 'Unauthorized' }),
          { status: 401, headers: { 'Content-Type': 'application/problem+json' } },
        ),
      ),
    );

    await expect(get('/api/v1/test')).rejects.toThrow(UnauthenticatedError);
    expect(onUnauthenticated).toHaveBeenCalledWith(null);
  });

  it('surfaces the RFC 7807 detail field as ApiError.message for non-2xx responses (re #104)', async () => {
    // This test demonstrates the bug: a 400 response with a problem+json body
    // whose human-readable text is in the standard `detail` field (as herold
    // produces) MUST be surfaced as ApiError.message — not collapsed to "HTTP 400".
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            type: 'https://netzhansa.com/problems/invalid_body',
            title: 'request body could not be parsed',
            status: 400,
            detail: 'json: unknown field "auth_method"',
            instance: '/api/v1/identities/abc/submission',
          }),
          { status: 400, headers: { 'Content-Type': 'application/problem+json' } },
        ),
      ),
    );

    let thrown: unknown;
    try {
      await put('/api/v1/identities/abc/submission', { bad_field: 1 });
    } catch (err) {
      thrown = err;
    }
    expect(thrown).toBeInstanceOf(ApiError);
    expect((thrown as ApiError).status).toBe(400);
    // The message must come from the server's `detail` field, not be "HTTP 400".
    expect((thrown as ApiError).message).toBe('json: unknown field "auth_method"');
  });

  it('sets problemType on the thrown UnauthenticatedError for typed 401', async () => {
    setOnUnauthenticated(() => {});

    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            type: 'https://netzhansa.com/problems/session_expired',
            title: 'authentication required',
          }),
          { status: 401, headers: { 'Content-Type': 'application/problem+json' } },
        ),
      ),
    );

    let thrown: unknown;
    try {
      await get('/api/v1/test');
    } catch (err) {
      thrown = err;
    }
    expect(thrown).toBeInstanceOf(UnauthenticatedError);
    expect((thrown as UnauthenticatedError).problemType).toBe(
      'https://netzhansa.com/problems/session_expired',
    );
  });
});

// ── Debug-ring logging (re #124) ────────────────────────────────────────────

describe('api client — debug-ring logging (re #124)', () => {
  beforeEach(() => {
    vi.mocked(appendEvent).mockClear();
  });

  it('records a 422 probe-failure (category + diagnostic) as api.error in the ring', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            type: 'https://netzhansa.com/problems/external_submission_probe_failed',
            title: 'external submission probe failed',
            category: 'auth-failed',
            diagnostic: 'auth probe: AUTH PLAIN: 535 Error: authentication failed',
            status: 422,
          }),
          { status: 422, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    );

    await expect(put('/api/v1/identities/id-1/submission', {})).rejects.toThrow(ApiError);

    expect(vi.mocked(appendEvent)).toHaveBeenCalledWith(
      'page',
      'error',
      'api.error',
      expect.objectContaining({
        method: 'PUT',
        path: '/api/v1/identities/id-1/submission',
        status: 422,
        title: 'external submission probe failed',
        category: 'auth-failed',
        diagnostic: 'auth probe: AUTH PLAIN: 535 Error: authentication failed',
      }),
    );
  });

  it('records a network error (status 0) as api.error in the ring', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockRejectedValue(new TypeError('Failed to fetch')),
    );

    await expect(get('/api/v1/identities')).rejects.toThrow(ApiError);

    expect(vi.mocked(appendEvent)).toHaveBeenCalledWith(
      'page',
      'error',
      'api.error',
      expect.objectContaining({
        method: 'GET',
        path: '/api/v1/identities',
        status: 0,
        title: 'Failed to fetch',
      }),
    );
  });

  it('records a 500 response as api.error in the ring', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({ detail: 'internal server error', title: 'Internal Server Error' }),
          { status: 500, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    );

    await expect(get('/api/v1/something')).rejects.toThrow(ApiError);

    expect(vi.mocked(appendEvent)).toHaveBeenCalledWith(
      'page',
      'error',
      'api.error',
      expect.objectContaining({ status: 500, title: 'Internal Server Error' }),
    );
  });

  it('does NOT call appendEvent for a successful 200 response', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([{ id: 'ident-1' }]),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    );

    await get('/api/v1/identities');

    expect(vi.mocked(appendEvent)).not.toHaveBeenCalled();
  });
});
