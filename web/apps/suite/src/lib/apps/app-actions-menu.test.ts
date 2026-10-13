import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/svelte';

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));

import ThreadMoreMenu from '../mail/ThreadMoreMenu.svelte';
import { appActionsSection, appActionTooltip } from './app-actions-menu';
import type { AppAction } from './app-actions';

const actions: AppAction[] = [
  { appId: '1', extensionId: 'open-url', point: 'thread.action', label: 'Open in fake app', appName: 'Fake app', icon: 'link' },
  { appId: '1', extensionId: 'notice', point: 'thread.action', label: 'Send to fake app', appName: 'Fake app', icon: 'send' },
];
const target = { accountId: 'A1', threadId: 'T1' };

function invoker(busy: string[] = []) {
  return {
    invoke: vi.fn(() => Promise.resolve()),
    isBusy: vi.fn((a: Pick<AppAction, 'extensionId'>) => busy.includes(a.extensionId)),
  };
}

async function openMenu() {
  await fireEvent.click(screen.getByRole('button', { name: 'thread.moreActions' }));
  return screen.getByRole('menu');
}

describe('appActionsSection', () => {
  it('renders the actions after a separator with icon and tooltip', async () => {
    const inv = invoker();
    render(ThreadMoreMenu, {
      props: {
        items: [{ id: 'builtin', label: 'Built-in', onclick: vi.fn() }],
        sections: [appActionsSection(actions, inv, target, 'App actions')],
      },
    });
    const menu = await openMenu();
    expect(within(menu).getByRole('separator')).toBeInTheDocument();
    const group = within(menu).getByRole('group', { name: 'App actions' });
    const items = within(group).getAllByRole('menuitem');
    expect(items.map((i) => i.textContent?.trim())).toEqual(['Open in fake app', 'Send to fake app']);
    expect(items[0]).toHaveAttribute('title', 'Open in fake app — Fake app');
    expect(items[0]?.querySelector('[data-app-icon="link"]')).not.toBeNull();
    expect(items[1]?.querySelector('[data-app-icon="send"]')).not.toBeNull();
  });

  it('invokes the action with the thread target on click', async () => {
    const inv = invoker();
    render(ThreadMoreMenu, { props: { items: [], sections: [appActionsSection(actions, inv, target, 'App actions')] } });
    const menu = await openMenu();
    await fireEvent.click(within(menu).getByRole('menuitem', { name: 'Send to fake app' }));
    expect(inv.invoke).toHaveBeenCalledWith(actions[1], target);
  });

  it('shows a busy action as disabled with a spinner', async () => {
    const inv = invoker(['open-url']);
    render(ThreadMoreMenu, { props: { items: [], sections: [appActionsSection(actions, inv, target, 'App actions')] } });
    const menu = await openMenu();
    const busy = within(menu).getByRole('menuitem', { name: 'Open in fake app' });
    expect(busy).toHaveAttribute('aria-busy', 'true');
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    expect(busy.querySelector('.spinner')).not.toBeNull();
    await fireEvent.click(busy);
    expect(inv.invoke).not.toHaveBeenCalled();
  });

  it('renders neither separator nor section without actions', async () => {
    render(ThreadMoreMenu, {
      props: {
        items: [{ id: 'builtin', label: 'Built-in', onclick: vi.fn() }],
        sections: [appActionsSection([], invoker(), target, 'App actions')],
      },
    });
    const menu = await openMenu();
    expect(within(menu).queryByRole('separator')).not.toBeInTheDocument();
    expect(within(menu).queryByRole('group')).not.toBeInTheDocument();
  });

  it('offers no items while the thread account is unknown', () => {
    expect(appActionsSection(actions, invoker(), null, 'App actions').items).toEqual([]);
  });

  it('builds the tooltip from label and app name', () => {
    expect(appActionTooltip({ label: 'Create', appName: 'Forum' })).toBe('Create — Forum');
    expect(appActionTooltip({ label: 'Create', appName: '' })).toBe('Create');
  });
});
