<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Calendar" {size}>
  <rect x="3" y="4" width="18" height="17" rx="2" />
  <line x1="3" y1="9" x2="21" y2="9" />
  <line x1="8" y1="2" x2="8" y2="6" />
  <line x1="16" y1="2" x2="16" y2="6" />
</Icon>
