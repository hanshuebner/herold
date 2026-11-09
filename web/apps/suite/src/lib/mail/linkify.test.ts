import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import { splitLinks } from './linkify';
import TextBody from './TextBody.svelte';
import { splitQuotedText } from './quoted';
import { buildPrintDocument } from './print-message';

const links = (s: string) =>
  splitLinks(s)
    .filter((x) => x.kind === 'link')
    .map((x) => (x as { href: string }).href);

describe('splitLinks', () => {
  it('links http, https, mailto and www', () => {
    expect(
      links('a http://a.example/x b https://b.example c mailto:x@y.example d www.c.example'),
    ).toEqual([
      'http://a.example/x',
      'https://b.example',
      'mailto:x@y.example',
      'https://www.c.example',
    ]);
  });
  it('trims trailing punctuation', () => {
    expect(links('see https://a.example/p.')).toEqual(['https://a.example/p']);
    expect(links('https://a.example/p, and https://b.example!')).toEqual([
      'https://a.example/p',
      'https://b.example',
    ]);
  });
  it('keeps balanced parentheses and drops unbalanced', () => {
    expect(links('https://en.wikipedia.org/wiki/Foo_(bar)')).toEqual([
      'https://en.wikipedia.org/wiki/Foo_(bar)',
    ]);
    expect(links('(see https://a.example/x)')).toEqual(['https://a.example/x']);
  });
  it('stops at quotes and angle brackets', () => {
    expect(links('"https://a.example/x" <https://b.example/y>')).toEqual([
      'https://a.example/x',
      'https://b.example/y',
    ]);
  });
  it('never links javascript: or data: URLs', () => {
    expect(links('javascript:alert(1) data:text/html,x')).toEqual([]);
  });
  it('does not link ticket references or bare addresses', () => {
    expect(links('fixed #509 and bob@example.com')).toEqual([]);
  });
  it('keeps a match reduced to a bare prefix as text', () => {
    for (const s of ['trailing: www.,', 'www.)', 'https://', 'mailto:', 'https://.', 'x www. y']) {
      expect(links(s)).toEqual([]);
      expect(
        splitLinks(s)
          .map((x) => x.text)
          .join(''),
      ).toBe(s);
    }
    expect(links('www.a')).toEqual(['https://www.a']);
  });
  it('round-trips the input exactly', () => {
    const s = 'x  https://a.example/p).\n\n  y www.b.example; z\n';
    expect(
      splitLinks(s)
        .map((x) => x.text)
        .join(''),
    ).toBe(s);
  });
});

describe('TextBody', () => {
  it('renders anchors with safe attributes and keeps HTML-like text as text', () => {
    const text = '<b>bold</b> https://a.example/x?a=1&b=2. <img src=x onerror=alert(1)>';
    const { container } = render(TextBody, { text });
    const a = container.querySelectorAll('a');
    expect(a).toHaveLength(1);
    expect(a[0]!.getAttribute('href')).toBe('https://a.example/x?a=1&b=2');
    expect(a[0]!.getAttribute('target')).toBe('_blank');
    expect(a[0]!.getAttribute('rel')).toBe('noopener noreferrer');
    expect(container.querySelector('b')).toBeNull();
    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('pre')!.textContent).toBe(text);
  });
  it('preserves whitespace and line breaks', () => {
    const t = 'a\n\n  https://a.example\n\tb';
    const { container } = render(TextBody, { text: t });
    expect(container.querySelector('pre')!.textContent).toBe(t);
  });
  it('links inside each part of a quoted-history split', () => {
    const body =
      'see https://a.example/head\n\nOn Mon, Bob wrote:\n> quoted https://b.example/q\n';
    const split = splitQuotedText(body);
    expect(split.collapsed).toContain('https://b.example/q');
    const h = render(TextBody, { text: split.head });
    expect(h.container.querySelector('a')!.getAttribute('href')).toBe('https://a.example/head');
    const c = render(TextBody, { text: split.collapsed, quoted: true });
    expect(c.container.querySelector('a')!.getAttribute('href')).toBe('https://b.example/q');
  });
});

describe('print document', () => {
  it('linkifies text bodies and escapes markup', () => {
    const doc = buildPrintDocument({
      subject: 's',
      date: 'd',
      from: [],
      to: [],
      cc: [],
      html: null,
      text: '<script>x</script> https://a.example/p?a=1&b=2.',
    });
    expect(doc).toContain(
      '<a href="https://a.example/p?a=1&amp;b=2" target="_blank" rel="noopener noreferrer">',
    );
    expect(doc).toContain('&lt;script&gt;');
    expect(doc).not.toContain('<script>x');
  });
});
