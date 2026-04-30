/**
 * Component tests for AdvancedSearchPanel.
 *
 * Asserts:
 *  - The panel renders its fields.
 *  - Clicking "in:sent" sets the mailbox dropdown to the Sent mailbox.
 *  - Submitting the form calls onSearch with the assembled query string.
 *  - Clearing the form resets all fields.
 *  - Pre-population from a currentQuery prop round-trips correctly.
 */

import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import AdvancedSearchPanel from './AdvancedSearchPanel.svelte';
import type { Mailbox } from './types';

// ── Store mock ────────────────────────────────────────────────────────────────

const sentMailbox: Mailbox = {
  id: 'mb-sent',
  name: 'Sent',
  role: 'sent',
  parentId: null,
  sortOrder: 2,
  totalEmails: 0,
  unreadEmails: 0,
  totalThreads: 0,
  unreadThreads: 0,
};
const workMailbox: Mailbox = {
  id: 'mb-work',
  name: 'Work',
  role: null,
  parentId: null,
  sortOrder: 5,
  totalEmails: 0,
  unreadEmails: 0,
  totalThreads: 0,
  unreadThreads: 0,
};

const mockMailboxes = new Map<string, Mailbox>([
  ['mb-sent', sentMailbox],
  ['mb-work', workMailbox],
]);

vi.mock('./store.svelte', () => ({
  mail: {
    get mailboxes() {
      return mockMailboxes;
    },
    get sent() {
      return sentMailbox;
    },
  },
}));

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('AdvancedSearchPanel', () => {
  it('renders the From, To, Subject, Body, After, Before fields', () => {
    render(AdvancedSearchPanel, {
      props: { onSearch: vi.fn(), onClose: vi.fn() },
    });
    expect(screen.getByPlaceholderText('Sender name or address')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Recipient name or address')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Subject contains')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('Body contains')).toBeInTheDocument();
    expect(screen.getByLabelText('After')).toBeInTheDocument();
    expect(screen.getByLabelText('Before')).toBeInTheDocument();
  });

  it('renders the mailbox dropdown with an "Any mailbox" default option', () => {
    render(AdvancedSearchPanel, {
      props: { onSearch: vi.fn(), onClose: vi.fn() },
    });
    const select = screen.getByLabelText('Mailbox') as HTMLSelectElement;
    expect(select).toBeInTheDocument();
    expect(select.value).toBe('');
  });

  it('renders the has:attachment checkbox', () => {
    render(AdvancedSearchPanel, {
      props: { onSearch: vi.fn(), onClose: vi.fn() },
    });
    expect(screen.getByLabelText(/Has attachment/i)).toBeInTheDocument();
  });

  it('clicking "in:sent" sets the mailbox dropdown to the Sent mailbox', async () => {
    render(AdvancedSearchPanel, {
      props: { onSearch: vi.fn(), onClose: vi.fn() },
    });
    const btn = screen.getByRole('button', { name: /in:sent/i });
    await fireEvent.click(btn);
    const select = screen.getByLabelText('Mailbox') as HTMLSelectElement;
    expect(select.value).toBe('mb-sent');
  });

  it('submitting with a from: value calls onSearch with from: token', async () => {
    const onSearch = vi.fn();
    render(AdvancedSearchPanel, {
      props: { onSearch, onClose: vi.fn() },
    });
    await fireEvent.input(
      screen.getByPlaceholderText('Sender name or address'),
      { target: { value: 'alice@x.test' } },
    );
    await fireEvent.submit(screen.getByRole('region', { name: 'Advanced search' }).querySelector('form')!);
    expect(onSearch).toHaveBeenCalledWith(expect.stringContaining('from:alice@x.test'));
  });

  it('submitting with has:attachment checked includes has:attachment token', async () => {
    const onSearch = vi.fn();
    render(AdvancedSearchPanel, {
      props: { onSearch, onClose: vi.fn() },
    });
    await fireEvent.click(screen.getByLabelText(/Has attachment/i));
    await fireEvent.submit(screen.getByRole('region', { name: 'Advanced search' }).querySelector('form')!);
    expect(onSearch).toHaveBeenCalledWith(expect.stringContaining('has:attachment'));
  });

  it('Clear button resets all fields', async () => {
    const onSearch = vi.fn();
    render(AdvancedSearchPanel, {
      props: { onSearch, onClose: vi.fn() },
    });
    // Fill in a field.
    await fireEvent.input(
      screen.getByPlaceholderText('Sender name or address'),
      { target: { value: 'alice@x.test' } },
    );
    await fireEvent.click(screen.getByRole('button', { name: 'Clear' }));
    const fromInput = screen.getByPlaceholderText('Sender name or address') as HTMLInputElement;
    expect(fromInput.value).toBe('');
  });

  it('pre-populates fields from the currentQuery prop', () => {
    render(AdvancedSearchPanel, {
      props: {
        currentQuery: 'from:bob@x.test has:attachment',
        onSearch: vi.fn(),
        onClose: vi.fn(),
      },
    });
    const fromInput = screen.getByPlaceholderText('Sender name or address') as HTMLInputElement;
    expect(fromInput.value).toBe('bob@x.test');
    const checkbox = screen.getByLabelText(/Has attachment/i) as HTMLInputElement;
    expect(checkbox.checked).toBe(true);
  });

  it('submitting an empty form calls onSearch with an empty string', async () => {
    const onSearch = vi.fn();
    render(AdvancedSearchPanel, {
      props: { onSearch, onClose: vi.fn() },
    });
    await fireEvent.submit(screen.getByRole('region', { name: 'Advanced search' }).querySelector('form')!);
    expect(onSearch).toHaveBeenCalledWith('');
  });
});
