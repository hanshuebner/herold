/**
 * Unit tests for the undecodable-inline-image detection helpers (issue
 * #269): the content-type hint and the pure <img> decode-status classifier.
 */
import { describe, it, expect } from 'vitest';
import { isKnownNonRenderableImageType, inlineImageDecodeStatus } from './image-decode';

describe('isKnownNonRenderableImageType', () => {
  it('flags image/tiff', () => {
    expect(isKnownNonRenderableImageType('image/tiff')).toBe(true);
  });

  it('flags image/tif and image/x-tiff', () => {
    expect(isKnownNonRenderableImageType('image/tif')).toBe(true);
    expect(isKnownNonRenderableImageType('image/x-tiff')).toBe(true);
  });

  it('is case-insensitive and tolerates a parameter suffix', () => {
    expect(isKnownNonRenderableImageType('IMAGE/TIFF')).toBe(true);
    expect(isKnownNonRenderableImageType('image/tiff; name="x.tiff"')).toBe(true);
  });

  it('does not flag ordinary web-renderable image types', () => {
    expect(isKnownNonRenderableImageType('image/png')).toBe(false);
    expect(isKnownNonRenderableImageType('image/jpeg')).toBe(false);
    expect(isKnownNonRenderableImageType('image/gif')).toBe(false);
    expect(isKnownNonRenderableImageType('image/webp')).toBe(false);
  });

  it('does not flag non-image types', () => {
    expect(isKnownNonRenderableImageType('application/pdf')).toBe(false);
  });

  it('returns false for missing type', () => {
    expect(isKnownNonRenderableImageType(undefined)).toBe(false);
    expect(isKnownNonRenderableImageType(null)).toBe(false);
    expect(isKnownNonRenderableImageType('')).toBe(false);
  });
});

describe('inlineImageDecodeStatus', () => {
  it('is pending while the image has not finished loading', () => {
    expect(
      inlineImageDecodeStatus({ complete: false, naturalWidth: 0, naturalHeight: 0 }),
    ).toBe('pending');
  });

  it('is success once complete with positive natural dimensions', () => {
    expect(
      inlineImageDecodeStatus({ complete: true, naturalWidth: 40, naturalHeight: 40 }),
    ).toBe('success');
  });

  it('is failure once complete with zero natural width (undecodable format)', () => {
    expect(
      inlineImageDecodeStatus({ complete: true, naturalWidth: 0, naturalHeight: 0 }),
    ).toBe('failure');
  });

  it('is failure when only one dimension is zero', () => {
    expect(
      inlineImageDecodeStatus({ complete: true, naturalWidth: 40, naturalHeight: 0 }),
    ).toBe('failure');
  });
});
