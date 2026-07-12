<script lang="ts">
  /**
   * Admin settings view (re #205). Locale used to be a toggle control
   * living in the header; it is a per-user setting now, presented here
   * as a dropdown (not a segmented toggle -- a toggle does not scale
   * past two or three languages, and more are expected to be added).
   */
  import { settings } from '../lib/settings/settings.svelte';
  import { t, LOCALES, type Locale } from '../lib/i18n/i18n.svelte';
</script>

<div class="settings">
  <div class="page-header">
    <h1 class="page-title">{t('settings.title')}</h1>
  </div>

  <div class="row vertical">
    <label class="label" for="settings-language">{t('settings.language')}</label>
    <select
      id="settings-language"
      class="select"
      value={settings.locale}
      onchange={(e) => settings.setLocale((e.currentTarget as HTMLSelectElement).value as Locale)}
    >
      {#each LOCALES as locale}
        <option value={locale}>{t(`settings.language.${locale}`)}</option>
      {/each}
    </select>
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

  .select {
    width: max-content;
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    cursor: pointer;
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .select:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }
</style>
