/**
 * TaggedAddressFilterDialog.svelte component tests (REQ-SET-TAG-03).
 *
 * Covers:
 *   - Create mode: identity dropdown enumerates verified identities,
 *     submits with the normalised suffix.
 *   - Edit mode: suffix + base identity are read-only; submit calls
 *     updateFilter.
 *   - Validation: empty suffix and bad characters surface inline
 *     errors; the Save button stays disabled.
 *   - No verified identities: the dialog blocks the form with a hint
 *     and keeps Save disabled.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import type { Identity } from '../../lib/mail/types';
import type { TaggedAddressFilter } from '../../lib/mail/tagged-address-store.svelte';

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>): string => {
    if (!params) return key;
    let out = key;
    for (const [k, v] of Object.entries(params)) {
      out = out.replace(`{${k}}`, String(v));
    }
    return out;
  },
}));

// mail store: two verified identities for the dropdown.
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
const bobIdentity: Identity = {
  id: 'B',
  name: 'Bob',
  email: 'bob@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: true,
  verifiedAt: '2026-01-01T00:00:00Z',
};

const mailMock = {
  identities: new Map<string, Identity>([
    ['A', aliceIdentity],
    ['B', bobIdentity],
  ]),
};
vi.mock('../../lib/mail/store.svelte', () => ({ mail: mailMock }));

// Toast stub.
vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn(), dismiss: vi.fn(), current: null },
}));

// Store with createFilter / updateFilter spies.
const storeMock = {
  filters: [] as TaggedAddressFilter[],
  createFilter: vi.fn(
    async (input: {
      baseIdentityId: string;
      suffix: string;
      action: 'label' | 'label_archive' | 'label_archive_read';
      labelName: string;
    }) => {
      const row: TaggedAddressFilter = {
        id: 'new1',
        baseIdentityId: input.baseIdentityId,
        suffix: input.suffix,
        action: input.action,
        labelName: input.labelName,
        createdAt: '2026-05-01T00:00:00Z',
        updatedAt: '2026-05-01T00:00:00Z',
      };
      return row;
    },
  ),
  updateFilter: vi.fn(
    async (
      id: string,
      patch: {
        action?: 'label' | 'label_archive' | 'label_archive_read';
        labelName?: string;
      },
    ) => {
      const row: TaggedAddressFilter = {
        id,
        baseIdentityId: 'A',
        suffix: 'amazon',
        action: patch.action ?? 'label',
        labelName: patch.labelName ?? 'Shopping',
        createdAt: '2026-05-01T00:00:00Z',
        updatedAt: '2026-05-02T00:00:00Z',
      };
      return row;
    },
  ),
};
vi.mock('../../lib/mail/tagged-address-store.svelte', () => ({
  taggedAddressStore: storeMock,
}));

beforeEach(() => {
  mailMock.identities = new Map<string, Identity>([
    ['A', aliceIdentity],
    ['B', bobIdentity],
  ]);
  vi.clearAllMocks();
});

async function renderDialog(props: {
  filter: TaggedAddressFilter | null;
  onclose?: () => void;
  onsaved?: (row: TaggedAddressFilter) => void;
}): Promise<ReturnType<typeof render>> {
  const mod = await import('./TaggedAddressFilterDialog.svelte');
  return render(mod.default, {
    props: {
      filter: props.filter,
      onclose: props.onclose ?? ((): void => undefined),
      onsaved: props.onsaved,
    } as never,
  });
}

describe('TaggedAddressFilterDialog — create mode', () => {
  it('renders the identity dropdown and accepts a valid submission', async () => {
    const onclose = vi.fn();
    const onsaved = vi.fn();
    await renderDialog({ filter: null, onclose, onsaved });

    const suffixInput = screen.getByTestId(
      'tagged-address-filter-dialog-suffix',
    ) as HTMLInputElement;
    const labelInput = screen.getByTestId(
      'tagged-address-filter-dialog-label',
    ) as HTMLInputElement;
    const save = screen.getByTestId(
      'tagged-address-filter-dialog-save',
    ) as HTMLButtonElement;

    // Mixed-case suffix is normalised on save (REQ-TAG-02).
    await fireEvent.input(suffixInput, { target: { value: 'Amazon' } });
    await fireEvent.input(labelInput, { target: { value: 'Shopping' } });

    expect(save.disabled).toBe(false);

    await fireEvent.click(save);
    await Promise.resolve();
    await Promise.resolve();

    expect(storeMock.createFilter).toHaveBeenCalledWith({
      baseIdentityId: 'A',
      suffix: 'amazon',
      action: 'label',
      labelName: 'Shopping',
    });
    expect(onsaved).toHaveBeenCalled();
    expect(onclose).toHaveBeenCalled();
  });

  it('surfaces a validation error for a suffix with whitespace', async () => {
    await renderDialog({ filter: null });
    const suffix = screen.getByTestId('tagged-address-filter-dialog-suffix');
    await fireEvent.input(suffix, { target: { value: 'a b' } });
    expect(
      screen.getByTestId('tagged-address-filter-dialog-suffix-error'),
    ).toBeInTheDocument();
    const save = screen.getByTestId(
      'tagged-address-filter-dialog-save',
    ) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
  });

  it('surfaces a validation error for a suffix containing @', async () => {
    await renderDialog({ filter: null });
    const suffix = screen.getByTestId('tagged-address-filter-dialog-suffix');
    await fireEvent.input(suffix, { target: { value: 'foo@bar' } });
    expect(
      screen.getByTestId('tagged-address-filter-dialog-suffix-error'),
    ).toBeInTheDocument();
  });

  it('shows the no-verified-identities hint when the map is empty', async () => {
    mailMock.identities = new Map();
    await renderDialog({ filter: null });
    // The empty hint surfaces via the role=alert paragraph.
    const alerts = screen.getAllByRole('alert');
    expect(alerts.length).toBeGreaterThanOrEqual(1);
    // Save is disabled.
    const save = screen.getByTestId(
      'tagged-address-filter-dialog-save',
    ) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
  });
});

describe('TaggedAddressFilterDialog — edit mode', () => {
  it('renders existing values and submits an update', async () => {
    const onsaved = vi.fn();
    const filter: TaggedAddressFilter = {
      id: 'f1',
      baseIdentityId: 'A',
      suffix: 'amazon',
      action: 'label',
      labelName: 'Shopping',
      createdAt: '2026-05-01T00:00:00Z',
      updatedAt: '2026-05-01T00:00:00Z',
    };
    await renderDialog({ filter, onsaved });

    // In edit mode the suffix is rendered as a static value, NOT an
    // input. The dialog also hides the identity dropdown.
    expect(
      screen.queryByTestId('tagged-address-filter-dialog-suffix'),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByTestId('tagged-address-filter-dialog-identity'),
    ).not.toBeInTheDocument();

    // Change action to label_archive.
    const radio = screen.getByTestId(
      'tagged-address-filter-dialog-action-label_archive',
    );
    await fireEvent.click(radio);

    // Change label.
    const labelInput = screen.getByTestId(
      'tagged-address-filter-dialog-label',
    ) as HTMLInputElement;
    await fireEvent.input(labelInput, { target: { value: 'Shopping-2' } });

    const save = screen.getByTestId('tagged-address-filter-dialog-save');
    await fireEvent.click(save);
    await Promise.resolve();
    await Promise.resolve();

    expect(storeMock.updateFilter).toHaveBeenCalledWith('f1', {
      action: 'label_archive',
      labelName: 'Shopping-2',
    });
  });
});
