/**
 * Unit tests for the shared preview Lightbox's navigation (REQ-MAIL-23,
 * issue #518): ArrowLeft/ArrowRight and the on-screen prev/next controls
 * step through the `items` array passed in, a "{current} of {total}"
 * position indicator shows only when there is more than one item, and
 * the boundary behaviour stops at the ends (the control disables rather
 * than wrapping around).
 */
import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import Lightbox from './Lightbox.svelte';

describe('Lightbox: single item (chat / inline-image preview, REQ-CHAT-22) is unaffected', () => {
  it('renders no nav controls and no position indicator for a single-entry items array', async () => {
    const onClose = vi.fn();
    render(Lightbox, {
      props: { items: [{ url: 'blob:one.png' }], onClose },
    });
    const dialog = await screen.findByRole('dialog');
    expect(dialog.querySelector('.nav-btn')).toBeNull();
    expect(dialog.querySelector('.lightbox-position')).toBeNull();
    expect(screen.getByRole('img')).toHaveAttribute('src', 'blob:one.png');
  });

  it('Escape still closes a single-item lightbox', async () => {
    const onClose = vi.fn();
    render(Lightbox, { props: { items: [{ url: 'blob:one.png' }], onClose } });
    const dialog = await screen.findByRole('dialog');
    await fireEvent.keyDown(dialog, { key: 'Escape' });
    expect(onClose).toHaveBeenCalled();
  });
});

describe('Lightbox: multi-item navigation (REQ-MAIL-23, issue #518)', () => {
  const items = [
    { url: 'blob:a.jpg', name: 'PC1.jpg', kind: 'image' as const },
    { url: 'blob:b.jpg', name: 'PC2.jpg', kind: 'image' as const },
    { url: 'blob:c.jpg', name: 'PC3.jpg', kind: 'image' as const },
  ];

  it('shows the position indicator and the opened item at the given index', async () => {
    render(Lightbox, { props: { items, index: 1, onClose: vi.fn() } });
    await screen.findByRole('dialog');
    expect(screen.getByText('2 of 3')).toBeInTheDocument();
    expect(screen.getByText('PC2.jpg')).toBeInTheDocument();
    expect(screen.getByRole('img')).toHaveAttribute('src', 'blob:b.jpg');
  });

  it('ArrowRight steps to the next item', async () => {
    render(Lightbox, { props: { items, index: 0, onClose: vi.fn() } });
    const dialog = await screen.findByRole('dialog');
    expect(screen.getByText('PC1.jpg')).toBeInTheDocument();
    await fireEvent.keyDown(dialog, { key: 'ArrowRight' });
    expect(screen.getByText('PC2.jpg')).toBeInTheDocument();
    expect(screen.getByText('2 of 3')).toBeInTheDocument();
  });

  it('ArrowLeft steps to the previous item', async () => {
    render(Lightbox, { props: { items, index: 2, onClose: vi.fn() } });
    const dialog = await screen.findByRole('dialog');
    expect(screen.getByText('PC3.jpg')).toBeInTheDocument();
    await fireEvent.keyDown(dialog, { key: 'ArrowLeft' });
    expect(screen.getByText('PC2.jpg')).toBeInTheDocument();
  });

  it('clicking the next control steps forward', async () => {
    render(Lightbox, { props: { items, index: 0, onClose: vi.fn() } });
    await screen.findByRole('dialog');
    const nextBtn = screen.getByRole('button', { name: 'Next attachment' });
    await fireEvent.click(nextBtn);
    expect(screen.getByText('PC2.jpg')).toBeInTheDocument();
  });

  it('clicking the previous control steps backward', async () => {
    render(Lightbox, { props: { items, index: 2, onClose: vi.fn() } });
    await screen.findByRole('dialog');
    const prevBtn = screen.getByRole('button', { name: 'Previous attachment' });
    await fireEvent.click(prevBtn);
    expect(screen.getByText('PC2.jpg')).toBeInTheDocument();
  });

  it('stops at the end: the next control disables at the last item and ArrowRight is a no-op', async () => {
    render(Lightbox, { props: { items, index: 2, onClose: vi.fn() } });
    const dialog = await screen.findByRole('dialog');
    const nextBtn = screen.getByRole('button', { name: 'Next attachment' });
    expect(nextBtn).toBeDisabled();
    await fireEvent.keyDown(dialog, { key: 'ArrowRight' });
    expect(screen.getByText('PC3.jpg')).toBeInTheDocument();
    expect(screen.getByText('3 of 3')).toBeInTheDocument();
  });

  it('stops at the start: the previous control disables at the first item and ArrowLeft is a no-op', async () => {
    render(Lightbox, { props: { items, index: 0, onClose: vi.fn() } });
    const dialog = await screen.findByRole('dialog');
    const prevBtn = screen.getByRole('button', { name: 'Previous attachment' });
    expect(prevBtn).toBeDisabled();
    await fireEvent.keyDown(dialog, { key: 'ArrowLeft' });
    expect(screen.getByText('PC1.jpg')).toBeInTheDocument();
    expect(screen.getByText('1 of 3')).toBeInTheDocument();
  });

  it('Escape still closes a multi-item lightbox', async () => {
    const onClose = vi.fn();
    render(Lightbox, { props: { items, index: 1, onClose } });
    const dialog = await screen.findByRole('dialog');
    await fireEvent.keyDown(dialog, { key: 'Escape' });
    expect(onClose).toHaveBeenCalled();
  });
});

