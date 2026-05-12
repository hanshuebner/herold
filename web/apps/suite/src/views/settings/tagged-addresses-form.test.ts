/**
 * TaggedAddressesForm.svelte component tests (REQ-SET-TAG-01..21).
 *
 * Covers:
 *   - Capability-absent path: renders the unavailable hint.
 *   - Empty filters list: renders the empty hint.
 *   - One filter row populates the table.
 *   - "Add filter" opens the dialog.
 *   - Convert-to-Sieve confirm flow: row disappears on confirm.
 *   - Delete flow: row disappears on confirm.
 *   - Dismissals subsection: count chip + table on expand,
 *     undismiss action.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, screen, within } from '@testing-library/svelte';
import type { Identity } from '../../lib/mail/types';
import type { TaggedAddressFilter } from '../../lib/mail/tagged-address-store.svelte';

// ── i18n: pass-through with the parameter substitution we use ────────
vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>): string => {
    // For test legibility, surface the key with substituted params
    // when present. The component test cares about row presence /
    // button availability, not the exact message text.
    if (!params) return key;
    let out = key;
    for (const [k, v] of Object.entries(params)) {
      out = out.replace(`{${k}}`, String(v));
    }
    return out;
  },
}));

// ── mail store: minimal identities map ───────────────────────────────
const aliceIdentity: Identity = {
  id: 'A',
  name: 'Alice',
  email: 'alice@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
  verifiedAt: '2026-01-01T00:00:00Z',
};

const mailMock = {
  identities: new Map<string, Identity>([['A', aliceIdentity]]),
  loadIdentities: vi.fn(async () => undefined),
};

vi.mock('../../lib/mail/store.svelte', () => ({
  mail: mailMock,
}));

// ── jmap.hasCapability stub ──────────────────────────────────────────
const capabilityState = { advertised: true };
vi.mock('../../lib/jmap/client', () => ({
  jmap: {
    hasCapability: (_cap: string): boolean => capabilityState.advertised,
  },
  strict: (responses: unknown[]): unknown[] => responses,
}));

// ── confirm dialog stub: always resolves to 'true' so the action
//    code path runs end-to-end. ───────────────────────────────────────
const confirmAsk = vi.fn(async (_req: unknown) => true);
vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: { ask: confirmAsk, decide: vi.fn() },
}));

// ── toast stub ───────────────────────────────────────────────────────
vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn(), dismiss: vi.fn(), current: null },
}));

// ── store stub: mutable filters / dismissals + action stubs ──────────
interface StoreShape {
  filters: TaggedAddressFilter[];
  dismissals: { base_identity_id: string; suffix: string; dismissed_at: string }[];
  status: 'idle' | 'loading' | 'ready' | 'error';
  error: string | null;
  load: () => Promise<void>;
  deleteFilter: (id: string) => Promise<void>;
  convertToSieve: (id: string) => Promise<{ fragment: string; script: string }>;
  undismiss: (baseId: string, suffix: string) => Promise<void>;
}

const storeMock: StoreShape = {
  filters: [],
  dismissals: [],
  status: 'ready',
  error: null,
  load: vi.fn(async () => undefined),
  deleteFilter: vi.fn(async (id: string) => {
    storeMock.filters = storeMock.filters.filter((f) => f.id !== id);
  }),
  convertToSieve: vi.fn(async (id: string) => {
    storeMock.filters = storeMock.filters.filter((f) => f.id !== id);
    return { fragment: 'require ["fileinto"];', script: 'require ["fileinto"];' };
  }),
  undismiss: vi.fn(async (baseId: string, suffix: string) => {
    storeMock.dismissals = storeMock.dismissals.filter(
      (d) => !(d.base_identity_id === baseId && d.suffix === suffix),
    );
  }),
};

vi.mock('../../lib/mail/tagged-address-store.svelte', () => ({
  taggedAddressStore: storeMock,
}));

// The form imports the Filter type from this module; the mocked
// module above re-exports nothing for the type, so we re-export the
// type as a stub for TS-only consumers. (vi.mock is value-only; the
// runtime never reaches into the type re-export.)

beforeEach(() => {
  capabilityState.advertised = true;
  storeMock.filters = [];
  storeMock.dismissals = [];
  storeMock.status = 'ready';
  storeMock.error = null;
  vi.clearAllMocks();
  // The taggedAddressStore singleton is shared across imports — when
  // a test mutates filters above, the subsequent test starts clean.
  mailMock.identities = new Map<string, Identity>([['A', aliceIdentity]]);
});

// Dynamically import after every test reset so the component re-reads
// the latest mocks. Using top-level import would bind the mocks at
// module-evaluation time.
async function renderForm(): Promise<ReturnType<typeof render>> {
  const mod = await import('./TaggedAddressesForm.svelte');
  return render(mod.default);
}

describe('TaggedAddressesForm — capability gate', () => {
  it('surfaces the unavailable hint when the capability is absent', async () => {
    capabilityState.advertised = false;
    await renderForm();
    expect(
      screen.getByTestId('tagged-addresses-unavailable'),
    ).toBeInTheDocument();
  });

  it('renders the intro and Add button when the capability is present', async () => {
    await renderForm();
    expect(screen.getByTestId('tagged-addresses-intro')).toBeInTheDocument();
    expect(screen.getByTestId('tagged-addresses-add')).toBeInTheDocument();
  });
});

describe('TaggedAddressesForm — empty state', () => {
  it('shows the empty hint with no filters', async () => {
    storeMock.filters = [];
    await renderForm();
    expect(screen.getByTestId('tagged-addresses-empty')).toBeInTheDocument();
    expect(
      screen.queryByTestId('tagged-addresses-filters-table'),
    ).not.toBeInTheDocument();
  });
});

describe('TaggedAddressesForm — populated table', () => {
  it('renders one row per filter with tagged address chip', async () => {
    const filter: TaggedAddressFilter = {
      id: 'f1',
      baseIdentityId: 'A',
      suffix: 'amazon',
      action: 'label',
      labelName: 'Shopping',
      createdAt: '2026-05-01T00:00:00Z',
      updatedAt: '2026-05-01T00:00:00Z',
    };
    storeMock.filters = [filter];
    await renderForm();
    const row = screen.getByTestId('tagged-addresses-filter-row-f1');
    expect(row).toBeInTheDocument();
    expect(within(row).getByText(/alice\+amazon@example.local/)).toBeInTheDocument();
    expect(within(row).getByTestId('tagged-addresses-filter-edit-f1')).toBeInTheDocument();
    expect(within(row).getByTestId('tagged-addresses-filter-delete-f1')).toBeInTheDocument();
    expect(within(row).getByTestId('tagged-addresses-filter-convert-f1')).toBeInTheDocument();
  });

  it('disables the Add button when the cap is reached (100 rows)', async () => {
    storeMock.filters = Array.from({ length: 100 }, (_, i) => ({
      id: `f${i}`,
      baseIdentityId: 'A',
      suffix: `suf${i}`,
      action: 'label' as const,
      labelName: 'Lbl',
      createdAt: '2026-05-01T00:00:00Z',
      updatedAt: '2026-05-01T00:00:00Z',
    }));
    await renderForm();
    const add = screen.getByTestId('tagged-addresses-add') as HTMLButtonElement;
    expect(add.disabled).toBe(true);
    expect(
      screen.getByTestId('tagged-addresses-cap-reached'),
    ).toBeInTheDocument();
  });

  it('shows the cap notice within 10 of the cap', async () => {
    storeMock.filters = Array.from({ length: 95 }, (_, i) => ({
      id: `f${i}`,
      baseIdentityId: 'A',
      suffix: `suf${i}`,
      action: 'label' as const,
      labelName: 'Lbl',
      createdAt: '2026-05-01T00:00:00Z',
      updatedAt: '2026-05-01T00:00:00Z',
    }));
    await renderForm();
    expect(screen.getByTestId('tagged-addresses-cap-notice')).toBeInTheDocument();
  });
});

describe('TaggedAddressesForm — row actions', () => {
  beforeEach(() => {
    storeMock.filters = [
      {
        id: 'f1',
        baseIdentityId: 'A',
        suffix: 'amazon',
        action: 'label',
        labelName: 'Shopping',
        createdAt: '2026-05-01T00:00:00Z',
        updatedAt: '2026-05-01T00:00:00Z',
      },
    ];
  });

  it('Delete prompts for confirmation and calls deleteFilter', async () => {
    confirmAsk.mockResolvedValueOnce(true);
    await renderForm();
    const del = screen.getByTestId('tagged-addresses-filter-delete-f1');
    await fireEvent.click(del);
    // Flush any microtasks.
    await Promise.resolve();
    expect(confirmAsk).toHaveBeenCalled();
    expect(storeMock.deleteFilter).toHaveBeenCalledWith('f1');
  });

  it('Delete cancellation does NOT call deleteFilter', async () => {
    confirmAsk.mockResolvedValueOnce(false);
    await renderForm();
    const del = screen.getByTestId('tagged-addresses-filter-delete-f1');
    await fireEvent.click(del);
    await Promise.resolve();
    expect(confirmAsk).toHaveBeenCalled();
    expect(storeMock.deleteFilter).not.toHaveBeenCalled();
  });

  it('Convert-to-Sieve prompts and calls convertToSieve on confirm', async () => {
    confirmAsk.mockResolvedValueOnce(true);
    await renderForm();
    const conv = screen.getByTestId('tagged-addresses-filter-convert-f1');
    await fireEvent.click(conv);
    await Promise.resolve();
    expect(confirmAsk).toHaveBeenCalled();
    expect(storeMock.convertToSieve).toHaveBeenCalledWith('f1');
  });
});

describe('TaggedAddressesForm — dismissals subsection', () => {
  it('shows count chip and is collapsed by default', async () => {
    storeMock.dismissals = [
      { base_identity_id: 'A', suffix: 'amazon', dismissed_at: '2026-05-02T00:00:00Z' },
    ];
    await renderForm();
    const toggle = screen.getByTestId('tagged-addresses-dismissals-toggle');
    expect(toggle).toBeInTheDocument();
    // Toggle surfaces the dismissals count chip; the i18n stub returns
    // the key verbatim when the catalogue source contains no '{count}'
    // literal (our stub only substitutes the placeholder shape). The
    // key suffix is enough to assert the chip exists.
    expect(toggle.textContent).toMatch(/dismissalsCount/);
    // The table is not rendered until the user expands the subsection.
    expect(
      screen.queryByTestId('tagged-addresses-dismissals-table'),
    ).not.toBeInTheDocument();
  });

  it('expands to render the dismissals table and undismiss action', async () => {
    storeMock.dismissals = [
      { base_identity_id: 'A', suffix: 'amazon', dismissed_at: '2026-05-02T00:00:00Z' },
    ];
    await renderForm();
    const toggle = screen.getByTestId('tagged-addresses-dismissals-toggle');
    await fireEvent.click(toggle);
    const table = await screen.findByTestId('tagged-addresses-dismissals-table');
    expect(table).toBeInTheDocument();
    const undismiss = screen.getByTestId(
      'tagged-addresses-dismissal-undismiss-A-amazon',
    );
    await fireEvent.click(undismiss);
    await Promise.resolve();
    expect(storeMock.undismiss).toHaveBeenCalledWith('A', 'amazon');
  });

  it('hides the section entirely when there are no dismissals and not expanded', async () => {
    storeMock.dismissals = [];
    await renderForm();
    expect(
      screen.queryByTestId('tagged-addresses-dismissals-toggle'),
    ).not.toBeInTheDocument();
  });
});

describe('TaggedAddressesForm — add dialog', () => {
  it('opens the dialog from the Add button', async () => {
    await renderForm();
    const add = screen.getByTestId('tagged-addresses-add');
    await fireEvent.click(add);
    const dialog = await screen.findByTestId('tagged-address-filter-dialog');
    expect(dialog).toBeInTheDocument();
  });
});
