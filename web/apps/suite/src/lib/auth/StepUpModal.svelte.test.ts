/**
 * StepUpModal.svelte — auto-submit, button-removal, and focus tests (re #79, re #120).
 *
 * Verifies that:
 *   1. Entering all six digits triggers submitCode() automatically.
 *   2. The code boxes are cleared after submitCode() resolves.
 *   3. No Confirm/Submit button appears in the modal.
 *   4. Auto-submit is suppressed when the lockout countdown is active.
 *   5. After a rejected code, focus returns to the first digit box (re #120).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import StepUpModal from './StepUpModal.svelte';

// ── Store mock ─────────────────────────────────────────────────────────────

const { mockSubmitCode, mockCancel, mockStepUp } = vi.hoisted(() => {
  const mockSubmitCode = vi.fn();
  const mockCancel = vi.fn();
  const mockStepUp = {
    visible: false,
    enrollRequired: false,
    error: null as string | null,
    submitting: false,
    lockoutUntil: null as Date | null,
    submitCode: mockSubmitCode,
    cancel: mockCancel,
    clearLockout: vi.fn(),
  };
  return { mockSubmitCode, mockCancel, mockStepUp };
});

vi.mock('./step-up.svelte', () => ({
  stepUp: mockStepUp,
}));

// ── i18n mock — return key so assertions are stable ────────────────────────

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string): string => key,
}));

// ── Helpers ────────────────────────────────────────────────────────────────

afterEach(() => cleanup());

function boxes(container: HTMLElement): HTMLInputElement[] {
  return Array.from(
    container.querySelectorAll<HTMLInputElement>('[data-testid^="stepup-code-"]'),
  );
}

/**
 * Simulate typing six digits into the CodeInput boxes one at a time.
 * Each box receives an `input` event with the digit value, matching the
 * CodeInput `oninput` handler's expectations.
 */
async function enterCode(b: HTMLInputElement[], digits: string): Promise<void> {
  for (let i = 0; i < 6; i++) {
    const box = b[i];
    if (!box) continue;
    box.focus();
    box.value = digits.charAt(i);
    await fireEvent.input(box);
    await tick();
  }
}

// ── Tests ──────────────────────────────────────────────────────────────────

describe('StepUpModal — auto-submit (re #79)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockStepUp.visible = false;
    mockStepUp.enrollRequired = false;
    mockStepUp.error = null;
    mockStepUp.submitting = false;
    mockStepUp.lockoutUntil = null;
    (mockSubmitCode as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
  });

  it('calls submitCode when all six digits are entered', async () => {
    mockStepUp.visible = true;
    const { container } = render(StepUpModal);
    await tick();

    const b = boxes(container);
    expect(b).toHaveLength(6);

    await enterCode(b, '123456');

    await waitFor(() => {
      expect(mockSubmitCode).toHaveBeenCalledWith('123456');
    });
  });

  it('clears the code boxes after submitCode resolves', async () => {
    mockStepUp.visible = true;
    const { container } = render(StepUpModal);
    await tick();

    const b = boxes(container);
    await enterCode(b, '654321');

    await waitFor(() => {
      expect(mockSubmitCode).toHaveBeenCalledOnce();
    });
    // After submitCode resolves the .then() sets code = '', clearing all boxes.
    await tick();
    for (const box of b) {
      expect(box.value).toBe('');
    }
  });

  it('returns focus to the first digit box after a rejected code (re #120)', async () => {
    // Simulate a wrong-code response: submitCode resolves (no throw), leaving
    // mockStepUp.error set. The modal stays visible.
    (mockSubmitCode as ReturnType<typeof vi.fn>).mockImplementation(async () => {
      mockStepUp.error = 'Incorrect code — try again.';
    });
    mockStepUp.visible = true;
    const { container } = render(StepUpModal);
    await tick();

    const b = boxes(container);
    // Focus the first box manually (mirrors autofocus in a real browser).
    b[0]!.focus();
    await enterCode(b, '999999');

    // Wait for submitCode to be called and code to be cleared.
    await waitFor(() => expect(mockSubmitCode).toHaveBeenCalledOnce());
    // Flush the .then() microtask and the resulting Svelte effect.
    await tick();
    await tick();

    // The first digit box must hold focus so the user can retype.
    expect(document.activeElement).toBe(b[0]);
    // All boxes must be empty.
    for (const box of b) {
      expect(box.value).toBe('');
    }
  });

  it('does not show a submit/confirm button in the code-entry variant', async () => {
    mockStepUp.visible = true;
    const { container } = render(StepUpModal);
    await tick();

    const submitBtn = container.querySelector('[type="submit"]');
    expect(submitBtn).toBeNull();
  });

  it('does not auto-submit when the lockout countdown is active', async () => {
    mockStepUp.visible = true;
    // lockoutUntil is read by the lockout $effect which drives the local
    // lockoutSeconds counter. In tests the setInterval does not fire, so we
    // need to set lockoutSeconds indirectly. We verify the guard by checking
    // that submitCode is NOT called when lockoutUntil is set.
    mockStepUp.lockoutUntil = new Date(Date.now() + 60_000);
    const { container } = render(StepUpModal);
    await tick();

    const b = boxes(container);
    // Boxes are disabled during lockout (disabled prop propagated from modal).
    // Simulate the input events anyway to exercise the guard in the effect.
    for (let i = 0; i < 6; i++) {
      b[i]!.value = String(i + 1);
      await fireEvent.input(b[i]!);
      await tick();
    }

    // Give any microtasks a chance to run.
    await tick();
    expect(mockSubmitCode).not.toHaveBeenCalled();
  });
});
