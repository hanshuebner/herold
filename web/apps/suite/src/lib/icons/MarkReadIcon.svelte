<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Mark as read" {size}>
  <path d="M3 7l9 6 9-6" />
  <rect x="3" y="5" width="18" height="14" rx="2" />
  <path d="M8 12l3 3 5-6" />
</Icon>
