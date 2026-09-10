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
  import { mail } from '../lib/mail/store.svelte';
  import { movePicker } from '../lib/mail/move-picker.svelte';
  import MessageKebabMenu, { type KebabItem } from '../lib/mail/MessageKebabMenu.svelte';
  import { compose } from '../lib/compose/compose.svelte';
  import { toast } from '../lib/toast/toast.svelte';
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

  // Mirror this account's fetched rows into the shared mail store cache
  // (issue #212, REQ-MAIL-SUB-04) so the SAME generalized bulk action
  // methods the primary/combined views use (move / delete / mark --
  // routed through mail store's #emailSetUpdateBulk, which resolves each
  // id's target account via emailAccountId) work here too, without a
  // second action implementation. subAccounts.emails / .reading stay the
  // source of truth for this view's OWN rendering -- this is purely
  // additive glue, not a rework of the list-loading mechanism.
  $effect(() => {
    if (subAccounts.emails.length > 0) mail.mirrorScopedEmails(accountId, subAccounts.emails);
  });
  $effect(() => {
    if (subAccounts.reading) mail.mirrorScopedEmails(accountId, [subAccounts.reading]);
  });

  async function openEmail(id: string): Promise<void> {
    selectedId = id;
    await subAccounts.openEmail(accountId, id);
  }

  /** Reload the scoped list after a mutation (move / delete) so the row
   * removal is reflected without a second bespoke removal path. */
  async function reloadList(): Promise<void> {
    const mb = effectiveMailboxId;
    if (mb) await subAccounts.loadEmails(accountId, mb);
  }

  function toggleSeen(email: Email): void {
    const nextSeen = !email.keywords.$seen;
    void mail.bulkSetSeen([email.id], nextSeen).then(reloadList);
  }

  function moveEmail(email: Email): void {
    // movePicker/MoveTargetPicker.svelte are the SAME global singleton +
    // overlay the primary list's Move action uses; passing accountId
    // scopes the candidate mailbox list to this sub-account's own tree
    // (issue #212, REQ-MAIL-SUB-04) and mail.bulkMoveToMailbox resolves
    // the source email's target account via the mirrored emailAccountId
    // tag, so no separate move implementation is needed here.
    movePicker.open(email.id, accountId);
  }
  // Reload the scoped list once a scoped move commits, so a moved-out
  // row disappears from this view. MoveTargetPicker.svelte's commit()
  // calls mail.bulkMoveToMailbox/moveEmailToMailbox directly and has no
  // per-caller completion hook, so the reload is driven by watching
  // movePicker close while it was scoped to this account.
  let wasMovePickerOpenForThisAccount = $state(false);
  $effect(() => {
    if (movePicker.isOpen && movePicker.accountId === accountId) {
      wasMovePickerOpenForThisAccount = true;
    } else if (wasMovePickerOpenForThisAccount && !movePicker.isOpen) {
      wasMovePickerOpenForThisAccount = false;
      void reloadList();
    }
  });

  function deleteEmail(email: Email): void {
    const trash = entry?.mailboxes.find((m) => m.role === 'trash');
    if (!trash) {
      toast.show({ message: t('archive.error.loadMessageFailed'), kind: 'error', timeoutMs: 5000 });
      return;
    }
    void mail.bulkMoveToMailbox([email.id], trash.id).then(() => {
      if (selectedId === email.id) closeReading();
      return reloadList();
    });
  }

  function kebabItemsFor(email: Email): KebabItem[] {
    const items: KebabItem[] = [
      {
        id: 'toggle-seen',
        label: email.keywords.$seen
          ? t('msg.kebab.markUnread')
          : t('msg.kebab.markRead'),
        onclick: () => toggleSeen(email),
      },
      {
        id: 'move',
        label: t('msg.kebab.move'),
        onclick: () => moveEmail(email),
      },
      {
        id: 'delete',
        label: t('msg.kebab.delete'),
        danger: true,
        onclick: () => deleteEmail(email),
      },
    ];
    return items;
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
                <div
                  class="message-row"
                  class:selected={selectedId === email.id}
                  class:unread={isUnread(email)}
                >
                  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
                  <div
                    class="message-row-main"
                    role="button"
                    tabindex="0"
                    onclick={() => void openEmail(email.id)}
                    onkeydown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        void openEmail(email.id);
                      }
                    }}
                  >
                    <span class="sender">{senderLabel(email)}</span>
                    <span class="subject">{email.subject || t('msg.noSubject')}</span>
                    <span class="preview">{email.preview}</span>
                    <span class="date">{formatDate(email.receivedAt)}</span>
                  </div>
                  <span class="message-row-kebab">
                    <MessageKebabMenu items={kebabItemsFor(email)} />
                  </span>
                </div>
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
    align-items: center;
    width: 100%;
    border-bottom: 1px solid var(--border-subtle-01);
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
  .message-row-main {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
    text-align: left;
    gap: 2px;
    padding: var(--spacing-03) var(--spacing-05);
    cursor: pointer;
  }
  .message-row-kebab {
    flex-shrink: 0;
    padding-right: var(--spacing-03);
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
