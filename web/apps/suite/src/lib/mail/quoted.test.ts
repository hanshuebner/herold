/**
 * Tests for splitQuotedText. The boundary heuristics drive what gets
 * hidden by default in the thread reader, so we want a clear contract
 * across the common reply styles.
 */
import { describe, it, expect } from 'vitest';
import { splitQuotedText } from './quoted';

describe('splitQuotedText', () => {
  it('returns the whole body as fresh when there is no quoted history', () => {
    const body = 'Hello there.\nMy reply has no quote.';
    expect(splitQuotedText(body)).toEqual({ fresh: body, quoted: '' });
  });

  it('splits at an English attribution line followed by a quote', () => {
    const body =
      'Thanks for the doc.\n' +
      '\n' +
      'On Mon, Apr 28, 2026 at 9:01 AM, Alice <a@x.test> wrote:\n' +
      '> First, the goals.\n' +
      '> Second, the approach.';
    const { fresh, quoted } = splitQuotedText(body);
    expect(fresh).toBe('Thanks for the doc.');
    expect(quoted.startsWith('On Mon, Apr 28, 2026')).toBe(true);
    expect(quoted).toContain('> First, the goals.');
  });

  it('splits at a German attribution line', () => {
    const body =
      'Danke!\n' +
      '\n' +
      'Am 27.04.2026 um 10:00 schrieb Bob <b@x.test>:\n' +
      '> hallo';
    const { fresh, quoted } = splitQuotedText(body);
    expect(fresh).toBe('Danke!');
    expect(quoted.startsWith('Am 27.04.2026')).toBe(true);
  });

  it('splits at a bare quote-prefix line with no attribution', () => {
    const body = 'See my reply inline.\n\n> The original\n> said this.';
    const { fresh, quoted } = splitQuotedText(body);
    expect(fresh).toBe('See my reply inline.');
    expect(quoted.startsWith('> The original')).toBe(true);
  });

  it('does not split on a sigdash line', () => {
    const body = 'Thanks!\n--\nAlice';
    expect(splitQuotedText(body)).toEqual({ fresh: body, quoted: '' });
  });

  it('does not split on a single ">" embedded inside a sentence', () => {
    const body = 'I think a > b in this case.';
    expect(splitQuotedText(body).quoted).toBe('');
  });

  it('returns empty halves for an empty body', () => {
    expect(splitQuotedText('')).toEqual({ fresh: '', quoted: '' });
  });

  it('splits at a deeply-nested ">>" quote prefix', () => {
    const body = 'Reply.\n\n>> earlier\n>> nested';
    const { fresh, quoted } = splitQuotedText(body);
    expect(fresh).toBe('Reply.');
    expect(quoted).toContain('>> earlier');
  });
});
