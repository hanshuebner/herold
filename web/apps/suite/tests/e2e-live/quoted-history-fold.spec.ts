/**
 * quoted-history-fold.spec.ts (re #455)
 *
 * End-to-end acceptance spec for the Suite's quoted-history fold
 * (`sanitize.ts`'s `collapseQuotedRegions`), which has been the subject of
 * seven tickets (#32, #49, #292, #422, #441, #448, #451) with #448 alone
 * absorbing four follow-up rounds in a single day. Per CLAUDE.md's fix-on-
 * fix cap, the missing acceptance test comes before any further symptom
 * fix: this spec opens real messages in a live Suite reader, against a
 * real herold backend, and asserts what is VISIBLE and what is BEHIND THE
 * CHIP in the live DOM -- the same distinction every one of those seven
 * tickets was actually about, which a string-position unit assertion
 * (`sanitize.test.ts`, `wrapcorpus448.test.ts`) cannot exercise: those run
 * the sanitizer in isolation and never render a byte of it.
 *
 * The corpus is the seven shapes established across #448 and #451, taken
 * from their own fixtures verbatim (see `sanitize.test.ts`'s "Thunderbird
 * attribution split", "a passed-over div then a nested reply-before-quote",
 * "leading children lifted out", "a bottom-posted reply outside the
 * citation's wrapper", and "ordinary prose matching the attribution regex"
 * describe blocks):
 *
 *   S1 - a Thunderbird reply written into a citation-prefix div: the reply
 *        stays visible, the attribution and quote fold.
 *   S2 - the same shape with an attribution the heuristic does not
 *        recognise: the whole div (reply AND unrecognised attribution
 *        line) stays visible, only the quote folds.
 *   S3 - a passed-over (unrecognised-attribution) citation-prefix div,
 *        followed by a blockquote holding a nested reply-before-quote: the
 *        nested reply's own historical text stays INSIDE the fold rather
 *        than leaking out as though freshly written.
 *   S4 - a quoted top-post (the correspondent's own words, sitting ahead
 *        of a still-older citation) that must not be lifted out just
 *        because the sender's own text precedes the whole quote elsewhere
 *        in the document.
 *   S5 - a bottom-posted reply outside the citation's wrapper: the reply
 *        vetoes the fold entirely (nothing folds) and is not absorbed
 *        into anything.
 *   S6 - ordinary prose shaped like an attribution line ("On the
 *        anniversary, my grandmother always wrote:") standing before a
 *        genuine citation-prefix div: the sender's own text stays visible
 *        (re #451; this is the collision that motivated this ticket).
 *   S7 - a correspondent's leading paragraph, interleaved between a real
 *        attribution and the quote it introduces, that must not leak out
 *        as though it were the reader's own fresh text (re #451, "Body B").
 *
 * Each shape runs plain and wrapped in an unrelated paragraph: leading,
 * trailing, and both (re #448 third follow-up and #451's own wrap corpus
 * found regressions exactly this way). A trailing paragraph legitimately
 * vetoes the fold altogether for every shape that would otherwise fold --
 * this is the pre-existing #32/#49 bottom-post rule, confirmed against the
 * live sanitizer before writing this spec (see the "trailing"/"both"
 * expectations below) -- so the "trailing"/"both" variants assert NO fold
 * happens and everything renders visible, while "plain"/"leading" assert
 * the fold happens exactly as the unwrapped fixture documents. S5 already
 * has no fold in its plain form and stays that way under every wrap.
 *
 * One herold instance, one login, one SMTP connection per message (this
 * arrangement's own established pattern, thread-back-navigation.spec.ts).
 * All 28 fixtures (7 shapes x 4 wrap variants) are delivered up front, then
 * checked in a single test via `test.step`, each step visiting the
 * message's own thread route directly (bypassing the folder list) so the
 * whole corpus is exercised in one browser session rather than one
 * instance per shape.
 *
 * Requires a live herold instance (scripts/dev-instance.sh) and:
 *   SUITE_URL - the instance's Suite URL (Vite dev server)
 *   SMTP_ADDR - host:port of the instance's SMTP listener
 *
 * Run with:
 *   SUITE_URL=http://localhost:PORT SMTP_ADDR=127.0.0.1:PORT \
 *     pnpm --filter @herold/suite exec playwright test \
 *       --config=playwright.live.config.ts tests/e2e-live/quoted-history-fold.spec.ts
 *
 * Not part of the `test:e2e` / `test:e2e:all` mocked CI lane, matching
 * every other spec in this directory.
 */

