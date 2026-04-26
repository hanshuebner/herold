<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Search" {size}>
  <circle cx="11" cy="11" r="6" />
  <path d="M16 16l4 4" />
</Icon>
