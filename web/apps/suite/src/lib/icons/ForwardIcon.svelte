<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Forward" {size}>
  <path d="M15 17l6-6-6-6" />
  <path d="M21 11H10a6 6 0 00-6 6v2" />
</Icon>
