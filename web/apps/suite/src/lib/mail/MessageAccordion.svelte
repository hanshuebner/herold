<script lang="ts">
  import HtmlBody from './HtmlBody.svelte';
  import AttachmentList from './AttachmentList.svelte';
  import EmojiPicker from './EmojiPicker.svelte';
  import ReactionsStrip from './ReactionsStrip.svelte';
  import { htmlHasExternalImages } from './sanitize';
  import { splitQuotedText } from './quoted';
  import { emailHtmlBody, emailTextBody, type Email } from './types';
  import { compose } from '../compose/compose.svelte';
  import { movePicker } from './move-picker.svelte';
  import { labelPicker } from './label-picker.svelte';
  import { snoozePicker } from './snooze-picker.svelte';
  import { mail } from './store.svelte';
  import { settings } from '../settings/settings.svelte';
  import { jmap } from '../jmap/client';
  import { auth } from '../auth/auth.svelte';
  import { reactionConfirm } from './reaction-confirm.svelte';
  import { keyboard } from '../keyboard/engine.svelte';
  import { untrack } from 'svelte';
  import { managedRules, type RuleCondition } from '../settings/managed-rules.svelte';
  import { filterLike } from '../settings/filter-like.svelte';
  import { router } from '../router/router.svelte';
  import ReplyIcon from '../icons/ReplyIcon.svelte';
  import ReplyAllIcon from '../icons/ReplyAllIcon.svelte';
  import ForwardIcon from '../icons/ForwardIcon.svelte';
  import ReactIcon from '../icons/ReactIcon.svelte';
  import MoveIcon from '../icons/MoveIcon.svelte';
  import MarkReadIcon from '../icons/MarkReadIcon.svelte';
  import MarkUnreadIcon from '../icons/MarkUnreadIcon.svelte';
  import ImportantIcon from '../icons/ImportantIcon.svelte';
  import SnoozeIcon from '../icons/SnoozeIcon.svelte';
  import UnsnoozeIcon from '../icons/UnsnoozeIcon.svelte';
  import RestoreIcon from '../icons/RestoreIcon.svelte';
  import MuteIcon from '../icons/MuteIcon.svelte';
  import UnmuteIcon from '../icons/UnmuteIcon.svelte';
  import SpamIcon from '../icons/SpamIcon.svelte';
  import PhishingIcon from '../icons/PhishingIcon.svelte';
  import BlockIcon from '../icons/BlockIcon.svelte';
  import FilterIcon from '../icons/FilterIcon.svelte';
  import LabelIcon from '../icons/LabelIcon.svelte';
  import { t, localeTag } from '../i18n/i18n.svelte';

  interface Props {
    email: Email;
    expanded: boolean;
    onToggle?: (id: string) => void;
  }
  let { email, expanded, onToggle }: Props = $props();

  let html = $derived(emailHtmlBody(email));
  let text = $derived(emailTextBody(email));
  let textSplit = $derived(text ? splitQuotedText(text) : null);
  let quotedExpanded = $state(false);

  // Per REQ-SEC-05 / REQ-SET-04..05: external images blocked by default;
  // user can flip the per-message toggle, or pre-allow at the per-sender
  // / always level via the settings panel.
  let perMessageOverride = $state(false);
  let loadImages = $derived(
    perMessageOverride || settings.isImageAllowed(email.from?.[0]?.email),
  );
  let hasExternalImages = $derived(html ? htmlHasExternalImages(html) : false);

  let senderName = $derived(
    email.from?.[0]?.name?.trim() || email.from?.[0]?.email || '(no sender)',
  );
  let senderEmail = $derived(email.from?.[0]?.email ?? '');
  let initial = $derived(senderName.slice(0, 1).toUpperCase());

  function formatRecipientSummary(email: Email): string {
    const to = email.to ?? [];
    const cc = email.cc ?? [];
    const total = to.length + cc.length;
    if (total === 0) return '';
    const first = to[0]?.name?.trim() || to[0]?.email || cc[0]?.email || '';
    if (total === 1) return `to ${first}`;
    return `to ${first} and ${total - 1} other${total - 1 === 1 ? '' : 's'}`;
  }

  function formatDateTime(iso: string): string {
    const d = new Date(iso);
    return d.toLocaleString(localeTag(), {
      weekday: 'short',
      day: 'numeric',
      month: 'short',
      year: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    });
  }

  let recipientSummary = $derived(formatRecipientSummary(email));

  // Show the Reply-all button only when there's somebody to add to Cc:
  // multiple To recipients, or any Cc recipient at all.
  let hasMultipleRecipients = $derived(
    (email.to?.length ?? 0) > 1 || (email.cc?.length ?? 0) > 0,
  );

  // True when the email currently lives in the Trash mailbox — drives
  // the per-message Restore button visibility.
  let isInTrash = $derived.by(() => {
    const t = mail.trash;
    if (!t) return false;
    return Boolean(email.mailboxIds[t.id]);
  });

  let isSeen = $derived(Boolean(email.keywords.$seen));
  let isImportant = $derived(Boolean(email.keywords.$important));
  let isSnoozed = $derived(Boolean(email.snoozedUntil));

  // Build a cid -> downloadUrl map from the email's attachments. Inline
  // images referenced by Content-ID land in the body as `cid:<id>`; the
  // sanitiser uses this map to rewrite them to a same-origin JMAP blob URL.
  let cidMap = $derived.by<Record<string, string>>(() => {
    const accountId = auth.session?.primaryAccounts['urn:ietf:params:jmap:mail'];
    if (!accountId) return {};
    const out: Record<string, string> = {};
    for (const part of email.attachments ?? []) {
      if (!part.cid || !part.blobId) continue;
      const url = jmap.downloadUrl({
        accountId,
        blobId: part.blobId,
        type: part.type,
        name: part.name ?? 'inline',
      });
      if (url) out[part.cid] = url;
    }
    return out;
  });

  /**
   * G16: metadata keyed by the resolved image URL (the value in cidMap).
   * HtmlBody uses this to render per-image download buttons in the overlay
   * layer positioned above the iframe (REQ-ATT-26).
   */
  let inlineImageMeta = $derived.by<
    Record<string, { name: string; downloadUrl: string }>
  >(() => {
    const out: Record<string, { name: string; downloadUrl: string }> = {};
    let idx = 0;
    for (const part of email.attachments ?? []) {
      if (part.disposition !== 'inline') continue;
      if (!part.cid || !part.blobId) continue;
      const url = cidMap[part.cid];
      if (!url) continue;
      const ext = part.type.split('/')[1] ?? 'bin';
      out[url] = {
        name: part.name ?? `inline-${++idx}.${ext}`,
        downloadUrl: url,
      };
    }
    return out;
  });

  // ── Reactions ──────────────────────────────────────────────────────────

  // Controls visibility of the emoji picker floating near the React button.
  let pickerOpen = $state(false);

  let reactButtonEl = $state<HTMLButtonElement | null>(null);

  /**
   * Total explicit recipient count (to + cc), used for the cross-server
   * confirmation threshold per REQ-MAIL-191.
   */
  let totalRecipients = $derived((email.to?.length ?? 0) + (email.cc?.length ?? 0));

  /**
   * The mailing-list id from the List-ID header, if present. A non-empty
   * value triggers the cross-server confirmation check.
   */
  let listId = $derived((email['header:List-ID:asText'] ?? '').trim() || null);

  function openPicker(): void {
    if (expanded) pickerOpen = true;
  }

  function handleReaction(emoji: string): void {
    const principalId = auth.principalId;
    if (!principalId) return;

    const proceed = (): void => {
      void mail.toggleReaction(email.id, emoji, principalId);
    };

    const needed = reactionConfirm.needsConfirm({
      listId,
      totalRecipients,
      emailId: email.id,
      emoji,
      onProceed: proceed,
      onAbort: () => {
        // User cancelled; nothing to do.
      },
    });

    if (!needed) proceed();
  }

  function handleChipAddReaction(emoji: string): void {
    handleReaction(emoji);
  }

  // Keyboard shortcut: `+` opens the emoji picker for the expanded/focused
  // message. Per the task spec, `r` is taken by Reply so `+` is used.
  // The layer is pushed only while this message is expanded to avoid
  // shadowing the global `+` key unnecessarily.
  $effect(() => {
    if (!expanded) return;
    const pop = untrack(() =>
      keyboard.pushLayer([
        {
          key: '+',
          action: () => {
            openPicker();
          },
        },
      ]),
    );
    return pop;
  });

  // Reading marks the message as read: when this accordion is expanded
  // and the email is currently unread, flip $seen. untrack() keeps the
  // setSeen write from re-fueling this effect via the keywords read.
  $effect(() => {
    if (!expanded) return;
    if (email.keywords.$seen) return;
    const id = email.id;
    untrack(() => {
      void mail.setSeen(id, true);
    });
  });

  // ── Mute thread ────────────────────────────────────────────────────────

  let isMuted = $derived(managedRules.isThreadMuted(email.threadId));

  async function handleMuteToggle(): Promise<void> {
    if (isMuted) {
      await managedRules.unmuteThread(email.threadId);
    } else {
      await managedRules.muteThread(email.threadId);
    }
  }

  // ── Block sender ───────────────────────────────────────────────────────

  let blockConfirmOpen = $state(false);
  let blockError = $state<string | null>(null);
  let blockInProgress = $state(false);

  function openBlockConfirm(): void {
    blockError = null;
    blockConfirmOpen = true;
  }

  function closeBlockConfirm(): void {
    blockConfirmOpen = false;
    blockError = null;
  }

  async function confirmBlock(): Promise<void> {
    if (!senderEmail) return;
    blockInProgress = true;
    blockError = null;
    try {
      await managedRules.blockSender(senderEmail);
      blockConfirmOpen = false;
    } catch (err) {
      blockError = err instanceof Error ? err.message : 'Block failed';
    } finally {
      blockInProgress = false;
    }
  }

  // ── Report spam / phishing ─────────────────────────────────────────────

  async function handleReportSpam(): Promise<void> {
    await mail.reportSpam(email.id, 'spam');
  }

  async function handleReportPhishing(): Promise<void> {
    await mail.reportSpam(email.id, 'phishing');
  }

  // ── Filter messages like this ──────────────────────────────────────────

  function handleFilterLike(): void {
    // Strip common reply/forward prefixes from the subject before using it
    // as a condition.
    const rawSubject = email.subject ?? '';
    const subject = rawSubject.replace(/^(re|fwd?|aw|sv):\s*/i, '').trim();

    const conditions: RuleCondition[] = [];
    if (senderEmail) {
      conditions.push({ field: 'from', op: 'equals', value: senderEmail });
    }
    if (subject) {
      conditions.push({ field: 'subject', op: 'contains', value: subject });
    }
    const listIdRaw = (email['header:List-ID:asText'] ?? '').trim();
    if (listIdRaw) {
      // List-ID format: "Name <list@example.com>" — extract the angle-bracket part.
      const match = listIdRaw.match(/<([^>]+)>/);
      const listId = match ? match[1]! : listIdRaw;
      conditions.push({ field: 'from', op: 'wildcard-match', value: `*@${listId.split('.').slice(1).join('.')}` });
    }

    // Set the pending payload so FiltersForm picks it up on mount.
    filterLike.set({ conditions });
    // Navigate to the filters settings section.
    router.navigate('/settings/filters');
  }
