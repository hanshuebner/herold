/**
 * Tests for splitQuotedText.
 *
 * Contract (per maintainer spec):
 *
 * 1. Collapse AT MOST ONE citation — the single contiguous trailing quoted
 *    region. Never reorder content.
 * 2. Collapse only when the trailing quoted region is followed by nothing
 *    real (empty / whitespace / signature).
 * 3. If the trailing quoted region is followed by real content, collapse
 *    NOTHING — whole body in head, in order.
 * 4. Signature block ("^-- ?$" to end) is always in tail (shown), not
 *    moved into head.
 * 5. Order invariant: head + collapsed + tail reconstructs the visible
 *    output in source order.
 */
import { describe, it, expect } from 'vitest';
import { splitQuotedText } from './quoted';

// ── helper ────────────────────────────────────────────────────────────────────

/** Concatenates head + collapsed + tail to verify source-order preservation. */
function visibleOrder(head: string, collapsed: string, tail: string): string {
  return [head, collapsed, tail].filter(Boolean).join('\n');
}

// ── shape 1: classic top-post — [head][trailing quote] ───────────────────────

describe('splitQuotedText — classic top-post (fresh text above a trailing citation)', () => {
  it('splits at an English attribution line followed by a quoted block', () => {
    const body =
      'Thanks for the doc.\n' +
      '\n' +
      'On Mon, Apr 28, 2026 at 9:01 AM, Alice <a@x.test> wrote:\n' +
      '> First, the goals.\n' +
      '> Second, the approach.';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(head).toBe('Thanks for the doc.');
    expect(collapsed).toContain('On Mon, Apr 28, 2026');
    expect(collapsed).toContain('> First, the goals.');
    expect(tail).toBe('');

    // Order invariant: visible output matches source order.
    expect(visibleOrder(head, collapsed, tail)).toBe(
      'Thanks for the doc.\n' +
        'On Mon, Apr 28, 2026 at 9:01 AM, Alice <a@x.test> wrote:\n' +
        '> First, the goals.\n' +
        '> Second, the approach.',
    );
  });

  it('splits at a German attribution line', () => {
    const body =
      'Danke!\n' +
      '\n' +
      'Am 27.04.2026 um 10:00 schrieb Bob <b@x.test>:\n' +
      '> hallo';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(head).toBe('Danke!');
    expect(collapsed).toContain('Am 27.04.2026');
    expect(collapsed).toContain('> hallo');
    expect(tail).toBe('');

    expect(visibleOrder(head, collapsed, tail)).toBe(
      'Danke!\nAm 27.04.2026 um 10:00 schrieb Bob <b@x.test>:\n> hallo',
    );
  });

  it('splits at a bare quote-prefix line with no attribution', () => {
    const body = 'See my reply inline.\n\n> The original\n> said this.';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(head).toBe('See my reply inline.');
    expect(collapsed).toContain('> The original');
    expect(tail).toBe('');

    expect(visibleOrder(head, collapsed, tail)).toBe(
      'See my reply inline.\n> The original\n> said this.',
    );
  });

  it('collapses deeply-nested ">>" quote prefix', () => {
    const body = 'Reply.\n\n>> earlier\n>> nested';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(head).toBe('Reply.');
    expect(collapsed).toContain('>> earlier');
    expect(tail).toBe('');
  });

  it('collapses a quoted block with blank lines within the run', () => {
    const body =
      'My text.\n' +
      '\n' +
      '> Quoted paragraph 1.\n' +
      '\n' +
      '> Quoted paragraph 2.';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(head).toBe('My text.');
    expect(collapsed).toContain('> Quoted paragraph 1.');
    expect(collapsed).toContain('> Quoted paragraph 2.');
    expect(tail).toBe('');

    // Both quoted paragraphs appear after head in source order.
    expect(visibleOrder(head, collapsed, tail)).toBe(
      'My text.\n> Quoted paragraph 1.\n\n> Quoted paragraph 2.',
    );
  });

  // re #234: Thunderbird's German locale puts the sender's name BEFORE
  // "schrieb" ("<Name> schrieb am <date> um <time>:"), unlike the
  // Am-first shape covered above.
  it('splits at a German name-first attribution line ("<Name> schrieb am ... um ...:")', () => {
    const body =
      'Danke für die Einladung.\n' +
      '\n' +
      'Hans Hübner (Vorstandsvorsitzender VzEkC e.V.) schrieb am 13.07.26 um 16:53:\n' +
      '> Liebe Mitglieder,\n' +
      '> die nächste Vereinsversammlung findet am 20. Juli statt.';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(head).toBe('Danke für die Einladung.');
    expect(collapsed).toContain('Hans Hübner (Vorstandsvorsitzender VzEkC e.V.) schrieb am 13.07.26 um 16:53:');
    expect(collapsed).toContain('> Liebe Mitglieder,');
    expect(tail).toBe('');

    expect(visibleOrder(head, collapsed, tail)).toBe(
      'Danke für die Einladung.\n' +
        'Hans Hübner (Vorstandsvorsitzender VzEkC e.V.) schrieb am 13.07.26 um 16:53:\n' +
        '> Liebe Mitglieder,\n' +
        '> die nächste Vereinsversammlung findet am 20. Juli statt.',
    );
  });

  it('does not treat an ordinary German sentence mentioning "schrieb am" as an attribution line', () => {
    // Heuristic discipline: genuine prose that happens to contain the words
    // "schrieb am" / "um" must NOT be mistaken for an auto-generated
    // citation line just because a quote follows somewhere later. This
    // sentence has no date/time shape and does not end in a colon, so it
    // must stay in head, not fold into the citation.
    const body =
      'Ich erinnere mich noch, wie er schrieb am Wochenende um die Ecke zu fahren.\n' +
      '\n' +
      '> Older quoted history.';
    const { head, collapsed } = splitQuotedText(body);

    expect(head).toContain('Ich erinnere mich noch, wie er schrieb am Wochenende um die Ecke zu fahren.');
    expect(collapsed).toBe('> Older quoted history.');
  });
});

