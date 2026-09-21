/**
 * Every quoted-history body used against issue #448, wrapped in an
 * unrelated leading paragraph (and, separately, both leading and
 * trailing). An independent verification pass found that a whole-document
 * "does anything precede the candidate" test the second #448 follow-up
 * added could be defeated by a single unrelated paragraph placed ahead of
 * the ticket's OWN body, reintroducing the original over-fold — one
 * paragraph away from the reported shape. This file is that stress corpus,
 * kept as a permanent regression guard: every fixture this ticket has ever
 * used, run again with padding on the side that broke.
 */
import { describe, it, expect } from 'vitest';
import { sanitizeHtml as sanitizeNew } from './sanitize';

function bodyOf(srcdoc: string): string {
  const m = srcdoc.match(/<body>([\s\S]*?)<\/body>/);
  return m?.[1] ?? '';
}

const LEAD = '<p>Unrelated leading paragraph.</p>\n';
const TAIL = '\n<p>Unrelated trailing paragraph.</p>';

const nestedReplyBlockquote =
  '<blockquote type="cite">' +
  '<div>Older reply text that should stay hidden behind the fold.</div>' +
  '<div>Am 10.09.26 um 18:21 schrieb John Doe:<br>' +
  '<blockquote type="cite">Original original text.</blockquote>' +
  '</div>' +
  '</blockquote>';