import { test, expect, type Page, type APIRequestContext, type FrameLocator, type Locator } from '@playwright/test';
import net from 'node:net';
import { login, clearMailbox, jmapSession, jmapCall, findEmailIdsBySubject, ALICE } from './live-helpers';

const SMTP_ADDR = process.env.SMTP_ADDR;

test.skip(!SMTP_ADDR, 'SMTP_ADDR not set -- run against scripts/dev-instance.sh');

/**
 * Minimal SMTP client delivering a single-part `text/html` message: EHLO,
 * MAIL FROM, RCPT TO, DATA, QUIT. Mirrors thread-back-navigation.spec.ts's
 * `sendSmtp`, extended with a `Content-Type: text/html` header so the
 * fold-relevant markup actually reaches the Suite as an HTML body rather
 * than being rendered as literal text.
 */
async function sendHtmlSmtp(
  addr: string,
  from: string,
  to: string,
  subject: string,
  html: string,
): Promise<void> {
  const [host, portStr] = addr.split(':');
  const port = Number(portStr);

  await new Promise<void>((resolve, reject) => {
    const socket = net.createConnection({ host, port });
    let buf = '';
    const steps = [`EHLO test.local\r\n`, `MAIL FROM:<${from}>\r\n`, `RCPT TO:<${to}>\r\n`, `DATA\r\n`];
    let stepIdx = 0;
    let inData = false;

    socket.setEncoding('utf8');
    socket.on('error', reject);
    socket.on('data', (chunk: string) => {
      buf += chunk;
      if (!buf.endsWith('\r\n')) return;
      const lastLine = buf.trim().split('\r\n').pop() ?? '';
      buf = '';
      const code = lastLine.slice(0, 3);

      if (inData) {
        if (code === '250') {
          socket.write('QUIT\r\n');
          socket.end();
          resolve();
        } else {
          reject(new Error(`SMTP DATA rejected: ${lastLine}`));
        }
        return;
      }

      if (!code.startsWith('2') && !code.startsWith('3')) {
        reject(new Error(`SMTP error at step ${stepIdx}: ${lastLine}`));
        return;
      }

      if (stepIdx < steps.length) {
        socket.write(steps[stepIdx]!);
        stepIdx++;
      } else if (!inData) {
        // Dot-stuff any line that starts with a literal '.' per RFC 5321.
        const stuffed = html.replace(/\r\n\./g, '\r\n..');
        const msg =
          `From: ${from}\r\n` +
          `To: ${to}\r\n` +
          `Subject: ${subject}\r\n` +
          `Date: ${new Date().toUTCString()}\r\n` +
          `Message-ID: <${Math.random().toString(36).slice(2)}@test.local>\r\n` +
          `MIME-Version: 1.0\r\n` +
          `Content-Type: text/html; charset=utf-8\r\n` +
          `\r\n` +
          `${stuffed}\r\n` +
          `.\r\n`;
        inData = true;
        socket.write(msg);
      }
    });
  });
}

/** Resolve a subject to its thread id via `Email/query` + `Email/get`. */
async function threadIdForSubject(
  request: APIRequestContext,
  apiUrl: string,
  cookieHeader: string,
  mailAccountId: string,
  subject: string,
): Promise<string> {
  const [emailId] = await findEmailIdsBySubject(request, apiUrl, cookieHeader, mailAccountId, subject);
  const body = await jmapCall(
    request,
    apiUrl,
    cookieHeader,
    ['urn:ietf:params:jmap:core', 'urn:ietf:params:jmap:mail'],
    [['Email/get', { accountId: mailAccountId, ids: [emailId], properties: ['threadId'] }, 'g']],
  );
  const responses = body.methodResponses as [string, { list: { threadId: string }[] }, string][];
  return responses[0]![1].list[0]!.threadId;
}

/** One assertable piece of text in a fixture's rendered body. */
interface Marker {
  text: string;
  /** True when this text is the correspondent's/historical text that the
   *  fold is expected to hide when folding happens. */
  foldsAway: boolean;
  /** Element-level locator used for a live-DOM visibility assertion, when
   *  the text maps to a real element rather than a bare text run. Bare
   *  text runs (no element wraps just that substring) are still checked
   *  structurally, via containment in/out of `.herold-quoted`. */
  locator?: (frame: FrameLocator) => Locator;
}

interface Shape {
  id: string;
  html: string;
  markers: Marker[];
  /** False for a shape that never folds regardless of wrap (S5). */
  foldsWhenUnwrapped: boolean;
}

