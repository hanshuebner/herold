<script lang="ts">
  /**
   * ManualOnThisPage renders the right-rail "On this page" outline.
   *
   * It receives the ordered list of h2/h3 headings for the current chapter
   * and fires `onNavigate` with the anchor hash when a heading is clicked.
   */
  import type { OutlineEntry } from '../markdoc/bundle.js';

  interface Props {
    outline: OutlineEntry[];
    activeId?: string;
    onNavigate: (hash: string) => void;
    t: (key: string) => string;
  }

  const { outline, activeId, onNavigate, t }: Props = $props();
</script>

{#if outline.length > 0}
  <nav class="on-this-page" aria-label={t('manual.onthispage.label')}>
    <p class="on-this-page__heading">{t('manual.onthispage.label')}</p>
    <ul class="otp-list" role="list">
      {#each outline as entry (entry.id)}
        <li class="otp-item otp-item--h{entry.level}">
          <button
            type="button"
            class="otp-link"
            class:otp-link--active={entry.id === activeId}
            onclick={() => onNavigate(entry.id)}
          >
            {entry.text}
          </button>
        </li>
      {/each}
    </ul>
  </nav>
{/if}

<style>
  .on-this-page {
    width: 100%;
  }

  .on-this-page__heading {
    margin: 0 0 var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    font-weight: var(--type-heading-compact-01-weight);
    color: var(--text-primary);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .otp-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .otp-item {
    display: block;
  }

  .otp-item--h3 .otp-link {
    padding-left: var(--spacing-05);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
  }

  .otp-link {
    display: block;
    width: 100%;
    text-align: left;
    padding: var(--spacing-02) var(--spacing-03);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    line-height: 1.4;
    border-left: 2px solid transparent;
    transition:
      color var(--duration-fast-01) var(--easing-productive-enter),
      border-color var(--duration-fast-01) var(--easing-productive-enter);
  }

  .otp-link:hover {
    color: var(--text-primary);
    border-left-color: var(--border-subtle-01);
  }

  .otp-link--active {
    color: var(--interactive);
    border-left-color: var(--interactive);
    font-weight: 600;
  }

  .otp-link--active:hover {
    color: var(--interactive);
    border-left-color: var(--interactive);
  }
</style>
