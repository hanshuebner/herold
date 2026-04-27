<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Mail" {size}>
  <rect x="3" y="5" width="18" height="14" rx="2" />
  <path d="M3 7l9 7 9-7" />
</Icon>
