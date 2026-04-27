<script lang="ts">
  // Tiny icon wrapper. Inline SVG so we don't pull in a dependency yet.
  // Each named icon is a separate component below; this is the shared base.
  interface Props {
    label: string;
    size?: number;
    children?: import('svelte').Snippet;
  }
  let { label, size = 20, children }: Props = $props();
</script>

<svg
  xmlns="http://www.w3.org/2000/svg"
  viewBox="0 0 24 24"
  width={size}
  height={size}
  fill="none"
  stroke="currentColor"
  stroke-width="1.6"
  stroke-linecap="round"
  stroke-linejoin="round"
  role="img"
  aria-label={label}
>
  {@render children?.()}
</svg>
