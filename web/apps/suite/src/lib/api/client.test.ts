/**
 * Tests for the REST API client's onUnauthenticated hook (re #57).
 *
 * When the server returns 401 the client must:
 *   1. invoke the registered onUnauthenticated callback so the auth state
 *      machine can transition to 'unauthenticated' and present the LoginView.
 *   2. still throw UnauthenticatedError so call-site error paths continue to
 *      work as before.
 *   3. not invoke the callback for non-401 error responses.
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
});
