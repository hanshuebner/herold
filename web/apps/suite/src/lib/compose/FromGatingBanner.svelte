<script lang="ts">
  /**
   * Inline explanation shown under the From row when Send is blocked.
   *
   * For external-submission conditions the banner states the actual
   * condition, links to the identity's submission settings and offers the
   * existing connection test (POST /api/v1/identities/{id}/submission/test).
   * The test refreshes the submission handle, so the gating re-derives and
   * Send re-enables once the relay accepts the credentials.
   */
  import { t } from '../i18n/i18n.svelte';
  import { testSubmission } from '../api/identity-submission';
  import { submissionStore } from '../identities/identity-submission.svelte';
  import {
    fromGatingMessageKey,
    isExternalSubmissionReason,
    type FromGatingReason,
  } from './from-picker';

  interface Props {
    identityId: string | null;
    reason: FromGatingReason;
    class?: string;
  }
  let { identityId, reason, class: extraClass = '' }: Props = $props();

  let canRetest = $derived(
    identityId !== null &&
      isExternalSubmissionReason(reason) &&
      reason !== 'external-setup-needed',
  );
  let showSettings = $derived(identityId !== null && isExternalSubmissionReason(reason));
  let testing = $state(false);
  let result = $state<{ ok: boolean; detail: string } | null>(null);

  async function retest(): Promise<void> {
    if (!identityId || testing) return;
    testing = true;
    result = null;
    try {
      result = await testSubmission(identityId);
      await submissionStore.forIdentity(identityId).refresh();
    } finally {
      testing = false;
    }
  }
</script>

<div class="from-gating-banner {extraClass}" role="status" data-testid="from-gating-banner">
  <span data-testid="from-gating-message">{t(fromGatingMessageKey(reason))}</span>
  {#if showSettings}
    <a
      class="from-gating-link"
      href={`#/settings/identities/${identityId}`}
      data-testid="from-gating-settings-link"
    >{t('compose.from.sendDisabled.settings')}</a>
  {/if}
  {#if canRetest}
    <button
      type="button"
      class="from-gating-test"
      disabled={testing}
      onclick={retest}
      data-testid="from-gating-test"
    >{testing ? t('compose.from.sendDisabled.testing') : t('compose.from.sendDisabled.test')}</button>
  {/if}
  {#if result}
    <span class="from-gating-result" data-testid="from-gating-result">
      {result.ok
        ? t('compose.from.sendDisabled.testOk')
        : t('compose.from.sendDisabled.testFailed', { detail: result.detail })}
    </span>
  {/if}
</div>

<style>
  .from-gating-banner {
    margin: 0;
    padding: var(--spacing-02) var(--spacing-05);
    background: color-mix(in srgb, var(--support-warning) 10%, transparent);
    border-left: 3px solid var(--support-warning);
    color: color-mix(in srgb, var(--support-warning) 90%, var(--text-primary));
    font-size: var(--type-body-compact-01-size);
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--spacing-02) var(--spacing-04);
  }
  .from-gating-link,
  .from-gating-test {
    color: inherit;
    text-decoration: underline;
  }
  .from-gating-test {
    font: inherit;
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
  }
  .from-gating-test:disabled {
    opacity: 0.6;
    cursor: default;
  }
</style>
