<script lang="ts">
  /**
   * Lists attachment parts from an Email as Gmail-style cards.
   *
   * G16 (REQ-ATT-26, REQ-ATT-40, REQ-ATT-41):
   *   - Each card shows a type-icon area (thumbnail for images; coloured
   *     badge for everything else), filename, and size + type.
   *   - A bottom-right dog-ear decoration echoes the Gmail visual language.
   *   - On hover / focus-within, a translucent overlay reveals icon-only
   *     "View" and "Download" affordances (CSS-only reveal).
   *   - Clicking the card body opens the shared Lightbox (REQ-MAIL-23).
   *   - "Download all (N)" zips attachments + inline images via fflate;
   *     inline images are placed under an `inline/` prefix in the zip.
   *   - "Attachments only" secondary action excludes inline images.
   *
   * REQ-MAIL-21: inline images (disposition=inline) are excluded from the
   * card strip entirely. They belong to the rendered body. The inlineParts
   * channel is kept only for the "Download all" bulk action.
   *
   * Architecture note: inline-image overlay buttons on the rendered iframe
   * body live in HtmlBody.svelte. This component handles the card strip and
   * bulk download only.
   */
  import type { Email, EmailBodyPart } from './types';
  import { jmap } from '../jmap/client';
  import { auth } from '../auth/auth.svelte';
  import { t } from '../i18n/i18n.svelte';
  import { zipBlobsAsDownload } from './download-zip';
  import { attachmentIcon } from './attachment-icon';
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
   * can grab the original bytes; it is no longer rendered as a card.
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
        {@const icon = attachmentIcon(p)}
        {@const viewable = isViewable(p)}
        {@const displayName = p.name ?? '(unnamed)'}
        <li>
          <!--
            The outer <li> is the card. Clicking anywhere on it (except the
            download anchor) opens the lightbox when the part is viewable.
            Non-viewable parts use the card only as a download trigger.
          -->
          <button
            type="button"
            class="card"
            class:has-thumb={icon.kind === 'thumbnail'}
            aria-label={t('att.aria.open', { name: displayName })}
            onclick={() => { if (viewable) openLightbox(p); }}
            tabindex="0"
          >
            <!-- Left: type-icon area -->
            <div class="card-icon" style={icon.kind === 'badge' ? `--icon-bg: ${icon.bg}` : undefined}>
              {#if icon.kind === 'thumbnail' && url}
                <img
                  class="thumb"
                  src={url}
                  alt={displayName}
                  loading="lazy"
                />
              {:else if icon.kind === 'badge'}
                <span class="badge-label" aria-hidden="true">{icon.label}</span>
              {:else}
                <span class="badge-label" aria-hidden="true">FILE</span>
              {/if}
            </div>

            <!-- Right: filename + size / type -->
            <div class="card-meta">
              <span class="card-name">{displayName}</span>
              <span class="card-sub">
                {p.type || 'application/octet-stream'} &middot; {formatSize(p.size)}
              </span>
            </div>

            <!-- Bottom-right dog-ear decoration -->
            <div class="dog-ear" aria-hidden="true"></div>

            <!-- Hover / focus-within action overlay -->
            <div class="card-overlay" aria-hidden="true">
              {#if viewable}
                <!-- View icon: simplified eye SVG -->
                <span class="overlay-btn view-icon" title={t('att.view')}>
                  <svg width="18" height="18" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                    <ellipse cx="10" cy="10" rx="8" ry="5" stroke="currentColor" stroke-width="1.8"/>
                    <circle cx="10" cy="10" r="2.5" fill="currentColor"/>
                  </svg>
                </span>
              {/if}
              {#if url}
                <!-- Download icon: arrow-down SVG; uses <a> so browser handles save -->
                <a
                  class="overlay-btn download-icon"
                  href={url}
                  download={displayName}
                  title={t('att.aria.download', { name: displayName })}
                  aria-label={t('att.aria.download', { name: displayName })}
                  onclick={(e) => e.stopPropagation()}
                  tabindex="-1"
                >
                  <svg width="18" height="18" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                    <path d="M10 3v10M6 10l4 4 4-4" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
                    <path d="M4 17h12" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>
                  </svg>
                </a>
              {/if}
            </div>
          </button>
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
    flex-wrap: wrap;
    gap: var(--spacing-03);
  }

  li {
    /* Each <li> is a flex item — the card button fills it. */
    display: contents;
  }

  /* ── Card ────────────────────────────────────────────────────────── */

  .card {
    position: relative;
    display: grid;
    grid-template-columns: 72px 1fr;
    grid-template-rows: 1fr auto;
    grid-template-areas:
      'icon meta'
      'icon sub';
    width: 240px;
    height: 72px;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    overflow: hidden;
    cursor: pointer;
    text-align: left;
    padding: 0;
    transition: box-shadow var(--duration-fast-02) var(--easing-productive-enter);
  }

  .card:hover,
  .card:focus-within {
    box-shadow: 0 2px 8px rgba(0, 0, 0, 0.18);
    outline: none;
  }

  .card:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }

  /* Taller card when showing a thumbnail */
  .card.has-thumb {
    height: 96px;
  }

  /* ── Icon area (left column) ─────────────────────────────────────── */

  .card-icon {
    grid-area: icon;
    grid-row: 1 / -1;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--icon-bg, var(--layer-03));
    width: 72px;
    /* full card height */
    align-self: stretch;
  }

  .has-thumb .card-icon {
    width: 72px;
  }

  .badge-label {
    color: #fff;
    font-weight: 700;
    font-size: 13px;
    letter-spacing: 0.04em;
    pointer-events: none;
    user-select: none;
  }

  .thumb {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }

  /* ── Text metadata (right column) ───────────────────────────────── */

  .card-meta {
    grid-column: 2;
    grid-row: 1 / -1;
    display: flex;
    flex-direction: column;
    justify-content: center;
    padding: var(--spacing-02) var(--spacing-04) var(--spacing-02) var(--spacing-03);
    overflow: hidden;
  }

  .card-name {
    display: block;
    color: var(--text-primary);
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding-right: var(--spacing-05); /* leave room for dog-ear */
  }

  .card-sub {
    display: block;
    color: var(--text-helper);
    font-size: var(--type-helper-text-01-size, 11px);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding-right: var(--spacing-05);
  }

  /* ── Dog-ear decoration (bottom-right corner) ───────────────────── */

  .dog-ear {
    position: absolute;
    bottom: 0;
    right: 0;
    width: 14px;
    height: 14px;
    /*
     * Two-colour triangle: bottom-right corner of the card.
     * The outer triangle is the card background (layer-02); the cut-out
     * is the border colour. We use a clipped gradient to achieve this
     * without an extra pseudo-element on older browsers.
     */
    background: linear-gradient(
      225deg,
      var(--border-subtle-01) 0%,
      var(--border-subtle-01) 50%,
      var(--layer-02) 50%
    );
    pointer-events: none;
  }

  /* ── Hover / focus-within overlay ───────────────────────────────── */

  .card-overlay {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--spacing-02);
    background: rgba(0, 0, 0, 0.45);
    border-radius: var(--radius-md);
    opacity: 0;
    pointer-events: none;
    transition: opacity var(--duration-fast-02) var(--easing-productive-enter);
  }

  .card:hover .card-overlay,
  .card:focus-within .card-overlay {
    opacity: 1;
    pointer-events: auto;
  }

  .overlay-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: var(--radius-pill);
    background: rgba(255, 255, 255, 0.15);
    border: 1px solid rgba(255, 255, 255, 0.35);
    color: #fff;
    cursor: pointer;
    text-decoration: none;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .overlay-btn:hover {
    background: rgba(255, 255, 255, 0.28);
  }

  /* ── Bulk download bar ───────────────────────────────────────────── */

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
