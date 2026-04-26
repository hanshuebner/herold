<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Help" {size}>
  <circle cx="12" cy="12" r="9" />
  <path d="M9.5 9a2.5 2.5 0 0 1 5 0c0 1.5-2.5 2-2.5 3.5" />
  <line x1="12" y1="17" x2="12" y2="17.01" />
</Icon>
