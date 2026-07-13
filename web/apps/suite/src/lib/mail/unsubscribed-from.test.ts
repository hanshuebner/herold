/**
 * Unit tests for the client-local "unsubscribed-from" set (REQ-UNS-50).
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { recordUnsubscribed, isUnsubscribedFrom } from './unsubscribed-from';

vi.mock('../auth/auth.svelte', () => ({
  auth: { session: { username: 'alice' } },
}));

describe('unsubscribed-from set', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('is false before anything is recorded', () => {
    expect(isUnsubscribedFrom('list@example.com')).toBe(false);
  });

  it('is true after recording, case-insensitively', () => {
    recordUnsubscribed('List@Example.com');
    expect(isUnsubscribedFrom('list@example.com')).toBe(true);
    expect(isUnsubscribedFrom('LIST@EXAMPLE.COM')).toBe(true);
  });

  it('ignores blank input', () => {
    recordUnsubscribed('   ');
    expect(isUnsubscribedFrom('   ')).toBe(false);
  });
});
