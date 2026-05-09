<script lang="ts">
  /**
   * ImageProcessingForm — surfaces the per-principal external-image
   * internalize backlog count from the JMAP session capability
   * `urn:netzhansa:params:jmap:internalize-status` (REQ-EXTIMG-BG-32).
   *
   * The capability shape, as emitted by the herold backend, is:
   *   { pending_messages: uint64, as_of: rfc3339 }
   * (REQ-EXTIMG-BG-21 also reserves `total_messages`; the SPA only reads
   * what is actually on the wire today.)
   *
   * Hidden when `pending_messages === 0` — the steady state where most
   * users will never see this section. The capability is computed at
   * session bootstrap; the count refreshes whenever the bootstrap re-runs
   * (e.g. on login or auth reset).
   */
  import { auth } from '../../lib/auth/auth.svelte';
  import { Capability } from '../../lib/jmap/types';
  import { t } from '../../lib/i18n/i18n.svelte';
  import { relativeTimeAgo } from '../../lib/mail/relative-time';

  interface InternalizeStatusCapability {
    pending_messages?: number;
    as_of?: string;
  }

  let cap = $derived.by<InternalizeStatusCapability | undefined>(() => {
    return auth.session?.capabilities[Capability.HeroldInternalizeStatus] as
      | InternalizeStatusCapability
      | undefined;
  });

  let pending = $derived<number>(cap?.pending_messages ?? 0);

  let asOfDisplay = $derived.by<string>(() => {
    const raw = cap?.as_of;
    if (!raw) return '';
    const parsed = new Date(raw);
    if (Number.isNaN(parsed.getTime())) return raw;
    return relativeTimeAgo(parsed);
  });
</script>

{#if pending > 0}
  <section class="image-processing">
    <h3>{t('settings.privacy.imageProcessing.heading')}</h3>
    <p class="pending">
      {pending === 1
        ? t('settings.privacy.imageProcessing.pending', { count: pending })
        : t('settings.privacy.imageProcessing.pending.other', {
            count: pending,
          })}
    </p>
    {#if asOfDisplay}
      <p class="as-of">
        {t('settings.privacy.imageProcessing.asOf', { time: asOfDisplay })}
      </p>
    {/if}
  </section>
{/if}

<style>
  .image-processing {
    padding: var(--spacing-04) 0;
    border-bottom: 1px solid var(--border-subtle-01);
  }
  h3 {
    font-size: var(--type-heading-01-size);
    font-weight: 600;
    margin: 0 0 var(--spacing-02);
    color: var(--text-primary);
  }
  .pending {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }
  .as-of {
    margin: var(--spacing-01) 0 0;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }
</style>
