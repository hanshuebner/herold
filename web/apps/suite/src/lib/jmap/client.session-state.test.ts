import { describe, it, expect, vi, afterEach } from 'vitest';
import { JmapClient, setJmapOnSessionStateChange } from './client';

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
}

const session = {
  capabilities: {},
  accounts: {},
  primaryAccounts: {},
  username: 'alice@example.local',
  apiUrl: '/jmap',
  downloadUrl: '',
  uploadUrl: '',
  eventSourceUrl: '',
  state: 's1',
};

async function bootstrapped(): Promise<JmapClient> {
  const client = new JmapClient();
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(session))));
  await client.bootstrap();
  return client;
}

describe('JmapClient session-state change', () => {
  afterEach(() => {
    setJmapOnSessionStateChange(null);
    vi.unstubAllGlobals();
  });

  it('reports a sessionState different from the pinned session', async () => {
    const client = await bootstrapped();
    const onChange = vi.fn();
    setJmapOnSessionStateChange(onChange);
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse({ methodResponses: [], sessionState: 's2' }))));
    await client.request({ using: [], methodCalls: [] });
    expect(onChange).toHaveBeenCalledWith('s2');
  });

  it('stays quiet while the sessionState matches', async () => {
    const client = await bootstrapped();
    const onChange = vi.fn();
    setJmapOnSessionStateChange(onChange);
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse({ methodResponses: [], sessionState: 's1' }))));
    await client.request({ using: [], methodCalls: [] });
    expect(onChange).not.toHaveBeenCalled();
  });
});
