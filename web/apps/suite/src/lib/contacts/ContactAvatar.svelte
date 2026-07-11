<script lang="ts">
  /**
   * Contact avatar — renders a contact photo fetched via the JMAP blob
   * download path, or falls back to a deterministic monogram initial.
   *
   * The download URL is constructed from the session's downloadUrl template
   * for the contacts account. The browser's HTTP cache handles in-session
   * caching; no additional client-side caching layer is needed.
   *
   * REQ-CONT-62: monogram fallback when no photo is present.
   * REQ-CONT-63: photos fetched via JMAP blob download path only; never
   *              third-party avatar services.
   */

  import { jmap } from '../jmap/client';
  import { Capability } from '../jmap/types';
  import { auth } from '../auth/auth.svelte';

  interface Props {
    /** Blob ID from the JSContact media/photo entry. Null renders monogram. */
    blobId: string | null;
    /** Character shown as the monogram fallback. */
    fallbackInitial: string;
    /** Width and height in pixels (default 36). */
    size?: number;
    /** Display name used as the img alt text; omit to silence screen readers. */
    displayName?: string;
  }

  let {
    blobId,
    fallbackInitial,
    size = 36,
    displayName = '',
  }: Props = $props();

  let imgError = $state(false);

  $effect(() => {
    // Reset img error when the blobId changes so a new photo is attempted.
    const _blobId = blobId;
    imgError = false;
  });

  let photoUrl = $derived(
    blobId && auth.session
      ? jmap.downloadUrl({
          accountId: auth.session.primaryAccounts[Capability.Contacts] ?? '',
          blobId,
          type: 'image/jpeg',
          name: 'photo.jpg',
          disposition: 'inline',
        })
      : null,
  );

  let showImg = $derived(photoUrl !== null && !imgError);
  let initial = $derived(fallbackInitial.slice(0, 1).toUpperCase() || '?');
</script>

<span
  class="contact-avatar"
  style:width="{size}px"
  style:height="{size}px"
  aria-hidden="true"
>
  {#if showImg}
    <img
      src={photoUrl ?? ''}
      alt={displayName || initial}
      width={size}
      height={size}
      class="photo-img"
      onerror={() => { imgError = true; }}
    />
  {:else}
    {initial}
  {/if}
</span>

<style>
  .contact-avatar {
    border-radius: var(--radius-pill);
    background: var(--interactive);
    color: var(--text-on-color);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    overflow: hidden;
    flex-shrink: 0;
  }

  .photo-img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    border-radius: var(--radius-pill);
    display: block;
  }
</style>
