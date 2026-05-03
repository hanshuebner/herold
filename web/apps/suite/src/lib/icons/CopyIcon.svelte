<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Copy" {size}>
  <rect x="9" y="9" width="11" height="11" rx="2" />
  <path d="M5 15V5a2 2 0 0 1 2-2h10" />
</Icon>
