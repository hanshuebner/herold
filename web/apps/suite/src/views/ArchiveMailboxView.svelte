<script lang="ts">
  /**
   * Member-scoped read-only mailing-list archive view (REQ-MLIST-73,
   * issue #187 Stage S4 -- "restricted web mailer"). Renders one shared
   * account's archive mailbox: a message list with search, and a reading
   * pane. There is deliberately no delete, move, expunge, flag/keyword,
   * drag-to-move, or mark-read/unread affordance anywhere in this view --
   * the server grant is `lookup+read` without the Seen right, so any of
   * those would either be silently refused or surface as an error toast
   * on a routine click. See lib/archive/archive-store.svelte.ts's header
   * comment for why this view does not reuse MailView / the mail store.
   */
  import { archive } from '../lib/archive/archive-store.svelte';
  import { router } from '../lib/router/router.svelte';
  import { t, localeTag } from '../lib/i18n/i18n.svelte';
  import { jmap } from '../lib/jmap/client';
  import HtmlBody from '../lib/mail/HtmlBody.svelte';
  import { sanitizeHtml } from '../lib/mail/sanitize';
  import type { Email } from '../lib/mail/types';

  interface Props {
    accountId: string;
    mailboxId?: string;
  }
  let { accountId, mailboxId }: Props = $props();

  let searchText = $state('');
  let selectedId = $state<string | null>(null);

  let mailbox = $derived(archive.findMailbox(accountId, mailboxId));

  $effect(() => {
    void archive.loadSharedAccounts();
  });

  // Re-runs when the resolved mailbox becomes available or the account
  // in the URL changes. untrack-free: loadEmails only reads its own
  // arguments and writes archive.emails, so this effect does not loop
  // back into itself.
  $effect(() => {
    const mb = mailbox;
    const acct = accountId;
    if (!mb) return;
    void archive.loadEmails(acct, mb.id, '');
  });

  function runSearch(e: SubmitEvent): void {
    e.preventDefault();
    const mb = mailbox;
    if (!mb) return;
    void archive.loadEmails(accountId, mb.id, searchText);
  }

  async function openEmail(id: string): Promise<void> {
    selectedId = id;
    await archive.openEmail(accountId, id);
  }

  function closeReading(): void {
    selectedId = null;
    archive.closeReading();
  }

  function senderLabel(email: Email): string {
    const a = email.from?.[0];
    if (!a) return t('msg.noSender');
    return a.name?.trim() || a.email;
  }

  function formatDate(iso: string): string {
    const d = new Date(iso);
    const now = new Date();
    const sameYear = d.getFullYear() === now.getFullYear();
    const opts: Intl.DateTimeFormatOptions = sameYear
      ? { month: 'short', day: 'numeric' }
      : { month: 'short', day: 'numeric', year: 'numeric' };
    return d.toLocaleDateString(localeTag(), opts);
  }

  function isUnread(email: Email): boolean {
    return !email.keywords.$seen;
  }

  function plainTextBody(email: Email): string {
    const part = email.textBody?.[0];
    if (!part?.partId) return '';
    return email.bodyValues?.[part.partId]?.value ?? '';
  }

  function htmlBodyValue(email: Email): string | null {
    const part = email.htmlBody?.[0];
    if (!part?.partId) return null;
    const raw = email.bodyValues?.[part.partId]?.value;
    return raw ?? null;
  }

  function attachmentUrl(email: Email, blobId: string, name: string | null, type: string): string | null {
    return jmap.downloadUrl({ accountId, blobId, type, name: name ?? 'attachment' });
  }
</script>

