/**
 * Unit tests for thread-subject helpers (re #150).
 *
 * threadSubject() must pick the first email in the thread with a non-empty
 * base subject (Re:/Fwd: stripped), rather than blindly using the oldest
 * message. baseSubject() must strip RFC 5256 §2.1-style prefixes.
 */

import { describe, it, expect } from 'vitest';
import { baseSubject, threadSubject } from './thread-subject';
import type { Email } from './types';

// Minimal Email stub — only the properties threadSubject reads.
function makeEmail(subject: string | null): Email {
  return {
    id: 'stub',
    threadId: 'tid',
    mailboxIds: { inbox: true } as Record<string, true>,
    keywords: {},
    from: null,
    to: null,
    subject,
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    blobId: 'blob',
    'header:List-ID:asText': null,
  } as Email;
}

describe('baseSubject', () => {
  it('returns plain subject unchanged', () => {
    expect(baseSubject('Hello World')).toBe('Hello World');
  });

  it('strips a single "Re: " prefix', () => {
    expect(baseSubject('Re: Hello World')).toBe('Hello World');
  });

  it('strips a single "Fwd: " prefix', () => {
    expect(baseSubject('Fwd: Hello World')).toBe('Hello World');
  });

  it('strips "Fw: " prefix', () => {
    expect(baseSubject('Fw: Hello World')).toBe('Hello World');
  });

  it('strips case-insensitively', () => {
    expect(baseSubject('RE: Hello')).toBe('Hello');
    expect(baseSubject('FWD: Hello')).toBe('Hello');
    expect(baseSubject('re: Hello')).toBe('Hello');
  });

  it('strips stacked prefixes', () => {
    expect(baseSubject('Re: Fwd: Re: Topic')).toBe('Topic');
  });

  it('returns empty string for a bare "Re:" with no base', () => {
    expect(baseSubject('Re:')).toBe('');
    expect(baseSubject('Re: ')).toBe('');
  });

  it('returns empty string for an empty input', () => {
    expect(baseSubject('')).toBe('');
  });

  it('trims surrounding whitespace', () => {
    expect(baseSubject('  Re: Hello  ')).toBe('Hello');
  });

  it('handles non-ASCII subjects unchanged', () => {
    expect(baseSubject('Re: Kostenübernahme Vereinstreffen')).toBe(
      'Kostenübernahme Vereinstreffen',
    );
  });
});

describe('threadSubject (re #150)', () => {
  it('returns the base subject of the first non-empty message', () => {
    const emails = [makeEmail('Re:'), makeEmail('Re:'), makeEmail('Re: Kostenübernahme Vereinstreffen')];
    expect(threadSubject(emails, '(no subject)')).toBe('Kostenübernahme Vereinstreffen');
  });

  it('returns the first meaningful subject even when it is the first message', () => {
    const emails = [makeEmail('Hello World'), makeEmail('Re: Hello World')];
    expect(threadSubject(emails, '(no subject)')).toBe('Hello World');
  });

  it('returns the fallback when every message has a bare subject', () => {
    const emails = [makeEmail('Re:'), makeEmail('Re: '), makeEmail(null)];
    expect(threadSubject(emails, '(no subject)')).toBe('(no subject)');
  });

  it('returns the fallback for an empty thread', () => {
    expect(threadSubject([], '(no subject)')).toBe('(no subject)');
  });

  it('skips null subjects', () => {
    const emails = [makeEmail(null), makeEmail('Re: Real Subject')];
    expect(threadSubject(emails, '(no subject)')).toBe('Real Subject');
  });

  it('returns the base subject (Re: stripped) even when the first message has a real subject', () => {
    // When the oldest message is "Re: Topic", the title is "Topic" (no Re: prefix).
    const emails = [makeEmail('Re: Topic'), makeEmail('Re: Topic')];
    expect(threadSubject(emails, '(no subject)')).toBe('Topic');
  });

  it('handles the exact reported scenario: oldest has bare Re:, third message has real subject', () => {
    // Mirrors thread 1745 from the bug report.
    const emails = [
      makeEmail('Re:'),
      makeEmail('Re:'),
      makeEmail('Re: Kostenübernahme Vereinstreffen'),
    ];
    expect(threadSubject(emails, '(kein Betreff)')).toBe('Kostenübernahme Vereinstreffen');
  });
});
