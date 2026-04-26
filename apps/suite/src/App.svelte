<script lang="ts">
  // Placeholder shell. Real layout primitives land in the next commit.
</script>

<main>
  <h1>Tabard</h1>
  <p class="lead">JMAP suite — design tokens loaded.</p>

  <section class="swatches">
    <h2>Theme tokens</h2>
    <div class="grid">
      <span class="swatch" style="background: var(--background);">background</span>
      <span class="swatch" style="background: var(--layer-01);">layer-01</span>
      <span class="swatch" style="background: var(--layer-02);">layer-02</span>
      <span class="swatch" style="background: var(--layer-03);">layer-03</span>
      <span class="swatch interactive">interactive</span>
      <span class="swatch error">support-error</span>
      <span class="swatch success">support-success</span>
      <span class="swatch warning">support-warning</span>
    </div>
  </section>
</main>

<style>
  main {
    padding: var(--spacing-06);
    max-width: 60ch;
    margin: 0 auto;
    min-height: 100vh;
    font-family: var(--font-sans);
  }
  h1 {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    margin: 0 0 var(--spacing-03);
  }
  .lead {
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-07);
  }
  h2 {
    font-size: var(--type-heading-compact-01-size);
    line-height: var(--type-heading-compact-01-line);
    font-weight: var(--type-heading-compact-01-weight);
    margin: 0 0 var(--spacing-04);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: var(--spacing-03);
  }
  .swatch {
    padding: var(--spacing-04);
    border-radius: var(--radius-md);
    border: 1px solid var(--border-subtle-01);
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-primary);
  }
  .interactive {
    background: var(--interactive);
    color: var(--text-on-color);
  }
  .error {
    background: var(--support-error);
    color: var(--text-on-color);
  }
  .success {
    background: var(--support-success);
    color: var(--text-on-color);
  }
  .warning {
    background: var(--support-warning);
    color: var(--text-primary);
  }
</style>
