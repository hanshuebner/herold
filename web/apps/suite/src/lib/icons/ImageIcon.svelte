<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Insert image" {size}>
  <rect x="3" y="4" width="18" height="16" rx="2" />
  <circle cx="9" cy="10" r="2" />
  <path d="M3 17l5-5 4 4 3-3 6 6" />
</Icon>
