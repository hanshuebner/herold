<script lang="ts">
  /**
   * Sender avatar component.
   *
   * Renders the initial-letter fallback immediately, then kicks off an
   * async resolve via the avatar resolver.  When the resolver returns a
   * URL the component swaps in an <img>; if the URL turns out broken
   * (network error, revoked blob:) it falls back to the initial again.
   *
   * Props:
   *   email           - sender email address used as the resolver key.
   *   fallbackInitial - single character shown while resolving / on error.
   *   size            - width and height in pixels (default 32).
   *   messageHeaders  - optional Face/X-Face headers from the email.
   */

  import { onMount } from 'svelte';
  import { resolve } from '../mail/avatar-resolver.svelte';
  import type { Identity } from '../mail/types';

  interface Props {
    email: string;
    fallbackInitial: string;
    size?: number;
    ownIdentities?: Identity[];
    messageHeaders?: { face?: string; xFace?: string };
  }
  let {
    email,
    fallbackInitial,
    size = 32,
    ownIdentities = [],
    messageHeaders,
  }: Props = $props();

  let resolvedUrl = $state<string | null>(null);
  let imgError = $state(false);

  // Re-resolve whenever the email changes.
  $effect(() => {
    const _email = email;
    resolvedUrl = null;
    imgError = false;
    void resolveAvatar();
  });

  async function resolveAvatar(): Promise<void> {
    const url = await resolve(email, ownIdentities, messageHeaders);
    resolvedUrl = url;
    imgError = false;
  }

  onMount(() => {
    void resolveAvatar();
  });

  function handleImgError(): void {
    imgError = true;
  }

  let showImg = $derived(resolvedUrl !== null && !imgError);
  let initial = $derived(fallbackInitial.slice(0, 1).toUpperCase());
</script>

<span
  class="avatar"
  style:width="{size}px"
  style:height="{size}px"
  aria-hidden="true"
>
  {#if showImg}
    <img
      src={resolvedUrl ?? ''}
      alt={initial}
      width={size}
      height={size}
      class="avatar-img"
      onerror={handleImgError}
    />
  {:else}
    {initial}
  {/if}
</span>

<style>
  .avatar {
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

  .avatar-img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    border-radius: var(--radius-pill);
    display: block;
  }
</style>
