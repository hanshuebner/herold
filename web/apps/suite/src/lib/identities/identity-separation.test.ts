import { describe, it, expect } from 'vitest';
import type { Identity } from '../mail/types';
import {
  separationOf,
  separationState,
  canSeparate,
  separationProgress,
} from './identity-separation';

function makeIdentity(overrides: Partial<Identity> = {}): Identity {
  return {
    id: '1',
    name: 'Alice',
    email: 'alice@example.com',
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
    ...overrides,
  };
}

describe('separationOf', () => {
  it('defaults to state none for a legacy server omitting the field', () => {
    const id = makeIdentity({ separation: undefined });
    expect(separationOf(id)).toEqual({
      state: 'none',
      messagesTotal: 0,
      messagesMoved: 0,
      messagesCopied: 0,
    });
    expect(separationState(id)).toBe('none');
  });

  it('reads the wire value when present', () => {
    const id = makeIdentity({
      separation: { state: 'migrating', messagesTotal: 10, messagesMoved: 3, messagesCopied: 1 },
    });
    expect(separationState(id)).toBe('migrating');
    expect(separationProgress(id)).toBe(4);
  });
});

describe('canSeparate', () => {
  it('is true for a non-default identity that has never been separated', () => {
    const id = makeIdentity({ mayDelete: true, separation: { state: 'none', messagesTotal: 5, messagesMoved: 0, messagesCopied: 0 } });
    expect(canSeparate(id)).toBe(true);
  });

  it('is false for the synthesized default identity (mayDelete: false)', () => {
    const id = makeIdentity({ mayDelete: false });
    expect(canSeparate(id)).toBe(false);
  });

  it('is false once separation has started (migrating or separated)', () => {
    const migrating = makeIdentity({ separation: { state: 'migrating', messagesTotal: 5, messagesMoved: 1, messagesCopied: 0 } });
    const separated = makeIdentity({ separation: { state: 'separated', messagesTotal: 5, messagesMoved: 5, messagesCopied: 0 } });
    expect(canSeparate(migrating)).toBe(false);
    expect(canSeparate(separated)).toBe(false);
  });
});
