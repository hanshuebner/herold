/**
 * Per-message print (REQ-MAIL-140). Opens a same-origin popup window
 * with only this message's header (sender / date / subject / to / cc)
 * and rendered body, then calls `print()` on the popup. The popup-
 * window approach isolates the print surface from the surrounding
 * thread and the sandboxed reader iframe — scoped `@media print` CSS
 * interacts poorly with the sandbox boundary and was rejected.
 *
 * The HTML body passed in MUST already be sanitised (e.g. via
 * `sanitizeHtml(html, { loadImages: true, cidMap })`). The caller is
 * responsible for sanitisation because the cid map and the
 * `loadImages` policy are caller context — and because this helper has
 * a synchronous popup-write contract.
 */

import type { Address } from './types';

export interface PrintArgs {
  subject: string;
  /** Pre-formatted absolute date string in the user's locale. */
  date: string;
  from: readonly Address[];
  to: readonly Address[];
  cc: readonly Address[];
  /** Sanitised HTML body. When absent, `text` is rendered instead. */
  html: string | null;
  /** Plain text body. Used when `html` is null. */
  text: string | null;
}

/**
 * Build the full popup-document HTML string. Exported for unit testing.
 */
export function buildPrintDocument(args: PrintArgs): string {
  const subject = escapeHtml(args.subject || '(no subject)');
  const date = escapeHtml(args.date);
  const from = renderAddresses(args.from);
  const to = renderAddresses(args.to);
  const cc = renderAddresses(args.cc);

  const bodyHtml = args.html
    ? args.html
    : `<pre class="text-body">${escapeHtml(args.text ?? '(no body)')}</pre>`;

  return `<!doctype html>
<html>
<head>
<meta charset="utf-8" />
<title>${subject}</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif; margin: 24px; color: #111; }
  header { border-bottom: 1px solid #ccc; padding-bottom: 12px; margin-bottom: 16px; }
  header .row { margin: 2px 0; font-size: 13px; }
  header .label { color: #666; display: inline-block; min-width: 56px; }
  header .subject { font-size: 16px; font-weight: 600; margin-top: 6px; }
  .body { font-size: 14px; line-height: 1.5; word-break: break-word; }
  .body img { max-width: 100%; height: auto; }
  .text-body { white-space: pre-wrap; font-family: inherit; margin: 0; }
  @media print { body { margin: 0; } }
</style>
</head>
<body>
<header>
  <div class="row"><span class="label">From:</span> ${from}</div>
  ${to ? `<div class="row"><span class="label">To:</span> ${to}</div>` : ''}
  ${cc ? `<div class="row"><span class="label">Cc:</span> ${cc}</div>` : ''}
  <div class="row"><span class="label">Date:</span> ${date}</div>
  <div class="subject">${subject}</div>
</header>
<div class="body">${bodyHtml}</div>
</body>
</html>`;
}

/**
 * Trigger the print flow. Returns the popup window handle (or null if
 * the browser blocked it). Caller does not need to retain the handle —
 * it is closed automatically after the print dialog completes.
 */
export function printMessage(args: PrintArgs): Window | null {
  const popup = window.open('', '_blank', 'noopener,noreferrer,width=820,height=900');
  if (!popup) return null;
  popup.document.open();
  popup.document.write(buildPrintDocument(args));
  popup.document.close();
  popup.focus();
  // Defer print to next microtask so the popup has rendered.
  setTimeout(() => {
    popup.print();
  }, 0);
  return popup;
}

function renderAddresses(addrs: readonly Address[]): string {
  if (!addrs || addrs.length === 0) return '';
  return addrs
    .map((a) => {
      const name = a.name?.trim();
      const email = escapeHtml(a.email ?? '');
      if (name) {
        return `${escapeHtml(name)} &lt;${email}&gt;`;
      }
      return email;
    })
    .join(', ');
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}
