/**
 * Tests for the prompt dialog store. Mirrors the confirm test surface:
 * ask/decide round-trips, cancellation when a new ask supplants a
 * pending one, decide-no-op when nothing is pending, and preservation
 * of the optional fields on the pending request.
 */
import { describe, it, expect, beforeEach } from 'vitest';
import { prompt } from './prompt.svelte';

beforeEach(() => {
  if (prompt.pending) {
    prompt.decide(null);
  }
});

describe('prompt.ask', () => {
  it('resolves with the provided value', async () => {
    const promise = prompt.ask({ label: 'URL' });
    expect(prompt.pending?.label).toBe('URL');
    prompt.decide('https://example.com');
    await expect(promise).resolves.toBe('https://example.com');
    expect(prompt.pending).toBeNull();
  });

  it('resolves null when decide(null) is called', async () => {
    const promise = prompt.ask({ label: 'URL' });
    prompt.decide(null);
    await expect(promise).resolves.toBeNull();
    expect(prompt.pending).toBeNull();
  });

  it('cancels the prior pending ask when a new ask comes in', async () => {
    const first = prompt.ask({ label: 'a' });
    const second = prompt.ask({ label: 'b' });
    await expect(first).resolves.toBeNull();
    expect(prompt.pending?.label).toBe('b');
    prompt.decide('value');
    await expect(second).resolves.toBe('value');
  });

  it('decide is a no-op when nothing is pending', () => {
    prompt.decide('x');
    expect(prompt.pending).toBeNull();
  });

  it('preserves the optional fields on the pending request', () => {
    void prompt.ask({
      label: 'URL',
      title: 'Insert link',
      message: 'Link target.',
      defaultValue: 'https://',
      placeholder: 'https://example.com',
      confirmLabel: 'Insert',
      cancelLabel: 'No thanks',
      kind: 'danger',
      allowEmpty: true,
    });
    expect(prompt.pending?.title).toBe('Insert link');
    expect(prompt.pending?.message).toBe('Link target.');
    expect(prompt.pending?.defaultValue).toBe('https://');
    expect(prompt.pending?.placeholder).toBe('https://example.com');
    expect(prompt.pending?.confirmLabel).toBe('Insert');
    expect(prompt.pending?.cancelLabel).toBe('No thanks');
    expect(prompt.pending?.kind).toBe('danger');
    expect(prompt.pending?.allowEmpty).toBe(true);
  });
});
