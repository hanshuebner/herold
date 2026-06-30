/**
 * Unit tests for callTranslateApi (translate-api.svelte.ts, re #84).
 *
 * Verifies that the function delegates to client.post() (not raw fetch),
 * so the shared REST client's CSRF-token injection fires automatically,
 * and that error-kind mapping for each HTTP status is correct.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

// ── Hoisted mocks ──────────────────────────────────────────────────────────
// Hoist so the vi.mock factories can reference them.
const { mockPost, MockApiError } = vi.hoisted(() => {
  class MockApiError extends Error {
    status: number;
    detail: unknown;
    constructor(status: number, message: string, detail: unknown = null) {
      super(message);
      this.name = 'ApiError';
      this.status = status;
      this.detail = detail;
    }
  }
  return { mockPost: vi.fn(), MockApiError };
});

vi.mock('../api/client', () => ({
  post: mockPost,
  ApiError: MockApiError,
}));

// Import after mocks are wired so the module picks up the mocked client.
import { callTranslateApi, translateFeature } from './translate-api.svelte';

// ── Tests ──────────────────────────────────────────────────────────────────

describe('callTranslateApi: uses client.post(), not raw fetch', () => {
  beforeEach(() => {
    mockPost.mockReset();
  });

  it('calls post() with the correct path and request body', async () => {
    mockPost.mockResolvedValue({ translated_text: 'Bonjour' });
    const result = await callTranslateApi({ text: 'Hello', target_lang: 'fr' });
    expect(mockPost).toHaveBeenCalledOnce();
    expect(mockPost).toHaveBeenCalledWith('/api/v1/translate', {
      text: 'Hello',
      target_lang: 'fr',
    });
    expect(result).toEqual({ ok: true, data: { translated_text: 'Bonjour' } });
  });

  it('passes source_lang when provided', async () => {
    mockPost.mockResolvedValue({ translated_text: 'Hallo', detected_source_lang: 'en' });
    await callTranslateApi({ text: 'Hello', target_lang: 'de', source_lang: 'en' });
    expect(mockPost).toHaveBeenCalledWith('/api/v1/translate', {
      text: 'Hello',
      target_lang: 'de',
      source_lang: 'en',
    });
  });
});

describe('callTranslateApi: error-kind mapping', () => {
  beforeEach(() => {
    mockPost.mockReset();
  });

  it('returns tooLong on 413', async () => {
    mockPost.mockRejectedValue(new MockApiError(413, 'text too long'));
    const result = await callTranslateApi({ text: 'x', target_lang: 'fr' });
    expect(result).toEqual({ ok: false, kind: 'tooLong' });
  });

  it('returns upstreamError on 502', async () => {
    mockPost.mockRejectedValue(new MockApiError(502, 'bad gateway'));
    const result = await callTranslateApi({ text: 'x', target_lang: 'fr' });
    expect(result).toEqual({ ok: false, kind: 'upstreamError' });
  });

  it('returns badRequest on 400', async () => {
    mockPost.mockRejectedValue(new MockApiError(400, 'bad request'));
    const result = await callTranslateApi({ text: 'x', target_lang: 'fr' });
    expect(result).toEqual({ ok: false, kind: 'badRequest' });
  });

  it('returns networkError on other ApiError statuses (e.g. 500)', async () => {
    mockPost.mockRejectedValue(new MockApiError(500, 'server error'));
    const result = await callTranslateApi({ text: 'x', target_lang: 'fr' });
    expect(result).toEqual({ ok: false, kind: 'networkError', message: 'HTTP 500' });
  });

  it('returns networkError on non-ApiError (e.g. network failure)', async () => {
    mockPost.mockRejectedValue(new Error('Network failure'));
    const result = await callTranslateApi({ text: 'x', target_lang: 'fr' });
    expect(result).toMatchObject({ ok: false, kind: 'networkError' });
    expect((result as { message: string }).message).toContain('Network failure');
  });

  // Run the 501 test last: it mutates module-level _available state.
  it('returns unavailable and marks feature off on 501', async () => {
    mockPost.mockRejectedValue(new MockApiError(501, 'not configured'));
    const result = await callTranslateApi({ text: 'x', target_lang: 'fr' });
    expect(result).toEqual({ ok: false, kind: 'unavailable' });
    expect(translateFeature.available).toBe(false);
  });
});
