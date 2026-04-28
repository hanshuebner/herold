<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Restore" {size}>
  <path d="M3 7v6h6" />
  <path d="M3 13a9 9 0 109-9" />
</Icon>