// ── shape 2: bottom-post — [quote][fresh] (ticket #116 example) ──────────────

describe('splitQuotedText — bottom-post (new text follows the quote)', () => {
  it('collapses NOTHING when the trailing-most line is not quoted (ticket example)', () => {
    // Exact body from issue #116: the sender's new text ("das ist noch nicht
    // alles") follows the quoted block. The trailing-most non-blank line is
    // NOT >-prefixed, so there is no trailing citation to collapse.
    const body =
      'Am 2026-07-04 17:07, schrieb Hans Hübner:\n' +
      '> es geht, finally!\n' +
      '> ...\n' +
      '>> hjgjh\n' +
      '\n' +
      'das ist noch nicht alles';
    const { head, collapsed, tail } = splitQuotedText(body);

    // Nothing collapsed — entire body rendered verbatim.
    expect(collapsed).toBe('');
    expect(tail).toBe('');
    expect(head).toBe(body);

    // The new content appears in head, in order.
    expect(head).toContain('das ist noch nicht alles');
    // The quoted attribution also appears in head (in order, not moved).
    expect(head).toContain('Am 2026-07-04 17:07, schrieb Hans Hübner:');

    // Order invariant: head alone equals the full body.
    expect(visibleOrder(head, collapsed, tail)).toBe(body);
  });

  it('collapses NOTHING when non-blank content follows the last >-prefixed line', () => {
    const body = '> A quoted line.\n\nFresh reply after the quote.';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(collapsed).toBe('');
    expect(head).toBe(body);
    expect(visibleOrder(head, collapsed, tail)).toBe(body);
  });
});

// ── shape 3: signature-only tail folds with the quote (re #234) ──────────────
//
// findSigDelimiter scans the WHOLE message, so whenever a citation is also
// found the signature necessarily immediately follows it (see splitQuotedText's
// rule 4 doc comment) -- there is no "quote, then real content, then
// signature" shape reachable through this function. The signature therefore
// always folds into `collapsed` alongside the citation; `tail` stays empty.

