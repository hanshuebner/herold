<script lang="ts">
  /**
   * The extensions of one app manifest, as the review view shows them
   * (REQ-APPS-32): point, label in every language, icon, endpoint as the
   * full URL, and each requested scope with its plain-language
   * explanation.
   *
   * With `side` set, the list is one column of a manifest update
   * (REQ-APPS-33): on the `approved` side removed extensions and scopes
   * are marked, on the `pending` side added ones; a changed endpoint is
   * marked on both.
   */
  import { t } from '../i18n/i18n.svelte';
  import {
    endpointUrl,
    extensionMarks,
    scopeExplanationKey,
    type AppManifest,
    type ManifestDiff,
  } from './apps.svelte';

  interface Props {
    manifest: AppManifest;
    origin: string;
    diff?: ManifestDiff | null;
    side?: 'approved' | 'pending' | null;
  }
  let { manifest, origin, diff = null, side = null }: Props = $props();
</script>

<ul class="extensions">
  {#each manifest.extensions as ext (ext.id)}
    {@const marks = extensionMarks(diff, ext.id)}
    {@const changeMark = side === 'pending' ? marks.added : side === 'approved' ? marks.removed : false}
    <li
      class="extension"
      class:added={side === 'pending' && marks.added}
      class:removed={side === 'approved' && marks.removed}
      data-extension-id={ext.id}
    >
      <div class="ext-head">
        <span class="ext-label">{ext.label.en ?? Object.values(ext.label)[0] ?? ext.id}</span>
        {#if changeMark}
          <span class="mark" class:mark-added={side === 'pending'} class:mark-removed={side === 'approved'}>
            {side === 'pending' ? t('apps.diff.added') : t('apps.diff.removed')}
          </span>
        {/if}
      </div>
      <dl class="ext-grid">
        <dt>{t('apps.ext.id')}</dt>
        <dd class="mono">{ext.id}</dd>
        <dt>{t('apps.ext.point')}</dt>
        <dd class="mono">{ext.point}</dd>
        <dt>{t('apps.ext.labels')}</dt>
        <dd>
          <ul class="labels">
            {#each Object.entries(ext.label) as [lang, text] (lang)}
              <li><span class="lang mono">{lang}</span> {text}</li>
            {/each}
          </ul>
        </dd>
        <dt>{t('apps.ext.icon')}</dt>
        <dd class="mono">{ext.icon || 'app'}</dd>
        <dt>{t('apps.ext.endpoint')}</dt>
        <dd class="mono" class:changed={marks.endpointChange !== null && side !== null} data-field="endpoint">
          {endpointUrl(origin, ext.endpoint)}
          {#if marks.endpointChange && side === 'pending'}
            <span class="mark mark-changed">{t('apps.diff.endpointChanged', { from: marks.endpointChange.from })}</span>
          {/if}
        </dd>
        <dt>{t('apps.ext.scopes')}</dt>
        <dd>
          {#if ext.scopes.length === 0}
            <span class="muted">{t('common.none')}</span>
          {:else}
            <ul class="scopes">
              {#each ext.scopes as scope (scope)}
                {@const scopeAdded = side === 'pending' && marks.addedScopes.includes(scope)}
                {@const scopeRemoved = side === 'approved' && marks.removedScopes.includes(scope)}
                <li class:added={scopeAdded} class:removed={scopeRemoved} data-scope={scope}>
                  <span class="mono scope-name">{scope}</span>
                  {#if scopeAdded}<span class="mark mark-added">{t('apps.diff.added')}</span>{/if}
                  {#if scopeRemoved}<span class="mark mark-removed">{t('apps.diff.removed')}</span>{/if}
                  <span class="scope-desc">{t(scopeExplanationKey(scope))}</span>
                </li>
              {/each}
            </ul>
          {/if}
        </dd>
      </dl>
    </li>
  {:else}
    <li class="muted">{t('apps.ext.none')}</li>
  {/each}
</ul>

<style>
  .extensions {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .extension {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-left: 3px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-04) var(--spacing-05);
  }
  .extension.added {
    border-left-color: var(--support-success);
  }
  .extension.removed {
    border-left-color: var(--support-error);
  }

  .ext-head {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    margin-bottom: var(--spacing-03);
  }

  .ext-label {
    font-weight: 600;
    color: var(--text-primary);
  }

  .ext-grid {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--spacing-02) var(--spacing-05);
    margin: 0;
  }
  .ext-grid dt {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
  }
  .ext-grid dd {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    word-break: break-all;
  }
  .ext-grid dd.changed {
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    border-radius: var(--radius-sm);
  }

  .labels,
  .scopes {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .scopes li {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--spacing-02) var(--spacing-03);
    word-break: normal;
  }
  .scopes li.added {
    color: var(--support-success);
  }
  .scopes li.removed {
    color: var(--support-error);
    text-decoration: line-through;
  }

  .scope-desc {
    color: var(--text-secondary);
    flex-basis: 100%;
  }

  .lang {
    display: inline-block;
    min-width: 2.5em;
    color: var(--text-secondary);
  }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  .muted {
    color: var(--text-helper);
  }

  .mark {
    display: inline-block;
    padding: 1px var(--spacing-02);
    font-family: var(--font-sans);
    font-size: var(--type-helper-text-01-size);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.02em;
    border-radius: var(--radius-sm);
    white-space: nowrap;
    text-decoration: none;
  }
  .mark-added {
    color: var(--support-success);
    background: color-mix(in srgb, var(--support-success) 15%, transparent);
  }
  .mark-removed {
    color: var(--support-error);
    background: color-mix(in srgb, var(--support-error) 15%, transparent);
  }
  .mark-changed {
    color: var(--text-primary);
    background: color-mix(in srgb, var(--support-warning) 25%, transparent);
    margin-left: var(--spacing-02);
    text-transform: none;
    font-family: var(--font-mono);
  }
</style>
