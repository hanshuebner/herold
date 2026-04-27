<script lang="ts">
  import HtmlBody from './HtmlBody.svelte';
  import { htmlHasExternalImages } from './sanitize';
  import { emailHtmlBody, emailTextBody, type Email } from './types';
  import { compose } from '../compose/compose.svelte';
  import { settings } from '../settings/settings.svelte';

  interface Props {
    email: Email;
    expanded: boolean;
    onToggle?: (id: string) => void;
  }
  let { email, expanded, onToggle }: Props = $props();

  let html = $derived(emailHtmlBody(email));
  let text = $derived(emailTextBody(email));

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
    return d.toLocaleString(undefined, {
      weekday: 'short',
      day: 'numeric',
      month: 'short',
      year: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    });
  }

  let recipientSummary = $derived(formatRecipientSummary(email));
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
        <HtmlBody {html} {loadImages} />
      {:else if text}
        <pre class="text-body">{text}</pre>
      {:else}
        <p class="empty">(no body)</p>
      {/if}

      <div class="actions">
        <button type="button" class="pill" onclick={() => compose.openReply(email)}>
          ↩ Reply
        </button>
        <button type="button" class="pill" onclick={() => compose.openForward(email)}>
          ↪ Forward
        </button>
      </div>
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
  .empty {
    margin: 0;
    padding: var(--spacing-04);
    color: var(--text-helper);
    font-style: italic;
  }
</style>