describe('splitQuotedText — quote followed only by a signature folds together (re #234)', () => {
  it('folds the citation and its trailing signature into collapsed; tail stays empty', () => {
    const body =
      'My text.\n' +
      '\n' +
      'On Mon, Alice <a@x.test> wrote:\n' +
      '> Quoted.\n' +
      '\n' +
      '-- \n' +
      'My signature.';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(head).toBe('My text.');
    expect(collapsed).toContain('On Mon, Alice');
    expect(collapsed).toContain('> Quoted.');
    // The signature folds away with the quote instead of staying visible.
    expect(collapsed).toContain('-- \nMy signature.');
    expect(tail).toBe('');
    expect(head).not.toContain('signature');

    // Order invariant: head → collapsed → tail matches source order. (The
    // blank separator between head and the citation is consumed like any
    // other head/citation boundary; the blank line WITHIN collapsed,
    // between the quote and the signature, is preserved verbatim.)
    expect(visibleOrder(head, collapsed, tail)).toBe(
      'My text.\n' +
        'On Mon, Alice <a@x.test> wrote:\n' +
        '> Quoted.\n' +
        '\n' +
        '-- \n' +
        'My signature.',
    );
  });

  it('accepts "--" (without trailing space) as the signature delimiter', () => {
    const body = 'Hello.\n\n> Quoted.\n\n--\nSig.';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(collapsed).toContain('> Quoted.');
    expect(collapsed).toContain('--\nSig.');
    expect(tail).toBe('');
    expect(head).not.toContain('Sig.');
  });

  it('does not fold the signature when real content sits between the quote and it', () => {
    // "some final remark" is real, non-quoted content between the quote and
    // the signature delimiter -- the trailing-most line of the pre-signature
    // body is not quoted, so findTrailingCitation finds no citation at all
    // and nothing collapses; the signature stays visible, unfolded, in head.
    const body = '> quoted\n\nsome final remark\n\n-- \nSig';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(collapsed).toBe('');
    expect(tail).toBe('');
    expect(head).toBe(body);
    expect(head).toContain('Sig');
  });

  it('leaves a signature with no citation visible in head (nothing to fold with)', () => {
    const body = 'Just a note.\n\n-- \nSig only, no quote.';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(collapsed).toBe('');
    expect(tail).toBe('');
    expect(head).toBe(body);
  });
});

// ── miscellaneous / edge cases ────────────────────────────────────────────────

describe('splitQuotedText — edge cases', () => {
  it('returns empty head/collapsed/tail for an empty body', () => {
    expect(splitQuotedText('')).toEqual({ head: '', collapsed: '', tail: '' });
  });

  it('does not treat a sigdash as a quote boundary when there is no citation', () => {
    // "--" is a sig delimiter, but with no trailing >-block there is no
    // citation, so the whole body is returned as head.
    const body = 'Thanks!\n--\nAlice';
    const { head, collapsed, tail } = splitQuotedText(body);
    expect(collapsed).toBe('');
    expect(tail).toBe('');
    expect(head).toBe(body);
  });

  it('does not split on a single ">" embedded in a sentence', () => {
    const body = 'I think a > b in this case.';
    const { collapsed } = splitQuotedText(body);
    expect(collapsed).toBe('');
  });

  it('collapses the whole body when the entire message is a citation', () => {
    const body = 'On Mon, Alice wrote:\n> Quoted only.';
    const { head, collapsed, tail } = splitQuotedText(body);

    expect(head).toBe('');
    expect(collapsed).toContain('On Mon, Alice wrote:');
    expect(collapsed).toContain('> Quoted only.');
    expect(tail).toBe('');
  });

  it('only collapses the LAST trailing citation when multiple quoted blocks exist', () => {
    // An inline reply: fresh, first quoted block, fresh, second quoted block.
    // Only the trailing (second) quoted block qualifies; the first, which has
    // non-quoted content after it, is shown verbatim in head.
    const body =
      'My reply to paragraph one.\n' +
      '\n' +
      '> Original paragraph one.\n' +
      '\n' +
      'My reply to paragraph two.\n' +
      '\n' +
      '> Original paragraph two.';
    const { head, collapsed, tail } = splitQuotedText(body);

    // The trailing citation is "> Original paragraph two."
    expect(collapsed).toBe('> Original paragraph two.');
    // Everything before the trailing citation appears verbatim in head.
    expect(head).toContain('My reply to paragraph one.');
    expect(head).toContain('> Original paragraph one.');
    expect(head).toContain('My reply to paragraph two.');
    expect(tail).toBe('');

    // Order invariant.
    expect(visibleOrder(head, collapsed, tail)).toBe(
      'My reply to paragraph one.\n' +
        '\n' +
        '> Original paragraph one.\n' +
        '\n' +
        'My reply to paragraph two.\n' +
        '> Original paragraph two.',
    );
  });
});
