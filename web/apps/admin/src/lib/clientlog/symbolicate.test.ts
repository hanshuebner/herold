/**
 * Unit tests for the symbolicate adapter (REQ-OPS-212).
 *
 * These tests replace globalThis.fetch with a stub that returns a minimal
 * inline source map so no real network calls or filesystem reads are needed.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { symbolicateStack, SymbolicateError, _resetConsumerCache } from './symbolicate';

// Stub document.querySelectorAll to return no scripts so mapUrlForBuildSha
// falls back to the deterministic /admin/assets/<sha>.js.map path.
Object.defineProperty(globalThis, 'document', {
  value: {
    querySelectorAll: () => [],
  },
  writable: true,
});

/**
 * A minimal valid source map that maps line 1 col 0 to
 * src/views/ClientlogView.svelte line 1 col 0.
 */
const STUB_MAP = JSON.stringify({
  version: 3,
  file: 'index.js',
  sourceRoot: '',
  sources: ['src/views/ClientlogView.svelte'],
  sourcesContent: [null],
  names: ['openRow'],
  mappings: 'AAAA,SAAS,OAAO',
});

describe('symbolicateStack', () => {
  beforeEach(() => {
    _resetConsumerCache();
    vi.restoreAllMocks();
  });

  it('returns a string (symbolicated or raw) when the map can be fetched', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => STUB_MAP,
    } as unknown as Response);

    const raw = [
      'Error: something broke',
      '    at openRow (http://localhost:5174/admin/assets/index-abc123.js:1:0)',
    ].join('\n');

    const result = await symbolicateStack(raw, 'abc123');
    // The first line is not a frame so it is kept verbatim.
    expect(result).toContain('Error: something broke');
    // Must return a string without throwing.
    expect(typeof result).toBe('string');
  });

  it('throws SymbolicateError on HTTP 404', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 404,
      text: async () => 'Not Found',
    } as unknown as Response);

    await expect(
      symbolicateStack('Error: x\n  at foo (http://localhost/assets/x.js:1:0)', 'sha404'),
    ).rejects.toBeInstanceOf(SymbolicateError);
  });

  it('throws SymbolicateError on network failure', async () => {
    globalThis.fetch = vi.fn().mockRejectedValue(new Error('Network error'));

    await expect(
      symbolicateStack('Error: x\n  at foo (http://localhost/assets/x.js:1:0)', 'shanet'),
    ).rejects.toBeInstanceOf(SymbolicateError);
  });

  it('caches the consumer so fetch is only called once per build SHA', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => STUB_MAP,
    } as unknown as Response);
    globalThis.fetch = fetchMock;

    const stack = 'Error: x\n    at fn (http://localhost/admin/assets/index.js:1:0)';
    await symbolicateStack(stack, 'cachedSha');
    await symbolicateStack(stack, 'cachedSha');

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('keeps non-frame lines verbatim', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => STUB_MAP,
    } as unknown as Response);

    const raw = 'TypeError: Cannot read properties of null\n    at eval (<anonymous>)';
    const result = await symbolicateStack(raw, 'sha-verbatim');
    expect(result).toContain('TypeError: Cannot read properties of null');
    // The eval frame has no matching URL pattern so it stays verbatim.
    expect(result).toContain('at eval (<anonymous>)');
  });

  it('error message contains HTTP status on 404', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 404,
      text: async () => 'Not Found',
    } as unknown as Response);

    let caught: SymbolicateError | null = null;
    try {
      await symbolicateStack('Error\n  at fn (http://h/assets/x.js:1:0)', 'sha-404-msg');
    } catch (err) {
      caught = err as SymbolicateError;
    }
    expect(caught).not.toBeNull();
    expect(caught?.message).toContain('404');
  });
});
