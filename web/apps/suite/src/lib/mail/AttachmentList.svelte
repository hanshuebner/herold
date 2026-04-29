<script lang="ts">
  /**
   * Lists attachment and inline-image parts from an Email.
   *
   * G16 (REQ-ATT-26, REQ-ATT-40, REQ-ATT-41):
   *   - Attachment chips: filename, size, type, download button.
   *   - Inline images sub-section: same chip format, thumbnail included.
   *   - "Download all (N)" zips attachments + inline images via fflate;
   *     inline images are placed under an `inline/` prefix in the zip.
   *   - "Attachments only" secondary action excludes inline images.
   *
   * Architecture note: inline-image overlay buttons on the rendered
   * iframe body live in HtmlBody.svelte (positioned via ResizeObserver
   * over the iframe). This component handles the chip strip and bulk
   * download only.
   */
  import type { Email, EmailBodyPart } from './types';
  import { jmap } from '../jmap/client';
  import { auth } from '../auth/auth.svelte';
  import { t } from '../i18n/i18n.svelte';
  import { zipBlobsAsDownload } from './download-zip';

  interface Props {
    email: Email;
    /**
     * Set of cid values that were successfully resolved to blob URLs by the
     * HTML body renderer (cidMap keys from MessageAccordion). Parts whose cid
     * appears here are already rendered inline in the message body; showing
     * them again as attachment chips would be a duplicate (defect 3, re #1).
     */
    resolvedCids?: Set<string>;
  }
  let { email, resolvedCids }: Props = $props();

  let accountId = $derived<string | null>(
    auth.session?.primaryAccounts['urn:ietf:params:jmap:mail'] ?? null,
  );

  let allParts = $derived<EmailBodyPart[]>(email.attachments ?? []);

  /** Regular attachments: disposition=attachment or no disposition set but not inline. */
  let attachParts = $derived(
    allParts.filter((p) => p.disposition !== 'inline'),
  );

  /**
   * Inline image parts: disposition=inline with a cid. These are the parts
   * the HTML body references via cid: URLs. We surface them in a separate
   * sub-section so each is independently downloadable (REQ-ATT-26).
   *
   * Parts whose cid was resolved to an inline reference (in resolvedCids) are
   * excluded here because they already appear rendered in the message body;
   * showing them again would be a duplicate (defect 3, re #1).
   */
  let inlineParts = $derived(
    allParts.filter(
      (p) =>
        p.disposition === 'inline' &&
        !(p.cid && resolvedCids?.has(p.cid)),
    ),
  );

  let totalCount = $derived(attachParts.length + inlineParts.length);

  let downloading = $state(false);

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
      part.size < 2 * 1024 * 1024 // 2 MB cap on inline thumbnail
    );
  }

  /** Derive a safe filename for a part that has no name field. */
  function fallbackName(part: EmailBodyPart, index: number): string {
    const ext = part.type.split('/')[1] ?? 'bin';
    return `inline-${index + 1}.${ext}`;
  }

  async function downloadAll(includeInline: boolean): Promise<void> {
    if (!accountId || downloading) return;
    const subject = email.subject ?? 'attachments';
    const parts: { url: string; zipPath: string }[] = [];

    for (const p of attachParts) {
      const url = urlFor(p);
      if (!url) continue;
      parts.push({ url, zipPath: p.name ?? 'attachment' });
    }

    if (includeInline) {
      inlineParts.forEach((p, i) => {
        const url = urlFor(p);
        if (!url) return;
        const name = p.name ?? fallbackName(p, i);
        parts.push({ url, zipPath: `inline/${name}` });
      });
    }

    if (parts.length === 0) return;
    downloading = true;
    try {
      await zipBlobsAsDownload(parts, `${subject}.zip`);
    } finally {
      downloading = false;
    }
  }
</script>

{#if totalCount > 0}
  <section class="attachments" aria-label="Attachments">
    {#if attachParts.length > 0}
      <h3>
        {attachParts.length === 1
          ? t('att.attachments', { count: attachParts.length })
          : t('att.attachments.other', { count: attachParts.length })}
      </h3>
      <ul>
        {#each attachParts as p (p.partId ?? p.blobId)}
          {@const url = urlFor(p)}
          <li>
            <span class="icon" aria-hidden="true">&#128206;</span>
            <span class="meta">
              <span class="name">{p.name ?? '(unnamed)'}</span>
              <span class="size">
                {p.type || 'application/octet-stream'} · {formatSize(p.size)}
              </span>
            </span>
            {#if url}
              <a class="download" href={url} download={p.name ?? 'attachment'}>{t('att.download')}</a>
              {#if isImagePreview(p)}
                <a class="preview" href={url} target="_blank" rel="noopener">
                  <img src={url} alt={p.name ?? 'attachment preview'} loading="lazy" />
                </a>
              {/if}
            {:else}
              <span class="download disabled">{t('att.noUrl')}</span>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}

    {#if inlineParts.length > 0}
      <h3 class="inline-header">{t('att.inlineImages')}</h3>
      <ul>
        {#each inlineParts as p, i (p.partId ?? p.blobId)}
          {@const url = urlFor(p)}
          {@const name = p.name ?? fallbackName(p, i)}
          <li>
            {#if url && isImagePreview(p)}
              <a class="thumb-link" href={url} target="_blank" rel="noopener">
                <img class="thumb" src={url} alt={name} loading="lazy" />
              </a>
            {:else}
              <span class="icon" aria-hidden="true">&#128247;</span>
            {/if}
            <span class="meta">
              <span class="name">{name}</span>
              <span class="size">{p.type} · {formatSize(p.size)}</span>
            </span>
            {#if url}
              <a class="download" href={url} download={name}>{t('att.download')}</a>
            {:else}
              <span class="download disabled">{t('att.noUrl')}</span>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}

    {#if totalCount > 1}
      <div class="bulk-actions">
        <button
          type="button"
          class="download-all"
          disabled={downloading}
          onclick={() => void downloadAll(true)}
        >
          {t('att.downloadAll', { count: totalCount })}
        </button>
        {#if inlineParts.length > 0 && attachParts.length > 0}
          <button
            type="button"
            class="attachments-only"
            disabled={downloading}
            onclick={() => void downloadAll(false)}
          >
            {t('att.attachmentsOnly')}
          </button>
        {/if}
      </div>
    {/if}
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
  .inline-header {
    margin-top: var(--spacing-04);
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

  /* Inline image thumbnail column */
  .thumb-link {
    grid-area: icon;
    display: block;
    width: 48px;
    height: 48px;
    border-radius: var(--radius-sm);
    overflow: hidden;
    background: var(--layer-02);
  }
  .thumb {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }

  /* Bulk download bar */
  .bulk-actions {
    display: flex;
    gap: var(--spacing-03);
    margin-top: var(--spacing-04);
    flex-wrap: wrap;
  }
  .download-all {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
  }
  .download-all:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .download-all:disabled {
    opacity: 0.5;
    cursor: progress;
  }
  .attachments-only {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-02);
    color: var(--text-secondary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .attachments-only:hover:not(:disabled) {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .attachments-only:disabled {
    opacity: 0.5;
    cursor: progress;
  }
</style>
