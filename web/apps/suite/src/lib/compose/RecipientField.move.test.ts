/**
 * Tests for cross-field recipient move (re #272):
 *   - Chip dragstart encodes { field, index } as the
 *     application/x-herold-recipient MIME payload.
 *   - Dropping that payload on a *different* field's row calls
 *     onRecipientMove(fromField, index, thisField).
 *   - Dropping on the chip's *own* field is a no-op -- onRecipientMove is
 *     never called.
 *   - The keyboard-accessible "Move to..." menu offers the other two
 *     fields and calls onRecipientMove the same way a drop would.
 *
 * happy-dom's DragEvent does not support a writable `dataTransfer`
 * (real DragEvents guard it as read-only), so drag interactions are
 * simulated by dispatching a plain Event of type 'dragstart' / 'drop'
 * with a `dataTransfer` property attached directly -- Svelte's
 * `ondragstart={...}` / `ondrop={...}` are plain addEventListener calls
 * and do not care about the concrete Event subclass.
 */
import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import RecipientField from './RecipientField.svelte';
import type { Recipient, RecipientFieldName } from './recipient-parse';

vi.mock('../contacts/store.svelte', () => ({
  contacts: {
    status: 'idle',
    suggestions: [],
    filter: () => [],
    filterAsync: vi.fn(async () => []),
    load: vi.fn(),
  },
}));

vi.mock('../auth/capabilities', () => ({
  hasDirectoryAutocomplete: vi.fn(() => false),
}));

vi.mock('../contacts/seen-addresses.svelte', () => ({
  seenAddresses: {
    status: 'idle',
    entries: [],
    load: vi.fn(),
  },
}));

vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn() },
  strict: vi.fn(),
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: { session: { primaryAccounts: {} } },
  registerAccountResetCallback: vi.fn(),
}));

const RECIPIENT_MIME = 'application/x-herold-recipient';

/** Minimal fake DataTransfer -- see file header for why a plain object works. */
function makeDataTransfer(preset?: Record<string, string>) {
  const store: Record<string, string> = { ...(preset ?? {}) };
  return {
    setData: (type: string, value: string) => {
      store[type] = value;
    },
    getData: (type: string) => store[type] ?? '',
    get types() {
      return Object.keys(store);
    },
    dropEffect: 'none',
    effectAllowed: 'uninitialized',
    _store: store,
  };
}

function renderField(opts: {
  field: RecipientFieldName;
  chips: Recipient[];
  onRecipientMove: (from: RecipientFieldName, idx: number, to: RecipientFieldName) => void;
}) {
  return render(RecipientField, {
    label: opts.field,
    field: opts.field,
    chips: opts.chips,
    onChipsChange: vi.fn(),
    onWarning: vi.fn(),
    onRecipientMove: opts.onRecipientMove,
  });
}

describe('RecipientField chip drag-start (re #272)', () => {
  it('encodes { field, index } as the recipient MIME payload', async () => {
    const { container } = renderField({
      field: 'to',
      chips: [{ email: 'alice@example.com' }, { email: 'bob@example.com' }],
      onRecipientMove: vi.fn(),
    });

    const chips = container.querySelectorAll('.chip');
    expect(chips).toHaveLength(2);
    const dataTransfer = makeDataTransfer();
    // Dragging the second chip (index 1). Dispatched manually (rather than
    // via fireEvent) so a `dataTransfer` property can be attached before
    // dispatch -- see the file header for why a plain Event works here.
    const evt = new Event('dragstart', { bubbles: true }) as Event & {
      dataTransfer?: unknown;
    };
    evt.dataTransfer = dataTransfer;
    chips[1]!.dispatchEvent(evt);

    expect(JSON.parse(dataTransfer.getData(RECIPIENT_MIME))).toEqual({
      field: 'to',
      index: 1,
    });
    expect(dataTransfer.effectAllowed).toBe('move');
  });
});

