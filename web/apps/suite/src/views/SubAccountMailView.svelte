<script lang="ts">
  /**
   * Scoped message list + reading pane for a drilled-into sub-account
   * (issue #212, REQ-MAIL-SUB-02/04/05). Rendered at /account/<id> (that
   * account's Inbox) and /account/<id>/folder/<mailboxId> (any other
   * mailbox in its tree) -- the scoped sidebar (AccountSidebar.svelte)
   * is the only source of the folder navigation into this view.
   *
   * Modeled closely on ArchiveMailboxView.svelte's list+read layout, but
   * unlike that read-only grant-scoped view, a sub-account is fully
   * owned by the caller: messages are marked seen on open
   * (lib/mail/sub-accounts.svelte.ts's openEmail), and the header
   * Compose button defaults the From identity to this account's own
   * Identity (REQ-MAIL-SUB-05).
   */
  import { subAccounts } from '../lib/mail/sub-accounts.svelte';
  import { compose } from '../lib/compose/compose.svelte';
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

  let entry = $derived(subAccounts.find(accountId));

  let effectiveMailboxId = $derived(
    mailboxId ?? entry?.mailboxes.find((m) => m.role === 'inbox')?.id ?? null,
  );

  let selectedId = $state<string | null>(null);

  $effect(() => {
    void subAccounts.load();
  });

  // Re-runs when the resolved mailbox becomes available or the route
  // changes. untrack-free: loadEmails only reads its own arguments and
  // writes subAccounts state, so this does not loop back into itself.
  $effect(() => {
    const mb = effectiveMailboxId;
    const acct = accountId;
    if (!mb) return;
    void subAccounts.loadEmails(acct, mb);
  });

  async function openEmail(id: string): Promise<void> {
    selectedId = id;
    await subAccounts.openEmail(accountId, id);
  }

  function closeReading(): void {
    selectedId = null;
    subAccounts.closeReading();
  }

  function composeFromHere(): void {
    compose.openWith({
      to: '',
      cc: '',
      bcc: '',
      subject: '',
      body: '',
      identity: entry?.identity ?? null,
    });
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
    return email.bodyValues?.[part.partId]?.value ?? null;
  }

  function attachmentUrl(blobId: string, name: string | null, type: string): string | null {
    return jmap.downloadUrl({ accountId, blobId, type, name: name ?? 'attachment' });
  }
</script>

<div class="scoped-view">
  {#if !entry}
    <div class="state-msg">{t('common.loading')}</div>
  {:else}
    <div class="scoped-layout">
      <div class="list-pane" class:hidden-on-narrow={selectedId !== null}>
        <div class="scoped-header">
          <h1 class="scoped-title" data-testid="scoped-account-title">{entry.name}</h1>
          <button type="button" class="compose-btn" onclick={composeFromHere}>
            {t('sidebar.compose')}
          </button>
        </div>

        {#if subAccounts.listStatus === 'loading'}
          <div class="state-msg">{t('common.loading')}</div>
        {:else if subAccounts.listStatus === 'error'}
          <div class="state-msg error" role="alert">{subAccounts.listErrorMessage}</div>
        {:else if subAccounts.emails.length === 0}
          <div class="state-msg">{t('archive.empty')}</div>
        {:else}
          <ul class="message-list">
            {#each subAccounts.emails as email (email.id)}
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

      <div class="reading-pane" class:hidden-on-narrow={selectedId === null}>
        {#if selectedId === null}
          <div class="state-msg reading-empty">{t('archive.selectMessage')}</div>
        {:else if subAccounts.readingStatus === 'loading'}
          <div class="state-msg">{t('common.loading')}</div>
        {:else if subAccounts.readingStatus === 'error'}
          <div class="state-msg error" role="alert">{t('archive.error.loadMessageFailed')}</div>
        {:else if subAccounts.reading}
          {@const email = subAccounts.reading}
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
                    {@const url = attachmentUrl(part.blobId, part.name, part.type)}
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
  .scoped-view {
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

  .scoped-layout {
    display: flex;
    height: 100%;
    min-height: 0;
  }

  .list-pane {
    width: 380px;
    flex-shrink: 0;
    border-right: 1px solid var(--border-subtle-01);
    display: flex;
    flex-direction: column;
    overflow-y: auto;
  }

  .reading-pane {
    flex: 1;
    min-width: 0;
    overflow-y: auto;
    padding: var(--spacing-06);
  }

  @media (max-width: 800px) {
    .list-pane {
      width: 100%;
    }
    .hidden-on-narrow {
      display: none;
    }
  }

  .scoped-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-03);
    padding: var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .scoped-title {
    font-size: var(--type-heading-02-size);
    font-weight: var(--type-heading-02-weight);
    margin: 0;
    color: var(--text-primary);
    word-break: break-word;
  }

  .compose-btn {
    flex-shrink: 0;
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }
  .compose-btn:hover {
    filter: brightness(1.08);
  }

  .back-btn {
    background: none;
    border: none;
    color: var(--interactive);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    padding: 0 0 var(--spacing-02);
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
