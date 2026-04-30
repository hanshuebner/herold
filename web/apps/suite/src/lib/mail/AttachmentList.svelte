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
  import Lightbox from '../preview/Lightbox.svelte';

  interface Props {
    email: Email;
  }
  let { email }: Props = $props();

  let accountId = $derived<string | null>(
    auth.session?.primaryAccounts['urn:ietf:params:jmap:mail'] ?? null,
  );

  let allParts = $derived<EmailBodyPart[]>(email.attachments ?? []);

  /** Regular attachments: disposition=attachment or no disposition set but not inline. */
  let attachParts = $derived(
    allParts.filter((p) => p.disposition !== 'inline'),
  );

  /**
   * Inline image parts: disposition=inline with a cid. These belong to the
   * rendered body (HtmlBody resolves them via the cidMap and shows them
   * inline). REQ-MAIL-21: inline images must not appear in the attachment
   * list at all — even when cid resolution failed at render time, the user
   * intent is "this is part of the body" and a duplicate chip is a worse
   * answer than an inline broken-image icon. The "inlineParts" channel
   * still exists for the bulk-download "Download all" action so the user
   * can grab the original bytes; it is no longer rendered as a chip strip.
   */
  let inlineParts = $derived(
    allParts.filter((p) => p.disposition === 'inline'),
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

  /**
   * isViewable: the attachment can be previewed in-browser without a
   * native viewer launch. REQ-MAIL-23: image and PDF parts get a "View"
   * affordance; everything else falls through to "Download" only.
   */
  function isViewable(part: EmailBodyPart): boolean {
    if (!part.type) return false;
    if (part.type.startsWith('image/')) return true;
    if (part.type === 'application/pdf') return true;
    return false;
  }

  type LightboxState = {
    url: string;
    name: string;
    kind: 'image' | 'pdf';
  };
  let lightbox = $state<LightboxState | null>(null);

  function openViewer(part: EmailBodyPart): void {
    openLightbox(part);
  }

  function openLightbox(part: EmailBodyPart): void {
    if (!accountId || !part.blobId) return;
    const kind: LightboxState['kind'] | null = part.type.startsWith('image/')
      ? 'image'
      : part.type === 'application/pdf'
        ? 'pdf'
        : null;
    if (!kind) return;
    // Use Content-Disposition: inline so the iframe / img renders the
    // resource in-page instead of the browser handing it to the
    // download UI. Without this the PDF lightbox ships a blank pane and
    // the browser pops a download prompt.
    const url = jmap.downloadUrl({
      accountId,
      blobId: part.blobId,
      type: part.type,
      name: part.name ?? 'attachment',
      disposition: 'inline',
    });
    if (!url) return;
    lightbox = { url, name: part.name ?? 'attachment', kind };
  }

  function closeLightbox(): void {
    lightbox = null;
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

{#if attachParts.length > 0}
  <section class="attachments" aria-label="Attachments">
    <h3>
      {attachParts.length === 1
        ? t('att.attachments', { count: attachParts.length })
        : t('att.attachments.other', { count: attachParts.length })}
    </h3>
    <ul>
      {#each attachParts as p (p.partId ?? p.blobId)}
        {@const url = urlFor(p)}
        {@const previewable = isViewable(p)}
        <li class:image-att={isImagePreview(p)}>
          {#if isImagePreview(p) && url}
            <button
              type="button"
              class="thumb-button"
              aria-label={`Open ${p.name ?? 'image'} in lightbox`}
              onclick={() => openLightbox(p)}
            >
              <img class="thumb" src={url} alt={p.name ?? 'attachment preview'} loading="lazy" />
            </button>
          {:else}
            <span class="icon" aria-hidden="true">&#128206;</span>
          {/if}
          <span class="meta">
            <span class="name">{p.name ?? '(unnamed)'}</span>
            <span class="size">
              {p.type || 'application/octet-stream'} · {formatSize(p.size)}
            </span>
          </span>
          <span class="actions">
            {#if url && previewable}
              <button
                type="button"
                class="overlay view"
                onclick={() => openViewer(p)}
              >{t('att.view')}</button>
            {/if}
            {#if url}
              <a class="overlay download" href={url} download={p.name ?? 'attachment'}>{t('att.download')}</a>
            {:else}
              <span class="overlay download disabled">{t('att.noUrl')}</span>
            {/if}
          </span>
        </li>
      {/each}
    </ul>

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

{#if lightbox}
  <Lightbox
    src={lightbox.url}
    name={lightbox.name}
    kind={lightbox.kind}
    onClose={closeLightbox}
  />
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
    grid-template-areas: 'icon meta actions';
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

  /* Image-attachment thumbnail column (replaces icon for image parts). */
  .thumb-button {
    grid-area: icon;
    display: block;
    width: 64px;
    height: 64px;
    padding: 0;
    border: 0;
    border-radius: var(--radius-sm);
    overflow: hidden;
    background: var(--layer-02);
    cursor: zoom-in;
  }
  .thumb {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }
  .actions {
    grid-area: actions;
    display: flex;
    gap: var(--spacing-02);
  }
  .overlay {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-pill);
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    text-decoration: none;
    cursor: pointer;
  }
  .overlay.view {
    background: var(--layer-02);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
  }
  .overlay.view:hover {
    background: var(--layer-03);
  }
  .overlay.download {
    background: var(--interactive);
    color: var(--text-on-color);
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
  }
  .overlay.download:hover {
    filter: brightness(1.1);
  }
  .overlay.download.disabled {
    background: var(--layer-03);
    color: var(--text-helper);
    cursor: not-allowed;
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
