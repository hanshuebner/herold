<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Download" {size}>
  <path d="M12 4v11" />
  <path d="M7 10l5 5 5-5" />
  <path d="M5 20h14" />
</Icon>