const byClass = (cls: string) => (frame: FrameLocator) => frame.locator(cls);
const byExactText = (text: string) => (frame: FrameLocator) => frame.getByText(text, { exact: true });

const shapes: Shape[] = [
  {
    id: 's1-thunderbird-reply',
    foldsWhenUnwrapped: true,
    html:
      '<div class="moz-cite-prefix">Hallo Jane,<br><br>das passt mir gut.<br><br>' +
      'Am 20.09.26 um 14:12 schrieb ' +
      '<a class="moz-txt-link-abbreviated" href="mailto:jane@example.test">jane@example.test</a>:<br></div>\n' +
      '<blockquote type="cite" cite="mid:abc@example.test">Original quoted text.</blockquote>',
    markers: [
      { text: 'Hallo Jane,', foldsAway: false },
      { text: 'das passt mir gut.', foldsAway: false },
      { text: 'Am 20.09.26 um 14:12 schrieb', foldsAway: true, locator: byClass('.moz-cite-prefix') },
      { text: 'Original quoted text.', foldsAway: true, locator: byExactText('Original quoted text.') },
    ],
  },
  {
    id: 's2-unrecognised-attribution',
    foldsWhenUnwrapped: true,
    html:
      '<div class="moz-cite-prefix">Hallo Jane,<br><br>das passt mir gut.<br><br>' +
      'Op 20-09-26 om 14:12 schreef ' +
      '<a class="moz-txt-link-abbreviated" href="mailto:jane@example.test">jane@example.test</a>:<br></div>\n' +
      '<blockquote type="cite" cite="mid:abc@example.test">Original quoted text.</blockquote>',
    markers: [
      { text: 'Hallo Jane,', foldsAway: false, locator: byClass('.moz-cite-prefix') },
      { text: 'das passt mir gut.', foldsAway: false, locator: byClass('.moz-cite-prefix') },
      { text: 'Op 20-09-26 om 14:12 schreef', foldsAway: false, locator: byClass('.moz-cite-prefix') },
      { text: 'Original quoted text.', foldsAway: true, locator: byExactText('Original quoted text.') },
    ],
  },
  {
    id: 's3-compound-leak',
    foldsWhenUnwrapped: true,
    html:
      '<div class="moz-cite-prefix">Hallo Jane,<br><br>das passt mir gut.<br><br>' +
      'Op 20-09-26 om 14:12 schreef ' +
      '<a class="moz-txt-link-abbreviated" href="mailto:jane@example.test">jane@example.test</a>:<br></div>\n' +
      '<blockquote type="cite">' +
      '<div>Older reply text that should stay hidden behind the fold.</div>' +
      '<div>Am 10.09.26 um 18:21 schrieb John Doe:<br>' +
      '<blockquote type="cite">Original original text.</blockquote>' +
      '</div>' +
      '</blockquote>',
    markers: [
      { text: 'Hallo Jane,', foldsAway: false, locator: byClass('.moz-cite-prefix') },
      { text: 'das passt mir gut.', foldsAway: false, locator: byClass('.moz-cite-prefix') },
      { text: 'Op 20-09-26 om 14:12 schreef', foldsAway: false, locator: byClass('.moz-cite-prefix') },
      {
        text: 'Older reply text that should stay hidden behind the fold.',
        foldsAway: true,
        locator: byExactText('Older reply text that should stay hidden behind the fold.'),
      },
      { text: 'Original original text.', foldsAway: true, locator: byExactText('Original original text.') },
    ],
  },
  {
    id: 's4-quoted-top-post',
    foldsWhenUnwrapped: true,
    html:
      '<p>F5</p><p>Mit freundlichen Gruessen</p>\n' +
      '<div><p>Le 15 septembre 2026 a 18:21, Alice a ecrit:</p>\n' +
      '<blockquote type="cite"><p>Q5i</p>\n' +
      '<div class="moz-cite-prefix">Am 14.09.26 um 08:00 schrieb bob@example.test:<br></div>\n' +
      '<blockquote type="cite">Q5</blockquote></blockquote></div>\n',
    markers: [
      { text: 'F5', foldsAway: false, locator: byExactText('F5') },
      { text: 'Mit freundlichen Gruessen', foldsAway: false, locator: byExactText('Mit freundlichen Gruessen') },
      {
        text: 'Le 15 septembre 2026 a 18:21, Alice a ecrit:',
        foldsAway: true,
        locator: byExactText('Le 15 septembre 2026 a 18:21, Alice a ecrit:'),
      },
      { text: 'Q5i', foldsAway: true, locator: byExactText('Q5i') },
      {
        text: 'Am 14.09.26 um 08:00 schrieb bob@example.test:',
        foldsAway: true,
        locator: byClass('.moz-cite-prefix'),
      },
      { text: 'Q5', foldsAway: true, locator: byExactText('Q5') },
    ],
  },
  {
    id: 's5-bottom-posted-veto',
    foldsWhenUnwrapped: false,
    html:
      '<div class="moz-forward-container">\n' +
      '<div class="moz-cite-prefix">Am 15.09.26 um 18:21 schrieb Alice:<br></div>\n' +
      '<blockquote type="cite">Q9</blockquote></div>\n' +
      '<p>F9 written below the quote</p>\n',
    markers: [
      {
        text: 'Am 15.09.26 um 18:21 schrieb Alice:',
        foldsAway: false,
        locator: byClass('.moz-cite-prefix'),
      },
      { text: 'Q9', foldsAway: false, locator: byExactText('Q9') },
      {
        text: 'F9 written below the quote',
        foldsAway: false,
        locator: byExactText('F9 written below the quote'),
      },
    ],
  },
  {
    id: 's6-grandmother-collision',
    foldsWhenUnwrapped: true,
    html:
      '<p>On the anniversary, my grandmother always wrote:</p>\n' +
      '<div class="moz-cite-prefix">Hallo Jane,<br><br>das passt mir gut.<br><br>Am 20.09.26 um 14:12 schrieb ' +
      '<a class="moz-txt-link-abbreviated" href="mailto:jane@example.test">jane@example.test</a>:<br></div>\n' +
      '<blockquote type="cite">Original quoted text.</blockquote>',
    markers: [
      {
        text: 'On the anniversary, my grandmother always wrote:',
        foldsAway: false,
        locator: byExactText('On the anniversary, my grandmother always wrote:'),
      },
      { text: 'Hallo Jane,', foldsAway: false },
      { text: 'das passt mir gut.', foldsAway: false },
      { text: 'Am 20.09.26 um 14:12 schrieb', foldsAway: true, locator: byClass('.moz-cite-prefix') },
      { text: 'Original quoted text.', foldsAway: true, locator: byExactText('Original quoted text.') },
    ],
  },
  {
    id: 's7-correspondent-own-paragraph',
    foldsWhenUnwrapped: true,
    html:
      '<p>On Mon, 15 Sep 2026, Alice wrote:</p>' +
      '<p>Prose of my own in between.</p>' +
      '<blockquote type="cite"><p>What Alice wrote above her own quote.</p>' +
      '<div class="moz-cite-prefix">Am 14.09.26 um 08:00 schrieb bob@example.test:<br></div>' +
      '<blockquote type="cite">The oldest message.</blockquote></blockquote>',
    markers: [
      {
        text: 'On Mon, 15 Sep 2026, Alice wrote:',
        foldsAway: false,
        locator: byExactText('On Mon, 15 Sep 2026, Alice wrote:'),
      },
      {
        text: 'Prose of my own in between.',
        foldsAway: false,
        locator: byExactText('Prose of my own in between.'),
      },
      {
        text: 'What Alice wrote above her own quote.',
        foldsAway: true,
        locator: byExactText('What Alice wrote above her own quote.'),
      },
      {
        text: 'Am 14.09.26 um 08:00 schrieb bob@example.test:',
        foldsAway: true,
        locator: byClass('.moz-cite-prefix'),
      },
      { text: 'The oldest message.', foldsAway: true, locator: byExactText('The oldest message.') },
    ],
  },
];

