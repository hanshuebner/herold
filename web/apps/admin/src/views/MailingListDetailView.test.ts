/**
 * MailingListDetailView component test (epic #183, REQ-MLIST-42).
 *
 * Imported statically at module top level per web/CLAUDE.md's CI-safety
 * rule (the f4a89ab4 CI break / 0ef95abc fix): a Svelte component's
 * transform must be paid during module collection, not inside a test
 * body via `await import(...)`, or it counts against the per-test
 * timeout on the CI runner.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/svelte';
import MailingListDetailView from './MailingListDetailView.svelte';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function mailingListDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    principal_id: 100,
    posting_address: 'announce@example.local',
    domain: 'example.local',
    display_name: 'Announcements',
    owner_principal_id: 2,
    arc_seal: true,
    posting_policy: 'open',
    subscribe_policy: 'closed',
    created_at: '2026-07-12T18:39:05.400041Z',
    updated_at: '2026-07-12T18:39:05.425518Z',
    ...overrides,
  };
}

function memberDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 10,
    list_id: 1,
    external_address: 'someone@gmail.com',
    state: 'active',
    delivery_mode: 'each',
    added_at: '2026-07-12T18:39:05.400041Z',
    ...overrides,
  };
}

describe('MailingListDetailView', () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('renders the list config and the roster unwrapped from the {items,next} envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        if (url.includes('/members')) {
          return Promise.resolve(jsonResponse({ items: [memberDTO()], next: null }));
        }
        return Promise.resolve(jsonResponse(mailingListDTO()));
      }),
    );

    render(MailingListDetailView, { props: { id: '1' } });

    // "announce@example.local" appears twice (the header subtitle and the
    // delete-confirmation label) -- assert on the count rather than a
    // single unique match.
    expect((await screen.findAllByText('announce@example.local')).length).toBeGreaterThanOrEqual(2);
    expect(await screen.findByText('someone@gmail.com')).toBeInTheDocument();
    const displayNameInput = (await screen.findByLabelText('Display name')) as HTMLInputElement;
    expect(displayNameInput.value).toBe('Announcements');
  });

  it('PATCHes config edits and shows Saved on success', async () => {
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === 'PATCH') {
        const body = JSON.parse(init.body as string) as Record<string, unknown>;
        return Promise.resolve(jsonResponse(mailingListDTO({ display_name: body.display_name })));
      }
      if (url.includes('/members')) return Promise.resolve(jsonResponse({ items: [], next: null }));
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    render(MailingListDetailView, { props: { id: '1' } });

    const displayNameInput = (await screen.findByLabelText('Display name')) as HTMLInputElement;
    await fireEvent.input(displayNameInput, { target: { value: 'Renamed list' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => {
      const patchCall = fetchMock.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'PATCH');
      expect(patchCall).toBeDefined();
    });

    const patchCall = fetchMock.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'PATCH');
    const body = JSON.parse((patchCall?.[1] as RequestInit).body as string) as { display_name?: string };
    expect(body.display_name).toBe('Renamed list');
    expect(await screen.findByText('Saved')).toBeInTheDocument();
  });

  it('removes a member via the two-stage confirm', async () => {
    let deleteCalled = false;
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') {
        deleteCalled = true;
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      if (url.includes('/members')) return Promise.resolve(jsonResponse({ items: [memberDTO()], next: null }));
      return Promise.resolve(jsonResponse(mailingListDTO()));
    });
    vi.stubGlobal('fetch', fetchMock);

    render(MailingListDetailView, { props: { id: '1' } });

    await screen.findByText('someone@gmail.com');
    await fireEvent.click(screen.getByRole('button', { name: 'Remove' }));
    expect(screen.getByText('Remove?')).toBeInTheDocument();

    await fireEvent.click(screen.getByRole('button', { name: 'Confirm' }));

    await waitFor(() => {
      expect(deleteCalled).toBe(true);
    });
  });

  it('shows a DKIM-key-missing warning banner when arc_seal is enabled but the domain has no active key', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        if (url.includes('/members')) {
          return Promise.resolve(jsonResponse({ items: [], next: null }));
        }
        return Promise.resolve(jsonResponse(mailingListDTO({ arc_seal: true, dkim_key_missing: true })));
      }),
    );

    render(MailingListDetailView, { props: { id: '1' } });

    const banner = await screen.findByText(/ARC-seal is enabled/i);
    expect(banner).toBeInTheDocument();
    expect(banner.getAttribute('role')).toBe('alert');
    expect(banner.textContent).toContain('example.local');
  });

  it('shows no warning banner when the domain has an active DKIM key', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        if (url.includes('/members')) {
          return Promise.resolve(jsonResponse({ items: [], next: null }));
        }
        return Promise.resolve(jsonResponse(mailingListDTO({ arc_seal: true })));
      }),
    );

    render(MailingListDetailView, { props: { id: '1' } });

    await screen.findByLabelText('Display name');
    expect(screen.queryByText(/ARC-seal is enabled/i)).not.toBeInTheDocument();
  });
});
