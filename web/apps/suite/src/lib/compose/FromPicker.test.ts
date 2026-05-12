/**
 * FromPicker.svelte component tests (REQ-MAIL-12).
 *
 * Covers:
 *   - Rendering the trigger with the selected identity label.
 *   - Opening the dropdown reveals all identities in sorted order.
 *   - Each row shows the correct chip (verifying / unverified /
 *     external) and the disabled visual treatment with a tooltip.
 *   - Clicking a selectable row invokes onSelect; clicking a
 *     disabled row is a no-op.
 *   - Pressing Escape closes the dropdown.
 *   - data-testid hooks for puppeteer flows.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import { tick } from 'svelte';
import type { Identity } from '../mail/types';
import type { SubmissionSummary } from '../identities/identity-status';

// ── Mocks ────────────────────────────────────────────────────────────────

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string): string => {
    // Hand-rolled tiny translator for the picker's keys. Returning the
    // key when unrecognised so the assertions don't accidentally match
    // a fall-through string.
    const map: Record<string, string> = {
      'compose.from': 'From',
      'compose.from.picker.aria': 'From identity',
      'compose.from.picker.open': 'Change From identity',
      'compose.from.chip.verifying': 'Verification pending',
      'compose.from.chip.unverified': 'Unverified',
      'compose.from.chip.external': 'External SMTP missing',
      'compose.from.disabled.unverified':
        'Verify this address before you can send from it.',
      'compose.from.disabled.verifying':
        'Waiting for verification of this address.',
      'compose.from.disabled.external':
        'Set up external SMTP submission before you can send from this address.',
    };
    return map[key] ?? key;
  },
}));

import FromPicker from './FromPicker.svelte';

// ── Fixtures ─────────────────────────────────────────────────────────────

function makeIdentity(
  id: string,
  email: string,
  overrides: Partial<Identity> = {},
): Identity {
  return {
    id,
    name: '',
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
    ...overrides,
  };
}

const DEFAULT: Identity = makeIdentity('1', 'alice@example.local', {
  name: 'Alice',
  verifiedAt: '2026-01-01T00:00:00Z',
  isDefault: true,
});

const VERIFIED_OTHER: Identity = makeIdentity('2', 'bob@example.local', {
  name: 'Bob',
  verifiedAt: '2026-02-01T00:00:00Z',
});

const VERIFYING: Identity = makeIdentity('3', 'pending@example.local', {
  verifiedAt: null,
  verificationPendingSince: '2026-05-09T00:00:00Z',
});

const UNVERIFIED: Identity = makeIdentity('4', 'unverified@example.local', {
  verifiedAt: null,
});

const EXTERNAL_BROKEN: Identity = makeIdentity('5', 'ext@example.local', {
  verifiedAt: '2026-03-01T00:00:00Z',
});

const noSub = (_id: string): SubmissionSummary | null => null;

const brokenResolver = (id: string): SubmissionSummary | null =>
  id === EXTERNAL_BROKEN.id ? { configured: true, state: 'auth-failed' } : null;

// ── Tests ────────────────────────────────────────────────────────────────

describe('FromPicker', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders the trigger with the selected identity label', () => {
    const onSelect = vi.fn();
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, VERIFIED_OTHER],
        selected: DEFAULT,
        submissionResolver: noSub,
        onSelect,
      },
    });
    const trigger = container.querySelector(
      '[data-testid="from-picker-trigger"]',
    );
    expect(trigger).not.toBeNull();
    expect(trigger?.textContent).toMatch(/Alice/);
    expect(trigger?.textContent).toMatch(/alice@example\.local/);
  });

  it('renders the dropdown closed by default (no list element in DOM)', () => {
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, VERIFIED_OTHER],
        selected: DEFAULT,
        submissionResolver: noSub,
        onSelect: vi.fn(),
      },
    });
    expect(
      container.querySelector('[data-testid="from-picker-list"]'),
    ).toBeNull();
  });

  it('opens the dropdown on trigger click and lists every identity', async () => {
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, VERIFIED_OTHER, VERIFYING, UNVERIFIED, EXTERNAL_BROKEN],
        selected: DEFAULT,
        submissionResolver: brokenResolver,
        onSelect: vi.fn(),
      },
    });
    const trigger = container.querySelector<HTMLButtonElement>(
      '[data-testid="from-picker-trigger"]',
    );
    await fireEvent.click(trigger!);
    await tick();
    const rows = container.querySelectorAll('[data-testid="from-picker-row"]');
    expect(rows.length).toBe(5);
  });

  it('sorts rows: default first, then verified, then verifying, then unverified, then external-broken', async () => {
    const { container } = render(FromPicker, {
      props: {
        // Pass in an out-of-order list to force the sort.
        identities: [
          EXTERNAL_BROKEN,
          UNVERIFIED,
          VERIFYING,
          VERIFIED_OTHER,
          DEFAULT,
        ],
        selected: DEFAULT,
        submissionResolver: brokenResolver,
        onSelect: vi.fn(),
      },
    });
    await fireEvent.click(
      container.querySelector('[data-testid="from-picker-trigger"]')!,
    );
    await tick();
    const rows = container.querySelectorAll<HTMLElement>(
      '[data-testid="from-picker-row"]',
    );
    const order = Array.from(rows).map((r) => r.dataset.identityId);
    expect(order).toEqual(['1', '2', '3', '4', '5']);
  });

  it('marks unverified, verifying, and external-broken rows as disabled', async () => {
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, VERIFYING, UNVERIFIED, EXTERNAL_BROKEN],
        selected: DEFAULT,
        submissionResolver: brokenResolver,
        onSelect: vi.fn(),
      },
    });
    await fireEvent.click(
      container.querySelector('[data-testid="from-picker-trigger"]')!,
    );
    await tick();
    const rows = container.querySelectorAll<HTMLButtonElement>(
      '[data-testid="from-picker-row"]',
    );
    // Row 0 is DEFAULT (id=1), row 1 = VERIFYING, row 2 = UNVERIFIED, row 3 = EXTERNAL_BROKEN.
    expect(rows[0]!.dataset.disabled).toBe('false');
    expect(rows[1]!.dataset.disabled).toBe('true');
    expect(rows[2]!.dataset.disabled).toBe('true');
    expect(rows[3]!.dataset.disabled).toBe('true');
    expect(rows[1]!.disabled).toBe(true);
    expect(rows[2]!.disabled).toBe(true);
    expect(rows[3]!.disabled).toBe(true);
  });

  it('renders the correct state chip on each non-selectable row', async () => {
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, VERIFYING, UNVERIFIED, EXTERNAL_BROKEN],
        selected: DEFAULT,
        submissionResolver: brokenResolver,
        onSelect: vi.fn(),
      },
    });
    await fireEvent.click(
      container.querySelector('[data-testid="from-picker-trigger"]')!,
    );
    await tick();
    const chips = Array.from(
      container.querySelectorAll<HTMLElement>('[data-testid="from-chip"]'),
    ).map((el) => el.textContent?.trim());
    expect(chips).toEqual([
      'Verification pending',
      'Unverified',
      'External SMTP missing',
    ]);
  });

  it('renders disabled-row tooltips mapped to the state', async () => {
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, VERIFYING, UNVERIFIED, EXTERNAL_BROKEN],
        selected: DEFAULT,
        submissionResolver: brokenResolver,
        onSelect: vi.fn(),
      },
    });
    await fireEvent.click(
      container.querySelector('[data-testid="from-picker-trigger"]')!,
    );
    await tick();
    const rows = container.querySelectorAll<HTMLButtonElement>(
      '[data-testid="from-picker-row"]',
    );
    expect(rows[1]!.title).toBe('Waiting for verification of this address.');
    expect(rows[2]!.title).toBe(
      'Verify this address before you can send from it.',
    );
    expect(rows[3]!.title).toBe(
      'Set up external SMTP submission before you can send from this address.',
    );
  });

  it('invokes onSelect when a selectable row is clicked', async () => {
    const onSelect = vi.fn();
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, VERIFIED_OTHER],
        selected: DEFAULT,
        submissionResolver: noSub,
        onSelect,
      },
    });
    await fireEvent.click(
      container.querySelector('[data-testid="from-picker-trigger"]')!,
    );
    await tick();
    const rows = container.querySelectorAll<HTMLButtonElement>(
      '[data-testid="from-picker-row"]',
    );
    // Click VERIFIED_OTHER (row 1) — DEFAULT is index 0.
    await fireEvent.click(rows[1]!);
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenCalledWith(VERIFIED_OTHER);
  });

  it('does NOT invoke onSelect when a disabled row is clicked', async () => {
    const onSelect = vi.fn();
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, UNVERIFIED],
        selected: DEFAULT,
        submissionResolver: noSub,
        onSelect,
      },
    });
    await fireEvent.click(
      container.querySelector('[data-testid="from-picker-trigger"]')!,
    );
    await tick();
    const rows = container.querySelectorAll<HTMLButtonElement>(
      '[data-testid="from-picker-row"]',
    );
    // UNVERIFIED is row 1. Native disabled buttons swallow click events;
    // we assert both the disabled attribute and the no-op behaviour.
    expect(rows[1]!.disabled).toBe(true);
    await fireEvent.click(rows[1]!);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it('marks the currently selected row as aria-selected', async () => {
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, VERIFIED_OTHER],
        selected: VERIFIED_OTHER,
        submissionResolver: noSub,
        onSelect: vi.fn(),
      },
    });
    await fireEvent.click(
      container.querySelector('[data-testid="from-picker-trigger"]')!,
    );
    await tick();
    const rows = container.querySelectorAll<HTMLButtonElement>(
      '[data-testid="from-picker-row"]',
    );
    expect(rows[0]!.getAttribute('aria-selected')).toBe('false'); // DEFAULT
    expect(rows[1]!.getAttribute('aria-selected')).toBe('true'); // VERIFIED_OTHER
  });

  it('closes the dropdown when Escape is pressed', async () => {
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, VERIFIED_OTHER],
        selected: DEFAULT,
        submissionResolver: noSub,
        onSelect: vi.fn(),
      },
    });
    await fireEvent.click(
      container.querySelector('[data-testid="from-picker-trigger"]')!,
    );
    await tick();
    expect(
      container.querySelector('[data-testid="from-picker-list"]'),
    ).not.toBeNull();
    const wrap = container.querySelector('.from-picker')!;
    await fireEvent.keyDown(wrap, { key: 'Escape' });
    await tick();
    expect(
      container.querySelector('[data-testid="from-picker-list"]'),
    ).toBeNull();
  });

  it('disables the trigger when the disabled prop is true', () => {
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, VERIFIED_OTHER],
        selected: DEFAULT,
        submissionResolver: noSub,
        onSelect: vi.fn(),
        disabled: true,
      },
    });
    const trigger = container.querySelector<HTMLButtonElement>(
      '[data-testid="from-picker-trigger"]',
    );
    expect(trigger?.disabled).toBe(true);
  });

  it('renders the [ext] cosmetic indicator on rows whose externalConfigured callback returns true', async () => {
    const externalConfigured = (id: string): boolean => id === DEFAULT.id;
    const { container } = render(FromPicker, {
      props: {
        identities: [DEFAULT, VERIFIED_OTHER],
        selected: DEFAULT,
        submissionResolver: noSub,
        externalConfigured,
        onSelect: vi.fn(),
      },
    });
    await fireEvent.click(
      container.querySelector('[data-testid="from-picker-trigger"]')!,
    );
    await tick();
    const rows = container.querySelectorAll<HTMLElement>(
      '[data-testid="from-picker-row"]',
    );
    // Row 0 (DEFAULT) should have an [ext] marker; row 1 (VERIFIED_OTHER) should not.
    expect(rows[0]!.textContent).toMatch(/\[ext\]/);
    expect(rows[1]!.textContent).not.toMatch(/\[ext\]/);
  });
});
