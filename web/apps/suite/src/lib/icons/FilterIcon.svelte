<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Filter messages like this" {size}>
  <path d="M3 5h18l-7 8v6l-4 2v-8L3 5z" />
</Icon>
