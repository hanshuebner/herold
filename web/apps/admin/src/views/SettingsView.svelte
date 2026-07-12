<script lang="ts">
  /**
   * Admin settings view (re #205). Locale used to be a toggle control
   * living in the header; it is a per-user setting now, presented here
   * with the same segmented-control pattern as the Suite's Appearance
   * section (`web/apps/suite/src/views/SettingsView.svelte`).
   */
  import { settings } from '../lib/settings/settings.svelte';
  import { t, LOCALES, type Locale } from '../lib/i18n/i18n.svelte';
</script>

<div class="settings">
  <div class="page-header">
    <h1 class="page-title">{t('settings.title')}</h1>
  </div>

  <div class="row vertical">
    <span class="label">{t('settings.language')}</span>
    <div class="segmented" role="radiogroup" aria-label={t('settings.language')}>
      {#each LOCALES as locale}
        <button
          type="button"
          role="radio"
          aria-checked={settings.locale === locale}
          class:on={settings.locale === locale}
          onclick={() => settings.setLocale(locale as Locale)}
        >
          {t(`settings.language.${locale}`)}
        </button>
      {/each}
    </div>
  </div>
</div>

<style>
  .settings {
    max-width: 640px;
  }

  .page-header {
    margin-bottom: var(--spacing-06);
  }

  .page-title {
    font-family: var(--font-sans);
    font-size: var(--type-heading-compact-02-size);
    font-weight: var(--type-heading-compact-02-weight);
    color: var(--text-primary);
    margin: 0;
  }

  .row.vertical {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-primary);
  }

  .segmented {
    display: inline-flex;
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    overflow: hidden;
    width: fit-content;
  }

  .segmented button {
    padding: var(--spacing-02) var(--spacing-04);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    background: none;
    border: none;
    cursor: pointer;
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .segmented button:not(:last-child) {
    border-right: 1px solid var(--border-subtle-01);
  }

  .segmented button:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .segmented button.on {
    background: var(--interactive);
    color: var(--text-on-color);
  }
</style>
