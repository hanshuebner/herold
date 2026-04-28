<script lang="ts">
  /**
   * Lists every Attachment part on an Email and offers a per-row
   * Download button that links straight to the JMAP downloadUrl
   * resolved against the active session. Inline image previews appear
   * for image/* parts under 2 MB.
   */
  import type { Email, EmailBodyPart } from './types';
  import { jmap } from '../jmap/client';
  import { auth } from '../auth/auth.svelte';

  interface Props {
    email: Email;
  }
  let { email }: Props = $props();

  let accountId = $derived<string | null>(
    auth.session?.primaryAccounts['urn:ietf:params:jmap:mail'] ?? null,
  );

  let parts = $derived<EmailBodyPart[]>(email.attachments ?? []);

  function urlFor(part: EmailBodyPart): string | null {
    if (!accountId || !part.blobId) return null;
    return jmap.downloadUrl({
      accountId,
      blobId: part.blobId,
      type: part.type,
      name: part.name ?? 'attachment',
    });
  }

  function formatSize(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  }

  function isImagePreview(part: EmailBodyPart): boolean {
    return (
      part.type.startsWith('image/') &&
      part.size > 0 &&
      part.size < 2 * 1024 * 1024 // 2 MB cap on inline preview
    );
  }
</script>

{#if parts.length > 0}
  <section class="attachments" aria-label="Attachments">
    <h3>{parts.length} attachment{parts.length === 1 ? '' : 's'}</h3>
    <ul>
      {#each parts as p (p.partId ?? p.blobId)}
        {@const url = urlFor(p)}
        <li>
          <span class="icon" aria-hidden="true">📎</span>
          <span class="meta">
            <span class="name">{p.name ?? '(unnamed)'}</span>
            <span class="size">
              {p.type || 'application/octet-stream'} · {formatSize(p.size)}
            </span>
          </span>
          {#if url}
            <a class="download" href={url} download={p.name ?? 'attachment'}>Download</a>
            {#if isImagePreview(p)}
              <a class="preview" href={url} target="_blank" rel="noopener">
                <img src={url} alt={p.name ?? 'attachment preview'} loading="lazy" />
              </a>
            {/if}
          {:else}
            <span class="download disabled">No URL</span>
          {/if}
        </li>
      {/each}
    </ul>
  </section>
{/if}

<style>
  .attachments {
    margin-top: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }
  h3 {
    margin: 0 0 var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-primary);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }
  li {
    display: grid;
    grid-template-columns: auto 1fr auto;
    grid-template-areas:
      'icon meta download'
      'preview preview preview';
    gap: var(--spacing-03);
    align-items: center;
  }
  .icon {
    grid-area: icon;
    color: var(--text-helper);
    font-size: 18px;
  }
  .meta {
    grid-area: meta;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .name {
    color: var(--text-primary);
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .size {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }
  .download {
    grid-area: download;
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    text-decoration: none;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
  }
  .download:hover {
    filter: brightness(1.1);
  }
  .download.disabled {
    background: var(--layer-03);
    color: var(--text-helper);
    cursor: not-allowed;
  }
  .preview {
    grid-area: preview;
    margin-top: var(--spacing-02);
    border-radius: var(--radius-md);
    overflow: hidden;
    background: var(--layer-02);
    display: block;
  }
  .preview img {
    display: block;
    max-width: 100%;
    max-height: 320px;
    object-fit: contain;
  }
</style>
