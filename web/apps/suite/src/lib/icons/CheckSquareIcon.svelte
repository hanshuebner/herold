<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
    /** Render the square as filled-checked instead of empty. */
    checked?: boolean;
    /** Render the indeterminate (mixed-selection) marker. */
    indeterminate?: boolean;
  }
  let { size = 20, checked = false, indeterminate = false }: Props = $props();
</script>

<Icon label={checked ? 'Checked' : indeterminate ? 'Some selected' : 'Unchecked'} {size}>
  <rect x="3" y="3" width="18" height="18" rx="3" />
  {#if checked}
    <path d="M7 12l3 3 7-8" />
  {:else if indeterminate}
    <path d="M8 12h8" />
  {/if}
</Icon>
