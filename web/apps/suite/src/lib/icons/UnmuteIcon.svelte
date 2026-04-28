<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Unmute thread" {size}>
  <path d="M11 5L6 9H3v6h3l5 4V5z" />
  <path d="M15 9a4 4 0 010 6" />
  <path d="M18 6a8 8 0 010 12" />
</Icon>
