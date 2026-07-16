/**
 * Tests for the reply/forward quote fold (issue #253): opening a reply or
 * forward shows the quoted original collapsed behind a toggle. The
 * mechanism is a view-level ProseMirror decoration (quoteFoldPlugin) that
 * never touches the document -- these tests exist primarily to nail down
 * the hard correctness requirement: whether or not the writer ever
 * expands the quote, docToHtml() / the outbound text/plain projection
 * must always carry the full quoted original.
 */
import { describe, it, expect, afterEach } from 'vitest';
import type { EditorView } from 'prosemirror-view';
import {
  createComposeEditor,
  docToHtml,
  docToText,
  isQuoteFolded,
  toggleQuoteFold,
} from './editor';
import { _internals_forTest } from './compose.svelte';
import type { Email } from '../mail/types';

const { formatReplyQuote, formatForwardQuote, htmlToPlainText } = _internals_forTest;

let view: EditorView | null = null;

function mountEditor(initialHtml: string, collapseQuote: boolean): EditorView {
  const host = document.createElement('div');
  document.body.appendChild(host);
  view = createComposeEditor(host, {
    initialHtml,
    onChange: () => undefined,
    collapseQuote,
  });
  return view;
}

afterEach(() => {
  view?.destroy();
  view = null;
});

function parentEmail(over: Partial<Email> = {}): Email {
  return {
    id: 'p1',
    threadId: 't1',
    mailboxIds: {},
    keywords: {},
    from: [{ name: 'Alice A', email: 'alice@example.test' }],
    to: [{ name: null, email: 'me@example.test' }],
    subject: 'Project plan',
    preview: '',
    receivedAt: '2026-07-10T12:00:00Z',
    sentAt: '2026-07-10T12:00:00Z',
    hasAttachment: false,
    messageId: ['<p1@example.test>'],
    references: null,
    textBody: [{ partId: '1', type: 'text/plain', charset: 'utf-8' }],
    bodyValues: {
      '1': { value: 'Original message body.\n\nSecond paragraph.', isTruncated: false, isEncodingProblem: false },
    },
    ...over,
  } as unknown as Email;
}

describe('formatReplyQuote / formatForwardQuote produce a foldable blockquote', () => {
  it('reply: wraps the quoted body in <blockquote>, attribution stays outside', () => {
    const html = formatReplyQuote(parentEmail());
    expect(html).toContain('On ');
    expect(html).toContain('wrote:');
    expect(html).toMatch(/<blockquote>[\s\S]*Original message body[\s\S]*<\/blockquote>/);
    // The attribution paragraph must come before the blockquote, not inside it.
    const attributionIdx = html.indexOf('wrote:');
    const blockquoteIdx = html.indexOf('<blockquote>');
    expect(attributionIdx).toBeLessThan(blockquoteIdx);
  });

  it('forward: wraps the forwarded body in <blockquote>, header lines stay outside', () => {
    const html = formatForwardQuote(parentEmail());
    expect(html).toContain('Forwarded message');
    expect(html).toMatch(/<blockquote>[\s\S]*Original message body[\s\S]*<\/blockquote>/);
    const headerIdx = html.indexOf('Forwarded message');
    const blockquoteIdx = html.indexOf('<blockquote>');
    expect(headerIdx).toBeLessThan(blockquoteIdx);
  });
});

