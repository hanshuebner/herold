/**
 * Unit tests for the URL auto-linking input rules and schema link mark.
 *
 * Input rules fire through the editor view's handleTextInput prop and
 * cannot be exercised purely via state transactions; we test the rule
 * handler and the schema link-mark toDOM/parseDOM directly instead.
 */

import { describe, it, expect, vi } from 'vitest';
import { EditorState } from 'prosemirror-state';
import { chatSchema } from './schema';
import { handleUrlPaste } from './url-input-rules';

// ---------------------------------------------------------------------------
// Schema link mark
// ---------------------------------------------------------------------------

describe('chatSchema link mark toDOM', () => {
  it('serialises with target=_blank and rel=noopener noreferrer', () => {
    const mark = chatSchema.marks.link!.create({
      href: 'https://example.com',
    });
    const dom = mark.type.spec.toDOM!(mark, false) as [
      string,
      Record<string, string>,
      number,
    ];
    expect(dom[0]).toBe('a');
    expect(dom[1]['target']).toBe('_blank');
    expect(dom[1]['rel']).toBe('noopener noreferrer');
    expect(dom[1]['href']).toBe('https://example.com');
  });

  it('includes title when set', () => {
    const mark = chatSchema.marks.link!.create({
      href: 'https://example.com',
      title: 'My link',
    });
    const dom = mark.type.spec.toDOM!(mark, false) as [
      string,
      Record<string, string>,
      number,
    ];
    expect(dom[1]['title']).toBe('My link');
  });

  it('omits title attribute when title is null', () => {
    const mark = chatSchema.marks.link!.create({
      href: 'https://example.com',
      title: null,
    });
    const dom = mark.type.spec.toDOM!(mark, false) as [
      string,
      Record<string, string>,
      number,
    ];
    expect('title' in dom[1]).toBe(false);
  });
});

describe('chatSchema link mark parseDOM', () => {
  const spec = chatSchema.marks.link!.spec;
  const parseDOM = spec.parseDOM as Array<{
    tag: string;
    getAttrs: (dom: Element) => false | Record<string, unknown> | null;
  }>;
  const rule = parseDOM[0]!;

  it('rejects javascript: href', () => {
    const el = document.createElement('a');
    el.setAttribute('href', 'javascript:alert(1)');
    expect(rule.getAttrs(el)).toBe(false);
  });

  it('rejects mailto: href', () => {
    const el = document.createElement('a');
    el.setAttribute('href', 'mailto:user@example.com');
    expect(rule.getAttrs(el)).toBe(false);
  });

  it('rejects data: href', () => {
    const el = document.createElement('a');
    el.setAttribute('href', 'data:text/html,<h1>x</h1>');
    expect(rule.getAttrs(el)).toBe(false);
  });

  it('accepts https: href', () => {
    const el = document.createElement('a');
    el.setAttribute('href', 'https://herold.dev');
    const attrs = rule.getAttrs(el);
    expect(attrs).not.toBe(false);
    expect((attrs as Record<string, string>)['href']).toBe('https://herold.dev');
  });

  it('accepts http: href', () => {
    const el = document.createElement('a');
    el.setAttribute('href', 'http://example.com/path?q=1');
    const attrs = rule.getAttrs(el);
    expect(attrs).not.toBe(false);
    expect((attrs as Record<string, string>)['href']).toBe('http://example.com/path?q=1');
  });
});

// ---------------------------------------------------------------------------
// handleUrlPaste
// ---------------------------------------------------------------------------

function makeViewMock(schema = chatSchema): {
  view: import('prosemirror-view').EditorView;
  dispatched: import('prosemirror-state').Transaction[];
} {
  const doc = schema.topNodeType.createAndFill()!;
  const state = EditorState.create({ schema, doc });
  const dispatched: import('prosemirror-state').Transaction[] = [];
  const view = {
    state,
    dispatch: (tr: import('prosemirror-state').Transaction) => {
      dispatched.push(tr);
    },
  } as unknown as import('prosemirror-view').EditorView;
  return { view, dispatched };
}

function makeClipboardEvent(text: string): ClipboardEvent {
  return {
    clipboardData: {
      getData: (type: string) => (type === 'text/plain' ? text : ''),
    },
  } as unknown as ClipboardEvent;
}

describe('handleUrlPaste', () => {
  it('handles a bare https URL paste', () => {
    const { view, dispatched } = makeViewMock();
    const event = makeClipboardEvent('https://example.com/path');
    const handled = handleUrlPaste(view, event);
    expect(handled).toBe(true);
    expect(dispatched.length).toBe(1);
  });

  it('inserts text with link mark for https URL', () => {
    const { view, dispatched } = makeViewMock();
    const event = makeClipboardEvent('https://example.com');
    handleUrlPaste(view, event);
    const tr = dispatched[0]!;
    // Walk the transaction's new doc to find a link-marked text node.
    let foundLink = false;
    tr.doc.descendants((node) => {
      if (
        node.isText &&
        node.marks.some((m) => m.type.name === 'link' && m.attrs['href'] === 'https://example.com')
      ) {
        foundLink = true;
      }
    });
    expect(foundLink).toBe(true);
  });

  it('handles a bare http URL paste', () => {
    const { view, dispatched } = makeViewMock();
    const event = makeClipboardEvent('http://example.com');
    const handled = handleUrlPaste(view, event);
    expect(handled).toBe(true);
    expect(dispatched.length).toBe(1);
  });

  it('does not handle plain text that is not a URL', () => {
    const { view, dispatched } = makeViewMock();
    const event = makeClipboardEvent('hello world');
    const handled = handleUrlPaste(view, event);
    expect(handled).toBe(false);
    expect(dispatched.length).toBe(0);
  });

  it('does not handle multi-line text even if it contains a URL', () => {
    const { view, dispatched } = makeViewMock();
    const event = makeClipboardEvent('see https://example.com for details');
    const handled = handleUrlPaste(view, event);
    expect(handled).toBe(false);
    expect(dispatched.length).toBe(0);
  });

  it('does not handle javascript: scheme', () => {
    const { view, dispatched } = makeViewMock();
    const event = makeClipboardEvent('javascript:alert(1)');
    const handled = handleUrlPaste(view, event);
    expect(handled).toBe(false);
    expect(dispatched.length).toBe(0);
  });

  it('returns false when clipboardData is absent', () => {
    const { view, dispatched } = makeViewMock();
    const event = { clipboardData: null } as unknown as ClipboardEvent;
    const handled = handleUrlPaste(view, event);
    expect(handled).toBe(false);
    expect(dispatched.length).toBe(0);
  });
});
