<script lang="ts">
  /**
   * Settings → Tagged addresses (REQ-SET-TAG-01..21).
   *
   * Two subsections:
   *   1. Filters table  — every TaggedAddressFilter row for the principal
   *      (REQ-SET-TAG-01..05). Add / edit / convert-to-sieve / delete.
   *   2. Dismissals view — every dismissal the user has set from the
   *      per-message banner (REQ-SET-TAG-06). Re-enable action per row.
   *
   * Both lists are sourced from the shared `taggedAddressStore`. The
   * Identity state-change push (REQ-TAG-72) drives refreshes — when the
   * user creates a filter on another device, this view updates without
   * the user clicking refresh.
   */

  import { untrack } from 'svelte';
  import { mail } from '../../lib/mail/store.svelte';
  import {
    taggedAddressStore,
    type TaggedAddressFilter,
  } from '../../lib/mail/tagged-address-store.svelte';
  import type { TaggedAddressDismissal } from '../../lib/api/tagged-addresses';
  import {
    actionLabelKey,
    composeTaggedAddress,
    DISMISSAL_CAP,
    DISMISSAL_CAP_WARN_REMAINING,
    FILTER_CAP,
    FILTER_CAP_WARN_REMAINING,
    sortDismissals,
    sortFilters,
  } from '../../lib/settings/tagged-addresses';
  import { jmap } from '../../lib/jmap/client';
  import { Capability } from '../../lib/jmap/types';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import TaggedAddressFilterDialog from './TaggedAddressFilterDialog.svelte';
  import Button from '@herold/design-system/Button.svelte';

  // Identities feed both the email-for sort and the row labels. The
  // settings panel already lazy-loads them when the Account section
  // mounts; we trigger a load too in case the user landed directly on
  // /settings/tagged-addresses.
  $effect(() => {
    if (mail.identities.size === 0) {
      untrack(() => void mail.loadIdentities());
    }
  });

  // Capability is the gate: when absent the section header surfaces a
  // hint and skips the data load. SettingsView hides the nav entry too,
  // but the form itself is defensive in case of a stale route.
  const capabilityAdvertised = $derived(
    jmap.hasCapability(Capability.HeroldTaggedAddresses),
  );

  // Lazy-load the store on mount when the capability is advertised.
  $effect(() => {
    if (capabilityAdvertised) {
      untrack(() => void taggedAddressStore.load());
    }
  });

  function emailFor(baseIdentityId: string): string | null {
    return mail.identities.get(baseIdentityId)?.email ?? null;
  }

  const sortedFilters = $derived<TaggedAddressFilter[]>(
    sortFilters(taggedAddressStore.filters, emailFor),
  );

  const sortedDismissals = $derived<TaggedAddressDismissal[]>(
    sortDismissals(
      taggedAddressStore.dismissals.map((d) => ({
        ...d,
        baseIdentityId: d.base_identity_id,
      })),
      emailFor,
    ).map((d) => ({
      base_identity_id: d.baseIdentityId,
      suffix: d.suffix,
      dismissed_at: d.dismissed_at,
    })),
  );

  const filterCount = $derived(taggedAddressStore.filters.length);
  const remainingFilterSlots = $derived(FILTER_CAP - filterCount);
  const capReached = $derived(remainingFilterSlots <= 0);
  const capApproaching = $derived(
    remainingFilterSlots > 0 && remainingFilterSlots <= FILTER_CAP_WARN_REMAINING,
  );

  const dismissalCount = $derived(taggedAddressStore.dismissals.length);
  const dismissalApproaching = $derived(
    DISMISSAL_CAP - dismissalCount > 0 &&
      DISMISSAL_CAP - dismissalCount <= DISMISSAL_CAP_WARN_REMAINING,
  );

  // ── Dialog state ─────────────────────────────────────────────────────
  let dialogOpen = $state(false);
  /** null = create mode; a filter row = edit mode. */
  let dialogFilter = $state<TaggedAddressFilter | null>(null);

  function openCreate(): void {
    dialogFilter = null;
    dialogOpen = true;
  }
  function openEdit(filter: TaggedAddressFilter): void {
    dialogFilter = filter;
    dialogOpen = true;
  }
  function closeDialog(): void {
    dialogOpen = false;
    dialogFilter = null;
  }

  // ── Row actions ──────────────────────────────────────────────────────

  async function onDelete(filter: TaggedAddressFilter): Promise<void> {
    const address = composeTaggedAddress(
      mail.identities.get(filter.baseIdentityId)?.email,
      filter.suffix,
    );
    const ok = await confirm.ask({
      title: t('settings.taggedAddresses.confirmDelete.title'),
      message: t('settings.taggedAddresses.confirmDelete.body', { address }),
      confirmLabel: t('settings.taggedAddresses.confirmDelete.confirm'),
      cancelLabel: t('settings.taggedAddresses.confirmDelete.cancel'),
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await taggedAddressStore.deleteFilter(filter.id);
      toast.show({ message: t('settings.taggedAddresses.toast.deleted') });
    } catch (err) {
      surfaceError(err);
    }
  }

  async function onConvert(filter: TaggedAddressFilter): Promise<void> {
    const address = composeTaggedAddress(
      mail.identities.get(filter.baseIdentityId)?.email,
      filter.suffix,
    );
    const ok = await confirm.ask({
      title: t('settings.taggedAddresses.confirmConvert.title'),
      message: t('settings.taggedAddresses.confirmConvert.body', { address }),
      confirmLabel: t('settings.taggedAddresses.confirmConvert.confirm'),
      cancelLabel: t('settings.taggedAddresses.confirmConvert.cancel'),
    });
    if (!ok) return;
    try {
      await taggedAddressStore.convertToSieve(filter.id);
      toast.show({ message: t('settings.taggedAddresses.toast.converted'), timeoutMs: 8000 });
    } catch (err) {
      surfaceError(err);
    }
  }

  async function onUndismiss(dismissal: TaggedAddressDismissal): Promise<void> {
    try {
      await taggedAddressStore.undismiss(dismissal.base_identity_id, dismissal.suffix);
      const address = composeTaggedAddress(
        emailFor(dismissal.base_identity_id),
        dismissal.suffix,
      );
      toast.show({
        message: t('settings.taggedAddresses.toast.undismissed', { address }),
      });
    } catch (err) {
      surfaceError(err);
    }
  }

  function surfaceError(err: unknown): void {
    const msg = err instanceof Error ? err.message : String(err);
    toast.show({
      message: t('settings.taggedAddresses.toast.failed', { error: msg }),
      kind: 'error',
      timeoutMs: 6000,
    });
  }

  // ── Dismissals expand/collapse (REQ-SET-TAG-06: collapsed by default) ─
  let dismissalsOpen = $state(false);

  function formatDate(iso: string): string {
    // The server emits an RFC-3339 UTC timestamp; surface it in the user
    // locale's short date form. Falling through unconverted on parse
    // failures so the user sees the raw stamp rather than "Invalid
    // Date".
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleDateString();
  }

  function rowAddress(filter: { baseIdentityId: string; suffix: string }): string {
    return composeTaggedAddress(emailFor(filter.baseIdentityId), filter.suffix);
  }
  function dismissalAddress(d: TaggedAddressDismissal): string {
    return composeTaggedAddress(emailFor(d.base_identity_id), d.suffix);
  }
</script>

{#if !capabilityAdvertised}
  <p class="muted" data-testid="tagged-addresses-unavailable">
    {t('settings.taggedAddresses.unavailable')}
  </p>
{:else}
  <p class="intro" data-testid="tagged-addresses-intro">
    {t('settings.taggedAddresses.intro')}
  </p>

  <section class="block">
    <header class="block-header">
      <h3>{t('settings.taggedAddresses.filtersHeading')}</h3>
      <div class="block-actions">
        {#if capApproaching}
          <span class="cap-notice" data-testid="tagged-addresses-cap-notice">
            {t('settings.taggedAddresses.capNotice', { used: filterCount, cap: FILTER_CAP })}
          </span>
        {/if}
        <Button
          variant="primary"
          compact
          onclick={openCreate}
          disabled={capReached}
          testid="tagged-addresses-add"
        >
          {t('settings.taggedAddresses.addFilter')}
        </Button>
      </div>
    </header>

    {#if capReached}
      <p class="cap-reached" role="alert" data-testid="tagged-addresses-cap-reached">
        {t('settings.taggedAddresses.capReached', { cap: FILTER_CAP })}
      </p>
    {/if}

    {#if taggedAddressStore.status === 'loading' && sortedFilters.length === 0}
      <p class="muted" data-testid="tagged-addresses-loading">
        {t('settings.taggedAddresses.loading')}
      </p>
    {:else if taggedAddressStore.status === 'error'}
      <p class="error" role="alert" data-testid="tagged-addresses-load-error">
        {taggedAddressStore.error ??
          t('settings.taggedAddresses.toast.failed', { error: 'load' })}
      </p>
    {:else if sortedFilters.length === 0}
      <p class="muted empty" data-testid="tagged-addresses-empty">
        {t('settings.taggedAddresses.empty')}
      </p>
    {:else}
      <table class="grid" data-testid="tagged-addresses-filters-table">
        <thead>
          <tr>
            <th scope="col">{t('settings.taggedAddresses.col.address')}</th>
            <th scope="col">{t('settings.taggedAddresses.col.action')}</th>
            <th scope="col">{t('settings.taggedAddresses.col.label')}</th>
            <th scope="col">{t('settings.taggedAddresses.col.created')}</th>
            <th scope="col" class="actions-col">{t('settings.taggedAddresses.col.actions')}</th>
          </tr>
        </thead>
        <tbody>
          {#each sortedFilters as filter (filter.id)}
            <tr data-testid={`tagged-addresses-filter-row-${filter.id}`}>
              <td class="address mono">{rowAddress(filter)}</td>
              <td>{t(actionLabelKey(filter.action), { label: filter.labelName })}</td>
              <td>{filter.labelName}</td>
              <td>{formatDate(filter.createdAt)}</td>
              <td class="row-actions">
                <Button
                  variant="secondary"
                  compact
                  onclick={() => openEdit(filter)}
                  ariaLabel={t('settings.taggedAddresses.row.editAria', {
                    address: rowAddress(filter),
                  })}
                  testid={`tagged-addresses-filter-edit-${filter.id}`}
                >
                  {t('settings.taggedAddresses.row.edit')}
                </Button>
                <Button
                  variant="secondary"
                  compact
                  onclick={() => void onConvert(filter)}
                  ariaLabel={t('settings.taggedAddresses.row.convertAria', {
                    address: rowAddress(filter),
                  })}
                  testid={`tagged-addresses-filter-convert-${filter.id}`}
                >
                  {t('settings.taggedAddresses.row.convert')}
                </Button>
                <Button
                  variant="danger"
                  compact
                  onclick={() => void onDelete(filter)}
                  ariaLabel={t('settings.taggedAddresses.row.deleteAria', {
                    address: rowAddress(filter),
                  })}
                  testid={`tagged-addresses-filter-delete-${filter.id}`}
                >
                  {t('settings.taggedAddresses.row.delete')}
                </Button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>

  {#if dismissalCount > 0 || dismissalsOpen}
    <section class="block">
      <header class="block-header">
        <button
          type="button"
          class="block-toggle"
          aria-expanded={dismissalsOpen}
          aria-controls="tagged-addresses-dismissals-body"
          onclick={() => (dismissalsOpen = !dismissalsOpen)}
          data-testid="tagged-addresses-dismissals-toggle"
        >
          <span class="caret" aria-hidden="true">{dismissalsOpen ? '▾' : '▸'}</span>
          <h3>{t('settings.taggedAddresses.dismissalsHeading')}</h3>
          <span class="count-chip">
            {t('settings.taggedAddresses.dismissalsCount', { count: dismissalCount })}
          </span>
        </button>
        {#if dismissalApproaching}
          <span class="cap-notice" data-testid="tagged-addresses-dismissals-cap-notice">
            {t('settings.taggedAddresses.dismissalsCapNotice', {
              used: dismissalCount,
              cap: DISMISSAL_CAP,
            })}
          </span>
        {/if}
      </header>

      {#if dismissalsOpen}
        <div id="tagged-addresses-dismissals-body">
          <p class="intro">{t('settings.taggedAddresses.dismissalsIntro')}</p>
          {#if sortedDismissals.length === 0}
            <p class="muted">{t('settings.taggedAddresses.dismissalsEmpty')}</p>
          {:else}
            <table class="grid" data-testid="tagged-addresses-dismissals-table">
              <thead>
                <tr>
                  <th scope="col">{t('settings.taggedAddresses.col.address')}</th>
                  <th scope="col">{t('settings.taggedAddresses.col.dismissedAt')}</th>
                  <th scope="col" class="actions-col">
                    {t('settings.taggedAddresses.col.actions')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {#each sortedDismissals as dismissal (dismissal.base_identity_id + '/' + dismissal.suffix)}
                  <tr
                    data-testid={`tagged-addresses-dismissal-row-${dismissal.base_identity_id}-${dismissal.suffix}`}
                  >
                    <td class="address mono">{dismissalAddress(dismissal)}</td>
                    <td>{formatDate(dismissal.dismissed_at)}</td>
                    <td class="row-actions">
                      <Button
                        variant="secondary"
                        compact
                        onclick={() => void onUndismiss(dismissal)}
                        ariaLabel={t('settings.taggedAddresses.row.undismissAria', {
                          address: dismissalAddress(dismissal),
                        })}
                        testid={`tagged-addresses-dismissal-undismiss-${dismissal.base_identity_id}-${dismissal.suffix}`}
                      >
                        {t('settings.taggedAddresses.row.undismiss')}
                      </Button>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      {/if}
    </section>
  {/if}

  {#if dialogOpen}
    <TaggedAddressFilterDialog filter={dialogFilter} onclose={closeDialog} />
  {/if}
{/if}

<style>
  .intro {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .block {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    margin-top: var(--spacing-04);
  }

  .block-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    flex-wrap: wrap;
  }
  .block-header h3 {
    margin: 0;
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    color: var(--text-secondary);
    flex: 0 0 auto;
  }
  .block-actions {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  .block-toggle {
    background: transparent;
    border: 0;
    padding: 0;
    color: inherit;
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    cursor: pointer;
  }
  .block-toggle:hover h3 {
    color: var(--text-primary);
  }
  .caret {
    color: var(--text-helper);
    width: 1em;
    text-align: center;
  }

  .count-chip {
    background: var(--layer-02);
    color: var(--text-secondary);
    padding: 2px var(--spacing-03);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
  }

  .cap-notice {
    color: var(--support-warning);
    font-size: var(--type-body-compact-01-size);
  }
  .cap-reached {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .empty {
    padding: var(--spacing-04) 0;
  }
  .muted {
    color: var(--text-helper);
    font-style: italic;
    margin: 0;
  }
  .error {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  .grid {
    width: 100%;
    border-collapse: collapse;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    overflow: hidden;
  }
  .grid th,
  .grid td {
    padding: var(--spacing-03) var(--spacing-04);
    text-align: left;
    vertical-align: middle;
    border-bottom: 1px solid var(--border-subtle-01);
    font-size: var(--type-body-compact-01-size);
  }
  .grid th {
    color: var(--text-secondary);
    font-weight: 600;
    background: var(--layer-02);
  }
  .grid tbody tr:last-child td {
    border-bottom: 0;
  }
  .grid .address {
    word-break: break-all;
  }
  .actions-col {
    width: 1%;
    white-space: nowrap;
  }
  .row-actions {
    display: flex;
    gap: var(--spacing-02);
    justify-content: flex-end;
  }

</style>
