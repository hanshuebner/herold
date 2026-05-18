/**
 * Component tests for CodeInput — the six-box single-digit
 * verification-code input shared by the add-identity wizard step 2
 * and the standalone IdentityVerifyDialog (re #21).
 *
 * Covers: per-box digit entry with auto-advance, backspace clear +
 * focus step-back, paste filling all boxes, and numeric-only input.
 *
 * The component's `value` prop is `$bindable`; to observe the
 * assembled value the tests render inside an `$effect.root` and pass
 * a `$state`-backed getter/setter pair so two-way binding works.
 */

import { describe, it, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import { tick } from 'svelte';
import CodeInput from './CodeInput.svelte';

afterEach(() => cleanup());

function boxes(container: HTMLElement): HTMLInputElement[] {
  return Array.from(
    container.querySelectorAll<HTMLInputElement>('[data-testid^="code-input-"]'),
  );
}

/**
 * Render CodeInput with a reactive `value` binding. Returns the
 * container, the box inputs, a `read()` accessor for the current
 * assembled value, and a `stop()` to dispose the reactive root.
 */
function renderBound(initial = '') {
  let value = $state(initial);
  let container!: HTMLElement;
  const stop = $effect.root(() => {
    const r = render(CodeInput, {
      props: {
        get value() {
          return value;
        },
        set value(v: string) {
          value = v;
        },
      },
    });
    container = r.container;
  });
  return {
    container,
    boxes: boxes(container),
    read: () => value,
    stop,
  };
}

describe('CodeInput', () => {
  it('renders six single-digit boxes', () => {
    const { container, stop } = renderBound();
    const b = boxes(container);
    expect(b).toHaveLength(6);
    for (const el of b) expect(el.getAttribute('maxlength')).toBe('1');
    stop();
  });

  it('typing a digit fills the box and auto-advances focus', async () => {
    const { boxes: b, stop } = renderBound();
    b[0]!.focus();
    b[0]!.value = '4';
    await fireEvent.input(b[0]!);
    await tick();
    expect(b[0]!.value).toBe('4');
    expect(document.activeElement).toBe(b[1]);
    stop();
  });

  it('assembles the full value into the bindable prop', async () => {
    const { boxes: b, read, stop } = renderBound();
    for (let i = 0; i < 6; i++) {
      b[i]!.focus();
      b[i]!.value = String(i + 1);
      await fireEvent.input(b[i]!);
    }
    await tick();
    expect(read()).toBe('123456');
    stop();
  });

  it('backspace clears the current box when it has a value', async () => {
    const { boxes: b, read, stop } = renderBound('12');
    await tick();
    b[1]!.focus();
    await fireEvent.keyDown(b[1]!, { key: 'Backspace' });
    await tick();
    expect(read()).toBe('1');
    stop();
  });

  it('backspace on an empty box steps focus back and clears the previous box', async () => {
    const { boxes: b, read, stop } = renderBound('12');
    await tick();
    // Box index 2 is empty (value '12' fills boxes 0 and 1).
    b[2]!.focus();
    await fireEvent.keyDown(b[2]!, { key: 'Backspace' });
    await tick();
    expect(read()).toBe('1');
    expect(document.activeElement).toBe(b[1]);
    stop();
  });

  it('pasting a six-digit string fills every box', async () => {
    const { boxes: b, read, stop } = renderBound();
    const dt = new DataTransfer();
    dt.setData('text', '987654');
    await fireEvent.paste(b[0]!, { clipboardData: dt });
    await tick();
    expect(read()).toBe('987654');
    stop();
  });

  it('paste strips non-digit characters before filling', async () => {
    const { boxes: b, read, stop } = renderBound();
    const dt = new DataTransfer();
    dt.setData('text', '12-34 56');
    await fireEvent.paste(b[0]!, { clipboardData: dt });
    await tick();
    expect(read()).toBe('123456');
    stop();
  });

  it('blocks non-digit printable keystrokes', async () => {
    const { boxes: b, stop } = renderBound();
    b[0]!.focus();
    // fireEvent returns false when a handler called preventDefault.
    const notPrevented = await fireEvent.keyDown(b[0]!, { key: 'a' });
    expect(notPrevented).toBe(false);
    stop();
  });

  it('marks every box invalid when the invalid prop is set', () => {
    let container!: HTMLElement;
    const stop = $effect.root(() => {
      container = render(CodeInput, { props: { invalid: true } }).container;
    });
    for (const el of boxes(container)) {
      expect(el.getAttribute('aria-invalid')).toBe('true');
    }
    stop();
  });
});
