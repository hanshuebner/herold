<script lang="ts">
  import { defaultT } from '../../chrome/strings.js';
  import { tokenize } from '../../markdoc/tokenize.js';

  interface Props {
    file: string;
    lang?: string;
    content?: string;
    t?: (key: string) => string;
  }

  const { file, lang = 'text', content = '', t = defaultT }: Props = $props();

  const tokens = $derived(tokenize(content, lang));
  let copied = $state(false);

  function copy() {
    void navigator.clipboard.writeText(content).then(() => {
      copied = true;
      setTimeout(() => {
        copied = false;
      }, 1500);
    });
  }
</script>

<div class="code-block">
  <div class="code-block__header">
    <span class="code-block__filename">{file}</span>
    <span class="code-block__lang">{lang}</span>
    <button
      type="button"
      class="code-block__copy"
      onclick={copy}
      aria-label={t('manual.code.copy')}
    >
      {copied ? t('manual.code.copied') : t('manual.code.copy')}
    </button>
  </div>
  <pre class="code-block__pre"><code class="code-block__code lang-{lang}"
    >{#each tokens as token}<span class="tok tok--{token.type}"
        >{token.text}</span
      >{/each}</code></pre>
</div>

<style>
  .code-block {
    margin: var(--spacing-05) 0;
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    overflow: hidden;
    background: var(--layer-02);
  }

  .code-block__header {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-03);
    border-bottom: 1px solid var(--border-subtle-01);
    font-size: var(--type-code-01-size);
    line-height: var(--type-code-01-line);
  }

  .code-block__filename {
    flex: 1;
    color: var(--text-secondary);
    font-family: var(--font-mono);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .code-block__lang {
    color: var(--text-helper);
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  .code-block__copy {
    color: var(--text-helper);
    font-size: var(--type-code-01-size);
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-sm);
    border: 1px solid transparent;
  }

  .code-block__copy:hover {
    color: var(--text-primary);
    background: var(--layer-01);
    border-color: var(--border-subtle-01);
  }

  .code-block__pre {
    margin: 0;
    padding: var(--spacing-04);
    overflow-x: auto;
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
    line-height: var(--type-code-02-line);
    color: var(--text-primary);
  }

  .code-block__code {
    display: block;
  }

  /* Token colours (minimal subset, no external tokenizer dependency). */
  :global(.tok--keyword)  { color: #78a9ff; }
  :global(.tok--string)   { color: #42be65; }
  :global(.tok--comment)  { color: var(--text-helper); font-style: italic; }
  :global(.tok--number)   { color: #ff7eb6; }
  :global(.tok--operator) { color: #be95ff; }
  :global(.tok--punct)    { color: var(--text-secondary); }
  :global(.tok--text)     { color: var(--text-primary); }
  :global(.tok--key)      { color: #78a9ff; }
  :global(.tok--value)    { color: #42be65; }
  :global(.tok--section)  { color: #ff7eb6; font-weight: 600; }
</style>
