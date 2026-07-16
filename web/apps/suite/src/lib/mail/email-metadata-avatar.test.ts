/**
 * Tests for the pure-TS email-metadata avatar helpers, in particular
 * `tryFetchGravatar` (re #177).
 *
 * `tryFetchGravatar` used to issue a cross-origin `fetch(url, {method:
 * 'HEAD'})`. `www.gravatar.com` sends no `Access-Control-Allow-Origin`
 * header, so that fetch is blocked by the browser's CORS check on every
 * call — it floods the console with "blocked by CORS policy" /
 * `net::ERR_FAILED` errors and always resolves false, regardless of
 * whether the address actually has a Gravatar picture. These tests pin
 * the current implementation, which probes via `<img>` load/error
 * instead (governed by `img-src`, not CORS) so no `fetch()` to the
 * Gravatar host is issued at all.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { tryFetchGravatar } from './email-metadata-avatar';

// ── fetch spy ────────────────────────────────────────────────────────────
//
// Asserts the fix: no fetch() call is made for the existence probe.

const fetchSpy = vi.fn();

// ── fake <img> ─────────────────────────────────────────────────────────
//
// happy-dom's Image element does not perform real network loads, so we
// replace the global constructor with a controllable fake that fires
// onload/onerror synchronously once `src` is assigned, based on the URL.

class FakeImage {
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  #src = '';
  set src(value: string) {
    this.#src = value;
    queueMicrotask(() => {
      if (value.includes('missing')) {
        this.onerror?.();
      } else {
        this.onload?.();
      }
    });
  }
  get src(): string {
    return this.#src;
  }
}

beforeEach(() => {
  fetchSpy.mockReset();
  vi.stubGlobal('fetch', fetchSpy);
  vi.stubGlobal('Image', FakeImage);
});

describe('tryFetchGravatar', () => {
  it('resolves true when the image loads (address has a picture)', async () => {
    const found = await tryFetchGravatar(
      'https://www.gravatar.com/avatar/found-hash?s=128&d=404',
    );
    expect(found).toBe(true);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('resolves false when the image errors (404, unknown address)', async () => {
    const found = await tryFetchGravatar(
      'https://www.gravatar.com/avatar/missing-hash?s=128&d=404',
    );
    expect(found).toBe(false);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('never issues a cross-origin fetch() for the probe', async () => {
    await tryFetchGravatar(
      'https://www.gravatar.com/avatar/either-hash?s=128&d=404',
    );
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});