// Every body used on #448, across all three rounds: the two original
// scenarios, the compound-leak shape and its two controls, shape 1 and its
// two controls, shape 2 and its three controls, and the third-round
// regression fixture. Each entry names the substrings that MUST be the
// sender's own fresh text and must therefore never end up inside a
// `<details class="herold-quoted">` fold.
const bodies: Record<string, { html: string; freshMarkers: string[] }> = {
  '#448 ticket body': {
    html:
      '<div class="moz-cite-prefix">Hallo Jane,<br><br>das passt mir gut.<br><br>Am 20.09.26 um 14:12 schrieb ' +
      '<a class="moz-txt-link-abbreviated" href="mailto:jane@example.test">jane@example.test</a>:<br></div>\n' +
      '<blockquote type="cite" cite="mid:abc@example.test">Original quoted text.</blockquote>',
    freshMarkers: ['Hallo Jane,', 'das passt mir gut.'],
  },
  '#448 unrecognised attribution': {
    html:
      '<div class="moz-cite-prefix">Hallo Jane,<br><br>das passt mir gut.<br><br>Op 20-09-26 om 14:12 schreef ' +
      '<a class="moz-txt-link-abbreviated" href="mailto:jane@example.test">jane@example.test</a>:<br></div>\n' +
      '<blockquote type="cite" cite="mid:abc@example.test">Original quoted text.</blockquote>',
    freshMarkers: ['Hallo Jane,', 'das passt mir gut.'],
  },
  '#448 compound leak shape': {
    html:
      '<div class="moz-cite-prefix">Hallo Jane,<br><br>das passt mir gut.<br><br>' +
      'Op 20-09-26 om 14:12 schreef ' +
      '<a class="moz-txt-link-abbreviated" href="mailto:jane@example.test">jane@example.test</a>:<br></div>\n' +
      nestedReplyBlockquote,
    freshMarkers: ['Hallo Jane,', 'das passt mir gut.'],
  },
  '#448 compound control A': {
    html: nestedReplyBlockquote,
    freshMarkers: [],
  },
  '#448 compound control B': {
    html:
      '<div class="moz-cite-prefix">Hallo Jane,<br><br>das passt mir gut.<br><br>' +
      'Am 20.09.26 um 14:12 schrieb ' +
      '<a class="moz-txt-link-abbreviated" href="mailto:jane@example.test">jane@example.test</a>:<br></div>\n' +
      nestedReplyBlockquote,
    freshMarkers: ['Hallo Jane,', 'das passt mir gut.'],
  },
  '#448 shape1 reported': {
    html:
      '<p>F5</p><p>Mit freundlichen Gruessen</p>\n' +
      '<div><p>Le 15 septembre 2026 a 18:21, Alice a ecrit:</p>\n' +
      '<blockquote type="cite"><p>Q5i</p>\n' +
      '<div class="moz-cite-prefix">Am 14.09.26 um 08:00 schrieb bob@example.test:<br></div>\n' +
      '<blockquote type="cite">Q5</blockquote></blockquote></div>\n',
    freshMarkers: ['F5', 'Mit freundlichen Gruessen'],
  },
  '#448 shape1 control (no wrapper)': {
    html:
      '<blockquote type="cite"><p>Q5i</p>\n' +
      '<div class="moz-cite-prefix">Am 14.09.26 um 08:00 schrieb bob@example.test:<br></div>\n' +
      '<blockquote type="cite">Q5</blockquote></blockquote>',
    freshMarkers: [],
  },
  '#448 shape1 German variant': {
    html:
      '<p>F5b</p><p>Mit freundlichen Gruessen</p>\n' +
      '<div><p>Am 15.09.26 um 18:21 schrieb Alice:</p>\n' +
      '<blockquote type="cite"><p>Q5ib</p>\n' +
      '<div class="moz-cite-prefix">Am 14.09.26 um 08:00 schrieb bob@example.test:<br></div>\n' +
      '<blockquote type="cite">Q5b</blockquote></blockquote></div>\n',
    freshMarkers: ['F5b', 'Mit freundlichen Gruessen'],
  },
  '#448 shape2 reported': {
    html:
      '<div class="moz-forward-container">\n' +
      '<div class="moz-cite-prefix">Am 15.09.26 um 18:21 schrieb Alice:<br></div>\n' +
      '<blockquote type="cite">Q9</blockquote></div>\n',
    freshMarkers: [],
  },
  '#448 shape2 control (no wrapper)': {
    html:
      '<div class="moz-cite-prefix">Am 15.09.26 um 18:21 schrieb Alice:<br></div>\n' +
      '<blockquote type="cite">Q9</blockquote>',
    freshMarkers: [],
  },
  '#448 shape2 doubly nested': {
    html:
      '<div class="outer-wrap"><div class="moz-forward-container">\n' +
      '<div class="moz-cite-prefix">Am 15.09.26 um 18:21 schrieb Alice:<br></div>\n' +
      '<blockquote type="cite">Q9c</blockquote></div></div>\n',
    freshMarkers: [],
  },
  '#448 third-round regression': {
    html:
      '<div class="moz-cite-prefix">Hallo Jane,<br><br>das passt mir gut.<br><br>Am 20.09.26 um 14:12 schrieb ' +
      '<a class="moz-txt-link-abbreviated" href="mailto:jane@example.test">jane@example.test</a>:<br></div>\n' +
      '<blockquote type="cite">Original quoted text.</blockquote>',
    freshMarkers: ['Hallo Jane,', 'das passt mir gut.'],
  },
  '#451 collision (ordinary prose matching the attribution regex)': {
    html:
      '<p>On the anniversary, my grandmother always wrote:</p>\n' +
      '<div class="moz-cite-prefix">Hallo Jane,<br><br>das passt mir gut.<br><br>Am 20.09.26 um 14:12 schrieb ' +
      '<a class="moz-txt-link-abbreviated" href="mailto:jane@example.test">jane@example.test</a>:<br></div>\n' +
      '<blockquote type="cite">Original quoted text.</blockquote>',
    freshMarkers: [
      'On the anniversary, my grandmother always wrote:',
      'Hallo Jane,',
      'das passt mir gut.',
    ],
  },
  '#451 Body A (genuine attribution grammar the sender wrote, ahead of an unrelated later citation)': {
    html:
      '<p>Am 19.09.26 um 09:00 schrieb Bob:</p>' +
      '<div class="moz-cite-prefix">Hallo Jane,<br><br>das passt mir gut.<br><br>Am 20.09.26 um 14:12 schrieb ' +
      '<a href="mailto:jane@example.test">jane@example.test</a>:<br></div>' +
      '<blockquote type="cite">Original quoted text.</blockquote>',
    freshMarkers: ['Am 19.09.26 um 09:00 schrieb Bob:', 'Hallo Jane,', 'das passt mir gut.'],
  },
  '#451 Body B (a real interleaved paragraph must not shield the quote it precedes)': {
    html:
      '<p>On Mon, 15 Sep 2026, Alice wrote:</p>' +
      '<p>Prose of my own in between.</p>' +
      '<blockquote type="cite"><p>What Alice wrote above her own quote.</p>' +
      '<div class="moz-cite-prefix">Am 14.09.26 um 08:00 schrieb bob@example.test:<br></div>' +
      '<blockquote type="cite">The oldest message.</blockquote></blockquote>',
    freshMarkers: ['On Mon, 15 Sep 2026, Alice wrote:', 'Prose of my own in between.'],
  },
};