<div class="archive-view">
  {#if archive.status === 'loading' && archive.sharedAccounts.length === 0}
    <div class="state-msg">{t('common.loading')}</div>
  {:else if archive.status === 'error'}
    <div class="state-msg error" role="alert">{archive.errorMessage}</div>
  {:else if !mailbox}
    <div class="state-msg">{t('archive.notFound')}</div>
  {:else}
    <div class="archive-layout">
      <div class="archive-list-pane" class:hidden-on-narrow={selectedId !== null}>
        <div class="archive-header">
          <button
            type="button"
            class="back-btn"
            onclick={() => router.navigate('/mail')}
            aria-label={t('archive.backToMail')}
          >
            {t('archive.backToMail')}
          </button>
          <h1 class="archive-title">{mailbox.name}</h1>
          <p class="archive-subtitle">{t('archive.readOnlyNotice')}</p>
        </div>

        <form class="search-form" onsubmit={runSearch}>
          <input
            type="text"
            class="search-input"
            placeholder={t('archive.searchPlaceholder')}
            bind:value={searchText}
            aria-label={t('archive.searchPlaceholder')}
          />
          <button type="submit" class="search-btn">{t('archive.search')}</button>
        </form>

        {#if archive.listStatus === 'loading'}
          <div class="state-msg">{t('common.loading')}</div>
        {:else if archive.listStatus === 'error'}
          <div class="state-msg error" role="alert">{archive.listErrorMessage}</div>
        {:else if archive.emails.length === 0}
          <div class="state-msg">{t('archive.empty')}</div>
        {:else}
          <ul class="message-list">
            {#each archive.emails as email (email.id)}
              <li>
                <button
                  type="button"
                  class="message-row"
                  class:selected={selectedId === email.id}
                  class:unread={isUnread(email)}
                  onclick={() => void openEmail(email.id)}
                >
                  <span class="sender">{senderLabel(email)}</span>
                  <span class="subject">{email.subject || t('msg.noSubject')}</span>
                  <span class="preview">{email.preview}</span>
                  <span class="date">{formatDate(email.receivedAt)}</span>
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      </div>

      <div class="archive-reading-pane" class:hidden-on-narrow={selectedId === null}>
        {#if selectedId === null}
          <div class="state-msg reading-empty">{t('archive.selectMessage')}</div>
        {:else if archive.readingStatus === 'loading'}
          <div class="state-msg">{t('common.loading')}</div>
        {:else if archive.readingStatus === 'error'}
          <div class="state-msg error" role="alert">{t('archive.error.loadMessageFailed')}</div>
        {:else if archive.reading}
          {@const email = archive.reading}
          <div class="reading-header">
            <button type="button" class="back-btn narrow-only" onclick={closeReading}>
              {t('archive.backToList')}
            </button>
            <h2 class="reading-subject">{email.subject || t('msg.noSubject')}</h2>
            <div class="reading-meta">
              <span class="reading-from">{senderLabel(email)}</span>
              <span class="reading-date">{formatDate(email.receivedAt)}</span>
            </div>
          </div>
          <div class="reading-body">
            {#if htmlBodyValue(email)}
              <HtmlBody html={sanitizeHtml(htmlBodyValue(email) ?? '', { loadImages: true })} loadImages={true} />
            {:else}
              <pre class="plain-body">{plainTextBody(email)}</pre>
            {/if}
          </div>
          {#if email.attachments && email.attachments.length > 0}
            <div class="attachments">
              <h3 class="attachments-title">{t('archive.attachments')}</h3>
              <ul>
                {#each email.attachments as part (part.partId ?? part.blobId)}
                  {#if part.blobId}
                    {@const url = attachmentUrl(email, part.blobId, part.name, part.type)}
                    {#if url}
                      <li><a href={url}>{part.name ?? part.type}</a></li>
                    {/if}
                  {/if}
                {/each}
              </ul>
            </div>
          {/if}
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .archive-view {
    height: 100%;
    display: flex;
    flex-direction: column;
  }

  .state-msg {
    padding: var(--spacing-06);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }
  .state-msg.error {
    color: var(--support-error);
  }

  .archive-layout {
    display: flex;
    height: 100%;
    min-height: 0;
  }

  .archive-list-pane {
    width: 380px;
    flex-shrink: 0;
    border-right: 1px solid var(--border-subtle-01);
    display: flex;
    flex-direction: column;
    overflow-y: auto;
  }

  .archive-reading-pane {
    flex: 1;
    min-width: 0;
    overflow-y: auto;
    padding: var(--spacing-06);
  }

  @media (max-width: 800px) {
    .archive-list-pane {
      width: 100%;
    }
    .hidden-on-narrow {
      display: none;
    }
  }

  .archive-header {
    padding: var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .back-btn {
    background: none;
    border: none;
    color: var(--interactive);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    padding: 0 0 var(--spacing-02);
  }

  .archive-title {
    font-size: var(--type-heading-02-size);
    font-weight: var(--type-heading-02-weight);
    margin: 0;
    color: var(--text-primary);
  }

  .archive-subtitle {
    margin: var(--spacing-02) 0 0;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }

  .search-form {
    display: flex;
    gap: var(--spacing-02);
    padding: var(--spacing-04) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .search-input {
    flex: 1;
    padding: var(--spacing-02) var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .search-btn {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    cursor: pointer;
  }

  .message-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .message-row {
    display: flex;
    flex-direction: column;
    width: 100%;
    text-align: left;
    gap: 2px;
    padding: var(--spacing-03) var(--spacing-05);
    border: none;
    border-bottom: 1px solid var(--border-subtle-01);
    background: none;
    cursor: pointer;
    color: var(--text-primary);
  }
  .message-row:hover {
    background: var(--layer-02);
  }
  .message-row.selected {
    background: var(--layer-selected, var(--layer-02));
  }
  .message-row.unread .sender,
  .message-row.unread .subject {
    font-weight: 600;
  }

  .sender {
    font-size: var(--type-body-compact-01-size);
  }
  .subject {
    font-size: var(--type-body-compact-01-size);
  }
  .preview {
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .date {
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
  }

  .reading-empty {
    color: var(--text-helper);
  }

  .reading-header {
    margin-bottom: var(--spacing-05);
  }
  .narrow-only {
    display: none;
  }
  @media (max-width: 800px) {
    .narrow-only {
      display: inline-block;
    }
  }

  .reading-subject {
    font-size: var(--type-heading-02-size);
    margin: 0 0 var(--spacing-02);
    color: var(--text-primary);
  }

  .reading-meta {
    display: flex;
    gap: var(--spacing-04);
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }

  .plain-body {
    white-space: pre-wrap;
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    color: var(--text-primary);
  }

  .attachments {
    margin-top: var(--spacing-05);
    border-top: 1px solid var(--border-subtle-01);
    padding-top: var(--spacing-04);
  }
  .attachments-title {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-02);
  }
  .attachments ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
</style>
