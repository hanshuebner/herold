/**
 * Readable plain text of an HTML document, for quoting in forward and reply.
 *
 * Unlike `htmlToText` (language-detection sampling, whitespace collapsed to
 * one line) the result keeps line structure: block boundaries (p, div, tr,
 * li, headings, ...) and `<br>` start new lines, table cells on one row are
 * separated by a space, and the content of style, script, head, noscript
 * elements and comments is dropped. Needs a DOM (DOMParser), which the
 * browser and the happy-dom test environment provide.
 */

const SKIP_TAGS = new Set(['style', 'script', 'head', 'noscript', 'template', 'title']);
const BLOCK_TAGS = new Set([
  'p', 'div', 'li', 'ul', 'ol', 'tr', 'table', 'thead', 'tbody', 'tfoot', 'blockquote', 'pre',
  'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'section', 'article', 'header', 'footer', 'center',
  'address', 'hr', 'dl', 'dt', 'dd', 'form', 'fieldset', 'figure', 'figcaption', 'main', 'nav',
]);

export function htmlToQuoteText(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  if (!doc.body) return '';
  let out = '';
  const newline = (): void => {
    if (out !== '' && !/\n *$/.test(out)) out += '\n';
  };
  const walk = (node: Node): void => {
    if (node.nodeType === 3) {
      out += (node.nodeValue ?? '').replace(/[ \t\r\n\f ]+/g, ' ');
      return;
    }
    if (node.nodeType !== 1) return; // comments and everything else
    const el = node as Element;
    const tag = el.tagName.toLowerCase();
    if (SKIP_TAGS.has(tag)) return;
    if (tag === 'br') {
      out += '\n';
      return;
    }
    const block = BLOCK_TAGS.has(tag);
    const cell = tag === 'td' || tag === 'th';
    if (block) newline();
    if (cell) out += ' ';
    for (const child of Array.from(el.childNodes)) walk(child);
    if (cell) out += ' ';
    if (block) newline();
  };
  walk(doc.body);
  return out
    .split('\n')
    .map((line) => line.replace(/ {2,}/g, ' ').trim())
    .join('\n')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}
