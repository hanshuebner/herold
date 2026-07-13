/**
 * Unit tests for the `List-*` / unsubscribe header parsing helpers.
 * See docs/design/web/requirements/16-mailing-lists.md (REQ-LIST-01/02,
 * 20..22) and docs/design/web/requirements/14-unsubscribe.md
 * (REQ-UNS-01..23).
 */

import { describe, it, expect } from 'vitest';
import {
  parseAngleBracketUrls,
  pickPreferredAction,
  parseListId,
  parseListPostAddress,
  hasOneClickPost,
  chooseUnsubscribeMechanism,
  parseMailtoUri,
} from './list-headers';

describe('parseAngleBracketUrls', () => {
  it('returns empty for absent/blank header', () => {
    expect(parseAngleBracketUrls(null)).toEqual([]);
    expect(parseAngleBracketUrls(undefined)).toEqual([]);
    expect(parseAngleBracketUrls('')).toEqual([]);
  });

  it('extracts a single https URL', () => {
    expect(parseAngleBracketUrls('<https://example.com/list>')).toEqual([
      { scheme: 'https', url: 'https://example.com/list' },
    ]);
  });

  it('extracts multiple comma-separated alternatives, tagging each scheme', () => {
    const raw =
      '<https://example.com/unsub?id=1>, <mailto:unsub@example.com?subject=unsubscribe>';
    expect(parseAngleBracketUrls(raw)).toEqual([
      { scheme: 'https', url: 'https://example.com/unsub?id=1' },
      { scheme: 'mailto', url: 'mailto:unsub@example.com?subject=unsubscribe' },
    ]);
  });

  it('tags a cleartext http URL distinctly from https', () => {
    expect(parseAngleBracketUrls('<http://example.com/unsub>')).toEqual([
      { scheme: 'http', url: 'http://example.com/unsub' },
    ]);
  });

  it('drops unrecognised schemes', () => {
    expect(parseAngleBracketUrls('<ftp://example.com/x>')).toEqual([]);
  });
});

describe('pickPreferredAction', () => {
  it('prefers https over mailto when both present and mailto allowed', () => {
    const urls = parseAngleBracketUrls(
      '<mailto:help@example.com>, <https://example.com/help>',
    );
    expect(pickPreferredAction(urls, { allowMailto: true })).toEqual({
      kind: 'https',
      url: 'https://example.com/help',
    });
  });

  it('falls back to mailto when no https present and mailto allowed', () => {
    const urls = parseAngleBracketUrls('<mailto:help@example.com>');
    expect(pickPreferredAction(urls, { allowMailto: true })).toEqual({
      kind: 'mailto',
      url: 'mailto:help@example.com',
    });
  });

  it('ignores mailto when not allowed (archive action)', () => {
    const urls = parseAngleBracketUrls('<mailto:archive@example.com>');
    expect(pickPreferredAction(urls, { allowMailto: false })).toBeNull();
  });

  it('flags a cleartext-only URL as http-only rather than dropping it', () => {
    const urls = parseAngleBracketUrls('<http://example.com/archive>');
    expect(pickPreferredAction(urls, { allowMailto: false })).toEqual({
      kind: 'http-only',
      url: 'http://example.com/archive',
    });
  });

  it('returns null when no URL at all', () => {
    expect(pickPreferredAction([], { allowMailto: true })).toBeNull();
  });
});

describe('parseListId', () => {
  it('REQ-LIST-02: uses the quoted description as the label', () => {
    expect(
      parseListId('"Project X discuss" <projectx-discuss.example.com>'),
    ).toEqual({ id: 'projectx-discuss.example.com', label: 'Project X discuss' });
  });

  it('falls back to the local part of the identifier when no description', () => {
    expect(parseListId('<projectx-discuss.example.com>')).toEqual({
      id: 'projectx-discuss.example.com',
      label: 'projectx-discuss',
    });
  });

  it('handles an identifier with no dot at all (fallback is the whole id)', () => {
    expect(parseListId('<justanid>')).toEqual({ id: 'justanid', label: 'justanid' });
  });

  it('unescapes a backslash-escaped quote in the description', () => {
    expect(parseListId('"Say \\"hi\\"" <a.example.com>')).toEqual({
      id: 'a.example.com',
      label: 'Say "hi"',
    });
  });

  it('returns null for absent/blank/malformed header', () => {
    expect(parseListId(null)).toBeNull();
    expect(parseListId(undefined)).toBeNull();
    expect(parseListId('')).toBeNull();
    expect(parseListId('not a list id header')).toBeNull();
  });
});

