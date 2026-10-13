/**
 * Session refetch on a changed session state (RFC 8620 §2, REQ-APPS-05).
 * The JMAP client reports a differing `sessionState`; auth refetches the
 * descriptor, coalescing reports that arrive while a refetch runs.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';

function sessionWith(state: string, caps: Record<string, unknown> = {}) {
  return {
    capabilities: {},
    accounts: { A1: { name: 'a', isPersonal: true, isReadOnly: false, accountCapabilities: caps } },
    primaryAccounts: {},
    username: 'alice@example.local',
    apiUrl: '/jmap',
    downloadUrl: '',
    uploadUrl: '',
    eventSourceUrl: '',
    state,
  };
}

const jmapBootstrap = vi.fn();
let onStateChange: ((s: string) => void) | null = null;

vi.mock('../jmap/client', () => ({
  jmap: { bootstrap: jmapBootstrap },
  setJmapOnUnauthenticated: vi.fn(),
  setJmapOnSessionStateChange: vi.fn((fn: (s: string) => void) => {
    onStateChange = fn;
  }),
}));

vi.mock('../api/client', () => ({
  get: vi.fn(async () => ({ principal_id: 1, email: 'alice@example.local', scopes: ['end-user'] })),
  setOnUnauthenticated: vi.fn(),
}));

async function readyAuth() {
  vi.resetModules();
  const mod = await import('./auth.svelte');
  jmapBootstrap.mockResolvedValueOnce(sessionWith('s1'));
  await mod.auth.bootstrap();
  expect(mod.auth.status).toBe('ready');
  return mod;
}

describe('auth.sessionStateChanged', () => {
  beforeEach(() => {
    jmapBootstrap.mockReset();
  });

  it('is registered with the JMAP client', async () => {
    const { auth } = await readyAuth();
    jmapBootstrap.mockResolvedValue(sessionWith('s2'));
    onStateChange?.('s2');
    await vi.waitFor(() => expect(auth.session?.state).toBe('s2'));
  });

  it('keeps the descriptor and updates only state when nothing else changed', async () => {
    const { auth } = await readyAuth();
    const before = auth.session;
    jmapBootstrap.mockResolvedValue(sessionWith('s2'));
    await auth.sessionStateChanged();
    expect(auth.session).toBe(before);
    expect(auth.session?.state).toBe('s2');
  });

  it('replaces the descriptor when a capability changed', async () => {
    const { auth } = await readyAuth();
    const before = auth.session;
    const caps = { 'https://netzhansa.com/jmap/apps': { actions: [] } };
    jmapBootstrap.mockResolvedValue(sessionWith('s2', caps));
    await auth.sessionStateChanged();
    expect(auth.session).not.toBe(before);
    expect(auth.session?.accounts.A1?.accountCapabilities).toEqual(caps);
  });

  it('coalesces reports during a running refetch into one follow-up', async () => {
    const { auth } = await readyAuth();
    let release!: () => void;
    jmapBootstrap.mockImplementationOnce(
      () =>
        new Promise((r) => {
          release = () => r(sessionWith('s2'));
        }),
    );
    jmapBootstrap.mockResolvedValue(sessionWith('s3'));
    const first = auth.sessionStateChanged();
    void auth.sessionStateChanged();
    void auth.sessionStateChanged();
    release();
    await first;
    expect(jmapBootstrap).toHaveBeenCalledTimes(3);
    expect(auth.session?.state).toBe('s3');
  });

  it('does nothing before the session is ready', async () => {
    vi.resetModules();
    const { auth } = await import('./auth.svelte');
    await auth.sessionStateChanged();
    expect(jmapBootstrap).not.toHaveBeenCalled();
  });
});
