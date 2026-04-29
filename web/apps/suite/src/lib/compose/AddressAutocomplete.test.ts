/**
 * Tests for AddressAutocomplete.svelte.
 *
 * Verifies:
 *   - With capability on and directory results: dropdown shows merged results.
 *   - With capability off: dropdown shows contacts + seen only (no Directory/search call).
 *   - Debounce: typing several characters fires only one Directory/search call.
 *   - Empty-state hint renders with the appropriate text per capability state.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import AddressAutocomplete from './AddressAutocomplete.svelte';

// ── Mocks ────────────────────────────────────────────────────────────────────

vi.mock('../auth/capabilities', () => ({
  hasDirectoryAutocomplete: vi.fn(() => false),
  directoryAutocompleteMode: vi.fn(() => null),
  hasExternalSubmission: vi.fn(() => false),
}));

vi.mock('../contacts/store.svelte', () => ({
  contacts: {
    status: 'idle',
    suggestions: [],
    filter: vi.fn(() => []),
    filterAsync: vi.fn(async () => []),
    load: vi.fn(),
  },
}));

vi.mock('../contacts/seen-addresses.svelte', () => ({
  seenAddresses: {
    status: 'idle',
    entries: [],
  },
}));

vi.mock('../jmap/client', () => ({
  jmap: {
    session: null,
    hasCapability: vi.fn(() => false),
    batch: vi.fn(),
  },
  strict: vi.fn((r: unknown) => r),
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: { primaryAccounts: {} },
  },
}));

import * as capsMod from '../auth/capabilities';
import * as storeMod from '../contacts/store.svelte';
import type { ContactSuggestion } from '../contacts/store.svelte';

const mockHasDirAC = vi.mocked(capsMod.hasDirectoryAutocomplete);
const mockFilterAsync = vi.mocked(storeMod.contacts.filterAsync);
const mockLoad = vi.mocked(storeMod.contacts.load);

// Use fake timers globally for debounce control.
beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  mockHasDirAC.mockReturnValue(false);
  mockFilterAsync.mockResolvedValue([]);
});

afterEach(() => {
  vi.useRealTimers();
});

function makeOnChange() {
  return vi.fn();
}

const ALICE: ContactSuggestion = { id: 'c1', name: 'Alice Liddell', email: 'alice@x.test' };
const CAROL: ContactSuggestion = { id: 'sa:sa1', name: 'Carol', email: 'carol@z.test' };
const EVE_DIR: ContactSuggestion = { id: 'dir:d1', name: 'Eve Adams', email: 'eve@corp.test' };

// ── Tests ────────────────────────────────────────────────────────────────────

describe('AddressAutocomplete: suggestions with capability on', () => {
  it('shows merged results when directory capability is on', async () => {
    mockHasDirAC.mockReturnValue(true);
    mockFilterAsync.mockResolvedValue([ALICE, CAROL, EVE_DIR]);

    const onChange = makeOnChange();
    const { getByRole, findByRole } = render(AddressAutocomplete, {
      props: { value: '', onChange },
    });

    const input = getByRole('textbox');
    await fireEvent.focus(input);
    await fireEvent.input(input, { target: { value: 'ali' } });

    // Advance debounce timer.
    vi.advanceTimersByTime(150);
    await vi.runAllTimersAsync();

    const listbox = await findByRole('listbox');
    expect(listbox).toBeTruthy();
    expect(listbox.querySelectorAll('li').length).toBe(3);
  });
});

describe('AddressAutocomplete: capability off', () => {
  it('shows contacts + seen without directory call', async () => {
    mockHasDirAC.mockReturnValue(false);
    mockFilterAsync.mockResolvedValue([ALICE, CAROL]);

    const onChange = makeOnChange();
    const { getByRole, findByRole } = render(AddressAutocomplete, {
      props: { value: '', onChange },
    });

    const input = getByRole('textbox');
    await fireEvent.focus(input);
    await fireEvent.input(input, { target: { value: 'ali' } });
    vi.advanceTimersByTime(150);
    await vi.runAllTimersAsync();

    const listbox = await findByRole('listbox');
    expect(listbox.querySelectorAll('li').length).toBe(2);
  });
});

describe('AddressAutocomplete: debounce', () => {
  it('fires filterAsync only once after multiple rapid keystrokes', async () => {
    mockHasDirAC.mockReturnValue(true);
    mockFilterAsync.mockResolvedValue([ALICE]);

    const onChange = makeOnChange();
    const { getByRole } = render(AddressAutocomplete, {
      props: { value: '', onChange },
    });

    const input = getByRole('textbox');
    await fireEvent.focus(input);

    // Rapid typing — each fires oninput but the debounce should coalesce.
    await fireEvent.input(input, { target: { value: 'a' } });
    vi.advanceTimersByTime(50);
    await fireEvent.input(input, { target: { value: 'al' } });
    vi.advanceTimersByTime(50);
    await fireEvent.input(input, { target: { value: 'ali' } });
    vi.advanceTimersByTime(50);

    // Before debounce fires, no call yet (token < 2 at start but now 'ali').
    // Advance the full debounce.
    vi.advanceTimersByTime(150);
    await vi.runAllTimersAsync();

    // filterAsync should have been called exactly once (the final debounce tick).
    expect(mockFilterAsync).toHaveBeenCalledTimes(1);
    expect(mockFilterAsync).toHaveBeenCalledWith('ali', 8);
  });
});

describe('AddressAutocomplete: empty-state hint', () => {
  it('shows directory-aware hint when capability is on and no results', async () => {
    mockHasDirAC.mockReturnValue(true);
    mockFilterAsync.mockResolvedValue([]);

    const onChange = makeOnChange();
    const { getByRole, findByRole } = render(AddressAutocomplete, {
      props: { value: '', onChange },
    });

    const input = getByRole('textbox');
    await fireEvent.focus(input);
    await fireEvent.input(input, { target: { value: 'xy' } });
    vi.advanceTimersByTime(150);
    await vi.runAllTimersAsync();

    const hint = await findByRole('status');
    expect(hint.textContent).toContain('contacts, recent recipients, or directory');
  });

  it('shows contacts-only hint when capability is off and no results', async () => {
    mockHasDirAC.mockReturnValue(false);
    mockFilterAsync.mockResolvedValue([]);

    const onChange = makeOnChange();
    const { getByRole, findByRole } = render(AddressAutocomplete, {
      props: { value: '', onChange },
    });

    const input = getByRole('textbox');
    await fireEvent.focus(input);
    await fireEvent.input(input, { target: { value: 'xy' } });
    vi.advanceTimersByTime(150);
    await vi.runAllTimersAsync();

    const hint = await findByRole('status');
    expect(hint.textContent).toContain('contacts or recent recipients');
    expect(hint.textContent).not.toContain('directory');
  });

  it('does not show empty-state hint for tokens shorter than 2 chars', async () => {
    mockHasDirAC.mockReturnValue(false);
    mockFilterAsync.mockResolvedValue([]);

    const onChange = makeOnChange();
    const { getByRole, queryByRole } = render(AddressAutocomplete, {
      props: { value: '', onChange },
    });

    const input = getByRole('textbox');
    await fireEvent.focus(input);
    await fireEvent.input(input, { target: { value: 'a' } });
    vi.advanceTimersByTime(200);
    await vi.runAllTimersAsync();

    expect(queryByRole('status')).toBeNull();
  });
});

describe('AddressAutocomplete: keyboard navigation', () => {
  it('ArrowDown/Up navigates the focused item', async () => {
    mockFilterAsync.mockResolvedValue([ALICE, CAROL]);

    const onChange = makeOnChange();
    const { getByRole } = render(AddressAutocomplete, {
      props: { value: '', onChange },
    });

    const input = getByRole('textbox');
    await fireEvent.focus(input);
    await fireEvent.input(input, { target: { value: 'al' } });
    vi.advanceTimersByTime(150);
    await vi.runAllTimersAsync();

    await waitFor(() => {
      expect(getByRole('listbox')).toBeTruthy();
    });

    const listbox = getByRole('listbox');
    const items = listbox.querySelectorAll('li');
    // First item focused by default.
    expect(items[0]!.classList.contains('focused')).toBe(true);

    await fireEvent.keyDown(input, { key: 'ArrowDown' });
    expect(items[1]!.classList.contains('focused')).toBe(true);

    await fireEvent.keyDown(input, { key: 'ArrowUp' });
    expect(items[0]!.classList.contains('focused')).toBe(true);
  });

  it('Escape closes the dropdown', async () => {
    mockFilterAsync.mockResolvedValue([ALICE]);

    const onChange = makeOnChange();
    const { getByRole, queryByRole } = render(AddressAutocomplete, {
      props: { value: 'ali', onChange },
    });

    const input = getByRole('textbox');
    await fireEvent.focus(input);
    vi.advanceTimersByTime(150);
    await vi.runAllTimersAsync();

    await waitFor(() => {
      expect(getByRole('listbox')).toBeTruthy();
    });

    await fireEvent.keyDown(input, { key: 'Escape' });
    expect(queryByRole('listbox')).toBeNull();
  });

  it('loads contacts on first focus', async () => {
    mockFilterAsync.mockResolvedValue([]);

    const onChange = makeOnChange();
    const { getByRole } = render(AddressAutocomplete, {
      props: { value: '', onChange },
    });

    const input = getByRole('textbox');
    await fireEvent.focus(input);
    expect(mockLoad).toHaveBeenCalledOnce();
  });
});