describe('Lightbox: loading feedback and prefetch (issue #518, field report)', () => {
  const items = [
    { url: 'blob:a.jpg', name: 'PC1.jpg', kind: 'image' as const },
    { url: 'blob:b.jpg', name: 'PC2.jpg', kind: 'image' as const },
    { url: 'blob:c.jpg', name: 'PC3.jpg', kind: 'image' as const },
    { url: 'blob:d.jpg', name: 'PC4.jpg', kind: 'image' as const },
  ];

  it('shows a spinner until the image loads, then hides it', async () => {
    render(Lightbox, { props: { items, index: 0, onClose: vi.fn() } });
    await screen.findByRole('dialog');
    expect(screen.getByRole('status')).toBeInTheDocument();
    expect(screen.getByRole('img')).toHaveClass('pending');
    await fireEvent.load(screen.getByRole('img'));
    expect(screen.queryByRole('status')).toBeNull();
    expect(screen.getByRole('img')).not.toHaveClass('pending');
  });

  it('stepping replaces the stale image with a spinner and updates the indicator at once', async () => {
    render(Lightbox, { props: { items, index: 0, onClose: vi.fn() } });
    const dialog = await screen.findByRole('dialog');
    await fireEvent.load(screen.getByRole('img'));
    expect(screen.queryByRole('status')).toBeNull();

    await fireEvent.keyDown(dialog, { key: 'ArrowRight' });
    expect(screen.getByText('2 of 4')).toBeInTheDocument();
    expect(screen.getByRole('status')).toBeInTheDocument();
    const img = screen.getByRole('img');
    expect(img).toHaveAttribute('src', 'blob:b.jpg');
    expect(img).toHaveClass('pending');

    await fireEvent.load(img);
    expect(screen.queryByRole('status')).toBeNull();
    expect(screen.getByRole('img')).not.toHaveClass('pending');
  });

  it('shows an error placeholder when the image fails to load', async () => {
    render(Lightbox, { props: { items, index: 0, onClose: vi.fn() } });
    await screen.findByRole('dialog');
    await fireEvent.error(screen.getByRole('img'));
    expect(screen.getByRole('alert')).toBeInTheDocument();
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('prefetches the adjacent images and shows a loaded neighbour without a spinner', async () => {
    const created: { src: string; onload: (() => void) | null }[] = [];
    const RealImage = globalThis.Image;
    class FakeImage {
      src = '';
      onload: (() => void) | null = null;
      onerror: (() => void) | null = null;
      constructor() {
        created.push(this);
      }
    }
    // @ts-expect-error test double
    globalThis.Image = FakeImage;
    try {
      render(Lightbox, { props: { items, index: 1, onClose: vi.fn() } });
      const dialog = await screen.findByRole('dialog');
      await Promise.resolve();
      expect(created.map((c) => c.src)).toEqual(
        expect.arrayContaining(['blob:c.jpg', 'blob:a.jpg', 'blob:d.jpg']),
      );
      created.find((c) => c.src === 'blob:c.jpg')!.onload!();
      await fireEvent.keyDown(dialog, { key: 'ArrowRight' });
      expect(screen.queryByRole('status')).toBeNull();
      expect(screen.getByRole('img')).not.toHaveClass('pending');
    } finally {
      globalThis.Image = RealImage;
    }
  });

  it('Escape still closes while an image is loading', async () => {
    const onClose = vi.fn();
    render(Lightbox, { props: { items, index: 0, onClose } });
    const dialog = await screen.findByRole('dialog');
    await fireEvent.keyDown(dialog, { key: 'Escape' });
    expect(onClose).toHaveBeenCalled();
  });
});
