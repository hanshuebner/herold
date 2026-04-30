/**
 * Tests for chip label rendering in RecipientField (issue #45).
 *
 * Verifies:
 *   - A chip with a name shows the name only; the email is on the title attribute.
 *   - A chip with no name shows the full email.
 *   - A chip with an extremely long name still ellipsises but does NOT show
 *     the email as part of the visible text.
 */
import { describe, it, expect, vi } from 'vitest';
import { render } from '@testing-library/svelte';
import RecipientField from './RecipientField.svelte';
import type { Recipient } from './recipient-parse';

// Stub the contacts store so it does not make network calls.
vi.mock('../contacts/store.svelte', () => ({
  contacts: {
    status: 'idle',
    suggestions: [],
    filter: () => [],
    load: vi.fn(),
  },
}));

// Stub seenAddresses.
vi.mock('../contacts/seen-addresses.svelte', () => ({
  seenAddresses: {
    status: 'idle',
    entries: [],
    load: vi.fn(),
  },
}));

// Stub JMAP client.
vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn() },
  strict: vi.fn(),
}));

// Stub auth.
vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: {},
    },
  },
}));

function renderField(chips: Recipient[]) {
  return render(RecipientField, {
    label: 'To',
    chips,
    onChipsChange: vi.fn(),
    onWarning: vi.fn(),
  });
}

describe('RecipientField chip label rendering (issue #45)', () => {
  it('shows name only when chip has a name, with email in title', () => {
    const { container } = renderField([{ name: 'Alice Smith', email: 'alice@example.com' }]);
    const label = container.querySelector('.chip-label') as HTMLElement;
    expect(label).not.toBeNull();
    // Visible text must be exactly the name, with no email fragment.
    expect(label.textContent?.trim()).toBe('Alice Smith');
    // Email is accessible via title attribute.
    expect(label.getAttribute('title')).toBe('alice@example.com');
  });

  it('shows email when chip has no name, with no title attribute', () => {
    const { container } = renderField([{ email: 'bob@example.com' }]);
    const label = container.querySelector('.chip-label') as HTMLElement;
    expect(label).not.toBeNull();
    expect(label.textContent?.trim()).toBe('bob@example.com');
    // No title attribute needed when there is no name to abbreviate.
    expect(label.getAttribute('title')).toBeNull();
  });

  it('chip with an extremely long name shows only the name, not email text', () => {
    const longName = 'A'.repeat(200);
    const { container } = renderField([{ name: longName, email: 'long@example.com' }]);
    const label = container.querySelector('.chip-label') as HTMLElement;
    expect(label).not.toBeNull();
    // The visible text is the long name — the email must not appear in it.
    expect(label.textContent?.trim()).toBe(longName);
    expect(label.textContent).not.toContain('long@example.com');
    expect(label.getAttribute('title')).toBe('long@example.com');
  });
});

describe('RecipientField blur commits a complete address (REQ-MAIL-11t)', () => {
  it('typing a complete address and blurring commits it as a chip', async () => {
    const onChipsChange = vi.fn();
    const onWarning = vi.fn();
    const { container } = render(RecipientField, {
      label: 'To',
      chips: [],
      onChipsChange,
      onWarning,
    });
    const input = container.querySelector('input[type="text"]') as HTMLInputElement;
    expect(input).not.toBeNull();

    input.focus();
    input.value = 'alice@example.com';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    input.blur();

    // onBlur defers commit by 120 ms to allow click-on-suggestion to win.
    await new Promise((resolve) => setTimeout(resolve, 200));

    expect(onChipsChange).toHaveBeenCalledTimes(1);
    const chips = onChipsChange.mock.calls[0]?.[0] as Recipient[];
    expect(chips).toHaveLength(1);
    expect(chips[0]).toMatchObject({ email: 'alice@example.com' });
  });

  it('typing an unparseable buffer and blurring leaves a warning, not a chip', async () => {
    const onChipsChange = vi.fn();
    const onWarning = vi.fn();
    const { container } = render(RecipientField, {
      label: 'To',
      chips: [],
      onChipsChange,
      onWarning,
    });
    const input = container.querySelector('input[type="text"]') as HTMLInputElement;

    input.focus();
    input.value = 'not-an-email';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    input.blur();

    await new Promise((resolve) => setTimeout(resolve, 200));

    expect(onChipsChange).not.toHaveBeenCalled();
    // The latest onWarning call must surface the unrecognised text.
    const lastCall = onWarning.mock.calls.at(-1);
    expect(lastCall?.[0]).toMatch(/Couldn't recognize/);
  });
});