describe('parseListPostAddress', () => {
  it('extracts the mailto address from angle-bracket form', () => {
    expect(parseListPostAddress('<mailto:list@example.com>')).toBe('list@example.com');
  });

  it('REQ-LIST-22: returns null for the literal NO (no posting allowed)', () => {
    expect(parseListPostAddress('NO')).toBeNull();
    expect(parseListPostAddress('no')).toBeNull();
  });

  it('returns null when absent', () => {
    expect(parseListPostAddress(null)).toBeNull();
    expect(parseListPostAddress(undefined)).toBeNull();
    expect(parseListPostAddress('')).toBeNull();
  });
});

describe('hasOneClickPost', () => {
  it('detects the RFC 8058 marker', () => {
    expect(hasOneClickPost('List-Unsubscribe=One-Click')).toBe(true);
  });

  it('is case-insensitive', () => {
    expect(hasOneClickPost('list-unsubscribe=one-click')).toBe(true);
  });

  it('is false for absent header or unrelated value', () => {
    expect(hasOneClickPost(null)).toBe(false);
    expect(hasOneClickPost(undefined)).toBe(false);
    expect(hasOneClickPost('something-else')).toBe(false);
  });
});

describe('chooseUnsubscribeMechanism', () => {
  it('REQ-UNS-20: prefers one-click when Post header + https URL both present', () => {
    const mechanism = chooseUnsubscribeMechanism(
      '<https://example.com/unsub?id=1>, <mailto:unsub@example.com>',
      'List-Unsubscribe=One-Click',
    );
    expect(mechanism).toEqual({ kind: 'one-click', url: 'https://example.com/unsub?id=1' });
  });

  it('REQ-UNS-21: falls back to plain https when no one-click marker', () => {
    const mechanism = chooseUnsubscribeMechanism(
      '<https://example.com/unsub?id=1>, <mailto:unsub@example.com>',
      null,
    );
    expect(mechanism).toEqual({ kind: 'https', url: 'https://example.com/unsub?id=1' });
  });

  it('REQ-UNS-22/23: uses mailto when only mailto is present, one-click silent fallback', () => {
    const mechanism = chooseUnsubscribeMechanism('<mailto:unsub@example.com>', null);
    expect(mechanism).toEqual({ kind: 'mailto', url: 'mailto:unsub@example.com' });
  });

  it('REQ-UNS-04: a cleartext-only http URL is flagged, not silently dropped', () => {
    const mechanism = chooseUnsubscribeMechanism('<http://example.com/unsub>', null);
    expect(mechanism).toEqual({ kind: 'http-only', url: 'http://example.com/unsub' });
  });

  it('one-click marker without an https URL does not trigger one-click', () => {
    const mechanism = chooseUnsubscribeMechanism(
      '<mailto:unsub@example.com>',
      'List-Unsubscribe=One-Click',
    );
    expect(mechanism).toEqual({ kind: 'mailto', url: 'mailto:unsub@example.com' });
  });

  it('REQ-UNS-03: returns null when List-Unsubscribe is absent', () => {
    expect(chooseUnsubscribeMechanism(null, 'List-Unsubscribe=One-Click')).toBeNull();
    expect(chooseUnsubscribeMechanism(undefined, undefined)).toBeNull();
    expect(chooseUnsubscribeMechanism('', null)).toBeNull();
  });
});

describe('parseMailtoUri', () => {
  it('parses to/subject/body from URI parameters', () => {
    expect(
      parseMailtoUri('mailto:unsub@example.com?subject=Unsubscribe&body=please%20remove%20me'),
    ).toEqual({ to: 'unsub@example.com', subject: 'Unsubscribe', body: 'please remove me' });
  });

  it('defaults subject/body to empty string when absent', () => {
    expect(parseMailtoUri('mailto:unsub@example.com')).toEqual({
      to: 'unsub@example.com',
      subject: '',
      body: '',
    });
  });

  it('falls back to the raw text on malformed percent-encoding rather than throwing', () => {
    expect(() => parseMailtoUri('mailto:%E0%A4%A')).not.toThrow();
  });
});
