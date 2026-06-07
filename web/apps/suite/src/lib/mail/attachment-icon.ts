/**
 * Maps an EmailBodyPart MIME type to a visual representation for the
 * Gmail-style attachment card.
 *
 * Returns one of two kinds:
 *   - 'thumbnail': the card should render an <img> preview using the
 *     part's download URL (image/* types within the thumbnail size cap).
 *   - 'badge': the card should render a coloured square with a short
 *     text label (PDF, DOC, XLS, ZIP, or generic).
 */
import type { EmailBodyPart } from './types';

/** Maximum file size for which a thumbnail is shown (2 MB). */
const THUMB_SIZE_CAP = 2 * 1024 * 1024;

export type AttachmentIconBadge = {
  kind: 'badge';
  label: string;
  bg: string;
};

export type AttachmentIconThumbnail = {
  kind: 'thumbnail';
};

export type AttachmentIcon = AttachmentIconBadge | AttachmentIconThumbnail;

/**
 * Background colours for badge icons. Values chosen for legible contrast
 * against white (`--text-on-color`).
 *
 * PDF  : #da1e28  (Carbon red-60)
 * DOC  : #0043ce  (Carbon blue-70)
 * XLS  : #198038  (Carbon green-60)
 * ZIP  : #697077  (Carbon cool-gray-60)
 * IMG  : #007d79  (Carbon teal-60, used only when thumbnail is suppressed)
 * FILE : #697077  (same as ZIP)
 */
const BG_PDF = '#da1e28';
const BG_DOC = '#0043ce';
const BG_XLS = '#198038';
const BG_ZIP = '#697077';
const BG_IMG = '#007d79';
const BG_FILE = '#697077';

/**
 * Pure helper: given a MIME content type and filename, return the badge
 * label and background colour. Image content types always map to "IMG"
 * here; the thumbnail threshold check is the caller's responsibility.
 *
 * Exported so the compose strip and other contexts can obtain a badge
 * descriptor without constructing a full EmailBodyPart.
 */
export function attachmentBadge(contentType: string, _filename: string): { label: string; bg: string } {
  const type = contentType ?? '';

  if (type.startsWith('image/')) {
    return { label: 'IMG', bg: BG_IMG };
  }

  if (type === 'application/pdf') {
    return { label: 'PDF', bg: BG_PDF };
  }

  if (
    type === 'application/msword' ||
    type.includes('officedocument.wordprocessingml')
  ) {
    return { label: 'DOC', bg: BG_DOC };
  }

  if (
    type === 'application/vnd.ms-excel' ||
    type.includes('spreadsheetml')
  ) {
    return { label: 'XLS', bg: BG_XLS };
  }

  if (
    type === 'application/zip' ||
    type === 'application/x-7z-compressed' ||
    type.includes('tar') ||
    type.includes('gzip')
  ) {
    return { label: 'ZIP', bg: BG_ZIP };
  }

  return { label: 'FILE', bg: BG_FILE };
}

/**
 * Returns the icon descriptor for a body part.
 *
 * Image parts under THUMB_SIZE_CAP return `{kind: 'thumbnail'}` so the
 * card renders an `<img>` crop instead of a coloured badge. Larger images
 * fall through to the badge path with label "IMG".
 */
export function attachmentIcon(part: EmailBodyPart): AttachmentIcon {
  const type = part.type ?? '';

  if (type.startsWith('image/')) {
    if (part.size > 0 && part.size <= THUMB_SIZE_CAP) {
      return { kind: 'thumbnail' };
    }
  }

  return { kind: 'badge', ...attachmentBadge(type, part.name ?? '') };
}
