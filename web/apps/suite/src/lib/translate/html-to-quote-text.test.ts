import { describe, it, expect } from 'vitest';
import { htmlToQuoteText } from './html-to-quote-text';

describe('htmlToQuoteText (re #533)', () => {
  it('drops body-level style source', () => {
    expect(htmlToQuoteText('<style>.a{color:red}</style><p>Hi</p>')).toBe('Hi');
  });
  it('drops body-level script source', () => {
    expect(htmlToQuoteText('<p>Hi</p><script>var x = 1;</script>')).toBe('Hi');
  });
  it('drops comments, including conditional comments', () => {
    expect(
      htmlToQuoteText('<!-- note --><p>Hi<!--[if mso]><p>Outlook only</p><![endif]--></p>'),
    ).toBe('Hi');
  });
  it('drops head content', () => {
    expect(
      htmlToQuoteText('<html><head><title>T</title><style>p{x:y}</style></head><body><p>Hi</p></body></html>'),
    ).toBe('Hi');
  });
  it('breaks lines between whitespace-free adjacent paragraphs', () => {
    expect(htmlToQuoteText('<p>A</p><p>B</p>')).toBe('A\nB');
  });
  it('breaks lines at br, div, li and headings', () => {
    expect(htmlToQuoteText('<h1>T</h1><div>a<br>b</div><ul><li>x</li><li>y</li></ul>')).toBe(
      'T\na\nb\nx\ny',
    );
  });
  it('separates whitespace-free table cells and breaks rows', () => {
    expect(htmlToQuoteText('<table><tr><td>A</td><td>B</td></tr><tr><td>C</td><td>D</td></tr></table>')).toBe(
      'A B\nC D',
    );
  });
  it('collapses source whitespace inside a paragraph', () => {
    expect(htmlToQuoteText('<p>Hello\n   big\n  world</p>')).toBe('Hello big world');
  });
  it('handles a newsletter shape: nested layout tables with style blocks in body', () => {
    const html =
      '<!DOCTYPE html><html><head><style>.h{x:y}</style></head><body>' +
      '<style>@media (max-width:600px){.w{width:100%}}</style>' +
      '<!--[if mso]><table><tr><td><![endif]-->' +
      '<table class="w"><tr><td><table><tr><td><style>.in{a:b}</style>' +
      '<h2>Schlagzeile</h2></td></tr><tr><td>Erster Absatz</td><td>rechts</td></tr></table></td></tr>' +
      '<tr><td><p>Zweiter Absatz</p></td></tr></table><!-- footer --></body></html>';
    const out = htmlToQuoteText(html);
    expect(out).toBe('Schlagzeile\nErster Absatz rechts\nZweiter Absatz');
  });
});
