<script lang="ts">
  /**
   * Contacts duplicates review view (REQ-CONT-90, 91, 94).
   *
   * Fetches all contacts (paged Contact/query), clusters them client-side
   * by shared email, shared normalised phone, or close display-name match,
   * then shows candidate clusters for the user to confirm or dismiss.
   *
   * Dismissed pairs are remembered in localStorage per principal so the same
   * non-duplicate is not re-flagged on the next visit (REQ-CONT-91).
   *
   * "Merge" navigates to #/contacts/merge?ids=id1,id2,...
   */

  import { jmap } from '../lib/jmap/client';
  import { Capability } from '../lib/jmap/types';
  import { auth } from '../lib/auth/auth.svelte';
  import { router } from '../lib/router/router.svelte';
  import { t } from '../lib/i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';
  import ContactAvatar from '../lib/contacts/ContactAvatar.svelte';
  import {
    extractCandidate,
    clusterDuplicates,
    loadDismissedPairs,
    dismissPair,
    type DuplicateCluster,
    type ContactCandidate,
  } from '../lib/contacts/dedup';
  import { extractPhotoFromCard } from '../lib/contacts/jscontact';

  const FETCH_LIMIT = 500; // fetch at most 500 contacts at a time

  /** Properties needed for dedup — fetched narrow, not the full Card. */
  const DEDUP_PROPERTIES = ['id', 'name', 'emails', 'phones'];

  let status = $state<'idle' | 'loading' | 'ready' | 'error'>('idle');
  let clusters = $state<DuplicateCluster[]>([]);
  let dismissed = $state<Set<string>>(new Set());

  $effect(() => {
    if (status === 'idle') void runDetection();
  });

  async function runDetection(): Promise<void> {
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) { status = 'error'; return; }

    const principalId = auth.principalId ?? 'unknown';
    dismissed = loadDismissedPairs(principalId);

    status = 'loading';
    try {
      const candidates = await fetchAllCandidates(accountId);
      clusters = clusterDuplicates(candidates, dismissed);
      status = 'ready';
    } catch {
      status = 'error';
    }
  }

  async function fetchAllCandidates(accountId: string): Promise<ContactCandidate[]> {
    const all: ContactCandidate[] = [];
    let position = 0;
    let total: number | null = null;

    // Page through all contacts.
    while (true) {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Contact/query',
          {
            accountId,
            sort: [{ property: 'displayName', isAscending: true }],
            position,
            limit: FETCH_LIMIT,
            calculateTotal: true,
          },
          [Capability.Contacts],
        );
        b.call(
          'Contact/get',
          {
            accountId,
            '#ids': q.ref('/ids'),
            properties: DEDUP_PROPERTIES,
          },
          [Capability.Contacts],
        );
      });

      const qResp = responses[0];
      const gResp = responses[1];
      if (!qResp || qResp[0] === 'error' || !gResp || gResp[0] === 'error') {
        throw new Error('fetch-failed');
      }

      const qArgs = qResp[1] as { total?: number; ids?: string[] };
      const gArgs = gResp[1] as { list?: unknown[] };

      if (typeof qArgs.total === 'number') total = qArgs.total;

      const batch = (gArgs.list ?? []).flatMap((card) => {
        if (typeof card !== 'object' || card === null) return [];
        return [extractCandidate(card as Record<string, unknown>)];
      });
      all.push(...batch);

      position += batch.length;
      if (batch.length < FETCH_LIMIT || (total !== null && all.length >= total)) break;
    }
    return all;
  }

  function handleDismiss(cluster: DuplicateCluster): void {
    const principalId = auth.principalId ?? 'unknown';
    // Dismiss every unique pair in the cluster.
    for (let i = 0; i < cluster.contacts.length; i++) {
      for (let j = i + 1; j < cluster.contacts.length; j++) {
        dismissed = dismissPair(principalId, cluster.contacts[i]!.id, cluster.contacts[j]!.id);
      }
    }
    // Remove from displayed clusters.
    clusters = clusters.filter((c) => c !== cluster);
  }

  function handleMerge(cluster: DuplicateCluster): void {
    const ids = cluster.contacts.map((c) => c.id).join(',');
    router.navigate(`/contacts/merge?ids=${encodeURIComponent(ids)}`);
  }

  function reasonLabel(reason: string): string {
    switch (reason) {
      case 'email': return t('contacts.duplicates.reason.email');
      case 'phone': return t('contacts.duplicates.reason.phone');
      case 'name':  return t('contacts.duplicates.reason.name');
      default:      return reason;
    }
  }

  function photoBlobId(c: ContactCandidate): string | null {
    return extractPhotoFromCard(c.raw)?.blobId ?? null;
  }
