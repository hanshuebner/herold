/**
 * Tests for RawSourceModal (REQ-MAIL-142):
 *  - Closed by default; renders nothing when `open=false`.
 *  - Open → fetches the sourceUrl with credentials=include.
 *  - Successful fetch renders the body inside a <pre data-testid="raw-source">.
 *  - HTTP error renders an inline error and disables Copy and Download.
 *  - Network error renders the message and disables Copy and Download.
 *  - Copy button calls navigator.clipboard.writeText with the loaded text
 *    and flips its label to the "Copied" string for ~1500 ms.
 *  - Download button creates a Blob (type message/rfc822), triggers a
 *    temporary anchor click, and does NOT use the clipboard (re #45).
 *  - Escape inside the dialog calls onClose.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import RawSourceModal from './RawSourceModal.svelte';
import type { Mock } from 'vitest';

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));

describe('RawSourceModal', () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  let writeTextMock: ReturnType<typeof vi.fn>;
  let createObjectURLMock: Mock;
  let revokeObjectURLMock: Mock;

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

    // Stub URL.createObjectURL / revokeObjectURL (not available in happy-dom).
    createObjectURLMock = vi.fn().mockReturnValue('blob:mock-url');
    revokeObjectURLMock = vi.fn();
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      writable: true,
      value: createObjectURLMock,
    });
    Object.defineProperty(URL, 'revokeObjectURL', {
      configurable: true,
      writable: true,
      value: revokeObjectURLMock,
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

  // ── Download button (re #45) ─────────────────────────────────────────────
  //
  // The Download button saves the already-loaded raw source as a .eml file
  // using a Blob + temporary <a download> click. It does NOT use the clipboard
  // so large messages that exceed the Clipboard API limit still work.

  it('Download button is disabled before the source has loaded', () => {
    fetchMock.mockReturnValue(new Promise(() => {})); // never resolves

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/x', filename: 'test.eml', onClose: vi.fn() },
    });

    expect(screen.getByRole('button', { name: 'msg.rawSource.download' })).toBeDisabled();
  });

  it('Download button is disabled when the fetch fails', async () => {
    fetchMock.mockResolvedValue(new Response('err', { status: 404 }));

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/x', filename: 'test.eml', onClose: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: 'msg.rawSource.download' })).toBeDisabled();
  });

  it('Download button creates a Blob with type message/rfc822 and triggers anchor click', async () => {
    const rawSource = 'From: alice@example.test\r\nSubject: Test\r\n\r\nHello';
    fetchMock.mockResolvedValue(new Response(rawSource, { status: 200 }));

    // Intercept the temporary <a> element to inspect its attributes and
    // capture the click without triggering a real navigation.
    let capturedAnchor: HTMLAnchorElement | null = null;
    const origAppendChild = document.body.appendChild.bind(document.body);
    const appendChildSpy = vi.spyOn(document.body, 'appendChild').mockImplementation((node) => {
      if (node instanceof HTMLAnchorElement) {
        capturedAnchor = node;
        vi.spyOn(node, 'click').mockImplementation(() => {});
      }
      return origAppendChild(node);
    });

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/x', filename: 'My message.eml', onClose: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByTestId('raw-source')).toBeInTheDocument();
    });

    const downloadBtn = screen.getByRole('button', { name: 'msg.rawSource.download' });
    expect(downloadBtn).not.toBeDisabled();
    await fireEvent.click(downloadBtn);

    // A Blob object URL was created.
    expect(createObjectURLMock).toHaveBeenCalledOnce();
    const blobArg = createObjectURLMock.mock.calls[0]![0] as Blob;
    expect(blobArg).toBeInstanceOf(Blob);
    expect(blobArg.type).toBe('message/rfc822');
    const blobText = await blobArg.text();
    expect(blobText).toBe(rawSource);

    // The temporary anchor carried the correct href and download filename.
    expect(capturedAnchor).not.toBeNull();
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    expect(capturedAnchor!.href).toContain('blob:');
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    expect(capturedAnchor!.download).toBe('My message.eml');

    // The object URL was revoked after use.
    expect(revokeObjectURLMock).toHaveBeenCalledWith('blob:mock-url');

    // The clipboard was NOT used.
    expect(writeTextMock).not.toHaveBeenCalled();

    appendChildSpy.mockRestore();
  });

  it('Download uses the fallback filename when filename prop is null', async () => {
    fetchMock.mockResolvedValue(new Response('DATA', { status: 200 }));

    let capturedAnchor: HTMLAnchorElement | null = null;
    const origAppendChild = document.body.appendChild.bind(document.body);
    const appendChildSpy = vi.spyOn(document.body, 'appendChild').mockImplementation((node) => {
      if (node instanceof HTMLAnchorElement) {
        capturedAnchor = node;
        vi.spyOn(node, 'click').mockImplementation(() => {});
      }
      return origAppendChild(node);
    });

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/x', filename: null, onClose: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByTestId('raw-source')).toBeInTheDocument();
    });

    await fireEvent.click(screen.getByRole('button', { name: 'msg.rawSource.download' }));
    // capturedAnchor is assigned inside the mock callback; cast it back to its
    // declared type so TypeScript's control-flow narrowing does not see null.
    expect((capturedAnchor as HTMLAnchorElement | null)?.download).toBe('message.eml');

    appendChildSpy.mockRestore();
  });
});
