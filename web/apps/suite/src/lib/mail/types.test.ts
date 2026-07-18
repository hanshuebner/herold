/**
 * Tests for the RFC 8621 4.1.4 multi-part body rendering in
 * `emailHtmlBody` / `emailTextBody` (re #258).
 *
 * `htmlBody` / `textBody` are ORDERED LISTS of parts to display, not a
 * single dominant part. A multipart/mixed message whose leading
 * text/plain sibling (a forwarder's own note) has no text/html
 * counterpart at its own level gets that leaf promoted into BOTH lists
 * by the server (`internal/protojmap/mail/email/render.go`,
 * commit 2949c678). The client must render every entry, in order,
 * according to its own type:
 *   - text/html entries render as sanitized HTML (unchanged from before).
 *   - text/plain entries appearing in htmlBody are HTML-escaped and
 *     wrapped in <pre> so they render as literal text, never as markup.
 *
 * Single-part messages (the overwhelmingly common case) must render
 * byte-identical to the pre-#258 behaviour — covered by the "single
 * part" describe block below and by the pre-existing re #44 coverage in
 * store.test.ts.
 */

import { describe, it, expect } from 'vitest';
import { emailHtmlBody, emailTextBody, type Email, type EmailBodyPart } from './types';

function makePart(overrides: Partial<EmailBodyPart>): EmailBodyPart {
  return {
    partId: 'p1',
    blobId: 'b1',
    size: 100,
    type: 'text/html',
    charset: 'utf-8',
    disposition: null,
    name: null,
    cid: null,
    ...overrides,
  };
}

function makeEmail(overrides: Partial<Email> = {}): Email {
  return {
    id: 'e1',
    threadId: 't1',
    mailboxIds: {},
    keywords: {},
    from: null,
    to: null,
    subject: null,
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: 'root-blob',
    ...overrides,
  } as Email;
}

describe('emailHtmlBody: single html part (no regression)', () => {
  it('returns the part value unchanged', () => {
    const email = makeEmail({
      htmlBody: [makePart({ partId: 'p1', type: 'text/html' })],
      bodyValues: { p1: { value: '<p>hello</p>', isEncodingProblem: false, isTruncated: false } },
    });
    expect(emailHtmlBody(email)).toBe('<p>hello</p>');
  });

  it('returns null when htmlBody is empty', () => {
    const email = makeEmail({ htmlBody: [], bodyValues: {} });
    expect(emailHtmlBody(email)).toBeNull();
  });

  it('returns null when htmlBody is absent', () => {
    const email = makeEmail({});
    expect(emailHtmlBody(email)).toBeNull();
  });

  it('returns null when the single part has no resolved bodyValue', () => {
    const email = makeEmail({
      htmlBody: [makePart({ partId: 'p1' })],
      bodyValues: {},
    });
    expect(emailHtmlBody(email)).toBeNull();
  });
});

describe('emailHtmlBody: issue #258 shape (leading text/plain note + html original)', () => {
  it('renders the note (escaped, preformatted) followed by the original html, in order', () => {
    const email = makeEmail({
      htmlBody: [
        makePart({ partId: 'note', type: 'text/plain' }),
        makePart({ partId: 'orig', type: 'text/html' }),
      ],
      bodyValues: {
        note: {
          value: 'Hallo zusammen, ich leite euch mal die Mail weiter.',
          isEncodingProblem: false,
          isTruncated: false,
        },
        orig: {
          value: '<p>Sehr geehrte Damen und Herren</p>',
          isEncodingProblem: false,
          isTruncated: false,
        },
      },
    });
    const result = emailHtmlBody(email);
    expect(result).not.toBeNull();
    // Note comes first, wrapped in <pre> so it renders as literal text.
    expect(result).toBe(
      '<pre>Hallo zusammen, ich leite euch mal die Mail weiter.</pre>' +
        '<p>Sehr geehrte Damen und Herren</p>',
    );
    // The note text appears before the original's text in the concatenation.
    expect(result!.indexOf('Hallo zusammen')).toBeLessThan(
      result!.indexOf('Sehr geehrte'),
    );
  });

  it('HTML-escapes a text/plain part promoted into htmlBody (XSS-adjacent)', () => {
    const email = makeEmail({
      htmlBody: [
        makePart({ partId: 'note', type: 'text/plain' }),
        makePart({ partId: 'orig', type: 'text/html' }),
      ],
      bodyValues: {
        note: {
          value: '<script>alert(1)</script> 5 < 10 & "quoted"',
          isEncodingProblem: false,
          isTruncated: false,
        },
        orig: { value: '<p>original</p>', isEncodingProblem: false, isTruncated: false },
      },
    });
    const result = emailHtmlBody(email);
    // The raw tag must never appear unescaped in the output.
    expect(result).not.toContain('<script>alert(1)</script>');
    expect(result).toContain(
      '&lt;script&gt;alert(1)&lt;/script&gt; 5 &lt; 10 &amp; &quot;quoted&quot;',
    );
  });

  it('skips a part with no resolved bodyValue and renders the rest', () => {
    const email = makeEmail({
      htmlBody: [
        makePart({ partId: 'missing', type: 'text/plain' }),
        makePart({ partId: 'orig', type: 'text/html' }),
      ],
      bodyValues: {
        orig: { value: '<p>original</p>', isEncodingProblem: false, isTruncated: false },
      },
    });
    expect(emailHtmlBody(email)).toBe('<p>original</p>');
  });
});