const LEAD = '<p>Unrelated leading paragraph.</p>\n';
const TAIL = '\n<p>Unrelated trailing paragraph.</p>';

type WrapVariant = 'plain' | 'leading' | 'trailing' | 'both';
const WRAP_VARIANTS: WrapVariant[] = ['plain', 'leading', 'trailing', 'both'];

function wrap(html: string, variant: WrapVariant): string {
  switch (variant) {
    case 'plain':
      return html;
    case 'leading':
      return LEAD + html;
    case 'trailing':
      return html + TAIL;
    case 'both':
      return LEAD + html + TAIL;
  }
}

/**
 * A trailing paragraph legitimately vetoes the fold altogether for a shape
 * that would otherwise fold (the #32/#49 bottom-post rule) -- confirmed
 * directly against `sanitizeHtml` for all seven shapes before writing
 * this spec (leading-only left every shape's outcome unchanged; trailing
 * and both suppressed the fold for every shape except S5, which has no
 * fold to suppress). This function encodes that, per shape and variant,
 * rather than assuming the wrap is a no-op.
 */
function expectsFold(shape: Shape, variant: WrapVariant): boolean {
  if (!shape.foldsWhenUnwrapped) return false;
  return variant === 'plain' || variant === 'leading';
}

async function assertFoldPlacement(frame: FrameLocator, shape: Shape, variant: WrapVariant): Promise<void> {
  const expectFold = expectsFold(shape, variant);
  const details = frame.locator('.herold-quoted');
  await expect(details).toHaveCount(expectFold ? 1 : 0);

  const wrapMarkers: Marker[] = [];
  if (variant === 'leading' || variant === 'both') {
    wrapMarkers.push({
      text: 'Unrelated leading paragraph.',
      foldsAway: false,
      locator: byExactText('Unrelated leading paragraph.'),
    });
  }
  if (variant === 'trailing' || variant === 'both') {
    wrapMarkers.push({
      text: 'Unrelated trailing paragraph.',
      foldsAway: false,
      locator: byExactText('Unrelated trailing paragraph.'),
    });
  }
  const markers = [...shape.markers, ...wrapMarkers];

  for (const marker of markers) {
    const shouldBeFolded = expectFold && marker.foldsAway;
    if (expectFold) {
      if (shouldBeFolded) {
        await expect(details).toContainText(marker.text);
      } else {
        await expect(details).not.toContainText(marker.text);
      }
    }
    if (marker.locator) {
      const loc = marker.locator(frame).first();
      if (shouldBeFolded) {
        await expect(loc).not.toBeVisible();
      } else {
        await expect(loc).toBeVisible();
      }
    }
  }

  if (expectFold) {
    await frame.locator('.herold-quoted summary').click();
    for (const marker of markers) {
      if (marker.foldsAway && marker.locator) {
        await expect(marker.locator(frame).first()).toBeVisible();
      }
    }
  }
}

