/**
 * Unit tests for the shared unnamed-inline-part default name (issue #409).
 *
 * Both the cid download URL builder and the download overlay's `download`
 * attribute in `MessageAccordion.svelte` must agree on the same
 * extension-bearing name for the same unnamed part; `buildCidDefaultNames`
 * is the single source of that name.
 */

import { describe, it, expect } from 'vitest';
import {
  extensionFromType,
  defaultInlineName,
  buildCidDefaultNames,
  type InlinePartLike,
} from './inline-part-name';

describe('extensionFromType', () => {
  it('extracts the subtype as the extension', () => {
    expect(extensionFromType('image/webp')).toBe('webp');
    expect(extensionFromType('image/png')).toBe('png');
  });

  it('strips MIME parameters', () => {
    expect(extensionFromType('text/plain; charset=utf-8')).toBe('plain');
  });

  it('falls back to "bin" for a type with no usable subtype', () => {
    expect(extensionFromType('')).toBe('bin');
    expect(extensionFromType(null)).toBe('bin');
    expect(extensionFromType(undefined)).toBe('bin');
  });
});

describe('defaultInlineName', () => {
  it('builds "inline-N.ext"', () => {
    expect(defaultInlineName('image/webp', 1)).toBe('inline-1.webp');
    expect(defaultInlineName('image/png', 2)).toBe('inline-2.png');
  });
});

describe('buildCidDefaultNames', () => {
  function part(overrides: Partial<InlinePartLike>): InlinePartLike {
    return { cid: null, blobId: null, name: null, type: 'application/octet-stream', ...overrides };
  }

  it('assigns sequential extension-bearing names to unnamed cid parts', () => {
    const parts = [
      part({ cid: 'a@h', blobId: 'b1', type: 'image/webp' }),
      part({ cid: 'b@h', blobId: 'b2', type: 'image/webp' }),
    ];
    expect(buildCidDefaultNames(parts)).toEqual({
      'a@h': 'inline-1.webp',
      'b@h': 'inline-2.webp',
    });
  });

  it('skips parts that already have a name', () => {
    const parts = [
      part({ cid: 'a@h', blobId: 'b1', type: 'image/webp', name: 'photo.webp' }),
      part({ cid: 'b@h', blobId: 'b2', type: 'image/webp' }),
    ];
    // The named part contributes no entry and does not consume an index,
    // so the sole unnamed part is still "inline-1".
    expect(buildCidDefaultNames(parts)).toEqual({ 'b@h': 'inline-1.webp' });
  });

  it('skips parts with no cid or no blobId', () => {
    const parts = [
      part({ cid: null, blobId: 'b1', type: 'image/webp' }),
      part({ cid: 'a@h', blobId: null, type: 'image/webp' }),
    ];
    expect(buildCidDefaultNames(parts)).toEqual({});
  });
});
