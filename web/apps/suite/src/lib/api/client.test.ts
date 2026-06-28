/**
 * Tests for the REST API client's onUnauthenticated hook (re #57, re #77).
 *
 * When the server returns 401 the client must:
 *   1. invoke the registered onUnauthenticated callback so the auth state
 *      machine can transition to 'unauthenticated' and present the LoginView.
 *   2. still throw UnauthenticatedError so call-site error paths continue to
 *      work as before.
 *   3. not invoke the callback for non-401 error responses.
 *   4. pass the RFC 7807 `type` URI from the problem body to the callback so
 *      the auth state machine can distinguish session_expired from session_revoked
 *      and render the appropriate context message (REQ-AUTH-76, REQ-AS-10, re #77).
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { setOnUnauthenticated, UnauthenticatedError, ApiError, get } from './client';

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
