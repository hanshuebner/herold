<script lang="ts">
  import HtmlBody from './HtmlBody.svelte';
  import AttachmentList from './AttachmentList.svelte';
  import Avatar from '../avatar/Avatar.svelte';
  import EmojiPicker from './EmojiPicker.svelte';
  import ReactionsStrip from './ReactionsStrip.svelte';
  import { htmlHasExternalImages } from './sanitize';
  import { splitQuotedText } from './quoted';
  import { emailHtmlBody, emailTextBody, type Email } from './types';
  import { mail } from './store.svelte';
  import { settings } from '../settings/settings.svelte';
  import { jmap } from '../jmap/client';
  import { auth } from '../auth/auth.svelte';
  import { reactionConfirm } from './reaction-confirm.svelte';
  import { keyboard } from '../keyboard/engine.svelte';
  import { untrack } from 'svelte';
  import type { RuleCondition } from '../settings/managed-rules.svelte';
  import { filterLike } from '../settings/filter-like.svelte';
  import { router } from '../router/router.svelte';
  import ActionOverflowMenu from './ActionOverflowMenu.svelte';
  import ReactIcon from '../icons/ReactIcon.svelte';
  import { t, localeTag } from '../i18n/i18n.svelte';
  import { relativeTimeAgo } from './relative-time';
  import RecipientTrigger from './RecipientTrigger.svelte';
  import { type Address } from './types';

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

  // Identity list for the avatar resolver's own-identity tier.
  let ownIdentities = $derived(Array.from(mail.identities.values()));

  // Parse Face/X-Face headers from this email for avatar resolver tier-2.
  let avatarMessageHeaders = $derived.by<
    { face?: string; xFace?: string } | undefined
  >(() => {
    const face = (email['header:Face:asText'] ?? '').trim() || undefined;
    const xFace = (email['header:X-Face:asText'] ?? '').trim() || undefined;
    if (!face && !xFace) return undefined;
    return { face, xFace };
  });

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

  function nonEmptyAddresses(addrs: Address[] | null | undefined): Address[] {
    return (addrs ?? []).filter((a) => Boolean(a.email));
  }

  let toRecipients = $derived(nonEmptyAddresses(email.to));
  let ccRecipients = $derived(nonEmptyAddresses(email.cc));
  let bccRecipients = $derived(nonEmptyAddresses(email.bcc));

  // True when the message carries at least one non-inline attachment.
  // Used to surface a paperclip glyph in the accordion header next to the
  // date so the user can spot attachments without expanding each message.
  // Inline images alone (referenced by the body via cid:) do NOT trip the
  // indicator — they are part of the body, not a separate attachment.
  let hasNonInlineAttachment = $derived.by(() => {
    const parts = email.attachments;
    if (parts !== undefined) {
      return parts.some((p) => p.disposition !== 'inline');
    }
    return Boolean(email.hasAttachment);
  });

  // Relative annotation appended to the date in both collapsed and
  // expanded headers, e.g. "(17 hours ago)". The label is computed once
  // per render; a live ticker would add complexity with negligible UX
  // gain — the annotation is approximate by nature.
  let relativeAnnotation = $derived(
    `(${relativeTimeAgo(new Date(email.receivedAt))})`,
  );

  // True when the email currently lives in the Trash mailbox — drives
  // the per-message Restore overflow item visibility (re #98). Restore
  // is also offered at thread scope in ThreadToolbar.
  let isInTrash = $derived.by(() => {
    const t = mail.trash;
    if (!t) return false;
    return Boolean(email.mailboxIds[t.id]);
  });

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

  // ── Reactions (Gmail-style: anchored to the message header) ────────────
  //
  // Per re #98 the reactions surface lives in the message title row, not
  // in a row below the body. Visible state for the picker is gated on the
  // accordion being expanded so the picker only opens for the active
  // message.

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

  // ── Per-message overflow menu (re #98) ─────────────────────────────────
  // The per-message action row was removed; only the actions that depend
  // on a single message's identity (filterLike from THIS sender/subject,
  // viewOriginal of THIS rfc822 source, restore THIS message from trash)
  // remain reachable from the message header via a small kebab menu.
  // Other message-scope verbs (mark unread, snooze, mark important, move,
  // label) now apply at thread scope via ThreadToolbar.

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

  function sanitizeFilename(subject: string): string {
    return subject
      .replace(/[^a-zA-Z0-9 _-]/g, '_')
      .replace(/\s+/g, '_')
      .replace(/_{2,}/g, '_')
      .slice(0, 80);
  }

  function handleViewOriginal(): void {
    const accountId = auth.session?.primaryAccounts['urn:ietf:params:jmap:mail'];
    if (!accountId || !email.blobId) return;
    const rawSubject = (email.subject ?? 'message').trim() || 'message';
    const name = `${sanitizeFilename(rawSubject)}.eml`;
    const url = jmap.downloadUrl({
      accountId,
      blobId: email.blobId,
      type: 'text/plain',
      name,
      disposition: 'inline',
    });
    if (!url) return;
    window.open(url, '_blank', 'noopener');
  }

  function handleRestore(): void {
    void mail.restoreFromTrash(email.id);
    if (window.history.length > 1) {
      window.history.back();
    } else {
      const folder = mail.listFolder;
      router.navigate(folder === 'inbox' ? '/mail' : `/mail/folder/${encodeURIComponent(folder)}`);
    }
  }

  // Rarely-used per-message actions kept reachable via the header kebab.
  // Conditional items (restore in trash, viewOriginal when blobId is known)
  // drop out cleanly when not applicable; if nothing applies the kebab
  // hides itself entirely.
  let overflowItems = $derived.by(() => {
    const items: { id: string; label: string; onclick: () => void }[] = [];
    if (isInTrash) {
      items.push({ id: 'restore', label: t('msg.restore'), onclick: handleRestore });
    }
    items.push({ id: 'filterLike', label: t('msg.filterLike'), onclick: handleFilterLike });
    if (email.blobId) {
      items.push({ id: 'viewOriginal', label: t('msg.viewOriginal'), onclick: handleViewOriginal });
    }
    return items;
  });
