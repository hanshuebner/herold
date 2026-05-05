<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Edit" {size}>
  <!-- pencil: body + tip -->
  <path d="M17.5 2.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4 13.5-13.5z" />
</Icon>