</script>

<div class="duplicates-view">
  <div class="toolbar">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/contacts')}
    >
      {t('contacts.list.title')}
    </button>
  </div>

  <h1 class="page-title">{t('contacts.duplicates.title')}</h1>

  {#if status === 'loading'}
    <p class="state-msg">{t('contacts.duplicates.loading')}</p>
  {:else if status === 'error'}
    <p class="state-msg error">{t('contacts.duplicates.error')}</p>
  {:else if status === 'ready' && clusters.length === 0}
    <p class="state-msg">{t('contacts.duplicates.empty')}</p>
  {:else if status === 'ready'}
    <ul class="cluster-list" role="list">
      {#each clusters as cluster, clusterIdx (clusterIdx)}
        <li class="cluster-card">
          <div class="cluster-reasons">
            {#each cluster.reasons as reason}
              <span class="reason-badge">{reasonLabel(reason)}</span>
            {/each}
          </div>

          <div class="cluster-contacts">
            {#each cluster.contacts as contact (contact.id)}
              <div class="contact-row">
                <ContactAvatar
                  blobId={photoBlobId(contact)}
                  fallbackInitial={contact.displayName.charAt(0).toUpperCase() || '?'}
                  displayName={contact.displayName}
                  size={36}
                />
                <div class="contact-info">
                  <span class="contact-name">{contact.displayName || t('contacts.list.unnamed')}</span>
                  {#if contact.emails.length > 0}
                    <span class="contact-secondary">{contact.emails[0]}</span>
                  {:else if contact.phones.length > 0}
                    <span class="contact-secondary">{contact.phones[0]}</span>
                  {/if}
                </div>
              </div>
            {/each}
          </div>

          <div class="cluster-actions">
            <Button
              variant="primary"
              compact
              onclick={() => handleMerge(cluster)}
            >
              {t('contacts.duplicates.merge')}
            </Button>
            <Button
              variant="secondary"
              compact
              onclick={() => handleDismiss(cluster)}
            >
              {t('contacts.duplicates.dismiss')}
            </Button>
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .duplicates-view {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: auto;
    padding: var(--spacing-05);
    box-sizing: border-box;
  }

  .toolbar {
    margin-bottom: var(--spacing-04);
  }

  .back-btn {
    color: var(--interactive);
    font-weight: 500;
    font-size: var(--type-body-compact-01-size);
    padding: 0;
    background: none;
    border: none;
    cursor: pointer;
  }

  .back-btn::before {
    content: '\2190\00a0';
  }

  .page-title {
    font-size: var(--type-heading-04-size, 1.25rem);
    font-weight: 600;
    color: var(--text-primary);
    margin: 0 0 var(--spacing-05);
  }

  .state-msg {
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
    margin: 0;
  }

  .state-msg.error {
    color: var(--support-error);
  }

  .cluster-list {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    max-width: 560px;
  }

  .cluster-card {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-05);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .cluster-reasons {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
  }

  .reason-badge {
    font-size: var(--type-label-01-size, 0.7rem);
    background: var(--layer-02);
    color: var(--text-helper);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    padding: 1px var(--spacing-02);
    line-height: 1.4;
  }

  .cluster-contacts {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .contact-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
  }

  .contact-info {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  .contact-name {
    font-size: var(--type-body-01-size);
    font-weight: 500;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .contact-secondary {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .cluster-actions {
    display: flex;
    gap: var(--spacing-03);
  }
</style>