// Per-run token so a stale index entry from a previous run against a
// shared mailbox can never satisfy a later run's subject query. The JMAP
// `subject:` filter is a bag-of-words AND match (storefts's
// `appendFieldQueries`, `bleve.MatchQueryOperatorAnd`), not an exact
// phrase match: it is satisfied by any message whose subject contains
// every token of the query string, in any order. Shape keys must
// therefore never share a word with a wrap-variant name ("plain",
// "leading", "trailing", "both") -- an earlier version of this spec used
// a key containing "leading", which made the "leading" query also match
// the same shape's "both" message.
const RUN_TOKEN = Math.random().toString(36).slice(2, 8);

function subjectFor(shape: Shape, variant: WrapVariant): string {
  return `Fold corpus ${RUN_TOKEN} ${shape.id} ${variant} (re #455)`;
}

test.describe('quoted-history fold corpus (re #455)', () => {
  test('every shape renders with the correct text visible by default, behind the chip, and revealed on expand', async ({
    page,
    request,
  }) => {
    // 7 shapes x 4 wrap variants, each opened via its own thread route and
    // asserted against the live rendered iframe -- budget generously above
    // the 30s Playwright default.
    test.setTimeout(180_000);

    await login(page);
    await clearMailbox(page, request);

    for (const shape of shapes) {
      for (const variant of WRAP_VARIANTS) {
        await sendHtmlSmtp(
          SMTP_ADDR!,
          'sender@example.com',
          ALICE,
          subjectFor(shape, variant),
          wrap(shape.html, variant),
        );
      }
    }

    const { cookieHeader, mailAccountId, apiUrl } = await jmapSession(page, request);

    for (const shape of shapes) {
      for (const variant of WRAP_VARIANTS) {
        const subject = subjectFor(shape, variant);
        await test.step(subject, async () => {
          const threadId = await threadIdForSubject(
            request,
            apiUrl,
            cookieHeader,
            mailAccountId,
            subject,
          );
          await page.goto(`/#/mail/thread/${threadId}`);
          await expect(page.locator('.thread-frame h1')).toHaveText(subject);

          const frame = page.frameLocator('iframe[title="Message body"]');
          // The iframe renders via `srcdoc`; wait for its own body to be
          // present before asserting on its contents.
          await expect(frame.locator('body')).toBeVisible();

          await assertFoldPlacement(frame, shape, variant);
        });
      }
    }
  });
});
