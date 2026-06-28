/**
 * Minimal HTML-to-plain-text extraction for language detection.
 *
 * The goal is to get enough representative body text to give franc a
 * reasonable sample. We do not need a full-fidelity conversion: stripping
 * tags and decoding common entities is sufficient. This is intentionally
 * a tiny pure helper with no DOM access so it can run in test environments
 * without a real browser.
 */

/**
 * Strip HTML tags and decode a small set of common HTML entities, returning
 * the resulting plain text. Collapses runs of whitespace into single spaces
 * and trims the result.
 *
 * Not a general-purpose HTML->text converter: it is used solely to produce
 * a text sample for language detection, so full entity-set coverage and
 * structural formatting (headings, lists) are deliberately not attempted.
 */
export function htmlToText(html: string): string {
  // Remove <style> and <script> blocks entirely — their content is not body text.
  let text = html
    .replace(/<style[^>]*>[\s\S]*?<\/style>/gi, ' ')
    .replace(/<script[^>]*>[\s\S]*?<\/script>/gi, ' ');

  // Replace block-level end tags with a space so words don't run together.
  text = text.replace(/<\/(p|div|li|td|th|br|tr|h[1-6]|blockquote|pre)[^>]*>/gi, ' ');

  // Strip all remaining tags.
  text = text.replace(/<[^>]+>/g, '');

  // Decode a small set of common entities.
  text = text
    .replace(/&amp;/gi, '&')
    .replace(/&lt;/gi, '<')
    .replace(/&gt;/gi, '>')
    .replace(/&nbsp;/gi, ' ')
    .replace(/&quot;/gi, '"')
    .replace(/&#39;/gi, "'")
    .replace(/&apos;/gi, "'")
    .replace(/&#(\d+);/g, (_m, n) => String.fromCharCode(Number(n)));

  // Collapse whitespace and trim.
  return text.replace(/\s+/g, ' ').trim();
}
