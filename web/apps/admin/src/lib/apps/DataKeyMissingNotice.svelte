<script lang="ts">
  /**
   * Shown when the apps REST surface answers 503 for a missing data key
   * (REQ-APPS-36, server REQ-APP-14): apps need
   * `[server.secrets].data_key_ref`. Links the configuration reference.
   */
  import { t } from '../i18n/i18n.svelte';
</script>

<div class="notice" role="alert" data-testid="apps-data-key-missing">
  <p class="title">{t('apps.dataKey.title')}</p>
  <p>{t('apps.dataKey.body')}</p>
  <a href="#/help/configure">{t('apps.dataKey.link')}</a>
</div>

<style>
  .notice {
    padding: var(--spacing-04) var(--spacing-05);
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    border-left: 3px solid var(--support-warning);
    border-radius: var(--radius-md);
    margin-bottom: var(--spacing-06);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }

  .notice p {
    margin: 0 0 var(--spacing-02);
  }

  .title {
    font-weight: 600;
  }

  a {
    color: var(--interactive);
  }
</style>