describe('re #448 third follow-up: every #448 fixture wrapped in an unrelated LEADING paragraph', () => {
  // Leading-only wrap: this is the shape the independent verification pass
  // actually found broken (an unrelated paragraph placed ahead of the
  // ticket's own div). A trailing paragraph is deliberately NOT added here
  // -- appending fresh content after the quote group legitimately blocks
  // any fold at all (the pre-existing #32/#49 bottom-post rule), which
  // would mask the very regression this corpus exists to catch.
  for (const [name, { html, freshMarkers }] of Object.entries(bodies)) {
    it(`${name}: the wrap paragraph and the fixture's own fresh markers never end up inside the fold`, () => {
      const wrapped = LEAD + html;
      const body = bodyOf(sanitizeNew(wrapped, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');

      // The leading wrap paragraph is always the sender's own text and must
      // never be swallowed into a fold, whether or not one occurs.
      const leadPos = body.indexOf('Unrelated leading paragraph.');
      expect(leadPos).toBeGreaterThan(-1);
      if (detailsStart >= 0) {
        expect(leadPos).toBeLessThan(detailsStart);
      }

      // Every marker the fixture author identified as the CURRENT sender's
      // own fresh text must never end up inside a fold either.
      for (const marker of freshMarkers) {
        const pos = body.indexOf(marker);
        expect(pos, `expected "${marker}" to appear in the output`).toBeGreaterThan(-1);
        if (detailsStart >= 0) {
          expect(pos, `expected "${marker}" to render outside the fold`).toBeLessThan(detailsStart);
        }
      }
    });

  }
});

describe('re #448 third follow-up: every #448 fixture wrapped in unrelated LEADING and TRAILING paragraphs (sanity only)', () => {
  // With trailing content added, folding is expected to stop happening
  // entirely for shapes that would otherwise fold (#32/#49) -- this block
  // only guards against a crash or a dropped paragraph, not fold placement.
  for (const [name, { html }] of Object.entries(bodies)) {
    it(`${name}: both wrap paragraphs survive`, () => {
      const wrapped = LEAD + html + TAIL;
      const body = bodyOf(sanitizeNew(wrapped, { loadImages: false }));
      expect(body).toContain('Unrelated leading paragraph.');
      expect(body).toContain('Unrelated trailing paragraph.');
    });
  }
});

describe('re #451: every fixture nested one level deeper inside an extra wrapping <div>', () => {
  // The ancestor-chain walk (`isBeneathACitation`) climbs past every
  // wrapper looking for a preceding sibling that introduces the level it
  // just left. Wrapping each fixture's ENTIRE body in one more containing
  // <div> pushes every candidate one ancestor level deeper without
  // changing any node's immediate siblings, so this is a regression guard
  // on the walk itself continuing to find (or correctly not find) the
  // same introducer once an extra level of ancestry sits between it and
  // the candidate.
  for (const [name, { html, freshMarkers }] of Object.entries(bodies)) {
    it(`${name}: unaffected by one extra wrapping <div>`, () => {
      const wrapped = `<div class="extra-wrap">${html}</div>`;
      const body = bodyOf(sanitizeNew(wrapped, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');
      for (const marker of freshMarkers) {
        const pos = body.indexOf(marker);
        expect(pos, `expected "${marker}" to appear in the output`).toBeGreaterThan(-1);
        if (detailsStart >= 0) {
          expect(pos, `expected "${marker}" to render outside the fold`).toBeLessThan(detailsStart);
        }
      }
    });
  }
});

describe('re #451: every fixture wrapped in a LEADING paragraph AND an extra wrapping <div>', () => {
  // Combines both stress dimensions: the unrelated leading paragraph from
  // the #448 third-follow-up corpus, plus the extra ancestor level above.
  for (const [name, { html, freshMarkers }] of Object.entries(bodies)) {
    it(`${name}: unaffected by leading paragraph plus one extra wrapping <div>`, () => {
      const wrapped = `<div class="extra-wrap">${LEAD}${html}</div>`;
      const body = bodyOf(sanitizeNew(wrapped, { loadImages: false }));
      const detailsStart = body.indexOf('<details class="herold-quoted">');
      const leadPos = body.indexOf('Unrelated leading paragraph.');
      expect(leadPos).toBeGreaterThan(-1);
      if (detailsStart >= 0) {
        expect(leadPos).toBeLessThan(detailsStart);
      }
      for (const marker of freshMarkers) {
        const pos = body.indexOf(marker);
        expect(pos, `expected "${marker}" to appear in the output`).toBeGreaterThan(-1);
        if (detailsStart >= 0) {
          expect(pos, `expected "${marker}" to render outside the fold`).toBeLessThan(detailsStart);
        }
      }
    });
  }
});
