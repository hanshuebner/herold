<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Important" {size}>
  <path d="M5 4v17" />
  <path d="M5 4h12l-2 4 2 4H5" />
</Icon>
