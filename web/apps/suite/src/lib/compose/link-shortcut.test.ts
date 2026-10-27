/**
 * Mod-k in the composer runs the same link flow as the toolbar button
 * (issue #541): the keymap entry calls the supplied handler and swallows
 * the key; promptLink applies the prompted URL to the selection and
 * removes an existing link.
 */
import { describe, it, expect, afterEach, vi } from 'vitest';
import { TextSelection } from 'prosemirror-state';
import type { EditorView } from 'prosemirror-view';
import { createComposeEditor, docToHtml } from './editor';

const ask = vi.fn();
vi.mock('../dialog/prompt.svelte', () => ({ prompt: { ask: (o: unknown) => ask(o) } }));

import { promptLink } from './link-flow';

let view: EditorView | null = null;

function mount(onLinkShortcut?: (v: EditorView) => void): EditorView {
  const host = document.createElement('div');
  document.body.appendChild(host);
  view = createComposeEditor(host, {
    initialHtml: '<p>hello world</p>',
    onChange: () => undefined,
    onLinkShortcut,
  });
  return view;
}

function select(v: EditorView, from: number, to: number): void {
  v.dispatch(v.state.tr.setSelection(TextSelection.create(v.state.doc, from, to)));
}

function keydown(v: EditorView, init: KeyboardEventInit): KeyboardEvent {
  const ev = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init });
  v.dom.dispatchEvent(ev);
  return ev;
}

afterEach(() => {
  view?.destroy();
  view = null;
  ask.mockReset();
});

describe('composer Mod-k link shortcut (#541)', () => {
  it('Ctrl-k invokes the link handler and prevents the browser default', () => {
    const handler = vi.fn();
    const v = mount(handler);
    select(v, 1, 6);
    const ev = keydown(v, { key: 'k', ctrlKey: true });
    expect(handler).toHaveBeenCalledTimes(1);
    expect(handler.mock.calls[0]![0]).toBe(v);
    expect(ev.defaultPrevented).toBe(true);
  });

  it('plain k does not invoke the link handler', () => {
    const handler = vi.fn();
    const v = mount(handler);
    keydown(v, { key: 'k' });
    expect(handler).not.toHaveBeenCalled();
  });

  it('the shortcut flow prompts and applies the link to the selection', async () => {
    ask.mockResolvedValue('https://example.com');
    const v = mount((view) => void promptLink(view));
    select(v, 1, 6);
    keydown(v, { key: 'k', ctrlKey: true });
    await vi.waitFor(() => expect(docToHtml(v.state.doc)).toContain('href="https://example.com"'));
    expect(ask).toHaveBeenCalledTimes(1);
  });

  it('promptLink on a linked selection removes the link without prompting', async () => {
    ask.mockResolvedValue('https://example.com');
    const v = mount();
    select(v, 1, 6);
    await promptLink(v);
    expect(docToHtml(v.state.doc)).toContain('href=');
    select(v, 1, 6);
    await promptLink(v);
    expect(docToHtml(v.state.doc)).not.toContain('href=');
    expect(ask).toHaveBeenCalledTimes(1);
  });
});
