import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/svelte';
import ThreadMoreMenu from './ThreadMoreMenu.svelte';
import type { MoreMenuItem, MoreMenuSection } from './ThreadMoreMenu.svelte';

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));

function renderMenu(items: MoreMenuItem[], sections?: MoreMenuSection[]) {
  return render(ThreadMoreMenu, { props: { items, sections } });
}

async function openMenu() {
  await fireEvent.click(screen.getByRole('button', { name: 'thread.moreActions' }));
  return screen.getByRole('menu');
}

describe('ThreadMoreMenu', () => {
  it('is closed until the trigger is clicked', async () => {
    renderMenu([{ id: 'a', label: 'Alpha', onclick: vi.fn() }]);
    const trigger = screen.getByRole('button', { name: 'thread.moreActions' });
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();

    await openMenu();
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
  });

  it('renders an href item as a download anchor', async () => {
    renderMenu([{ id: 'dl', label: 'Download', href: '/x/y.zip', download: 'y.zip' }]);
    const menu = await openMenu();
    const link = within(menu).getByRole('menuitem', { name: 'Download' });
    expect(link.tagName).toBe('A');
    expect(link).toHaveAttribute('href', '/x/y.zip');
    expect(link).toHaveAttribute('download', 'y.zip');
  });

  it('invokes the item handler and closes on activation', async () => {
    const onclick = vi.fn();
    renderMenu([{ id: 'a', label: 'Alpha', onclick }]);
    const menu = await openMenu();
    await fireEvent.click(within(menu).getByRole('menuitem', { name: 'Alpha' }));
    expect(onclick).toHaveBeenCalledOnce();
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('ignores activation of a busy item', async () => {
    const onclick = vi.fn();
    renderMenu([{ id: 'a', label: 'Alpha', onclick, busy: true }]);
    const menu = await openMenu();
    const item = within(menu).getByRole('menuitem', { name: 'Alpha' });
    expect(item).toHaveAttribute('aria-busy', 'true');
    await fireEvent.click(item);
    expect(onclick).not.toHaveBeenCalled();
  });

  it('renders non-empty sections after a separator, in order', async () => {
    renderMenu(
      [{ id: 'a', label: 'Alpha', onclick: vi.fn() }],
      [
        { id: 'apps', label: 'Apps', items: [{ id: 'x', label: 'Hand over', title: 'Hand over - App', onclick: vi.fn() }] },
        { id: 'empty', label: 'Empty', items: [] },
      ],
    );
    const menu = await openMenu();
    expect(within(menu).getAllByRole('separator')).toHaveLength(1);
    const group = within(menu).getByRole('group', { name: 'Apps' });
    const appItem = within(group).getByRole('menuitem', { name: 'Hand over' });
    expect(appItem).toHaveAttribute('title', 'Hand over - App');
    expect(within(menu).queryByRole('group', { name: 'Empty' })).not.toBeInTheDocument();
    expect(within(menu).getAllByRole('menuitem').map((el) => el.textContent?.trim())).toEqual([
      'Alpha',
      'Hand over',
    ]);
  });

  it('renders no separator when every section is empty', async () => {
    renderMenu([{ id: 'a', label: 'Alpha', onclick: vi.fn() }], [{ id: 'apps', items: [] }]);
    const menu = await openMenu();
    expect(within(menu).queryByRole('separator')).not.toBeInTheDocument();
  });

  it('closes on Escape and returns focus to the trigger', async () => {
    renderMenu([{ id: 'a', label: 'Alpha', onclick: vi.fn() }]);
    const menu = await openMenu();
    await fireEvent.keyDown(menu, { key: 'Escape' });
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'thread.moreActions' })).toHaveFocus();
  });

  it('moves focus between entries with the arrow keys', async () => {
    renderMenu([
      { id: 'a', label: 'Alpha', onclick: vi.fn() },
      { id: 'b', label: 'Beta', onclick: vi.fn() },
    ]);
    const menu = await openMenu();
    const [alpha, beta] = within(menu).getAllByRole('menuitem');
    expect(alpha).toHaveFocus();
    await fireEvent.keyDown(menu, { key: 'ArrowDown' });
    expect(beta).toHaveFocus();
    await fireEvent.keyDown(menu, { key: 'ArrowDown' });
    expect(alpha).toHaveFocus();
    await fireEvent.keyDown(menu, { key: 'ArrowUp' });
    expect(beta).toHaveFocus();
  });
});
