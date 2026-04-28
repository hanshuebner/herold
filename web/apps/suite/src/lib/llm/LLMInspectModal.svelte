<script lang="ts">
  /**
   * Per-message LLM inspect modal — G14, REQ-CAT-44, REQ-FILT-66.
   *
   * Triggered from the message overflow menu ("Show classification").
   * Calls Email/llmInspect and renders the assigned category, spam verdict,
   * confidence, reason, prompt-as-applied, model identifier, and the
   * operator-supplied disclosureNote from LLMTransparency/get.
   */
  import { llmTransparency } from './transparency.svelte';
  import type { MessageLLMInspect } from './transparency.svelte';

  interface Props {
    emailId: string;
    onClose: () => void;
  }

  let { emailId, onClose }: Props = $props();

  type ModalStatus = 'loading' | 'ready' | 'error';

  let status = $state<ModalStatus>('loading');
  let result = $state<MessageLLMInspect | null>(null);
  let errorMsg = $state<string | null>(null);

  $effect(() => {
    status = 'loading';
    errorMsg = null;
    llmTransparency
      .fetchInspect(emailId)
      .then((r) => {
        result = r;
        status = 'ready';
      })
      .catch((err: unknown) => {
        errorMsg = err instanceof Error ? err.message : String(err);
        status = 'error';
      });
  });

  // Ensure the transparency singleton is loaded for the disclosure note.
  $effect(() => {
    if (llmTransparency.loadStatus === 'idle') {
      void llmTransparency.load();
    }
  });

  let disclosureNote = $derived(llmTransparency.data?.disclosureNote ?? '');

  function handleKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape') onClose();
  }

  function confidencePct(c: number): string {
    return `${Math.round(c * 100)}%`;
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
  class="backdrop"
  role="dialog"
  aria-modal="true"
  aria-label="Message classification"
  tabindex="-1"
  onkeydown={handleKeydown}
>
  <div class="modal">
    <div class="modal-header">
      <h2>Message classification</h2>
      <button type="button" class="close-btn" aria-label="Close" onclick={onClose}>
        &#10005;
      </button>
    </div>

    <div class="modal-body">
      {#if status === 'loading'}
        <p class="hint">Loading…</p>
      {:else if status === 'error'}
        <p class="error" role="alert">{errorMsg}</p>
      {:else if result === null || (!result?.spam && !result?.category)}
        <p class="empty-state">
          This message was not classified. It may have been delivered before the
          classifier was configured, or classification may have failed.
        </p>
      {:else}
        {#if result.category}
          <section class="inspect-section">
            <h3>Category</h3>
            <div class="row">
              <span class="label">Assigned</span>
              <span class="value">{result.category.assigned}</span>
            </div>
            <div class="row">
              <span class="label">Confidence</span>
              <span class="value">{confidencePct(result.category.confidence)}</span>
            </div>
            <div class="row">
              <span class="label">Reason</span>
              <span class="value">{result.category.reason}</span>
            </div>
            <div class="row">
              <span class="label">Model</span>
              <span class="value mono">{result.category.model}</span>
            </div>
            <div class="row">
              <span class="label">Classified at</span>
              <span class="value">{new Date(result.category.classifiedAt).toLocaleString()}</span>
            </div>
            <div class="prompt-block">
              <p class="prompt-header">Prompt used for this message</p>
              <pre class="prompt-text">{result.category.promptApplied}</pre>
            </div>
          </section>
        {/if}

        {#if result.spam}
          <section class="inspect-section">
            <h3>Spam classification</h3>
            <div class="row">
              <span class="label">Verdict</span>
              <span class="value verdict-{result.spam.verdict}">{result.spam.verdict}</span>
            </div>
            <div class="row">
              <span class="label">Confidence</span>
              <span class="value">{confidencePct(result.spam.confidence)}</span>
            </div>
            <div class="row">
              <span class="label">Reason</span>
              <span class="value">{result.spam.reason}</span>
            </div>
            <div class="row">
              <span class="label">Model</span>
              <span class="value mono">{result.spam.model}</span>
            </div>
            <div class="row">
              <span class="label">Classified at</span>
              <span class="value">{new Date(result.spam.classifiedAt).toLocaleString()}</span>
            </div>
            <div class="prompt-block">
              <p class="prompt-header">Prompt used for this message</p>
              <pre class="prompt-text">{result.spam.promptApplied}</pre>
            </div>
          </section>
        {/if}
      {/if}

      {#if disclosureNote}
        <div class="disclosure-note" role="note">
          <p>{disclosureNote}</p>
        </div>
      {/if}
    </div>

    <div class="modal-footer">
      <button type="button" class="primary" onclick={onClose}>Close</button>
    </div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
    padding: var(--spacing-05);
  }

  .modal {
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg, var(--radius-md));
    max-width: 640px;
    width: 100%;
    max-height: 80vh;
    display: flex;
    flex-direction: column;
    box-shadow: var(--shadow-popup, 0 8px 32px rgba(0,0,0,0.24));
  }

  .modal-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: var(--spacing-05) var(--spacing-06);
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .modal-header h2 {
    font-size: var(--type-heading-compact-02-size);
    font-weight: var(--type-heading-compact-02-weight);
    margin: 0;
    color: var(--text-primary);
  }

  .close-btn {
    width: 32px;
    height: 32px;
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    display: flex;
    align-items: center;
    justify-content: center;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .close-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .modal-body {
    overflow-y: auto;
    padding: var(--spacing-05) var(--spacing-06);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    flex: 1;
  }

  .modal-footer {
    padding: var(--spacing-04) var(--spacing-06);
    border-top: 1px solid var(--border-subtle-01);
    display: flex;
    justify-content: flex-end;
  }

  .inspect-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .inspect-section h3 {
    font-size: var(--type-heading-compact-01-size);
    font-weight: var(--type-heading-compact-01-weight);
    margin: 0;
    color: var(--text-secondary);
  }

  .row {
    display: flex;
    gap: var(--spacing-04);
    padding: var(--spacing-02) 0;
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .label {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    min-width: 10em;
    flex: 0 0 auto;
  }

  .value {
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
    flex: 1;
    word-break: break-word;
  }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  .verdict-ham { color: var(--support-success, #24a148); }
  .verdict-spam { color: var(--support-error); }
  .verdict-suspect { color: var(--support-warning, #f1c21b); }

  .prompt-block {
    background: var(--layer-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-04);
    margin-top: var(--spacing-02);
  }

  .prompt-header {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-02);
    font-weight: 600;
  }

  .prompt-text {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-primary);
    white-space: pre-wrap;
    word-break: break-word;
    margin: 0;
    max-height: 240px;
    overflow-y: auto;
  }

  .disclosure-note {
    background: var(--layer-01);
    border-left: 3px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-03) var(--spacing-04);
  }

  .disclosure-note p {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  .empty-state {
    color: var(--text-secondary);
    font-style: italic;
    margin: 0;
    padding: var(--spacing-04) 0;
  }

  .hint {
    color: var(--text-helper);
    font-style: italic;
    margin: 0;
  }

  .error {
    color: var(--support-error);
    margin: 0;
  }

  .primary {
    padding: var(--spacing-02) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }
  .primary:hover {
    filter: brightness(1.1);
  }
</style>
