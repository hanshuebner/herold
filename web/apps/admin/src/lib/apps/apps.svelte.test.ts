import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { apps, appDetail, extensionMarks, endpointUrl, isDataKeyMissing, type AppRecord } from './apps.svelte';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const manifest = {
  herold_app: 1,
  app_key: 'local.herold.fakeapp',
  name: { en: 'Fake app', de: 'Test-App' },
  extensions: [
    {
      point: 'thread.action',
      id: 'open-url',
      label: { en: 'Open in fake app', de: 'In Test-App öffnen' },
      icon: 'link',
      scopes: ['thread.raw'],
      endpoint: '/actions/open-url',
    },
  ],
};

function appDTO(overrides: Partial<AppRecord> = {}): AppRecord {
  return {
    id: 7,
    app_key: 'local.herold.fakeapp',
    origin: 'http://127.0.0.1:9000',
    state: 'pending_approval',
    name: { en: 'Fake app' },
    all_principals: false,
    principals: [],
    manifest: null,
    manifest_digest: '',
    pending_manifest: manifest,
    pending_manifest_digest: 'd1',
    pending_diff: null,
    created_at: '2026-10-13T08:00:00Z',
    updated_at: '2026-10-13T08:00:00Z',
    last_invoked_at: null,
    ...overrides,
  };
}

type FetchArgs = [string, RequestInit | undefined];

function stubFetch(handler: (url: string, init?: RequestInit) => Response) {
  const fn = vi.fn((url: string, init?: RequestInit) => Promise.resolve(handler(url, init)));
  vi.stubGlobal('fetch', fn);
  return fn;
}

function bodyOf(call: FetchArgs | undefined): unknown {
  return call?.[1]?.body ? JSON.parse(call[1].body as string) : undefined;
}

describe('apps list store', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('loads the app list', async () => {
    stubFetch(() => jsonResponse({ items: [appDTO()], next: null }));
    await apps.load();
    expect(apps.status).toBe('ready');
    expect(apps.items.map((a) => a.id)).toEqual([7]);
    expect(apps.dataKeyMissing).toBe(false);
  });

  it('flags a missing data key on 503', async () => {
    stubFetch(() =>
      jsonResponse(
        { type: 'https://netzhansa.com/problems/apps/data_key_missing', title: 'data key missing', status: 503 },
        503,
      ),
    );
    await apps.load();
    expect(apps.status).toBe('error');
    expect(apps.dataKeyMissing).toBe(true);
  });

  it('hands the created app with its secret and warnings to the detail store', async () => {
    const created = appDTO({ secret: 's3cr3t', warnings: ['extension "x": unknown point "message.action"'] });
    const fetchFn = stubFetch((url, init) =>
      init?.method === 'POST' ? jsonResponse(created, 201) : jsonResponse({ items: [created], next: null }),
    );
    const res = await apps.create('http://127.0.0.1:9000');
    expect(res.ok).toBe(true);
    expect(bodyOf(fetchFn.mock.calls[0] as FetchArgs)).toEqual({ origin: 'http://127.0.0.1:9000' });
    expect(appDetail.app?.id).toBe(7);
    expect(appDetail.secret).toBe('s3cr3t');
    expect(appDetail.warnings).toHaveLength(1);
  });

  it('returns the manifest validation errors verbatim', async () => {
    stubFetch(() =>
      jsonResponse(
        {
          type: 'https://netzhansa.com/problems/apps/invalid_manifest',
          title: 'the app manifest failed validation',
          status: 422,
          errors: ['name: "en" is required', 'extensions[0].id: invalid'],
        },
        422,
      ),
    );
    const res = await apps.create('https://bad.example');
    expect(res.ok).toBe(false);
    expect(res.errors).toEqual(['name: "en" is required', 'extensions[0].id: invalid']);
    expect(res.errorMessage).toContain('the app manifest failed validation');
  });
});

