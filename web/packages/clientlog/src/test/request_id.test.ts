/**
 * Tests for RequestIdContext and wrapFetch.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { RequestIdContext, wrapFetch, uuidv7 } from '../request_id.js';
import { installFakeFetch, installFakeUuid } from './fakes.js';
import type { FakeFetch, FakeUuid } from './fakes.js';

let fakeFetch: FakeFetch;
let fakeUuid: FakeUuid;

beforeEach(() => {
  fakeFetch = installFakeFetch();
  fakeUuid = installFakeUuid();
});

afterEach(() => {
  fakeFetch.uninstall();
  fakeUuid.uninstall();
});

describe('RequestIdContext.current()', () => {
  it('returns undefined outside any run() context', () => {
    expect(RequestIdContext.current()).toBeUndefined();
  });

  it('returns the id inside run()', async () => {
    let captured: string | undefined;
    await RequestIdContext.run('req-abc', () => {
      captured = RequestIdContext.current();
      return Promise.resolve();
    });
    expect(captured).toBe('req-abc');
  });

  it('is undefined again after run() completes', async () => {
    await RequestIdContext.run('req-abc', () => Promise.resolve());
    expect(RequestIdContext.current()).toBeUndefined();
  });

  it('supports nested run() contexts', async () => {
    const outer: string[] = [];
    const inner: string[] = [];
    await RequestIdContext.run('outer-id', async () => {
      outer.push(RequestIdContext.current() ?? '');
      await RequestIdContext.run('inner-id', async () => {
        inner.push(RequestIdContext.current() ?? '');
      });
      outer.push(RequestIdContext.current() ?? '');
    });
    expect(inner).toEqual(['inner-id']);
    // After inner completes, outer's id should be back
    expect(outer[1]).toBe('outer-id');
  });
});

describe('wrapFetch', () => {
  it('adds X-Request-Id header to each call', async () => {
    const wrapped = wrapFetch(globalThis.fetch);
    await wrapped('/test');
    expect(fakeFetch.calls[0]!.headers['x-request-id']).toBeDefined();
  });

  it('sets RequestIdContext.current() during the fetch', async () => {
    let capturedId: string | undefined;
    const instrumentedFetch: typeof globalThis.fetch = async (input, init) => {
      capturedId = RequestIdContext.current();
      return globalThis.fetch(input, init);
    };
    const wrapped = wrapFetch(instrumentedFetch);
    await wrapped('/test');
    expect(capturedId).toBeDefined();
    expect(capturedId).toMatch(/^[0-9a-f]{8}-/);
  });

  it('preserves existing init headers', async () => {
    const wrapped = wrapFetch(globalThis.fetch);
    await wrapped('/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    });
    expect(fakeFetch.calls[0]!.headers['content-type']).toBe('application/json');
    expect(fakeFetch.calls[0]!.headers['x-request-id']).toBeDefined();
  });
});

describe('uuidv7', () => {
  it('returns a string matching UUID format', () => {
    const id = uuidv7();
    expect(id).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    );
  });

  it('generates unique values on each call', () => {
    // Restore real randomUUID for this test so uuidv7 uses real crypto.
    fakeUuid.uninstall();
    const ids = new Set(Array.from({ length: 100 }, () => uuidv7()));
    expect(ids.size).toBe(100);
    // Re-install to avoid polluting other tests
    fakeUuid = installFakeUuid();
  });
});
