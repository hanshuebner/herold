/**
 * Unit tests for the HTML-to-plain-text helper used by language detection.
 */

import { describe, it, expect } from 'vitest';
import { htmlToText } from './html-to-text';

describe('htmlToText', () => {
  it('strips all HTML tags', () => {
    expect(htmlToText('<p>Hello <b>world</b></p>')).toBe('Hello world');
  });

  it('removes script blocks entirely', () => {
    const html = '<p>Visible</p><script>var x = 1;</script><p>text</p>';
    const result = htmlToText(html);
    expect(result).not.toContain('var x');
    expect(result).toContain('Visible');
    expect(result).toContain('text');
  });

  it('removes style blocks entirely', () => {
    const html = '<style>body { color: red; }</style><p>Content</p>';
    const result = htmlToText(html);
    expect(result).not.toContain('color');
    expect(result).toContain('Content');
  });

  it('decodes common HTML entities', () => {
    // &nbsp; decodes to a space; whitespace-collapse merges adjacent spaces to one.
    expect(htmlToText('&amp; &lt; &gt; &nbsp; &quot; &#39;')).toBe('& < > " \'');
  });

  it('collapses whitespace to single spaces', () => {
    expect(htmlToText('<p>hello   world</p>')).toBe('hello world');
  });

  it('trims leading and trailing whitespace', () => {
    expect(htmlToText('   hello   ')).toBe('hello');
  });

  it('inserts space at block-level end tags to avoid word runs', () => {
    const result = htmlToText('<div>foo</div><div>bar</div>');
    // The two words should be separated by at least one space
    expect(result).toMatch(/foo\s+bar/);
  });

  it('returns empty string for empty input', () => {
    expect(htmlToText('')).toBe('');
  });

  it('handles plain text (no tags) without modification beyond whitespace', () => {
    expect(htmlToText('Hello, World!')).toBe('Hello, World!');
  });

  it('decodes numeric character references', () => {
    // &#65; = 'A'
    expect(htmlToText('&#65;BC')).toBe('ABC');
  });
});