</script>

<article class="message" class:expanded>
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="header"
    role="button"
    tabindex="0"
    aria-expanded={expanded}
    onclick={() => onToggle?.(email.id)}
    onkeydown={(e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        onToggle?.(email.id);
      }
    }}
  >
    {#if expanded && senderEmail}
      <RecipientTrigger
        email={senderEmail}
        capturedName={email.from?.[0]?.name ?? null}
        messageHeaders={avatarMessageHeaders}
      >
        <Avatar
          email={senderEmail}
          fallbackInitial={initial}
          size={32}
          {ownIdentities}
          messageHeaders={avatarMessageHeaders}
        />
      </RecipientTrigger>
    {:else}
      <Avatar
        email={senderEmail}
        fallbackInitial={initial}
        size={32}
        {ownIdentities}
        messageHeaders={avatarMessageHeaders}
      />
    {/if}
    <span class="meta">
      <span class="from">
        {#if expanded && senderEmail}
          <RecipientTrigger
            email={senderEmail}
            capturedName={email.from?.[0]?.name ?? null}
            messageHeaders={avatarMessageHeaders}
            inline
          >
            <span class="from-name">{senderName}</span>
            {#if senderEmail !== senderName}
              <span class="from-email">&lt;{senderEmail}&gt;</span>
            {/if}
          </RecipientTrigger>
        {:else}
          <span class="from-name">{senderName}</span>
        {/if}
      </span>
      {#if expanded}
        {#if toRecipients.length > 0}
          <span class="recipients-row" aria-label="To">
            <span class="recipients-label">To:</span>
            {#each toRecipients as r, i (r.email + i)}
              <RecipientTrigger email={r.email} capturedName={r.name} inline>
                <span class="recipient-chip-label">{r.name?.trim() || r.email}</span>
              </RecipientTrigger>{#if i < toRecipients.length - 1},&nbsp;{/if}
            {/each}
          </span>
        {/if}
        {#if ccRecipients.length > 0}
          <span class="recipients-row" aria-label="Cc">
            <span class="recipients-label">Cc:</span>
            {#each ccRecipients as r, i (r.email + i)}
              <RecipientTrigger email={r.email} capturedName={r.name} inline>
                <span class="recipient-chip-label">{r.name?.trim() || r.email}</span>
              </RecipientTrigger>{#if i < ccRecipients.length - 1},&nbsp;{/if}
            {/each}
          </span>
        {/if}
        {#if bccRecipients.length > 0}
          <span class="recipients-row" aria-label="Bcc">
            <span class="recipients-label">Bcc:</span>
            {#each bccRecipients as r, i (r.email + i)}
              <RecipientTrigger email={r.email} capturedName={r.name} inline>
                <span class="recipient-chip-label">{r.name?.trim() || r.email}</span>
              </RecipientTrigger>{#if i < bccRecipients.length - 1},&nbsp;{/if}
            {/each}
          </span>
        {/if}
      {:else}
        <span class="preview">{email.preview}</span>
      {/if}
    </span>
    <span class="header-right">
      {#if hasNonInlineAttachment}
        <span class="attachment-icon" aria-label={t('att.headerIcon.label')}>&#128206;</span>
      {/if}

      <!--
        Reactions live in the message header per re #98 (Gmail-style).
        Existing reaction chips render first, followed by the small "react"
        trigger that opens the emoji picker. Both are click-through to
        avoid toggling the accordion when interacting with reactions.
      -->
      {#if expanded}
        <span
          class="reactions-anchor"
          onclick={(e) => e.stopPropagation()}
          onkeydown={(e) => e.stopPropagation()}
          role="presentation"
        >
          <ReactionsStrip
            emailId={email.id}
            reactions={email.reactions}
            principalId={auth.principalId}
            onAddReaction={handleChipAddReaction}
          />
          <span class="react-wrapper">
            <button
              type="button"
              class="header-icon-btn"
              class:active={pickerOpen}
              bind:this={reactButtonEl}
              onclick={() => { pickerOpen = !pickerOpen; }}
              aria-label={t('msg.react')}
              title={t('msg.react')}
              aria-expanded={pickerOpen}
              aria-haspopup="dialog"
              aria-pressed={pickerOpen}
            >
              <ReactIcon size={16} />
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
          </span>

          {#if overflowItems.length > 0}
            <ActionOverflowMenu items={overflowItems} />
          {/if}
        </span>
      {/if}

      <span class="date">
        {formatDateTime(email.receivedAt)}{#if relativeAnnotation}&nbsp;<span class="date-relative">{relativeAnnotation}</span>{/if}
      </span>
    </span>
  </div>

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
              <span aria-hidden="true">...</span>
            </button>
          {/if}
        {/if}
      {:else}
        <p class="empty">(no body)</p>
      {/if}

      <AttachmentList {email} />
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

  .preview {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Per REQ-MAIL-46 the expanded header carries structured To: / Cc: /
     Bcc: rows so each recipient becomes its own hover trigger. The rows
     wrap on narrow viewports; the label sits flush left. */
  .recipients-row {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--spacing-01);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }
  .recipients-label {
    color: var(--text-helper);
    font-weight: 500;
    margin-right: var(--spacing-02);
  }
  .recipient-chip-label {
    color: var(--text-secondary);
  }

  .header-right {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-03);
    align-self: flex-start;
    padding-top: var(--spacing-01);
    flex-wrap: wrap;
    justify-content: flex-end;
  }
  .attachment-icon {
    color: var(--text-helper);
    font-size: 14px;
    line-height: 1;
  }
  .date {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    white-space: nowrap;
  }

  /* Relative annotation appended to the date in the expanded header,
     e.g. "(17 hours ago)". Slightly dimmer so it reads as secondary. */
  .date-relative {
    color: var(--text-placeholder);
    font-size: var(--type-body-compact-01-size);
  }

  /* Reactions strip + react button + per-message kebab live in a single
     anchor span inside the message header (re #98). Click-through is
     stopped here so interacting with reactions doesn't fold the
     accordion. */
  .reactions-anchor {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
  }

  /* Compact icon button used in the message header (react trigger).
     Kept visually quiet so it does not compete with the date. */
  .header-icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-pill);
    color: var(--text-secondary);
    background: transparent;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .header-icon-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .header-icon-btn.active {
    background: var(--support-warning);
    color: var(--text-primary);
  }

  /* The react wrapper positions the picker relative to the button. */
  .react-wrapper {
    position: relative;
    display: inline-flex;
  }
  .picker-anchor {
    position: absolute;
    top: calc(100% + var(--spacing-02));
    right: 0;
    z-index: 200;
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

  @media (max-width: 560px) {
    .header {
      grid-template-columns: 28px 1fr auto;
      padding: var(--spacing-03) var(--spacing-04);
      gap: var(--spacing-02);
    }
    .body {
      padding: 0 var(--spacing-04) var(--spacing-04);
    }
  }

  /* Print: drop interactive controls inside the message — the external
     -image banner buttons, the react wrapper, and the trimmed-content
     toggle — so the printout shows only the message content. The header
     avatar / button styling stays since it carries the from / date /
     recipients metadata the reader expects on paper. */
  @media print {
    .image-banner,
    .react-wrapper,
    .header-icon-btn,
    .quoted-toggle {
      display: none !important;
    }
  }
</style>
