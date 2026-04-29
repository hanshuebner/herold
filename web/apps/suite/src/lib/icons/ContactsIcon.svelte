<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Contacts" {size}>
  <circle cx="12" cy="8" r="4" />
  <path d="M4 20c0-4 3.6-7 8-7s8 3 8 7" />
</Icon>
