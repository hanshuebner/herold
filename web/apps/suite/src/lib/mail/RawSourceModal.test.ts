/**
 * Tests for RawSourceModal (REQ-MAIL-142):
 *  - Closed by default; renders nothing when `open=false`.
 *  - Open → fetches the sourceUrl with credentials=include.
 *  - Successful fetch renders the body inside a <pre data-testid="raw-source">.
 *  - HTTP error renders an inline error and disables Copy and Download.
 *  - Network error renders the message and disables Copy and Download.
 *  - Copy button calls navigator.clipboard.writeText with the loaded text
 *    and flips its label to the "Copied" string for ~1500 ms.
 *  - Download button fetches the complete blob independently of the
 *    (possibly truncated) preview fetch, creates a Blob (type
 *    message/rfc822), and triggers a temporary anchor click; it does NOT
 *    use the clipboard (re #45).
 *  - REQ-MAIL-142b: the preview fetch never reads more than the bounded
 *    prefix; when the source exceeds it, a truncation notice is shown and
 *    the underlying stream is cancelled rather than drained to completion.
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

const toastShowMock = vi.fn();
vi.mock('../toast/toast.svelte', () => ({
  toast: { show: (...args: unknown[]) => toastShowMock(...args) },
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

    toastShowMock.mockClear();
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
    // Below the 64 KiB preview limit: no truncation notice.
    expect(screen.queryByTestId('raw-source-truncated')).not.toBeInTheDocument();
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

  it('Copy button writes the loaded preview text to the clipboard and flips its label', async () => {
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

  // ── REQ-MAIL-142b: bounded preview fetch ────────────────────────────────
  //
  // The preview pane never renders more than PREVIEW_BYTE_LIMIT (64 KiB) of
  // the raw source, regardless of the actual message size, and the fetch
  // stream is cancelled once the limit is reached so the rest of a large
  // blob is never downloaded for the preview.

  it('truncates the preview to the bounded prefix and shows a truncation notice for a large source', async () => {
    const big = 'A'.repeat(100 * 1024); // 100 KiB, well above the 64 KiB limit
    fetchMock.mockResolvedValue(new Response(big, { status: 200 }));

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/jmap/download/acct/blob/big.eml', onClose: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByTestId('raw-source-truncated')).toBeInTheDocument();
    });
    const shown = screen.getByTestId('raw-source').textContent ?? '';
    expect(shown.length).toBeLessThanOrEqual(64 * 1024);
    expect(shown.length).toBeGreaterThan(0);
  });

  it('does not show a truncation notice when the source is within the bounded prefix', async () => {
    fetchMock.mockResolvedValue(new Response('short message', { status: 200 }));

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/x', onClose: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByTestId('raw-source')).toBeInTheDocument();
    });
    expect(screen.queryByTestId('raw-source-truncated')).not.toBeInTheDocument();
  });

  // ── Download button (re #45, re #245) ───────────────────────────────────
  //
  // The Download button saves the complete original message as a .eml file
  // via its own fetch of the full blob -- independent of the (possibly
  // truncated) preview fetch above -- using a Blob + temporary <a download>
  // click. It does NOT use the clipboard so large messages that exceed the
  // Clipboard API limit still work.

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

  it('Download button fetches the complete blob independently and creates a Blob with type message/rfc822', async () => {
    const rawSource = 'From: alice@example.test\r\nSubject: Test\r\n\r\nHello';
    // The preview load and the Download click each perform their own
    // fetch() call; each Response body can only be read once, so the mock
    // must hand back a fresh Response per call.
    fetchMock.mockImplementation(() => Promise.resolve(new Response(rawSource, { status: 200 })));

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
    expect(fetchMock).toHaveBeenCalledTimes(1);

    const downloadBtn = screen.getByRole('button', { name: 'msg.rawSource.download' });
    expect(downloadBtn).not.toBeDisabled();
    await fireEvent.click(downloadBtn);

    await waitFor(() => {
      expect(createObjectURLMock).toHaveBeenCalledOnce();
    });
    // Download performed its own, second fetch of the full blob.
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1]).toEqual(['/x', { credentials: 'include' }]);

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

  it('Download button yields the complete message even when the preview was truncated', async () => {
    const big = 'B'.repeat(100 * 1024); // 100 KiB, above the 64 KiB preview limit
    fetchMock.mockImplementation(() => Promise.resolve(new Response(big, { status: 200 })));

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/big', filename: 'big.eml', onClose: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByTestId('raw-source-truncated')).toBeInTheDocument();
    });

    await fireEvent.click(screen.getByRole('button', { name: 'msg.rawSource.download' }));

    await waitFor(() => {
      expect(createObjectURLMock).toHaveBeenCalledOnce();
    });
    const blobArg = createObjectURLMock.mock.calls[0]![0] as Blob;
    const blobText = await blobArg.text();
    expect(blobText.length).toBe(big.length);
    expect(blobText).toBe(big);
  });

  it('Download uses the fallback filename when filename prop is null', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(new Response('DATA', { status: 200 })));

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
    await waitFor(() => {
      expect(createObjectURLMock).toHaveBeenCalledOnce();
    });
    // capturedAnchor is assigned inside the mock callback; cast it back to its
    // declared type so TypeScript's control-flow narrowing does not see null.
    expect((capturedAnchor as HTMLAnchorElement | null)?.download).toBe('message.eml');

    appendChildSpy.mockRestore();
  });

  it('Download shows an error toast when its own fetch fails, without disturbing the loaded preview', async () => {
    let call = 0;
    fetchMock.mockImplementation(() => {
      call += 1;
      if (call === 1) {
        return Promise.resolve(new Response('preview text', { status: 200 }));
      }
      return Promise.resolve(new Response('nope', { status: 503 }));
    });

    render(RawSourceModal, {
      props: { open: true, sourceUrl: '/x', filename: 'test.eml', onClose: vi.fn() },
    });

    await waitFor(() => {
      expect(screen.getByTestId('raw-source')).toBeInTheDocument();
    });

    await fireEvent.click(screen.getByRole('button', { name: 'msg.rawSource.download' }));

    await waitFor(() => {
      expect(toastShowMock).toHaveBeenCalledWith(
        expect.objectContaining({ message: expect.stringContaining('HTTP 503'), kind: 'error' }),
      );
    });
    // The preview pane is unaffected by the failed download fetch.
    expect(screen.getByTestId('raw-source')).toHaveTextContent('preview text');
    expect(createObjectURLMock).not.toHaveBeenCalled();
  });
});
