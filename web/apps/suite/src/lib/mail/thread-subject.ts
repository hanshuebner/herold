import type { Email } from './types';

/**
 * Strips leading Re:, Fwd:, and Fw: prefixes (RFC 5256 §2.1-style) from a
 * subject string and returns the trimmed base subject. Handles multiple
 * stacked prefixes (e.g. "Re: Fwd: Re: Topic").
 */
export function baseSubject(subject: string): string {
  let s = subject.trim();
  const RE_PREFIX = /^(?:re|fwd?)\s*:\s*/i;
  let prev: string;
  do {
    prev = s;
    s = s.replace(RE_PREFIX, '').trim();
  } while (s !== prev);
  return s;
}

/**
 * Derives the best thread title from an array of thread emails (re #150).
 *
 * Iterates through `emails` in order and returns the base subject (Re:/Fwd:
 * prefixes stripped per RFC 5256 §2.1) of the first email that has a
 * non-empty base subject. Falls back to `fallback` when every email in the
 * thread has a bare or empty subject.
 *
 * This resolves the case where the oldest message has a bare `Re:` while a
 * later reply carries the real subject — the previous `emails[0]?.subject`
 * approach always picked the oldest, even when it was meaningless.
 */
export function threadSubject(emails: Email[], fallback: string): string {
  for (const email of emails) {
    const base = baseSubject(email.subject ?? '');
    if (base.length > 0) return base;
  }
  return fallback;
}