describe('app detail store', () => {
  beforeEach(() => {
    appDetail.adopt(appDTO());
    appDetail.dismissSecret();
  });
  afterEach(() => vi.unstubAllGlobals());

  it('approves with the reviewed digest', async () => {
    const fetchFn = stubFetch((url) =>
      url.endsWith('/approve')
        ? jsonResponse(appDTO({ state: 'active', manifest, manifest_digest: 'd1', pending_manifest: null }))
        : jsonResponse({ items: [], next: null }),
    );
    const res = await appDetail.approve('d1');
    expect(res.ok).toBe(true);
    expect(fetchFn.mock.calls[0]?.[0]).toBe('/api/v1/apps/7/approve');
    expect(bodyOf(fetchFn.mock.calls[0] as FetchArgs)).toEqual({ manifest_digest: 'd1' });
    expect(appDetail.app?.state).toBe('active');
    expect(appDetail.notice).toBe('Manifest approved.');
  });

  it('reloads with a notice when the manifest changed before approval', async () => {
    stubFetch((url) =>
      url.endsWith('/approve')
        ? jsonResponse({ type: 'https://netzhansa.com/problems/apps/manifest_changed', status: 409 }, 409)
        : jsonResponse(appDTO({ pending_manifest_digest: 'd2' })),
    );
    const res = await appDetail.approve('d1');
    expect(res.ok).toBe(false);
    expect(appDetail.app?.pending_manifest_digest).toBe('d2');
    expect(appDetail.notice).toMatch(/manifest changed/i);
  });

  it('reports an unchanged and a pending manifest after refresh', async () => {
    stubFetch(() => jsonResponse(appDTO({ state: 'active', manifest, pending_manifest: null, manifest_changed: false })));
    await appDetail.refresh();
    expect(appDetail.notice).toBe('The manifest is unchanged.');

    stubFetch(() =>
      jsonResponse(appDTO({ state: 'active', manifest, manifest_changed: true, warnings: ['dropped one'] })),
    );
    await appDetail.refresh();
    expect(appDetail.notice).toBe('A changed manifest is waiting for approval.');
    expect(appDetail.warnings).toEqual(['dropped one']);
  });

  it('keeps a rotated secret until dismissed', async () => {
    stubFetch(() => jsonResponse(appDTO({ secret: 'new-secret' })));
    await appDetail.rotateSecret();
    expect(appDetail.secret).toBe('new-secret');
    appDetail.dismissSecret();
    expect(appDetail.secret).toBeNull();
  });

  it('patches access with ids and emails', async () => {
    const fetchFn = stubFetch(() => jsonResponse(appDTO({ principals: [{ id: 3, email: 'alice@example.local' }] })));
    await appDetail.setAccess({ principals: [3, 'bob@example.local'] });
    expect(fetchFn.mock.calls[0]?.[1]?.method).toBe('PATCH');
    expect(bodyOf(fetchFn.mock.calls[0] as FetchArgs)).toEqual({ principals: [3, 'bob@example.local'] });
    await appDetail.setAccess({ all_principals: true });
    expect(bodyOf(fetchFn.mock.calls[1] as FetchArgs)).toEqual({ all_principals: true });
  });

  it('disables, enables and removes', async () => {
    const fetchFn = stubFetch((url, init) =>
      init?.method === 'DELETE' ? new Response(null, { status: 204 }) : jsonResponse(appDTO({ state: 'disabled' })),
    );
    await appDetail.setEnabled(false);
    expect(fetchFn.mock.calls[0]?.[0]).toBe('/api/v1/apps/7/disable');
    await appDetail.setEnabled(true);
    expect(fetchFn.mock.calls[1]?.[0]).toBe('/api/v1/apps/7/enable');
    const res = await appDetail.remove();
    expect(res.ok).toBe(true);
    expect(fetchFn.mock.calls[2]?.[1]?.method).toBe('DELETE');
    expect(appDetail.app).toBeNull();
  });
});

describe('helpers', () => {
  it('marks diff entries per extension', () => {
    const diff = {
      added_extensions: ['new'],
      removed_extensions: ['old'],
      added_scopes: [{ extension_id: 'kept', scope: 'thread.raw' }],
      removed_scopes: null,
      changed_endpoints: [{ extension_id: 'kept', from: '/a', to: '/b' }],
    };
    expect(extensionMarks(diff, 'new').added).toBe(true);
    expect(extensionMarks(diff, 'old').removed).toBe(true);
    expect(extensionMarks(diff, 'kept')).toEqual({
      added: false,
      removed: false,
      addedScopes: ['thread.raw'],
      removedScopes: [],
      endpointChange: { extension_id: 'kept', from: '/a', to: '/b' },
    });
    expect(extensionMarks(null, 'kept').endpointChange).toBeNull();
  });

  it('joins origin and endpoint', () => {
    expect(endpointUrl('https://forum.example/', '/x/y')).toBe('https://forum.example/x/y');
  });

  it('recognizes only the apps 503 as a missing data key', () => {
    expect(isDataKeyMissing({ status: 503, problem: null })).toBe(true);
    expect(isDataKeyMissing({ status: 503, problem: { type: 'https://netzhansa.com/problems/other' } })).toBe(false);
    expect(isDataKeyMissing({ status: 500, problem: null })).toBe(false);
  });
});
