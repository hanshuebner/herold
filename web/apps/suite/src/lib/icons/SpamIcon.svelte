<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Report spam" {size}>
  <path d="M12 3l10 18H2L12 3z" />
  <path d="M12 10v4" />
  <path d="M12 17h.01" />
</Icon>
