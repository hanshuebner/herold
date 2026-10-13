/**
 * AppDetailView: the review and manifest-update rendering (REQ-APPS-32,
 * 33) and the once-only secret (REQ-APPS-31, 35).
 */
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, cleanup, waitFor, within, fireEvent } from '@testing-library/svelte';
import AppDetailView from './AppDetailView.svelte';
import { appDetail, type AppManifest, type AppRecord } from '../lib/apps/apps.svelte';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

const approved: AppManifest = {
  herold_app: 1,
  app_key: 'de.example.forum',
  name: { en: 'Forum', de: 'Forum' },
  extensions: [
    {
      point: 'thread.action',
      id: 'donation-offer',
      label: { en: 'Create donation offer', de: 'Spendenangebot erstellen' },
      icon: 'gift',
      scopes: ['thread.raw'],
      endpoint: '/herold/actions/donation-offer',
    },
    {
      point: 'thread.action',
      id: 'old-action',
      label: { en: 'Old action' },
      icon: 'app',
      scopes: [],
      endpoint: '/herold/actions/old',
    },
  ],
};

const pending: AppManifest = {
  ...approved,
  extensions: [
    { ...approved.extensions[0]!, endpoint: '/herold/v2/donation-offer' },
    {
      point: 'thread.action',
      id: 'new-action',
      label: { en: 'New action' },
      icon: 'ticket',
      scopes: ['thread.raw'],
      endpoint: '/herold/actions/new',
    },
  ],
};

function appDTO(overrides: Partial<AppRecord> = {}): AppRecord {
  return {
    id: 7,
    app_key: 'de.example.forum',
    origin: 'https://forum.example',
    state: 'pending_approval',
    name: { en: 'Forum' },
    all_principals: false,
    principals: [{ id: 3, email: 'alice@example.local' }],
    manifest: null,
    manifest_digest: '',
    pending_manifest: approved,
    pending_manifest_digest: 'digest-1',
    pending_diff: null,
    created_at: '2026-10-13T08:00:00Z',
    updated_at: '2026-10-13T08:00:00Z',
    last_invoked_at: null,
    ...overrides,
  };
}

function stubGet(app: AppRecord) {
  const fn = vi.fn((url: string) =>
    Promise.resolve(url === '/api/v1/apps/7' ? jsonResponse(app) : jsonResponse({ items: [], next: null })),
  );
  vi.stubGlobal('fetch', fn);
  return fn;
}

describe('AppDetailView', () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('reviews a new app: labels in every language, full endpoint, scope explanation', async () => {
    stubGet(appDTO());
    render(AppDetailView, { props: { id: '7' } });
    const review = await screen.findByTestId('apps-review');
    expect(within(review).getByRole('heading', { name: 'Review' })).toBeInTheDocument();
    expect(within(review).getByText('Spendenangebot erstellen')).toBeInTheDocument();
    expect(within(review).getByText('https://forum.example/herold/actions/donation-offer')).toBeInTheDocument();
    expect(
      within(review).getByText(
        'Receives every message of a conversation, including attachments, when a user picks this action on it',
      ),
    ).toBeInTheDocument();
    expect(within(review).getByRole('button', { name: 'Approve' })).toBeInTheDocument();
  });

  it('approves the digest that is shown', async () => {
    const fetchFn = stubGet(appDTO());
    render(AppDetailView, { props: { id: '7' } });
    const approve = await screen.findByRole('button', { name: 'Approve' });
    fetchFn.mockImplementation(() =>
      Promise.resolve(jsonResponse(appDTO({ state: 'active', manifest: approved, pending_manifest: null }))),
    );
    await fireEvent.click(approve);
    await waitFor(() => expect(screen.getByTestId('apps-notice')).toHaveTextContent('Manifest approved.'));
    const call = fetchFn.mock.calls.find((c) => String(c[0]).endsWith('/approve')) as
      | [string, RequestInit]
      | undefined;
    expect(JSON.parse(call?.[1].body as string)).toEqual({ manifest_digest: 'digest-1' });
  });

  it('shows a manifest update side by side with the diff marked', async () => {
    stubGet(
      appDTO({
        state: 'active',
        manifest: approved,
        manifest_digest: 'digest-0',
        pending_manifest: pending,
        pending_diff: {
          added_extensions: ['new-action'],
          removed_extensions: ['old-action'],
          added_scopes: [{ extension_id: 'new-action', scope: 'thread.raw' }],
          removed_scopes: [],
          changed_endpoints: [
            { extension_id: 'donation-offer', from: '/herold/actions/donation-offer', to: '/herold/v2/donation-offer' },
          ],
        },
      }),
    );
    const { container } = render(AppDetailView, { props: { id: '7' } });
    const diff = await screen.findByTestId('apps-diff');
    expect(within(diff).getByRole('heading', { name: 'Approved' })).toBeInTheDocument();
    expect(within(diff).getByRole('heading', { name: 'Pending' })).toBeInTheDocument();
    expect(container.querySelector('.extension.removed[data-extension-id="old-action"]')).not.toBeNull();
    expect(container.querySelector('.extension.added[data-extension-id="new-action"]')).not.toBeNull();
    expect(within(diff).getByText('was /herold/actions/donation-offer')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve update' })).toBeInTheDocument();
    expect(screen.getAllByText('Manifest update pending').length).toBeGreaterThan(0);
  });

  it('shows the secret of a just-created app once', async () => {
    const created = appDTO({ secret: 'c2VjcmV0', warnings: ['extension "m": unknown point "message.action"'] });
    appDetail.adopt(created);
    stubGet(appDTO());
    render(AppDetailView, { props: { id: '7' } });
    const box = await screen.findByTestId('apps-secret');
    expect(within(box).getByRole('textbox')).toHaveValue('c2VjcmV0');
    expect(within(box).getByText(/will not be shown again/)).toBeInTheDocument();
    expect(screen.getByText('extension "m": unknown point "message.action"')).toBeInTheDocument();
    await fireEvent.click(within(box).getByRole('button', { name: 'I have stored the secret' }));
    expect(screen.queryByTestId('apps-secret')).not.toBeInTheDocument();
  });

  it('explains a missing data key', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          jsonResponse({ type: 'https://netzhansa.com/problems/apps/data_key_missing', status: 503 }, 503),
        ),
      ),
    );
    render(AppDetailView, { props: { id: '8' } });
    const notice = await screen.findByTestId('apps-data-key-missing');
    expect(notice).toHaveTextContent('[server.secrets].data_key_ref');
    expect(within(notice).getByRole('link')).toHaveAttribute('href', '#/help/configure');
  });
});
