/**
 * Lightbox load failure handling (issue #530): a load that never
 * completes must end in the error state rather than an endless spinner,
 * and the error state must offer a way to retry.
 */
import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import Lightbox from './Lightbox.svelte';

describe('Lightbox: stalled and failed loads end in a retryable error (issue #530)', () => {
  const items = [
    { url: 'blob:a.jpg', name: 'PC1.jpg', kind: 'image' as const, size: 2_000_000 },
    { url: 'blob:b.jpg', name: 'PC2.jpg', kind: 'image' as const },
  ];

  it('a load that never completes ends in the error state instead of an endless spinner', async () => {
    vi.useFakeTimers();
    try {
      render(Lightbox, { props: { items, index: 0, onClose: vi.fn() } });
      await screen.findByRole('dialog');
      expect(screen.getByRole('status')).toBeInTheDocument();
      // Budget for 2 MB: 30 s + 2 MB / 50 KB/s = 70 s. Still loading just before.
      await vi.advanceTimersByTimeAsync(60_000);
      expect(screen.getByRole('status')).toBeInTheDocument();
      expect(screen.queryByRole('alert')).toBeNull();
      await vi.advanceTimersByTimeAsync(11_000);
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.queryByRole('status')).toBeNull();
      expect(screen.queryByRole('img')).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it('an image that loads in time never reaches the error state', async () => {
    vi.useFakeTimers();
    try {
      render(Lightbox, { props: { items, index: 0, onClose: vi.fn() } });
      await screen.findByRole('dialog');
      await vi.advanceTimersByTimeAsync(10_000);
      await fireEvent.load(screen.getByRole('img'));
      await vi.advanceTimersByTimeAsync(600_000);
      expect(screen.queryByRole('alert')).toBeNull();
      expect(screen.getByRole('img')).not.toHaveClass('pending');
    } finally {
      vi.useRealTimers();
    }
  });

  it('the error state offers a Retry that reloads the image behind the spinner', async () => {
    render(Lightbox, { props: { items, index: 0, onClose: vi.fn() } });
    await screen.findByRole('dialog');
    await fireEvent.error(screen.getByRole('img'));
    expect(screen.getByRole('alert')).toBeInTheDocument();
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.getByRole('status')).toBeInTheDocument();
    const img = screen.getByRole('img');
    expect(img).toHaveAttribute('src', 'blob:a.jpg');
    expect(img).toHaveClass('pending');
    await fireEvent.load(img);
    expect(screen.queryByRole('status')).toBeNull();
    expect(screen.getByRole('img')).not.toHaveClass('pending');
  });

  it('a stalled load can be retried and times out again', async () => {
    vi.useFakeTimers();
    try {
      render(Lightbox, { props: { items, index: 0, onClose: vi.fn() } });
      await screen.findByRole('dialog');
      await vi.advanceTimersByTimeAsync(71_000);
      expect(screen.getByRole('alert')).toBeInTheDocument();
      await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
      expect(screen.getByRole('status')).toBeInTheDocument();
      await vi.advanceTimersByTimeAsync(71_000);
      expect(screen.getByRole('alert')).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });
});
