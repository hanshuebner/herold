<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Video call" {size}>
  <rect x="3" y="6" width="13" height="12" rx="2" />
  <path d="M16 10l5-3v10l-5-3z" />
</Icon>
