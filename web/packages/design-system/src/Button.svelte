<script lang="ts">
  /**
   * Shared button for the herold SPAs.
   *
   * Every variant uses a single consistent corner radius (--radius-lg,
   * 8px) — never pill, never the 4px medium radius. The visual weight
   * differs by `variant`; the geometry does not.
   *
   *   - primary   — solid interactive fill, the page's main CTA.
   *   - secondary — bordered, neutral fill; pairs with a primary.
   *   - danger    — bordered neutral that turns red on hover; for
   *                 destructive-but-not-loud actions (e.g. sign out).
   *   - ghost     — text-only until hover; lowest-emphasis affordance.
   *
   * An optional leading `icon` snippet renders before the label.
   */
  import type { Snippet } from 'svelte';

  type Variant = 'primary' | 'secondary' | 'danger' | 'ghost';

  interface Props {
    variant?: Variant;
    type?: 'button' | 'submit' | 'reset';
    disabled?: boolean;
    title?: string;
    /** Accessible label; required for icon-only buttons. */
    ariaLabel?: string;
    /** Forwarded aria-expanded for disclosure / popover triggers. */
    ariaExpanded?: boolean;
    /** Forwarded aria-haspopup for popover / menu triggers. */
    ariaHaspopup?: boolean;
    /** Compact padding for inline / row-level affordances. */
    compact?: boolean;
    /** Forwarded test hook. */
    testid?: string;
    onclick?: (event: MouseEvent) => void;
    /** Optional leading icon, rendered before the label. */
    icon?: Snippet;
    children?: Snippet;
  }

  let {
    variant = 'secondary',
    type = 'button',
    disabled = false,
    title,
    ariaLabel,
    ariaExpanded,
    ariaHaspopup,
    compact = false,
    testid,
    onclick,
    icon,
    children,
  }: Props = $props();
</script>

<button
  {type}
  {title}
  {disabled}
  class="ds-btn ds-btn-{variant}"
  class:compact
  aria-label={ariaLabel}
  aria-expanded={ariaExpanded}
  aria-haspopup={ariaHaspopup}
  data-testid={testid}
  onclick={(e) => onclick?.(e)}
>
  {#if icon}
    <span class="ds-btn-icon" aria-hidden="true">{@render icon()}</span>
  {/if}
  {#if children}
    <span class="ds-btn-label">{@render children()}</span>
  {/if}
</button>

<style>
  .ds-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: var(--spacing-02);
    padding: var(--spacing-03) var(--spacing-05);
    /* Single, consistent corner radius across every variant. */
    border-radius: var(--radius-lg);
    border: 1px solid transparent;
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    line-height: var(--type-body-compact-01-line);
    min-height: var(--touch-min);
    cursor: pointer;
    white-space: nowrap;
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      border-color var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .ds-btn.compact {
    padding: var(--spacing-02) var(--spacing-04);
    min-height: 32px;
  }

  .ds-btn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }

  .ds-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .ds-btn-icon {
    display: inline-flex;
    align-items: center;
  }

  /* ── primary ── */
  .ds-btn-primary {
    background: var(--interactive);
    color: var(--text-on-color);
    border-color: var(--interactive);
  }
  .ds-btn-primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }

  /* ── secondary ── */
  .ds-btn-secondary {
    background: var(--layer-02);
    color: var(--text-primary);
    border-color: var(--border-subtle-01);
  }
  .ds-btn-secondary:hover:not(:disabled) {
    background: var(--layer-03);
  }

  /* ── danger ── */
  .ds-btn-danger {
    background: var(--layer-02);
    color: var(--text-primary);
    border-color: var(--border-subtle-01);
  }
  .ds-btn-danger:hover:not(:disabled) {
    background: var(--support-error);
    color: var(--text-on-color);
    border-color: var(--support-error);
  }

  /* ── ghost ── */
  .ds-btn-ghost {
    background: transparent;
    color: var(--text-secondary);
    border-color: transparent;
  }
  .ds-btn-ghost:hover:not(:disabled) {
    background: var(--layer-02);
    color: var(--text-primary);
  }
</style>
