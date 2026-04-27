<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Chat" {size}>
  <path d="M4 5h16v10H8l-4 4z" />
</Icon>
