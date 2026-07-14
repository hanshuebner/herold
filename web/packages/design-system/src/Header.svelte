<script lang="ts">
  /**
   * Shared application header shell for the herold SPAs (re #205).
   *
   * Owns the outer `<header>` chrome common to both apps' top bars: the
   * flex row, the canonical height (`--spacing-08`), background, and
   * bottom border. It does not own padding, gap, or the brand/content
   * layout — those stay in each app's own component and its own scoped
   * styles, reached via the `class` prop and Svelte's `:global()`
   * escape hatch (the class name is passed as data; only the component
   * that renders an element can attach *its own* scoped styles to it,
   * so a caller styling *this* component's `<header>` needs `:global()`).
   *
   * Two named regions:
   *   - `brand` — the leftmost mark (wordmark, and in the suite's case
   *     the app-switcher button). Optional so a consumer without a
   *     distinct brand region can omit it.
   *   - default content (`children`) — everything else: search, sync
   *     status, sign-out, profile, whatever the app needs. Rendered as
   *     a sibling of the brand region, in document order.
   */
  import type { Snippet } from 'svelte';

  interface Props {
    /** Extra class(es) applied to the root `<header>`, e.g. for the
     * consuming app's own `:global()`-scoped padding/gap rules. */
    class?: string;
    brand?: Snippet;
    children?: Snippet;
  }

  let { class: className = '', brand, children }: Props = $props();
</script>

<header class="ds-header {className}">
  {#if brand}
    {@render brand()}
  {/if}
  {@render children?.()}
</header>

<style>
  .ds-header {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    height: var(--spacing-08);
    background: var(--layer-01);
    border-bottom: 1px solid var(--border-subtle-01);
  }
</style>
