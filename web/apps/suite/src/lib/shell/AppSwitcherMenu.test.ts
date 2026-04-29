/**
 * Component tests for AppSwitcherMenu.
 *
 * Covers:
 *   - Burger button renders with correct aria attributes.
 *   - Dropdown is hidden when closed; visible when open.
 *   - aria-expanded reflects internal state.
 *   - currentApp entry is omitted from the menu.
 *   - Entries are gated on capabilities and scopes.
 *   - Each visible entry has target="_blank" and rel="noopener".
 *   - Pressing Escape while open closes the menu.
 *   - Clicking outside closes the menu.
 *
 * REQ-UI-13k..n.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// ── mocks (hoisted) ─────────────────────────────────────────────────────────

vi.mock('../auth/auth.svelte', () => {
  const auth = {
    status: 'ready' as string,
    session: null as null | { capabilities: Record<string, unknown> },
    scopes: [] as string[],
  };
  return { auth };
});

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
}));

import AppSwitcherMenu from './AppSwitcherMenu.svelte';
import { auth } from '../auth/auth.svelte';
import type { SessionResource } from '../jmap/types';

// ── helpers ──────────────────────────────────────────────────────────────────

function setAuth(opts: {
  capabilities?: Record<string, unknown>;
  scopes?: string[];
}): void {
  auth.status = 'ready';
  // Only the `capabilities` field matters for the switcher; cast to satisfy
  // the full SessionResource shape without listing every property.
  auth.session = { capabilities: opts.capabilities ?? {} } as SessionResource;
  auth.scopes = opts.scopes ?? [];
}

function renderSwitcher(currentApp: 'mail' | 'calendar' | 'contacts' | 'chat' | 'admin') {
  return render(AppSwitcherMenu, { props: { currentApp } });
}

// ── tests ────────────────────────────────────────────────────────────────────

beforeEach(() => {
  setAuth({});
});

describe('AppSwitcherMenu — burger button', () => {
  it('renders a button with correct aria attributes when closed', () => {
    renderSwitcher('mail');
    const btn = screen.getByRole('button', { name: 'app.switch' });
    expect(btn).toBeInTheDocument();
    expect(btn).toHaveAttribute('aria-expanded', 'false');
    expect(btn).toHaveAttribute('aria-controls', 'app-switcher-menu');
    expect(btn).toHaveAttribute('aria-haspopup', 'menu');
  });

  it('aria-expanded becomes true after click', async () => {
    renderSwitcher('mail');
    const btn = screen.getByRole('button', { name: 'app.switch' });
    await fireEvent.click(btn);
    expect(btn).toHaveAttribute('aria-expanded', 'true');
  });

  it('menu list is absent when closed', () => {
    renderSwitcher('mail');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('menu list is present after clicking the button', async () => {
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    expect(screen.getByRole('menu')).toBeInTheDocument();
  });
});

describe('AppSwitcherMenu — entry visibility', () => {
  it('excludes currentApp=mail from menu', async () => {
    setAuth({ capabilities: {} });
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const items = screen.queryAllByRole('menuitem');
    const labels = items.map((el) => el.textContent?.trim());
    expect(labels).not.toContain('app.mail');
  });

  it('excludes currentApp=chat from menu', async () => {
    setAuth({ capabilities: { 'https://netzhansa.com/jmap/chat': {} } });
    renderSwitcher('chat');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const items = screen.queryAllByRole('menuitem');
    const labels = items.map((el) => el.textContent?.trim());
    expect(labels).not.toContain('app.chat');
  });

  it('shows mail entry when currentApp is chat', async () => {
    setAuth({ capabilities: { 'https://netzhansa.com/jmap/chat': {} } });
    renderSwitcher('chat');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const items = screen.queryAllByRole('menuitem');
    const labels = items.map((el) => el.textContent?.trim());
    expect(labels).toContain('app.mail');
  });

  it('omits chat entry when chat capability is absent', async () => {
    setAuth({ capabilities: {} });
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const items = screen.queryAllByRole('menuitem');
    const labels = items.map((el) => el.textContent?.trim());
    expect(labels).not.toContain('app.chat');
  });

  it('shows chat entry when chat capability is present', async () => {
    setAuth({ capabilities: { 'https://netzhansa.com/jmap/chat': {} } });
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const items = screen.queryAllByRole('menuitem');
    const labels = items.map((el) => el.textContent?.trim());
    expect(labels).toContain('app.chat');
  });

  it('omits admin entry when admin scope is absent', async () => {
    setAuth({ scopes: [] });
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const items = screen.queryAllByRole('menuitem');
    const labels = items.map((el) => el.textContent?.trim());
    expect(labels).not.toContain('app.admin');
  });

  it('shows admin entry when admin scope is present', async () => {
    setAuth({ scopes: ['admin'] });
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const items = screen.queryAllByRole('menuitem');
    const labels = items.map((el) => el.textContent?.trim());
    expect(labels).toContain('app.admin');
  });

  it('omits calendar entry when calendar capability is absent', async () => {
    setAuth({ capabilities: {} });
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const items = screen.queryAllByRole('menuitem');
    const labels = items.map((el) => el.textContent?.trim());
    expect(labels).not.toContain('app.calendar');
  });

  it('shows calendar entry when calendar capability is present', async () => {
    setAuth({ capabilities: { 'urn:ietf:params:jmap:calendars': {} } });
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const items = screen.queryAllByRole('menuitem');
    const labels = items.map((el) => el.textContent?.trim());
    expect(labels).toContain('app.calendar');
  });

  it('shows all non-current entries with full capabilities and admin scope', async () => {
    setAuth({
      capabilities: {
        'urn:ietf:params:jmap:calendars': {},
        'urn:ietf:params:jmap:contacts': {},
        'https://netzhansa.com/jmap/chat': {},
      },
      scopes: ['admin'],
    });
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const items = screen.queryAllByRole('menuitem');
    const labels = items.map((el) => el.textContent?.trim());
    expect(labels).toContain('app.calendar');
    expect(labels).toContain('app.contacts');
    expect(labels).toContain('app.chat');
    expect(labels).toContain('app.admin');
    expect(labels).not.toContain('app.mail');
  });
});

describe('AppSwitcherMenu — link attributes', () => {
  it('every entry has target="_blank" and rel="noopener"', async () => {
    setAuth({
      capabilities: { 'https://netzhansa.com/jmap/chat': {} },
      scopes: ['admin'],
    });
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const items = screen.getAllByRole('menuitem');
    for (const item of items) {
      expect(item).toHaveAttribute('target', '_blank');
      expect(item).toHaveAttribute('rel', 'noopener');
    }
  });
});

describe('AppSwitcherMenu — close behaviour', () => {
  it('pressing Escape closes the menu', async () => {
    renderSwitcher('mail');
    const btn = screen.getByRole('button', { name: 'app.switch' });
    await fireEvent.click(btn);
    expect(screen.getByRole('menu')).toBeInTheDocument();

    const menu = screen.getByRole('menu');
    await fireEvent.keyDown(menu, { key: 'Escape' });
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(btn).toHaveAttribute('aria-expanded', 'false');
  });

  it('clicking an entry closes the menu', async () => {
    setAuth({ capabilities: { 'https://netzhansa.com/jmap/chat': {} } });
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    const item = screen.getByRole('menuitem', { name: /app\.chat/ });
    await fireEvent.click(item);
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('clicking outside closes the menu', async () => {
    renderSwitcher('mail');
    await fireEvent.click(screen.getByRole('button', { name: 'app.switch' }));
    expect(screen.getByRole('menu')).toBeInTheDocument();

    // Simulate a mousedown on the document body (outside the menu).
    await fireEvent.mouseDown(document.body);
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });
});
