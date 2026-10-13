import { describe, it, expect } from 'vitest';
import {
  expandThreadExportUrl,
  threadDownloadLink,
  threadExportUrlTemplate,
} from './thread-download';

const CAP = 'https://netzhansa.com/jmap/thread-export';
const TEMPLATE = '/jmap/download-thread/{accountId}/{threadId}/{name}';

function session(capabilities: Record<string, unknown>) {
  return { capabilities };
}

describe('threadExportUrlTemplate', () => {
  it('returns the advertised downloadUrl', () => {
    expect(threadExportUrlTemplate(session({ [CAP]: { downloadUrl: TEMPLATE } }))).toBe(TEMPLATE);
  });

  it('returns null when the capability is absent, malformed, or the session is missing', () => {
    expect(threadExportUrlTemplate(session({}))).toBeNull();
    expect(threadExportUrlTemplate(session({ [CAP]: {} }))).toBeNull();
    expect(threadExportUrlTemplate(session({ [CAP]: { downloadUrl: 42 } }))).toBeNull();
    expect(threadExportUrlTemplate(null)).toBeNull();
  });
});

describe('expandThreadExportUrl', () => {
  it('substitutes and URL-encodes every variable', () => {
    expect(
      expandThreadExportUrl(TEMPLATE, {
        accountId: 'a/1',
        threadId: 'T 9',
        name: 'Plans & more.zip',
      }),
    ).toBe('/jmap/download-thread/a%2F1/T%209/Plans%20%26%20more.zip');
  });
});

describe('threadDownloadLink', () => {
  const withCap = session({ [CAP]: { downloadUrl: TEMPLATE } });

  it('derives href and filename from the subject', () => {
    expect(
      threadDownloadLink(withCap, { accountId: 'acc1', threadId: 'T42', subject: 'Hello: world' }),
    ).toEqual({
      href: '/jmap/download-thread/acc1/T42/Hello_%20world.zip',
      filename: 'Hello_ world.zip',
    });
  });

  it('uses the thread-id fallback filename for an empty subject', () => {
    expect(
      threadDownloadLink(withCap, { accountId: 'acc1', threadId: 'THREAD123456', subject: '' }),
    ).toEqual({
      href: '/jmap/download-thread/acc1/THREAD123456/thread-THREAD12.zip',
      filename: 'thread-THREAD12.zip',
    });
  });

  it('is null without the capability or without an account', () => {
    expect(
      threadDownloadLink(session({}), { accountId: 'acc1', threadId: 'T1', subject: 'x' }),
    ).toBeNull();
    expect(threadDownloadLink(withCap, { accountId: null, threadId: 'T1', subject: 'x' })).toBeNull();
  });
});
