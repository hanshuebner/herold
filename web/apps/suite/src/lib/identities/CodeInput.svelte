<script lang="ts">
  /**
   * Six-box single-digit verification-code input (re #21, re #93).
   *
   * Renders six distinct single-digit boxes instead of one wide text
   * field. Behaviour:
   *   - numeric-only: non-digit keystrokes are ignored.
   *   - auto-advance: typing a digit moves focus to the next box.
   *   - backspace: clears the current box; if already empty, moves
   *     focus back and clears the previous box.
   *   - arrow keys: move focus between boxes.
   *   - paste: pasting a string fills as many boxes as it has digits
   *     (capped at six) and focuses the next empty box.
   *   - autofocus: when true, the first box receives focus on mount so
   *     the user can type immediately without clicking.
   *   - oncomplete: called with the assembled 6-digit code when the
   *     sixth digit is entered or a full 6-digit code is pasted;
   *     covers both digit-at-a-time entry and paste-and-complete.
   *   - reset path: when `value` is cleared to `''` from the outside
   *     (e.g. after a wrong-code error), the first box is focused so
   *     the user can retype without touching the mouse.
   *
   * The assembled value is exposed via the bindable `value` prop. The
   * parent keeps its existing validation / submit semantics: `value`
   * is a plain string of 0..6 digits.
   */

  import { onMount } from 'svelte';

  interface Props {
    /** Assembled 0..6-digit code. Bindable. */
    value?: string;
    /** Disable every box (e.g. while a verify request is in flight). */
    disabled?: boolean;
    /** Mark every box invalid (drives the error border). */
    invalid?: boolean;
    /** Optional accessible label for the group. */
    ariaLabel?: string;
    /** data-testid for the wrapping group; boxes get `${testid}-N`. */
    testid?: string;
    /** When true, the first digit box receives focus after mount. */
    autofocus?: boolean;
    /**
     * Called with the assembled code when all six digits are present.
     * Covers both one-at-a-time digit entry and paste-and-complete.
     * The parent can wire this directly to its verify handler to
     * auto-submit without an explicit button click (re #93).
     */
    oncomplete?: (code: string) => void;
  }

  let {
    value = $bindable(''),
    disabled = false,
    invalid = false,
    ariaLabel,
    testid = 'code-input',
    autofocus = false,
    oncomplete,
  }: Props = $props();

  const SIZE = 6;

  let inputs = $state<(HTMLInputElement | null)[]>(new Array(SIZE).fill(null));

  onMount(() => {
    if (autofocus) {
      inputs[0]?.focus();
    }
  });

  // Reset path (re #93): when value is cleared to '' from the outside
  // (e.g. parent clears code after a wrong-code 400 response), focus
  // the first box so the user can retype without touching the mouse.
  // Uses a plain variable (not $state) so only `value` is tracked as
  // a reactive dependency; no loop risk from the write inside the effect.
  let prevValueLen = 0;
  $effect(() => {
    const v = value; // tracked dep
    if (v === '' && prevValueLen > 0) {
      focusBox(0);
    }
    prevValueLen = v.length;
  });

  /** Per-box digit derived from the assembled value. */
  let digits = $derived.by<string[]>(() => {
    const out = new Array<string>(SIZE).fill('');
    const clean = value.replace(/\D/g, '').slice(0, SIZE);
    for (let i = 0; i < clean.length; i++) out[i] = clean[i]!;
    return out;
  });

  /** Rebuild `value` from the six boxes. */
  function assemble(parts: string[]): void {
    value = parts.join('').replace(/\D/g, '').slice(0, SIZE);
  }

  function focusBox(i: number): void {
    if (i < 0 || i >= SIZE) return;
    const el = inputs[i];
    if (el) {
      el.focus();
      el.select();
    }
  }

  function onInput(i: number, e: Event): void {
    const el = e.target as HTMLInputElement;
    // Keep only the last typed digit (handles the case where the box
    // already had a value and the user overtyped it).
    const d = el.value.replace(/\D/g, '').slice(-1);
    const parts = [...digits];
    parts[i] = d;
    el.value = d;
    assemble(parts);
    // Fire oncomplete when the sixth digit completes the code (re #93).
    if (value.length === SIZE) oncomplete?.(value);
    if (d !== '' && i < SIZE - 1) focusBox(i + 1);
  }

  function onKeyDown(i: number, e: KeyboardEvent): void {
    if (e.key === 'Backspace') {
      const parts = [...digits];
      if (parts[i] !== '') {
        // Clear the current box, stay put.
        parts[i] = '';
        assemble(parts);
      } else if (i > 0) {
        // Already empty — step back and clear the previous box.
        parts[i - 1] = '';
        assemble(parts);
        focusBox(i - 1);
      }
      e.preventDefault();
      return;
    }
    if (e.key === 'ArrowLeft' && i > 0) {
      e.preventDefault();
      focusBox(i - 1);
      return;
    }
    if (e.key === 'ArrowRight' && i < SIZE - 1) {
      e.preventDefault();
      focusBox(i + 1);
      return;
    }
    // Block non-digit printable keys so the field stays numeric.
    if (e.key.length === 1 && !/\d/.test(e.key) && !e.ctrlKey && !e.metaKey) {
      e.preventDefault();
    }
  }

  function onPaste(i: number, e: ClipboardEvent): void {
    e.preventDefault();
    const pasted = (e.clipboardData?.getData('text') ?? '')
      .replace(/\D/g, '')
      .slice(0, SIZE);
    if (pasted === '') return;
    const parts = new Array<string>(SIZE).fill('');
    // Fill from the start (a pasted code always replaces the whole value).
    for (let k = 0; k < pasted.length; k++) parts[k] = pasted[k]!;
    assemble(parts);
    // Fire oncomplete when a full six-digit code is pasted (re #93).
    if (value.length === SIZE) oncomplete?.(value);
    // Focus the box after the last filled digit, or the last box.
    focusBox(Math.min(pasted.length, SIZE - 1));
  }
</script>

<div
  class="code-input"
  class:invalid
  role="group"
  aria-label={ariaLabel}
  data-testid={testid}
>
  {#each digits as digit, i (i)}
    <input
      bind:this={inputs[i]}
      type="text"
      inputmode="numeric"
      autocomplete={i === 0 ? 'one-time-code' : 'off'}
      maxlength="1"
      pattern="[0-9]*"
      value={digit}
      {disabled}
      aria-invalid={invalid}
      aria-label={`${ariaLabel ?? 'Digit'} ${i + 1}`}
      data-testid={`${testid}-${i}`}
      oninput={(e) => onInput(i, e)}
      onkeydown={(e) => onKeyDown(i, e)}
      onpaste={(e) => onPaste(i, e)}
      onfocus={(e) => (e.target as HTMLInputElement).select()}
    />
  {/each}
</div>

<style>
  .code-input {
    display: flex;
    gap: var(--spacing-03);
  }

  .code-input input {
    width: 3rem;
    height: 3.25rem;
    padding: 0;
    text-align: center;
    font-family: var(--font-mono);
    font-size: var(--type-heading-compact-02-size);
    font-weight: 600;
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
  }

  .code-input input:focus {
    outline: none;
    border-color: var(--focus);
    box-shadow: 0 0 0 1px var(--focus);
  }

  .code-input input:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .code-input.invalid input {
    border-color: var(--support-error);
  }

  @media (max-width: 420px) {
    .code-input {
      gap: var(--spacing-02);
    }
    .code-input input {
      width: 2.5rem;
      height: 3rem;
      font-size: var(--type-body-01-size);
    }
  }
</style>
