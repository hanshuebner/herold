<script lang="ts">
  /**
   * Per-message tagged-address banner (REQ-MAIL-12c).
   *
   * Renders above the thread reader's message column when the latest
   * delivered message's X-Herold-Recipient carries a +suffix that has
   * neither an existing managed filter (REQ-TAG-70) nor a dismissal
   * (REQ-TAG-60) for the matching (base_identity, suffix). Four primary
   * actions:
   *   1. Label — create filter with action `label`.
   *   2. Label + archive — action `label_archive`.
   *   3. Label + archive + mark read — action `label_archive_read`.
   *   4. Ignore future mail to +suffix — write a dismissal row.
   *
   * Filter-creating actions open the shared label-create dialog
   * pre-populated with the suffix capitalised; the user MAY edit the
   * label name before commit. Dismiss is fire-and-forget.
   *
   * The banner is reactive: once a filter is created or a dismissal
   * recorded, the store's local cache updates immediately and the
   * derived `visibility` becomes null, hiding the banner without a
   * round-trip. The Identity state-change push (REQ-TAG-72) keeps the
   * cache in sync across devices.
   *
   * Capability gating: the banner short-circuits when
   * https://netzhansa.com/jmap/tagged-addresses is absent from the
   * session, so SPAs against a server without the feature compile and
   * render normally.
   */

  import type { Email } from './types';
  import { mail } from './store.svelte';
  import { taggedAddressStore } from './tagged-address-store.svelte';
  import { bannerVisibility, capitalise } from './tagged-address';
  import { labelDialog } from '../dialog/label-dialog.svelte';
  import { toast } from '../toast/toast.svelte';
  import { t } from '../i18n/i18n.svelte';
  import { untrack } from 'svelte';
  import Button from '@herold/design-system/Button.svelte';

  interface Props {
    /** The message whose X-Herold-Recipient drives the banner. */
    email: Email | null | undefined;
  }
  let { email }: Props = $props();

  // Kick the store's lazy load once the capability is available. The
  // load is idempotent and cached; subsequent state-change pushes drive
  // re-fetches via the sync handler registered inside the store.
  $effect(() => {
    if (taggedAddressStore.capabilityAdvertised) {
      untrack(() => {
        void taggedAddressStore.load();
      });
    }
  });

  let headerValue = $derived(email?.['header:X-Herold-Recipient:asText'] ?? null);

  let visibility = $derived(
    bannerVisibility({
      capabilityAdvertised: taggedAddressStore.capabilityAdvertised,
      headerValue,
      identities: mail.identities.values(),
      filters: taggedAddressStore.filterRecords,
      dismissals: taggedAddressStore.dismissalRecords,
    }),
  );

  /** Open the label-create dialog and write the filter on confirm. */
  async function applyFilter(
    action: 'label' | 'label_archive' | 'label_archive_read',
  ): Promise<void> {
    const v = visibility;
    if (!v) return;
    const titleKey =
      action === 'label'
        ? 'mail.taggedBanner.dialog.title.label'
        : action === 'label_archive'
          ? 'mail.taggedBanner.dialog.title.labelArchive'
          : 'mail.taggedBanner.dialog.title.labelArchiveRead';
    const result = await labelDialog.open({
      title: t(titleKey, { suffix: v.suffix }),
      defaultName: capitalise(v.suffix),
      confirmLabel: t('mail.taggedBanner.dialog.confirm'),
      // REQ-MAIL-12c: the suffix-capitalised default name is a
      // suggestion the user MAY accept verbatim. Bypass the shared
      // dialog's edit-mode dirty check so the confirm button is
      // enabled the moment the dialog opens with a non-empty name.
      requireDirty: false,
    });
    if (!result) return;
    try {
      await taggedAddressStore.createFilter({
        baseIdentityId: v.baseIdentityId,
        suffix: v.suffix,
        action,
        labelName: result.name,
      });
      toast.show({
        message: t('mail.taggedBanner.toast.filterCreated', { suffix: v.suffix }),
      });
    } catch (err) {
      toast.show({
        message:
          err instanceof Error && err.message
            ? err.message
            : t('mail.taggedBanner.toast.filterFailed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  async function dismiss(): Promise<void> {
    const v = visibility;
    if (!v) return;
    try {
      await taggedAddressStore.dismiss(v.baseIdentityId, v.suffix);
      toast.show({
        message: t('mail.taggedBanner.toast.dismissed', { suffix: v.suffix }),
      });
    } catch (err) {
      toast.show({
        message:
          err instanceof Error && err.message
            ? err.message
            : t('mail.taggedBanner.toast.dismissFailed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }
</script>

{#if visibility}
  <section
    class="tagged-banner"
    aria-label={t('mail.taggedBanner.heading', { suffix: visibility.suffix })}
    data-testid="tagged-address-banner"
  >
    <div class="text">
      <strong>{t('mail.taggedBanner.heading', { suffix: visibility.suffix })}</strong>
      <span>{t('mail.taggedBanner.lead')}</span>
    </div>
    <div class="actions">
      <Button
        variant="primary"
        compact
        testid="tagged-banner-label"
        onclick={() => void applyFilter('label')}
      >
        {t('mail.taggedBanner.action.label')}
      </Button>
      <Button
        variant="primary"
        compact
        testid="tagged-banner-label-archive"
        onclick={() => void applyFilter('label_archive')}
      >
        {t('mail.taggedBanner.action.labelArchive')}
      </Button>
      <Button
        variant="primary"
        compact
        testid="tagged-banner-label-archive-read"
        onclick={() => void applyFilter('label_archive_read')}
      >
        {t('mail.taggedBanner.action.labelArchiveRead')}
      </Button>
      <Button
        variant="secondary"
        compact
        testid="tagged-banner-dismiss"
        onclick={() => void dismiss()}
      >
        {t('mail.taggedBanner.action.dismiss', { suffix: visibility.suffix })}
      </Button>
    </div>
  </section>
{/if}

<style>
  .tagged-banner {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    padding: var(--spacing-04) var(--spacing-05);
    margin: var(--spacing-04) var(--spacing-05) 0;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-left: 4px solid var(--interactive);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }

  .tagged-banner .text {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .tagged-banner strong {
    color: var(--text-primary);
    font-weight: 600;
  }

  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-03);
  }
</style>
