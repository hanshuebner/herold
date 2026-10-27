import type { EditorView } from 'prosemirror-view';
import { applyLink, computeActive, removeLink } from './editor';
import { prompt } from '../dialog/prompt.svelte';
import { t } from '../i18n/i18n.svelte';

/**
 * The composer's link action, shared by the toolbar button and the Mod-k
 * keymap entry: remove the link mark when the selection carries one,
 * otherwise ask for a URL and apply it to the selection.
 */
export async function promptLink(view: EditorView | null): Promise<void> {
  if (!view) return;
  if (computeActive(view.state).link) {
    removeLink(view);
    return;
  }
  const url = await prompt.ask({
    title: t('composeToolbar.linkPrompt.title'),
    label: 'URL',
    placeholder: 'https://example.com',
    confirmLabel: t('composeToolbar.linkPrompt.confirm'),
  });
  if (!url) {
    view.focus();
    return;
  }
  applyLink(view, url);
}
