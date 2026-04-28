<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="React" {size}>
  <circle cx="12" cy="12" r="9" />
  <path d="M9 10h.01" />
  <path d="M15 10h.01" />
  <path d="M8.5 14.5a4 4 0 007 0" />
</Icon>
