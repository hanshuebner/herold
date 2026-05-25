/**
 * Tests for RawSourceModal (REQ-MAIL-142):
 *  - Closed by default; renders nothing when `open=false`.
 *  - Open → fetches the sourceUrl with credentials=include.
 *  - Successful fetch renders the body inside a <pre data-testid="raw-source">.
 *  - HTTP error renders an inline error and disables Copy.
 *  - Network error renders the message and disables Copy.
 *  - Copy button calls navigator.clipboard.writeText with the loaded text
 *    and flips its label to the "Copied" string for ~1500 ms.
 *  - Escape inside the dialog calls onClose.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import RawSourceModal from './RawSourceModal.svelte';

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));

describe('RawSourceModal', () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  let writeTextMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn();
    // happy-dom does not provide fetch; install our mock on globalThis.
    Object.defineProperty(globalThis, 'fetch', {
      configurable: true,
      writable: true,
      value: fetchMock,
    });

    writeTextMock = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: writeTextMock },
    });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders nothing when open is false', () => {
    render(RawSourceModal, {
      props: { open: false, sourceUrl: '/x', onClose: vi.fn() },
    });
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('fetches the source URL with credentials and renders the body in a pre block', async () => {
    fetchMock.mockResolvedValue(new Response('Received: from foo\r\nSubject: hi\r\n\r\nbody', { status: 200 }));

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/jmap/download/acct/blob/x.eml', onClose: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByTestId('raw-source')).toBeInTheDocument();
    });
    expect(fetchMock).toHaveBeenCalledWith('/jmap/download/acct/blob/x.eml', {
      credentials: 'include',
    });
    expect(screen.getByTestId('raw-source').textContent).toContain('Subject: hi');
  });

  it('renders an error and disables Copy when the response is non-OK', async () => {
    fetchMock.mockResolvedValue(new Response('nope', { status: 500 }));

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/jmap/download/acct/blob/x.eml', onClose: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('msg.rawSource.error: HTTP 500');
    });
    expect(screen.queryByTestId('raw-source')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'msg.rawSource.copy' })).toBeDisabled();
  });

  it('renders an error when the fetch itself throws', async () => {
    fetchMock.mockRejectedValue(new Error('network down'));

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/jmap/download/acct/blob/x.eml', onClose: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('msg.rawSource.error: network down');
    });
  });

  it('Copy button writes the loaded text to the clipboard and flips its label', async () => {
    fetchMock.mockResolvedValue(new Response('SOURCE TEXT', { status: 200 }));

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/jmap/download/acct/blob/x.eml', onClose: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByTestId('raw-source')).toBeInTheDocument();
    });

    const copyBtn = screen.getByRole('button', { name: 'msg.rawSource.copy' });
    expect(copyBtn).not.toBeDisabled();
    await fireEvent.click(copyBtn);

    expect(writeTextMock).toHaveBeenCalledWith('SOURCE TEXT');
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'msg.rawSource.copied' })).toBeInTheDocument();
    });
  });

  it('Escape inside the dialog calls onClose', async () => {
    fetchMock.mockResolvedValue(new Response('x', { status: 200 }));
    const onClose = vi.fn();

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/x', onClose },
    });

    const dialog = await screen.findByRole('dialog');
    await fireEvent.keyDown(dialog, { key: 'Escape' });
    expect(onClose).toHaveBeenCalled();
  });
});