describe('RecipientField cross-field drop (re #272)', () => {
  it('calls onRecipientMove(fromField, index, thisField) for a drop from a different field', () => {
    const onRecipientMove = vi.fn();
    const { container } = renderField({
      field: 'cc',
      chips: [{ email: 'carol@example.com' }],
      onRecipientMove,
    });

    const row = container.querySelector('.recipient-field') as HTMLElement;
    const dataTransfer = makeDataTransfer({
      [RECIPIENT_MIME]: JSON.stringify({ field: 'to', index: 0 }),
    });
    const evt = new Event('drop', { bubbles: true, cancelable: true }) as Event & {
      dataTransfer?: unknown;
    };
    evt.dataTransfer = dataTransfer;
    row.dispatchEvent(evt);

    expect(onRecipientMove).toHaveBeenCalledWith('to', 0, 'cc');
  });

  it('does not call onRecipientMove when the drop lands back on its own field', () => {
    const onRecipientMove = vi.fn();
    const { container } = renderField({
      field: 'to',
      chips: [{ email: 'alice@example.com' }],
      onRecipientMove,
    });

    const row = container.querySelector('.recipient-field') as HTMLElement;
    const dataTransfer = makeDataTransfer({
      [RECIPIENT_MIME]: JSON.stringify({ field: 'to', index: 0 }),
    });
    const evt = new Event('drop', { bubbles: true, cancelable: true }) as Event & {
      dataTransfer?: unknown;
    };
    evt.dataTransfer = dataTransfer;
    row.dispatchEvent(evt);

    expect(onRecipientMove).not.toHaveBeenCalled();
  });
});

describe('RecipientField keyboard "Move to..." menu (re #272)', () => {
  it('offers the other two fields, excluding the chip\'s own field', async () => {
    const { container, getByRole } = renderField({
      field: 'to',
      chips: [{ name: 'Alice Smith', email: 'alice@example.com' }],
      onRecipientMove: vi.fn(),
    });

    const trigger = container.querySelector('.chip-move-trigger') as HTMLButtonElement;
    expect(trigger).not.toBeNull();
    await fireEvent.click(trigger);

    const menu = getByRole('menu');
    const items = menu.querySelectorAll('[role="menuitem"]');
    expect(items).toHaveLength(2);
    const labels = Array.from(items).map((el) => el.textContent?.trim());
    expect(labels.some((l) => l?.includes('Cc'))).toBe(true);
    expect(labels.some((l) => l?.includes('Bcc'))).toBe(true);
    expect(labels.some((l) => l?.includes('To'))).toBe(false);
  });

  it('calls onRecipientMove with this chip\'s field as source and the chosen field as destination', async () => {
    const onRecipientMove = vi.fn();
    const { container, getByRole } = renderField({
      field: 'to',
      chips: [{ email: 'alice@example.com' }],
      onRecipientMove,
    });

    const trigger = container.querySelector('.chip-move-trigger') as HTMLButtonElement;
    await fireEvent.click(trigger);
    const menu = getByRole('menu');
    const bccItem = Array.from(menu.querySelectorAll('[role="menuitem"]')).find((el) =>
      el.textContent?.includes('Bcc'),
    ) as HTMLButtonElement;
    expect(bccItem).toBeDefined();
    await fireEvent.click(bccItem);

    expect(onRecipientMove).toHaveBeenCalledWith('to', 0, 'bcc');
    // The menu closes after a selection.
    expect(container.querySelector('.chip-move-menu')).toBeNull();
  });

  it('closes the menu on Escape without calling onRecipientMove', async () => {
    const onRecipientMove = vi.fn();
    const { container } = renderField({
      field: 'to',
      chips: [{ email: 'alice@example.com' }],
      onRecipientMove,
    });

    const trigger = container.querySelector('.chip-move-trigger') as HTMLButtonElement;
    await fireEvent.click(trigger);
    expect(container.querySelector('.chip-move-menu')).not.toBeNull();

    const menu = container.querySelector('.chip-move-menu') as HTMLElement;
    await fireEvent.keyDown(menu, { key: 'Escape' });

    expect(container.querySelector('.chip-move-menu')).toBeNull();
    expect(onRecipientMove).not.toHaveBeenCalled();
  });
});
