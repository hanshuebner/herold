/**
 * PrincipalDetailView component tests (re #218).
 *
 * The server's principalDTO sends `canonical_email` and a `flags: string[]`
 * (internal/protoadmin/types.go). Before this fix the view rendered the
 * page title from a non-existent `email` field and derived each flag
 * checkbox via a bitwise AND against the array (`p.flags & FLAG_ADMIN`),
 * which always evaluates false regardless of the principal's real flags.
 * On Save it built the PATCH body's `flags` as a bare number, which does
 * not match the server's `Flags *[]string` decode
 * (internal/protoadmin/principals.go) -- a live data-loss/lockout hazard,
 * since a caller's own admin/disabled/ignore-limits flags could be
 * silently dropped by a save that "succeeded".
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/svelte';

// Imported statically so the Svelte transform of this view (and its bits-ui
// tab dependencies) is paid during module collection rather than inside a
// test body, where it counts against the per-test timeout. api/client.ts
// resolves `fetch` off the global at call time, so stubbing it inside a test
// still takes effect for a statically imported component.
import PrincipalDetailView from './PrincipalDetailView.svelte';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function principalDTO(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    kind: 'user',
    canonical_email: 'admin@example.local',
    display_name: '',
    quota_bytes: 0,
    flags: ['admin', 'totp_enabled'],
    totp_enabled: true,
    seen_addresses_enabled: false,
    created_at: '2026-07-12T18:39:05.400041Z',
    updated_at: '2026-07-12T18:39:05.425518Z',
    ...overrides,
  };
}

describe('PrincipalDetailView (re #218)', () => {
  afterEach(() => {
    // Unmount explicitly: a test that fails or times out mid-render would
    // otherwise leave its component mounted and the next test would match
    // duplicate elements.
    cleanup();
    vi.unstubAllGlobals();
  });

  it('renders the canonical_email title and checks flags from the string array', async () => {
    const currentPrincipal = principalDTO();
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        const u = url;
        if (u.includes('/api-keys')) return Promise.resolve(jsonResponse([]));
        if (u.includes('/oidc-links')) return Promise.resolve(jsonResponse([]));
        return Promise.resolve(jsonResponse(currentPrincipal));
      }),
    );

    render(PrincipalDetailView, { props: { id: '1' } });

    expect(await screen.findByText('admin@example.local')).toBeInTheDocument();

    const adminCheckbox = (await screen.findByLabelText('Admin')) as HTMLInputElement;
    const disabledCheckbox = screen.getByLabelText('Disabled') as HTMLInputElement;
    const ignoreLimitsCheckbox = screen.getByLabelText('Ignore download limits') as HTMLInputElement;

    expect(adminCheckbox.checked).toBe(true);
    expect(disabledCheckbox.checked).toBe(false);
    expect(ignoreLimitsCheckbox.checked).toBe(false);
  });

  it('Save round-trip: toggling one flag preserves the rest as a string[] PATCH body', async () => {
    let currentPrincipal = principalDTO();
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      const u = url;
      if (u.includes('/api-keys')) return Promise.resolve(jsonResponse([]));
      if (u.includes('/oidc-links')) return Promise.resolve(jsonResponse([]));
      if (init?.method === 'PATCH') {
        const body = JSON.parse(init.body as string) as { flags?: string[]; display_name?: string };
        currentPrincipal = principalDTO({ flags: body.flags, display_name: body.display_name });
        return Promise.resolve(jsonResponse(currentPrincipal));
      }
      return Promise.resolve(jsonResponse(currentPrincipal));
    });
    vi.stubGlobal('fetch', fetchMock);

    render(PrincipalDetailView, { props: { id: '1' } });

    const saveButton = await screen.findByRole('button', { name: 'Save changes' });

    const ignoreLimitsCheckbox = screen.getByLabelText('Ignore download limits') as HTMLInputElement;
    expect(ignoreLimitsCheckbox.checked).toBe(false);
    await fireEvent.click(ignoreLimitsCheckbox);

    await fireEvent.click(saveButton);

    await waitFor(() => {
      const patchCall = fetchMock.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'PATCH');
      expect(patchCall).toBeDefined();
    });

    const patchCall = fetchMock.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'PATCH');
    const body = JSON.parse((patchCall?.[1] as RequestInit).body as string) as { flags?: unknown };

    // The critical assertion: flags travels as a string array, not a number,
    // and the round trip neither drops the pre-existing flags this form has
    // no checkbox for (totp_enabled) nor the untouched admin flag.
    expect(Array.isArray(body.flags)).toBe(true);
    expect(body.flags).toEqual(expect.arrayContaining(['admin', 'totp_enabled', 'ignore_download_limits']));
    expect((body.flags as string[]).length).toBe(3);
  });
});