</script>

<article class="message" class:expanded>
  <button
    type="button"
    class="header"
    aria-expanded={expanded}
    onclick={() => onToggle?.(email.id)}
  >
    <span class="avatar" aria-hidden="true">{initial}</span>
    <span class="meta">
      <span class="from">
        <span class="from-name">{senderName}</span>
        {#if expanded && senderEmail && senderEmail !== senderName}
          <span class="from-email">&lt;{senderEmail}&gt;</span>
        {/if}
      </span>
      {#if expanded}
        <span class="recipients">{recipientSummary}</span>
      {:else}
        <span class="preview">{email.preview}</span>
      {/if}
    </span>
    <span class="date">{formatDateTime(email.receivedAt)}</span>
  </button>

  {#if expanded}
    <div class="body">
      {#if html}
        {#if hasExternalImages && !loadImages}
          <div class="image-banner" role="status">
            <span>External images are blocked.</span>
            <button type="button" onclick={() => (perMessageOverride = true)}>
              Load images
            </button>
            {#if email.from?.[0]?.email}
              <button
                type="button"
                onclick={() => {
                  const sender = email.from?.[0]?.email;
                  if (sender) settings.addImageAllowedSender(sender);
                  perMessageOverride = true;
                }}
              >
                Always from {email.from?.[0]?.email}
              </button>
            {/if}
          </div>
        {/if}
        <HtmlBody {html} {loadImages} {cidMap} {inlineImageMeta} />
      {:else if text && textSplit}
        <pre class="text-body">{textSplit.fresh}</pre>
        {#if textSplit.quoted}
          {#if quotedExpanded}
            <pre class="text-body quoted">{textSplit.quoted}</pre>
            <button
              type="button"
              class="quoted-toggle"
              onclick={() => (quotedExpanded = false)}
            >
              Hide trimmed content
            </button>
          {:else}
            <button
              type="button"
              class="quoted-toggle"
              onclick={() => (quotedExpanded = true)}
              aria-label="Show trimmed content"
            >
              <span aria-hidden="true">···</span>
            </button>
          {/if}
        {/if}
      {:else}
        <p class="empty">(no body)</p>
      {/if}

      <AttachmentList {email} />

      <!-- Reactions strip: shown whenever reactions exist on the message. -->
      <ReactionsStrip
        emailId={email.id}
        reactions={email.reactions}
        principalId={auth.principalId}
        onAddReaction={handleChipAddReaction}
      />

      <div class="actions">
        <button
          type="button"
          class="pill icon-only"
          aria-label={t('msg.reply')}
          title={t('msg.reply')}
          onclick={() => compose.openReply(email)}
        >
          <ReplyIcon size={18} />
        </button>
        {#if hasMultipleRecipients}
          <button
            type="button"
            class="pill icon-only"
            aria-label={t('msg.replyAll')}
            title={t('msg.replyAll')}
            onclick={() => compose.openReplyAll(email)}
          >
            <ReplyAllIcon size={18} />
          </button>
        {/if}
        <button
          type="button"
          class="pill icon-only"
          aria-label={t('msg.forward')}
          title={t('msg.forward')}
          onclick={() => compose.openForward(email)}
        >
          <ForwardIcon size={18} />
        </button>
        <!-- React button per REQ-MAIL-152. The `+` key also opens this
             picker when the message is expanded (see keyboard layer above). -->
        <div class="react-wrapper">
          <button
            type="button"
            class="pill icon-only"
            class:active={pickerOpen}
            bind:this={reactButtonEl}
            onclick={() => (pickerOpen = !pickerOpen)}
            aria-label={t('msg.react')}
            title={t('msg.react')}
            aria-expanded={pickerOpen}
            aria-haspopup="dialog"
          >
            <ReactIcon size={18} />
          </button>
          {#if pickerOpen}
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
              class="picker-anchor"
              onkeydown={(e) => { if (e.key === 'Escape') pickerOpen = false; }}
            >
              <EmojiPicker
                onSelect={handleReaction}
                onClose={() => (pickerOpen = false)}
              />
            </div>
          {/if}
        </div>
        <button
          type="button"
          class="pill icon-only"
          aria-label={t('msg.move')}
          title={t('msg.move')}
          onclick={() => movePicker.open(email.id)}
        >
          <MoveIcon size={18} />
        </button>
        <button
          type="button"
          class="pill icon-only"
          aria-label={t('msg.label')}
          title={t('msg.label')}
          onclick={() => labelPicker.open(email.id)}
        >
          <LabelIcon size={18} />
        </button>
        <button
          type="button"
          class="pill icon-only"
          aria-label={isSeen ? t('msg.markUnread') : t('msg.markRead')}
          title={isSeen ? t('msg.markUnread') : t('msg.markRead')}
          onclick={() => mail.setSeen(email.id, !isSeen)}
        >
          {#if isSeen}<MarkUnreadIcon size={18} />{:else}<MarkReadIcon size={18} />{/if}
        </button>
        <button
          type="button"
          class="pill icon-only"
          class:active={isImportant}
          aria-label={isImportant ? t('msg.unmarkImportant') : t('msg.markImportant')}
          aria-pressed={isImportant}
          title={isImportant ? t('msg.unmarkImportant') : t('msg.markImportant')}
          onclick={() => mail.toggleImportant(email.id)}
        >
          <ImportantIcon size={18} />
        </button>
        {#if isSnoozed}
          <button
            type="button"
            class="pill icon-only"
            aria-label={t('msg.unsnooze')}
            title={t('msg.unsnooze')}
            onclick={() => mail.unsnoozeEmail(email.id)}
          >
            <UnsnoozeIcon size={18} />
          </button>
        {:else}
          <button
            type="button"
            class="pill icon-only"
            aria-label={t('msg.snooze')}
            title={t('msg.snooze')}
            onclick={() => snoozePicker.open(email.id)}
          >
            <SnoozeIcon size={18} />
          </button>
        {/if}
        {#if isInTrash}
          <button
            type="button"
            class="pill icon-only"
            aria-label={t('msg.restore')}
            title={t('msg.restore')}
            onclick={() => mail.restoreFromTrash(email.id)}
          >
            <RestoreIcon size={18} />
          </button>
        {/if}

        <!-- Mute / Unmute thread per REQ-MAIL-160. -->
        <button
          type="button"
          class="pill icon-only"
          aria-label={isMuted ? t('msg.unmuteThread') : t('msg.muteThread')}
          aria-pressed={isMuted}
          title={isMuted ? t('msg.unmuteThread') : t('msg.muteThread')}
          onclick={() => void handleMuteToggle()}
        >
          {#if isMuted}<UnmuteIcon size={18} />{:else}<MuteIcon size={18} />{/if}
        </button>

        <!-- Report spam per REQ-MAIL-135. -->
        <button
          type="button"
          class="pill icon-only"
          aria-label={t('msg.reportSpam')}
          title={t('msg.reportSpam')}
          onclick={() => void handleReportSpam()}
        >
          <SpamIcon size={18} />
        </button>

        <!-- Report phishing per REQ-MAIL-136. -->
        <button
          type="button"
          class="pill icon-only"
          aria-label={t('msg.reportPhishing')}
          title={t('msg.reportPhishing')}
          onclick={() => void handleReportPhishing()}
        >
          <PhishingIcon size={18} />
        </button>

        <!-- Block sender per REQ-MAIL-134. -->
        {#if senderEmail}
          <button
            type="button"
            class="pill icon-only"
            aria-label={t('msg.blockSender')}
            title={t('msg.blockSender')}
            onclick={openBlockConfirm}
          >
            <BlockIcon size={18} />
          </button>
        {/if}

        <!-- Filter messages like this per REQ-MAIL-138. -->
        <button
          type="button"
          class="pill icon-only"
          aria-label={t('msg.filterLike')}
          title={t('msg.filterLike')}
          onclick={handleFilterLike}
        >
          <FilterIcon size={18} />
        </button>
      </div>

      <!-- Block sender confirmation modal (inline). -->
      {#if blockConfirmOpen}
        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <div
          class="block-modal"
          role="dialog"
          aria-modal="true"
          aria-label="Block sender"
          tabindex="-1"
          onkeydown={(e) => { if (e.key === 'Escape') closeBlockConfirm(); }}
        >
          <p class="block-modal-body">
            Block all messages from <strong>{senderEmail}</strong>?
            Existing messages stay; future messages go to Trash.
            You can unblock them later in Settings &rarr; Filters.
          </p>
          {#if blockError}
            <p class="block-modal-error" role="alert">{blockError}</p>
          {/if}
          <div class="block-modal-actions">
            <button
              type="button"
              class="pill"
              onclick={() => void confirmBlock()}
              disabled={blockInProgress}
            >
              {blockInProgress ? 'Blocking…' : 'Block sender'}
            </button>
            <button type="button" class="pill" onclick={closeBlockConfirm}>
              Cancel
            </button>
          </div>
        </div>
      {/if}
    </div>
  {/if}
</article>

<style>
  .message {
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .header {
    display: grid;
    grid-template-columns: auto 1fr auto;
    gap: var(--spacing-04);
    align-items: center;
    width: 100%;
    padding: var(--spacing-04) var(--spacing-05);
    text-align: left;
    color: var(--text-primary);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .header:hover {
    background: var(--layer-01);
  }

  .avatar {
    width: 32px;
    height: 32px;
    border-radius: var(--radius-pill);
    background: var(--layer-02);
    color: var(--text-primary);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
  }

  .meta {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    overflow: hidden;
  }

  .from {
    display: flex;
    align-items: baseline;
    gap: var(--spacing-03);
    overflow: hidden;
  }
  .from-name {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .from-email {
    color: var(--text-helper);
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .recipients,
  .preview {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .date {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    white-space: nowrap;
    align-self: flex-start;
    padding-top: var(--spacing-01);
  }

  .body {
    padding: 0 var(--spacing-05) var(--spacing-05);
  }

  .image-banner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-04);
    margin-bottom: var(--spacing-03);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }
  .image-banner button {
    color: var(--interactive);
    font-weight: 600;
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .image-banner button:hover {
    background: var(--layer-02);
  }

  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-03);
    padding: var(--spacing-04) 0 0;
  }
  .pill {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: 32px;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .pill:hover {
    background: var(--layer-02);
  }
  .pill.active {
    background: var(--support-warning);
    color: var(--text-primary);
    border-color: var(--support-warning);
  }
  /* Icon-only action buttons: square hit target, no horizontal padding
     so the SVG sits centered inside the pill. The accessible name and
     hover tooltip come from aria-label / title on the button. */
  .pill.icon-only {
    width: 36px;
    min-height: 36px;
    padding: 0;
    justify-content: center;
  }

  /* The react wrapper positions the picker relative to the button. */
  .react-wrapper {
    position: relative;
    display: inline-flex;
  }
  .picker-anchor {
    position: absolute;
    bottom: calc(100% + var(--spacing-02));
    left: 0;
    z-index: 200;
  }

  .text-body {
    margin: 0;
    padding: var(--spacing-04);
    background: var(--layer-01);
    border-radius: var(--radius-md);
    white-space: pre-wrap;
    word-break: break-word;
    font-family: var(--font-mono);
    font-size: var(--type-body-01-size);
    color: var(--text-primary);
    overflow: auto;
  }
  .text-body.quoted {
    color: var(--text-helper);
    margin-top: var(--spacing-03);
  }
  .quoted-toggle {
    display: inline-flex;
    align-items: center;
    margin-top: var(--spacing-03);
    padding: var(--spacing-01) var(--spacing-04);
    background: var(--layer-02);
    color: var(--text-helper);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .quoted-toggle:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .empty {
    margin: 0;
    padding: var(--spacing-04);
    color: var(--text-helper);
    font-style: italic;
  }

  /* Block-sender confirmation inline modal. */
  .block-modal {
    margin-top: var(--spacing-04);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }
  .block-modal-body {
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
  .block-modal-error {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
  .block-modal-actions {
    display: flex;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  @media (max-width: 560px) {
    .header {
      grid-template-columns: 28px 1fr auto;
      padding: var(--spacing-03) var(--spacing-04);
      gap: var(--spacing-02);
    }
    .body {
      padding: 0 var(--spacing-04) var(--spacing-04);
    }
    .actions {
      flex-wrap: wrap;
    }
    .pill {
      padding: var(--spacing-01) var(--spacing-03);
    }
  }

  /* Print: drop every interactive control inside the message — the
     per-message action bar, the external-image banner buttons, the
     react picker / button, the trimmed-content toggle, and the inline
     block-sender modal — so the printout shows only the message
     content. The header avatar / button styling stays since it
     carries the from / date / recipients metadata the reader expects
     on paper. */
  @media print {
    .actions,
    .image-banner,
    .react-wrapper,
    .quoted-toggle,
    .block-modal {
      display: none !important;
    }
  }
</style>