describe('quoteFoldPlugin: serialization is unaffected by fold state', () => {
  it('reply: docToHtml is identical whether mounted collapsed or expanded', () => {
    const html = formatReplyQuote(parentEmail());
    const collapsedView = mountEditor(html, true);
    const collapsedHtml = docToHtml(collapsedView.state.doc);
    collapsedView.destroy();

    const expandedView = mountEditor(html, false);
    const expandedHtml = docToHtml(expandedView.state.doc);
    expandedView.destroy();
    view = null;

    expect(collapsedHtml).toBe(expandedHtml);
    expect(collapsedHtml).toContain('Original message body');
    expect(collapsedHtml).toContain('<blockquote>');
  });

  it('forward: docToHtml is identical whether mounted collapsed or expanded', () => {
    const html = formatForwardQuote(parentEmail());
    const collapsedView = mountEditor(html, true);
    const collapsedHtml = docToHtml(collapsedView.state.doc);
    collapsedView.destroy();

    const expandedView = mountEditor(html, false);
    const expandedHtml = docToHtml(expandedView.state.doc);
    expandedView.destroy();
    view = null;

    expect(collapsedHtml).toBe(expandedHtml);
    expect(collapsedHtml).toContain('Original message body');
  });

  it('the outbound text/plain projection carries the full quote when the fold is never expanded', () => {
    const html = formatReplyQuote(parentEmail());
    const v = mountEditor(html, true);
    expect(isQuoteFolded(v.state)).toBe(true);

    // This mirrors exactly what compose.svelte.ts's send()/persistDraft()
    // do: take docToHtml(doc) as the HTML body, then project it to
    // text/plain via htmlToPlainText. Neither reads view-level decoration
    // state -- both operate on the doc.
    const bodyHtml = docToHtml(v.state.doc);
    const bodyText = htmlToPlainText(bodyHtml);

    expect(bodyText).toContain('> Original message body.');
    expect(bodyText).toContain('> Second paragraph.');
  });

  it('the outbound text/plain projection is identical whether or not the writer expands the quote', () => {
    const html = formatReplyQuote(parentEmail());

    const neverExpanded = mountEditor(html, true);
    const neverExpandedText = htmlToPlainText(docToHtml(neverExpanded.state.doc));
    neverExpanded.destroy();

    const expanded = mountEditor(html, true);
    toggleQuoteFold(expanded);
    expect(isQuoteFolded(expanded.state)).toBe(false);
    const expandedText = htmlToPlainText(docToHtml(expanded.state.doc));
    expanded.destroy();
    view = null;

    expect(neverExpandedText).toBe(expandedText);
  });

  it('toggling the fold does not mark the transaction as a doc change', () => {
    const html = formatReplyQuote(parentEmail());
    const v = mountEditor(html, true);
    const docBefore = v.state.doc;
    let sawDocChanged = false;
    const originalDispatch = v.dispatch.bind(v);
    v.dispatch = (tr) => {
      if (tr.docChanged) sawDocChanged = true;
      originalDispatch(tr);
    };
    toggleQuoteFold(v);
    expect(sawDocChanged).toBe(false);
    expect(v.state.doc).toBe(docBefore);
  });
});

describe('quoteFoldPlugin: view rendering', () => {
  it('hides the blockquote element and shows a toggle when collapseQuote=true', () => {
    const html = formatReplyQuote(parentEmail());
    const v = mountEditor(html, true);

    const toggle = v.dom.querySelector('[data-testid="quote-fold-toggle"]');
    expect(toggle).toBeTruthy();
    expect(toggle?.getAttribute('aria-expanded')).toBe('false');

    const blockquote = v.dom.querySelector('blockquote');
    expect(blockquote).toBeTruthy();
    expect(blockquote?.classList.contains('cq-folded')).toBe(true);
  });

  it('clicking the toggle expands the blockquote and flips aria-expanded', () => {
    const html = formatReplyQuote(parentEmail());
    const v = mountEditor(html, true);

    const toggle = v.dom.querySelector<HTMLButtonElement>(
      '[data-testid="quote-fold-toggle"]',
    );
    expect(toggle).toBeTruthy();
    toggle!.click();

    const toggleAfter = v.dom.querySelector('[data-testid="quote-fold-toggle"]');
    expect(toggleAfter?.getAttribute('aria-expanded')).toBe('true');
    const blockquote = v.dom.querySelector('blockquote');
    expect(blockquote?.classList.contains('cq-folded')).toBe(false);
  });

  it('renders the blockquote expanded (no cq-folded class) when collapseQuote=false', () => {
    const html = formatReplyQuote(parentEmail());
    const v = mountEditor(html, false);

    const blockquote = v.dom.querySelector('blockquote');
    expect(blockquote?.classList.contains('cq-folded')).toBe(false);
    // A toggle is still offered so the writer can fold it if they want to.
    expect(v.dom.querySelector('[data-testid="quote-fold-toggle"]')).toBeTruthy();
  });

  it('cursor lands in the compose area, not inside the folded quote', () => {
    const html = formatReplyQuote(parentEmail());
    const v = mountEditor(html, true);
    // formatReplyQuote's leading two empty <p></p> paragraphs are where the
    // writer types; default ProseMirror selection on mount is doc start.
    expect(v.state.selection.from).toBe(1);
    expect(v.state.selection.to).toBe(1);
  });
});

