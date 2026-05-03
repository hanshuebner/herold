<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Edit" {size}>
  <path d="M14 4l6 6-10 10H4v-6z" />
  <path d="M13 5l6 6" />
</Icon>
