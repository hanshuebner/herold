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

  // ── Trailing new content (issue #116) ─────────────────────────────────────

  it('extracts trailing new content after a quoted block as fresh (ticket example)', () => {
    // Exact body from issue #116: format=flowed reply where the sender's
    // new text ("das ist noch nicht alles") comes after the quoted block.
    const body =
      'Am 2026-07-04 17:07, schrieb Hans Hübner:\n' +
      '> es geht, finally!\n' +
      '> ...\n' +
      '>> hjgjh\n' +
      '\n' +
      'das ist noch nicht alles';
    const { fresh, quoted } = splitQuotedText(body);
    // The new content must be in fresh so it is always visible.
    expect(fresh).toBe('das ist noch nicht alles');
    // The quoted portion contains only the attributed + quoted lines.
    expect(quoted).toContain('Am 2026-07-04 17:07, schrieb Hans Hübner:');
    expect(quoted).toContain('> es geht, finally!');
    expect(quoted).toContain('>> hjgjh');
    // The new content must NOT appear in the quoted/hidden section.
    expect(quoted).not.toContain('das ist noch nicht alles');
  });

  it('combines pre-quote fresh text with trailing new content', () => {
    const body =
      'Initial reply.\n' +
      '\n' +
      'On Mon, 4 Jul 2026, Alice <a@x.test> wrote:\n' +
      '> Some quoted text.\n' +
      '\n' +
      'Additional comment after quote.';
    const { fresh, quoted } = splitQuotedText(body);
    expect(fresh).toContain('Initial reply.');
    expect(fresh).toContain('Additional comment after quote.');
    expect(quoted).toContain('> Some quoted text.');
    expect(quoted).not.toContain('Additional comment after quote.');
  });

  it('extracts inline replies interleaved between quoted segments', () => {
    // Inline reply style: non-quoted lines appear between quoted paragraphs.
    const body =
      'On date, Alice wrote:\n' +
      '> Original paragraph one.\n' +
      'My reply to paragraph one.\n' +
      '\n' +
      '> Original paragraph two.\n' +
      'My reply to paragraph two.';
    const { fresh, quoted } = splitQuotedText(body);
    expect(fresh).toContain('My reply to paragraph one.');
    expect(fresh).toContain('My reply to paragraph two.');
    expect(quoted).toContain('> Original paragraph one.');
    expect(quoted).toContain('> Original paragraph two.');
    expect(quoted).not.toContain('My reply to paragraph one.');
    expect(quoted).not.toContain('My reply to paragraph two.');
  });

  it('does not alter a reply with no trailing content (standard quote-at-end case)', () => {
    // Regression guard: purely-trailing quoted blocks must keep working.
    const body =
      'Thanks!\n' +
      '\n' +
      'Am 27.04.2026 um 10:00 schrieb Bob <b@x.test>:\n' +
      '> hallo\n' +
      '> welt';
    const { fresh, quoted } = splitQuotedText(body);
    expect(fresh).toBe('Thanks!');
    expect(quoted).toContain('Am 27.04.2026');
    expect(quoted).toContain('> hallo');
    expect(quoted).not.toContain('Thanks!');
  });

  it('returns only trailing content as fresh when there is no pre-quote text', () => {
    // Message starts immediately with the attributed quote, new text follows.
    const body = '> Quoted only.\n\nNew text after.';
    const { fresh, quoted } = splitQuotedText(body);
    expect(fresh).toBe('New text after.');
    expect(quoted).toBe('> Quoted only.');
  });
});
