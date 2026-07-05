<script module lang="ts">
  // Module-level session cache: email.id → full HTML body text.
  // Persists across accordion re-mounts within the same browser session so
  // navigating away and back to the same thread does not re-fetch.
  const _fullBodyCache = new Map<string, string>();
</script>

<script lang="ts">
  import HtmlBody from './HtmlBody.svelte';
  import AttachmentList from './AttachmentList.svelte';
  import Avatar from '../avatar/Avatar.svelte';
  import EmojiPicker from './EmojiPicker.svelte';
  import ReactionsStrip from './ReactionsStrip.svelte';
  import { htmlHasExternalImages } from './sanitize';
  import { splitQuotedText } from './quoted';
  import { emailHtmlBody, emailTextBody, type Email } from './types';
  import {
    htmlBodyIsTruncated,
    htmlBodyFullDownloadUrl,
    fetchFullHtmlBody,
  } from './html-body-full';
  import { mail } from './store.svelte';
  import { settings } from '../settings/settings.svelte';
  import { jmap } from '../jmap/client';
  import { auth } from '../auth/auth.svelte';
  import { reactionConfirm } from './reaction-confirm.svelte';
  import { keyboard } from '../keyboard/engine.svelte';
  import { untrack } from 'svelte';
  import ReactIcon from '../icons/ReactIcon.svelte';
  import ReplyIcon from '../icons/ReplyIcon.svelte';
  import { compose } from '../compose/compose.svelte';
  import { navigateBackFromThread } from './navigate-back';
  import MessageKebabMenu, { type KebabItem } from './MessageKebabMenu.svelte';
  import RawSourceModal from './RawSourceModal.svelte';
  import { emlDownloadFilename } from './download-filename';
  import { sanitizeHtml } from './sanitize';
  import { printMessage } from './print-message';
  import { t, localeTag, i18n } from '../i18n/i18n.svelte';
  import { relativeTimeAgo } from './relative-time';
  import RecipientTrigger from './RecipientTrigger.svelte';
  import { type Address } from './types';
  import { buildSelfEmailSet, isFromSelf } from './identity-match';
  import TranslateBar from './TranslateBar.svelte';
  import { htmlToText } from '../translate/html-to-text';

  interface Props {
    email: Email;
    expanded: boolean;
    onToggle?: (id: string) => void;
    /**
     * When true, the accordion is rendered inside the chrome-less standalone
     * thread-window popup (/#/thread-window/<id>). Mark-as-unread closes the
     * popup instead of navigating back to the mail list (re #129).
     */
    standalone?: boolean;
  }
  let { email, expanded, onToggle, standalone = false }: Props = $props();

  // ── Truncated-body recovery (Forgejo #48) ─────────────────────────────
  //
  // Email/get caps body values at 1 MiB (mailparse.DefaultMaxTextPartBytes)
  // and sets isTruncated: true on the affected bodyValue. When the inline
  // value is truncated, we fetch the full body via the JMAP download URL
  // for the html body part's blobId (no cap on the blob endpoint) and swap
  // the rendered content once the fetch completes.
  //
  // The cache is module-level so navigating away and back to the same thread
  // does not re-fetch within the same browser session.

  let _isTruncated = $derived(htmlBodyIsTruncated(email));

  // Rendered html: starts as the inline (possibly truncated) value and
  // swaps to the fetched full text once the download completes. A null
  // means "use the inline value", not "no body".
  let _htmlFull = $state<string | null>(null);
  let _htmlFetching = $state(false);
  // Plain boolean (not reactive) so the effect does not loop on its own write.
  let _htmlFetchAttempted = false;

  // Kick off the full-body fetch when the accordion is expanded and the
  // body value was truncated by the server. untrack() prevents the async
  // writes to _htmlFull/_htmlFetching from looping back into the effect.
  $effect(() => {
    if (!expanded || !_isTruncated) return;
    const emailId = email.id;
    untrack(() => {
      if (_htmlFetchAttempted) return;

      // Cache hit from a previous mount of this email within the session.
      const cached = _fullBodyCache.get(emailId);
      if (cached !== undefined) {
        _htmlFull = cached;
        _htmlFetchAttempted = true;
        return;
      }

      const accountId = auth.session?.primaryAccounts['urn:ietf:params:jmap:mail'];
      if (!accountId) return;
      const url = htmlBodyFullDownloadUrl(email, accountId, (args) => jmap.downloadUrl(args));
      if (!url) return;

      _htmlFetchAttempted = true;
      _htmlFetching = true;
      void fetchFullHtmlBody(url)
        .then((text) => {
          _fullBodyCache.set(emailId, text);
          _htmlFull = text;
        })
        .catch(() => {
          // Fall back to the inline truncated value — _htmlFull stays null.
        })
        .finally(() => {
          _htmlFetching = false;
        });
    });
  });

  let html = $derived(_htmlFull ?? emailHtmlBody(email));
  let text = $derived(emailTextBody(email));
  let textSplit = $derived(text ? splitQuotedText(text) : null);

  // Plain text extracted from the body for language detection. Prefers the
  // plain-text part; falls back to stripping HTML. Only computed when expanded
  // to avoid running franc on every row in the thread list.
  let bodyTextForDetection = $derived.by<string>(() => {
    if (!expanded) return '';
    if (text) return text;
    if (html) return htmlToText(html);
    return '';
  });
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

  // True when this message was sent by the signed-in user. Drives the
  // self-card visual treatment (tinted background + left accent border)
  // and the "You" sender label in place of the From name / email pair.
  let selfEmailSet = $derived(buildSelfEmailSet(mail.identities.values()));
  let isSelf = $derived(isFromSelf(email, selfEmailSet));

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

  // Build a cid -> intrinsic dimensions map from the email's attachments.
  // The server emits width/height on inline image parts when the bodymeta
  // worker has decoded the image (issue #47). The sanitiser uses this map
  // to inject `aspect-ratio: W / H` on each resolved cid <img> so the
  // browser can reserve layout space before the image bytes arrive.
  let cidDimensions = $derived.by<Record<string, { width: number; height: number }>>(() => {
    const out: Record<string, { width: number; height: number }> = {};
    for (const part of email.attachments ?? []) {
      const { cid, width, height } = part;
      if (!cid || !width || !height || width <= 0 || height <= 0) continue;
      out[cid] = { width, height };
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

  // Reading marks the message as read: on the first time this
  // accordion is seen in expanded state, flip $seen=true if it is
  // currently false. The latch consumes the auto-read OPPORTUNITY on
  // first expansion, irrespective of whether a server call was
  // actually needed (issue #102 — when a user opens an already-seen
  // message and then clicks "Mark unread", the keyword flip would
  // re-trigger this effect, find autoReadDone=false because the
  // initial run took the "already seen" short-circuit, and fire
  // setSeen(id, true) racing the user's setSeen(id, false). The
  // winner of the simultaneous Email/set calls is non-deterministic
  // and frequently "auto-read wins", silently undoing the user
  // action. Closing and re-opening the thread remounts the component,
  // which is the correct trigger to re-evaluate auto-read.)
  let autoReadDone = false;
  $effect(() => {
    if (!expanded) return;
    if (autoReadDone) return;
    autoReadDone = true;
    if (email.keywords.$seen) return;
    const id = email.id;
    untrack(() => {
      void mail.setSeen(id, true);
    });
  });

  // ── Per-message kebab menu (conservative slice, restored from re #98) ──
  // The kebab carries verbs that have an inherently per-message use case
  // (download .eml, show original, print this message) or whose
  // thread-scoped variant is wrong for multi-sender threads (delete one
  // msg, mark one msg unread, mark unread from here, report spam / phishing,
  // filter messages like this). Reply / forward live in the fixed reply
  // bar; block sender stays thread-only; report illegal and translate are
  // deferred. See docs/design/web/requirements/02-mail-basics.md
  // § Per-message context menu.

  /**
   * Leave the current thread after a per-message action, honouring the
   * standalone-popup exception — mirrors ThreadToolbar.leaveThread() (re #129).
   */
  function leaveThread(): void {
    if (standalone) {
      window.close();
    } else {
      navigateBackFromThread();
    }
  }

  function markUnread(): void {
    void mail.setSeen(email.id, false);
    leaveThread();
  }

  function markUnreadFromHere(): void {
    void mail.markUnreadFromHere(email.threadId, email.id);
    leaveThread();
  }

  // True when the inline composer is open — suppress the per-message reply
  // button in that window so it does not compete with the active composer.
  let isInlineOpen = $derived(compose.isOpen && compose.inlineMode);

  /**
   * Reply to THIS specific message (not necessarily the latest). Reuses the
   * same compose plumbing as ThreadReplyBar.reply() (re #129).
   */
  async function replyToThis(): Promise<void> {
    compose.inlineMode = true;
    await compose.openReply(email);
    if (!compose.isOpen) compose.inlineMode = false;
  }

  function deleteMessage(): void {
    void mail.deleteEmail(email.id);
  }

  function reportSpam(): void {
    void mail.reportSpam(email.id, 'spam');
  }

  function reportPhishing(): void {
    void mail.reportPhishing(email.id);
  }

  // REQ-MAIL-141: download URL + suggested filename for the raw RFC 5322
  // blob. Null when the JMAP session is not yet bootstrapped — the kebab
  // item is omitted in that window. REQ-MAIL-142 (Show original) reuses
  // the same downloadEmlInfo.href as the fetch URL for the modal.
  let downloadEmlInfo = $derived.by<{ href: string; filename: string } | null>(() => {
    const accountId = auth.session?.primaryAccounts['urn:ietf:params:jmap:mail'];
    if (!accountId || !email.blobId) return null;
    const filename = emlDownloadFilename({
      subject: email.subject,
      blobId: email.blobId,
      emailId: email.id,
    });
    const href = jmap.downloadUrl({
      accountId,
      blobId: email.blobId,
      type: 'message/rfc822',
      name: filename,
    });
    if (!href) return null;
    return { href, filename };
  });

  // REQ-MAIL-142: raw RFC 5322 modal open state.
  let showOriginalOpen = $state(false);

  function showOriginal(): void {
    showOriginalOpen = true;
  }

  // REQ-MAIL-140: print this message via a popup window. Sanitises the
  // body with loadImages=true at click time so the printed copy shows
  // inline images regardless of the per-message image-blocking state.
  function printThisMessage(): void {
    const sanitised = html
      ? sanitizeHtml(html, { loadImages: true, cidMap, cidDimensions })
      : null;
    printMessage({
      subject: email.subject ?? '',
      date: formatDateTime(email.receivedAt),
      from: email.from ?? [],
      to: email.to ?? [],
      cc: email.cc ?? [],
      html: sanitised,
      text,
    });
  }

  let kebabItems = $derived.by<KebabItem[]>(() => {
    const items: KebabItem[] = [];
    // REQ-MAIL-133.
    items.push({
      id: 'markUnread',
      label: t('msg.kebab.markUnread'),
      onclick: markUnread,
    });
    // REQ-MAIL-133a.
    items.push({
      id: 'markUnreadFromHere',
      label: t('msg.kebab.markUnreadFromHere'),
      onclick: markUnreadFromHere,
    });
    // REQ-MAIL-132.
    items.push({
      id: 'delete',
      label: t('msg.kebab.delete'),
      danger: true,
      onclick: deleteMessage,
    });
    // REQ-MAIL-135.
    items.push({
      id: 'reportSpam',
      label: t('msg.reportSpam'),
      onclick: reportSpam,
    });
    // REQ-MAIL-136.
    items.push({
      id: 'reportPhishing',
      label: t('msg.reportPhishing'),
      onclick: reportPhishing,
    });
    // REQ-MAIL-141. Anchor-style item — browser handles the save dialog.
    if (downloadEmlInfo) {
      items.push({
        id: 'download',
        label: t('msg.kebab.download'),
        href: downloadEmlInfo.href,
        download: downloadEmlInfo.filename,
      });
    }
    // REQ-MAIL-142.
    if (downloadEmlInfo) {
      items.push({
        id: 'showOriginal',
        label: t('msg.kebab.showOriginal'),
        onclick: showOriginal,
      });
    }
    // REQ-MAIL-140.
    items.push({
      id: 'print',
      label: t('msg.kebab.print'),
      onclick: printThisMessage,
    });
    return items;
  });
</script>

<article class="message" class:expanded class:self={isSelf}>
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
            <span class="from-name">{isSelf ? t('mail.thread.fromYou') : senderName}</span>
            <span class="from-email">&lt;{senderEmail}&gt;</span>
          </RecipientTrigger>
        {:else}
          <span class="from-name">{isSelf ? t('mail.thread.fromYou') : senderName}</span>
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
        {#if !isInlineOpen}
          <!-- Per-message reply button (re #129): targets THIS specific message,
               not the latest. Hidden while the inline composer is already open
               so it does not compete with the active compose session. Click is
               stopped at this span so expanding/collapsing the accordion is
               not triggered. -->
          <span
            class="reply-anchor"
            onclick={(e) => e.stopPropagation()}
            onkeydown={(e) => e.stopPropagation()}
            role="presentation"
          >
            <button
              type="button"
              class="header-icon-btn"
              onclick={() => void replyToThis()}
              aria-label={t('msg.reply')}
              title={t('msg.reply')}
            >
              <ReplyIcon size={16} />
            </button>
          </span>
        {/if}
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
        </span>
      {/if}

      <span class="date">
        {formatDateTime(email.receivedAt)}{#if relativeAnnotation}&nbsp;<span class="date-relative">{relativeAnnotation}</span>{/if}
      </span>

      {#if expanded}
        <MessageKebabMenu items={kebabItems} />
      {/if}
    </span>
  </div>

  {#if expanded}
    <div class="body">
      <!-- Translation affordance: shown when the body language differs from
           the active locale; manages its own consent gate and translated
           body overlay (issue #84). -->
      <TranslateBar
        bodyText={bodyTextForDetection}
        emailText={text ?? htmlToText(html ?? '')}
        locale={i18n.locale}
      />

      {#if html}
        {#if _htmlFetching}
          <div class="loading-banner" role="status">{t('msg.body.loadingFull')}</div>
        {/if}
        {#if hasExternalImages && !loadImages}
          <div class="image-banner" role="status">
            <span>{t('msg.imagesBlocked')}</span>
            <button type="button" onclick={() => (perMessageOverride = true)}>
              {t('msg.loadImages')}
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
                {t('msg.alwaysFrom', { sender: email.from?.[0]?.email ?? '' })}
              </button>
            {/if}
          </div>
        {/if}
        <HtmlBody {html} {loadImages} {cidMap} {cidDimensions} {inlineImageMeta} />
      {:else if text && textSplit}
        {#if textSplit.head}
          <pre class="text-body">{textSplit.head}</pre>
        {/if}
        {#if textSplit.collapsed}
          {#if quotedExpanded}
            <pre class="text-body quoted">{textSplit.collapsed}</pre>
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
        {#if textSplit.tail}
          <pre class="text-body">{textSplit.tail}</pre>
        {/if}
      {:else}
        <p class="empty">(no body)</p>
      {/if}

      <AttachmentList {email} />
    </div>
  {/if}
</article>

<RawSourceModal
  open={showOriginalOpen}
  sourceUrl={downloadEmlInfo?.href ?? null}
  filename={downloadEmlInfo?.filename ?? null}
  onClose={() => (showOriginalOpen = false)}
/>

<style>
  .message {
    border-bottom: 1px solid var(--border-subtle-01);
  }

  /* Self-authored card: faint tinted background + 3px left accent border.
   * Uses the --interactive token (Carbon blue) which adapts to both dark
   * and light themes. The padding-left on .header is reduced by 3px to
   * keep text alignment identical to non-self cards. This rule must not
   * interfere with class:expanded or any selection/unread state; it is a
   * purely additive background + border treatment. */
  .message.self {
    background: color-mix(in srgb, var(--interactive) 4%, transparent);
    border-left: 3px solid var(--interactive);
  }
  .message.self .header {
    padding-left: calc(var(--spacing-05) - 3px);
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

  /* Per-message reply button anchor (re #129). Click-through stopped so the
     button does not fold/unfold the accordion. */
  .reply-anchor {
    display: inline-flex;
    align-items: center;
  }

  /* Reactions strip + react button live in a single anchor span inside
     the message header (re #98). Click-through is stopped here so
     interacting with reactions doesn't fold the accordion. */
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

  /* Loading-full banner: shown while the truncated body is being replaced
     with the full download. Deliberately small and non-blocking so the
     user can already scroll the 1 MiB prefix while the rest loads. */
  .loading-banner {
    padding: var(--spacing-02) var(--spacing-04);
    margin-bottom: var(--spacing-03);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
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
    .message.self .header {
      padding-left: calc(var(--spacing-04) - 3px);
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
