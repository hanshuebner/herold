/**
 * Unit tests for the server-side one-click unsubscribe call (REQ-UNS-20,
 * issue #412). `postOneClickUnsubscribe` calls `Email/unsubscribe`
 * through the Suite's JMAP client rather than POSTing to the sender's
 * URL from the browser (a cross-origin request CORS would always
 * discard the response of).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn() },
  strict: (r: unknown[]) => r,
}));

function invocation(name: string, args: unknown, callId = 'c0'): [string, unknown, string] {
  return [name, args, callId];
}

describe('postOneClickUnsubscribe', () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let jmapMod: any;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let unsubscribeMod: any;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    jmapMod = await import('../jmap/client');
    unsubscribeMod = await import('./unsubscribe');
  });

  it('calls Email/unsubscribe with accountId + emailId under the unsubscribe capability', async () => {
    vi.mocked(jmapMod.jmap.batch).mockImplementationOnce(async (builder: (b: unknown) => void) => {
      const calls: Array<[string, unknown]> = [];
      const using = new Set<string>();
      builder({
        call: (name: string, args: unknown, usingArg: readonly string[] = []) => {
          calls.push([name, args]);
          for (const u of usingArg) using.add(u);
          return { ref: () => ({}) };
        },
      });
      expect(calls[0]?.[0]).toBe('Email/unsubscribe');
      expect(calls[0]?.[1]).toEqual({ accountId: 'acct-1', emailId: 'e1' });
      expect(using.has('https://netzhansa.com/jmap/unsubscribe')).toBe(true);
      return {
        responses: [
          invocation('Email/unsubscribe', { emailId: 'e1', status: 'ok', httpStatus: 200 }),
        ],
      };
    });

    const result = await unsubscribeMod.postOneClickUnsubscribe('acct-1', 'e1');
    expect(result).toEqual({ emailId: 'e1', status: 'ok', httpStatus: 200 });
  });

  it('returns a "failed" status with the upstream detail on a non-2xx response', async () => {
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/unsubscribe', {
          emailId: 'e1',
          status: 'failed',
          httpStatus: 500,
          error: 'upstream returned 500 Internal Server Error',
        }),
      ],
    });

    const result = await unsubscribeMod.postOneClickUnsubscribe('acct-1', 'e1');
    expect(result).toEqual({
      emailId: 'e1',
      status: 'failed',
      httpStatus: 500,
      error: 'upstream returned 500 Internal Server Error',
    });
  });

  it('returns an "unsupported" status when the message has no valid one-click pair', async () => {
    vi.mocked(jmapMod.jmap.batch).mockResolvedValueOnce({
      responses: [
        invocation('Email/unsubscribe', {
          emailId: 'e1',
          status: 'unsupported',
          error: 'no HTTPS List-Unsubscribe URL',
        }),
      ],
    });

    const result = await unsubscribeMod.postOneClickUnsubscribe('acct-1', 'e1');
    expect(result).toEqual({
      emailId: 'e1',
      status: 'unsupported',
      error: 'no HTTPS List-Unsubscribe URL',
    });
  });
});