describe('applyBlockquote toolbar command still works with the fold plugin active', () => {
  it('wraps a fresh selection in a blockquote without throwing', async () => {
    const { applyBlockquote } = await import('./editor');
    const v = mountEditor('<p>hello world</p>', false);
    const { doc } = v.state;
    const tr = v.state.tr.setSelection(
      // Select "hello world" inside the single paragraph.
      (await import('prosemirror-state')).TextSelection.create(doc, 1, doc.content.size - 1),
    );
    v.dispatch(tr);
    expect(() => applyBlockquote(v)).not.toThrow();
    expect(v.dom.querySelector('blockquote')).toBeTruthy();
  });
});

describe('docToText matches docToHtml content regardless of fold state', () => {
  it('plain-text projection of the doc contains the quoted body when folded', () => {
    const html = formatReplyQuote(parentEmail());
    const v = mountEditor(html, true);
    const text = docToText(v.state.doc);
    expect(text).toContain('Original message body');
  });
});

describe('toggling the fold does not spuriously dirty compose.body (regression)', () => {
  // RichEditor.svelte gates its onUpdate (which writes into compose.body)
  // on the `docChanged` flag createComposeEditor passes to onChange.
  // Clicking the quote-fold toggle dispatches a metadata-only transaction
  // (docChanged=false); if RichEditor re-serialized the doc on every
  // transaction regardless of docChanged, a lossy HTML round-trip could
  // reassign compose.body to a value that differs from the snapshot taken
  // at compose-open, marking an untouched reply "dirty" and triggering an
  // unwanted draft autosave from a UI-only interaction.
  it('reports docChanged=false for a toggle-only transaction and true for a real edit', () => {
    const html = formatReplyQuote(parentEmail());
    const host = document.createElement('div');
    document.body.appendChild(host);
    const docChangedCalls: boolean[] = [];
    const v = createComposeEditor(host, {
      initialHtml: html,
      onChange: (_state, docChanged) => {
        docChangedCalls.push(docChanged);
      },
      collapseQuote: true,
    });

    toggleQuoteFold(v);
    expect(docChangedCalls.at(-1)).toBe(false);

    v.dispatch(v.state.tr.insertText('x', 1));
    expect(docChangedCalls.at(-1)).toBe(true);

    v.destroy();
  });

  it('a RichEditor-style onUpdate gated on docChanged never fires for the toggle alone', () => {
    const html = formatReplyQuote(parentEmail());
    const host = document.createElement('div');
    document.body.appendChild(host);
    let updateCount = 0;
    const v = createComposeEditor(host, {
      initialHtml: html,
      onChange: (state, docChanged) => {
        if (docChanged) {
          docToHtml(state.doc);
          updateCount += 1;
        }
      },
      collapseQuote: true,
    });

    toggleQuoteFold(v);
    toggleQuoteFold(v);
    toggleQuoteFold(v);
    expect(updateCount).toBe(0);

    v.dispatch(v.state.tr.insertText('hello', 1));
    expect(updateCount).toBe(1);

    v.destroy();
  });
});