describe('emailHtmlBody: null when htmlBody has no genuine text/html part (re #258 follow-up, issue #233)', () => {
  // RFC 8621's symmetric-fill rule promotes a bare text/plain leaf with no
  // HTML sibling into BOTH textBody and htmlBody, so a plain-text-only
  // message now has a non-empty htmlBody too. MessageAccordion picks the
  // HTML iframe branch whenever emailHtmlBody is non-null, which would
  // silently disable the plain-text quote-collapse / "Show trimmed
  // content" toggle (issue #233) for every plain-text-only message.
  // emailHtmlBody must return null unless at least one htmlBody entry is
  // a genuine text/html part, so these messages fall through to the
  // plain-text rendering branch instead.

  it('returns null for a single symmetric-fill text/plain entry', () => {
    const email = makeEmail({
      htmlBody: [makePart({ partId: 'p1', type: 'text/plain' })],
      textBody: [makePart({ partId: 'p1', type: 'text/plain' })],
      bodyValues: {
        p1: { value: 'Just a plain text reply.', isEncodingProblem: false, isTruncated: false },
      },
    });
    expect(emailHtmlBody(email)).toBeNull();
  });

  it('returns null for two bare text/plain entries with no html anywhere', () => {
    const email = makeEmail({
      htmlBody: [
        makePart({ partId: 'a', type: 'text/plain' }),
        makePart({ partId: 'b', type: 'text/plain' }),
      ],
      bodyValues: {
        a: { value: 'first note', isEncodingProblem: false, isTruncated: false },
        b: { value: 'second note', isEncodingProblem: false, isTruncated: false },
      },
    });
    expect(emailHtmlBody(email)).toBeNull();
  });

  it('still renders when at least one entry is a genuine text/html part (re #258)', () => {
    const email = makeEmail({
      htmlBody: [
        makePart({ partId: 'note', type: 'text/plain' }),
        makePart({ partId: 'orig', type: 'text/html' }),
      ],
      bodyValues: {
        note: { value: 'the note', isEncodingProblem: false, isTruncated: false },
        orig: { value: '<p>the original</p>', isEncodingProblem: false, isTruncated: false },
      },
    });
    expect(emailHtmlBody(email)).toBe('<pre>the note</pre><p>the original</p>');
  });

  it('a genuine single text/html part still renders (no regression)', () => {
    const email = makeEmail({
      htmlBody: [makePart({ partId: 'p1', type: 'text/html' })],
      bodyValues: { p1: { value: '<p>hello</p>', isEncodingProblem: false, isTruncated: false } },
    });
    expect(emailHtmlBody(email)).toBe('<p>hello</p>');
  });
});

describe('emailHtmlBody: overrides parameter (truncation-recovery splice, re #258)', () => {
  it('substitutes only the overridden partId, leaving other parts on their inline value', () => {
    const email = makeEmail({
      htmlBody: [
        makePart({ partId: 'note', type: 'text/plain' }),
        makePart({ partId: 'orig', type: 'text/html' }),
      ],
      bodyValues: {
        note: { value: 'the note', isEncodingProblem: false, isTruncated: false },
        orig: { value: '<p>truncated...</p>', isEncodingProblem: false, isTruncated: true },
      },
    });
    const result = emailHtmlBody(email, { orig: '<p>the full recovered body</p>' });
    expect(result).toBe('<pre>the note</pre><p>the full recovered body</p>');
  });

  it('overriding a partId not present in htmlBody has no effect', () => {
    const email = makeEmail({
      htmlBody: [makePart({ partId: 'p1', type: 'text/html' })],
      bodyValues: { p1: { value: '<p>hello</p>', isEncodingProblem: false, isTruncated: false } },
    });
    expect(emailHtmlBody(email, { other: '<p>irrelevant</p>' })).toBe('<p>hello</p>');
  });
});

describe('emailTextBody: single part (no regression)', () => {
  it('returns the part value unchanged', () => {
    const email = makeEmail({
      textBody: [makePart({ partId: 'p1', type: 'text/plain' })],
      bodyValues: { p1: { value: 'plain text body', isEncodingProblem: false, isTruncated: false } },
    });
    expect(emailTextBody(email)).toBe('plain text body');
  });

  it('returns null when textBody is absent', () => {
    expect(emailTextBody(makeEmail({}))).toBeNull();
  });
});

describe('emailTextBody: issue #258 shape (note + forwarded text, in order)', () => {
  it('concatenates both parts in order, separated by a blank line', () => {
    const email = makeEmail({
      textBody: [
        makePart({ partId: 'note', type: 'text/plain' }),
        makePart({ partId: 'orig', type: 'text/plain' }),
      ],
      bodyValues: {
        note: { value: 'Hallo zusammen, ich leite weiter.', isEncodingProblem: false, isTruncated: false },
        orig: { value: 'Sehr geehrte Damen und Herren', isEncodingProblem: false, isTruncated: false },
      },
    });
    const result = emailTextBody(email);
    expect(result).toBe(
      'Hallo zusammen, ich leite weiter.\n\nSehr geehrte Damen und Herren',
    );
  });
});
