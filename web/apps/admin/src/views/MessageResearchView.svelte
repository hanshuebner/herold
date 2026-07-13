<script lang="ts">
  import {
    messageResearch,
    type ReceivedHit,
    type SmtpEventHit,
    type SendOutcomeHit,
    type MessageResearchHit,
  } from '../lib/message-research/message-research.svelte';
  import { formatRelative, formatAbsolute } from '../lib/format';
  import { t } from '../lib/i18n/i18n.svelte';

  // Load recent entries on first visit.
  $effect(() => {
    if (messageResearch.status === 'idle') {
      void messageResearch.load();
    }
  });

  function applyFilters(): void {
    void messageResearch.load();
  }

  function resetFilters(): void {
    messageResearch.resetFilters();
    void messageResearch.load();
  }

  // --- Source-type helpers --------------------------------------------------

  function isReceived(hit: MessageResearchHit): hit is ReceivedHit {
    return hit.source === 'received';
  }

  function isSmtpEvent(hit: MessageResearchHit): hit is SmtpEventHit {
    return hit.source === 'smtp_event';
  }

  function isSendOutcome(hit: MessageResearchHit): hit is SendOutcomeHit {
    return hit.source === 'send_outcome';
  }

  /** CSS class for the source badge. */
  function sourceClass(hit: MessageResearchHit): string {
    switch (hit.source) {
      case 'received': return 'source-received';
      case 'smtp_event': return smtpOutcomeClass((hit as SmtpEventHit).outcome);
      case 'send_outcome': return sendStateClass((hit as SendOutcomeHit).state);
      default: return 'source-grey';
    }
  }

  /** Label for the source badge. */
  function sourceLabel(hit: MessageResearchHit): string {
    switch (hit.source) {
      case 'received': return t('messageResearch.source.received');
      case 'smtp_event': return t('messageResearch.source.smtp');
      case 'send_outcome': return t('messageResearch.source.sendOutcome');
      default: return '';
    }
  }

  function smtpOutcomeClass(outcome: string): string {
    switch (outcome) {
      case 'success': return 'source-success';
      case 'failure': return 'source-error';
      default: return 'source-grey';
    }
  }

  function sendStateClass(state: string): string {
    switch (state) {
      case 'done': return 'source-success';
      case 'failed': return 'source-error';
      case 'deferred': return 'source-warning';
      case 'queued': return 'source-info';
      case 'inflight': return 'source-success';
      default: return 'source-grey';
    }
  }

  function outcomeChipClass(outcome: string): string {
    switch (outcome) {
      case 'success': return 'chip-green';
      case 'failure': return 'chip-red';
      default: return 'chip-grey';
    }
  }

  function stateChipClass(state: string): string {
    switch (state) {
      case 'done': return 'chip-green';
      case 'failed': return 'chip-red';
      case 'deferred': return 'chip-amber';
      case 'queued': return 'chip-blue';
      case 'inflight': return 'chip-green';
      default: return 'chip-grey';
    }
  }

  function verdictChipClass(verdict: string): string {
    switch (verdict) {
      case 'ham': return 'chip-green';
      case 'spam': return 'chip-red';
      case 'suspect': return 'chip-amber';
      default: return 'chip-grey';
    }
  }

  function truncate(s: string, max = 80): string {
    if (!s || s.length <= max) return s;
    return s.slice(0, max) + '...';
  }

  function formatFrom(from: string): string {
    return from || t('messageResearch.unknown');
  }

  /** Human label for the recorded-at-ingest delivery disposition. */
  function dispositionLabel(disposition: string): string {
    switch (disposition) {
      case 'delivered_inbox': return t('messageResearch.disposition.inbox');
      case 'delivered_junk': return t('messageResearch.disposition.junk');
      default: return t('messageResearch.disposition.unknown');
    }
  }

  function dispositionChipClass(disposition: string): string {
    switch (disposition) {
      case 'delivered_inbox': return 'chip-green';
      case 'delivered_junk': return 'chip-amber';
      default: return 'chip-grey';
    }
  }
</script>

<div class="research-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">{t('messageResearch.title')}</h1>
      {#if messageResearch.status === 'loading'}
        <div class="spinner" role="status" aria-label={t('common.loading')}></div>
      {/if}
    </div>
  </div>

  <!-- Search form -->
  <div class="filter-grid">
    <div class="filter-field">
      <label for="mr-sender" class="filter-label">{t('messageResearch.filter.sender')}</label>
      <input
        id="mr-sender"
        type="text"
        class="input"
        placeholder={t('messageResearch.filter.senderPlaceholder')}
        bind:value={messageResearch.senderFilter}
        onkeydown={(e) => { if (e.key === 'Enter') applyFilters(); }}
        aria-label={t('messageResearch.filter.senderAriaLabel')}
      />
    </div>
    <div class="filter-field">
      <label for="mr-recipient" class="filter-label">{t('messageResearch.filter.recipient')}</label>
      <input
        id="mr-recipient"
        type="text"
        class="input"
        placeholder={t('messageResearch.filter.recipientPlaceholder')}
        bind:value={messageResearch.recipientFilter}
        onkeydown={(e) => { if (e.key === 'Enter') applyFilters(); }}
        aria-label={t('messageResearch.filter.recipientAriaLabel')}
      />
    </div>
    <div class="filter-field">
      <label for="mr-subject" class="filter-label">{t('messageResearch.filter.subject')}</label>
      <input
        id="mr-subject"
        type="text"
        class="input"
        placeholder={t('messageResearch.filter.subjectPlaceholder')}
        bind:value={messageResearch.subjectFilter}
        onkeydown={(e) => { if (e.key === 'Enter') applyFilters(); }}
        aria-label={t('messageResearch.filter.subjectAriaLabel')}
      />
    </div>
    <div class="filter-field">
      <label for="mr-message-id" class="filter-label">{t('messageResearch.filter.messageId')}</label>
      <input
        id="mr-message-id"
        type="text"
        class="input"
        placeholder={t('messageResearch.filter.messageIdPlaceholder')}
        bind:value={messageResearch.messageIdFilter}
        onkeydown={(e) => { if (e.key === 'Enter') applyFilters(); }}
        aria-label={t('messageResearch.filter.messageIdAriaLabel')}
      />
    </div>
    <div class="filter-field filter-date">
      <label for="mr-date-from" class="filter-label">{t('messageResearch.filter.from')}</label>
      <input
        id="mr-date-from"
        type="datetime-local"
        class="input"
        bind:value={messageResearch.dateFromFilter}
        aria-label={t('messageResearch.filter.fromAriaLabel')}
      />
    </div>
    <div class="filter-field filter-date">
      <label for="mr-date-to" class="filter-label">{t('messageResearch.filter.to')}</label>
      <input
        id="mr-date-to"
        type="datetime-local"
        class="input"
        bind:value={messageResearch.dateToFilter}
        aria-label={t('messageResearch.filter.toAriaLabel')}
      />
    </div>
    <div class="filter-actions">
      <button
        type="button"
        class="btn-primary"
        onclick={applyFilters}
        disabled={messageResearch.status === 'loading'}
      >
        {messageResearch.status === 'loading' ? t('common.loading') : t('messageResearch.filter.search')}
      </button>
      <button
        type="button"
        class="btn-secondary"
        onclick={resetFilters}
        disabled={messageResearch.status === 'loading'}
      >
        {t('messageResearch.filter.reset')}
      </button>
    </div>
  </div>

  {#if messageResearch.errorMessage && messageResearch.status === 'error'}
    <div class="page-error" role="alert">{messageResearch.errorMessage}</div>
  {/if}

  {#if messageResearch.status === 'ready' || messageResearch.items.length > 0}
    {#if messageResearch.items.length === 0}
      <p class="empty-state">{t('messageResearch.empty')}</p>
    {:else}
      <div class="timeline">
        {#each messageResearch.items as hit, i (hit.at + i)}
          <div class="timeline-entry {sourceClass(hit)}">
            <div class="entry-header">
              <span class="source-badge {sourceClass(hit)}">{sourceLabel(hit)}</span>
              <span
                class="entry-time"
                title={formatAbsolute(hit.at)}
              >{formatRelative(hit.at)}</span>
            </div>

            {#if isReceived(hit)}
              <!-- Received mail disposition -->
              <div class="entry-body">
                <div class="entry-row">
                  <span class="entry-key">{t('messageResearch.field.from')}</span>
                  <span class="entry-val mono-sm">{formatFrom(hit.envelope.from)}</span>
                </div>
                {#if hit.envelope.to}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.to')}</span>
                    <span class="entry-val mono-sm">{hit.envelope.to}</span>
                  </div>
                {/if}
                {#if hit.envelope.subject}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.subject')}</span>
                    <span class="entry-val">{hit.envelope.subject}</span>
                  </div>
                {/if}
                <div class="entry-row">
                  <span class="entry-key">{t('messageResearch.field.deliveredTo')}</span>
                  <span class="entry-val mono-sm">{hit.principal_email || t('messageResearch.unknown')}</span>
                </div>
                <div class="entry-row">
                  <span class="entry-key">{t('messageResearch.field.disposition')}</span>
                  <span class="chip {dispositionChipClass(hit.disposition)}">{dispositionLabel(hit.disposition)}</span>
                </div>
                <div class="entry-row">
                  <span class="entry-key">{t('messageResearch.field.mailbox')}</span>
                  <span class="entry-val mono-sm">{hit.mailbox_name || t('messageResearch.unknown')}</span>
                </div>
                {#if hit.is_junk}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.junk')}</span>
                    <span class="chip chip-amber">{t('messageResearch.field.junk')}</span>
                  </div>
                {/if}
                {#if hit.spam_verdict}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.spamVerdict')}</span>
                    <span class="chip {verdictChipClass(hit.spam_verdict)}">{hit.spam_verdict}</span>
                    {#if hit.spam_confidence !== undefined && hit.spam_confidence !== null}
                      <span class="confidence">({Math.round(hit.spam_confidence * 100)}%)</span>
                    {/if}
                  </div>
                {/if}
                {#if hit.envelope.message_id}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.messageId')}</span>
                    <span class="entry-val mono-sm">{truncate(hit.envelope.message_id, 60)}</span>
                  </div>
                {/if}
              </div>

            {:else if isSmtpEvent(hit)}
              <!-- SMTP event (accept/reject/defer at SMTP time) -->
              <div class="entry-body">
                <div class="entry-row">
                  <span class="entry-key">{t('messageResearch.field.action')}</span>
                  <span class="entry-val mono-sm">{hit.action}</span>
                </div>
                {#if hit.subject}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.recipient')}</span>
                    <span class="entry-val mono-sm">{hit.subject}</span>
                  </div>
                {/if}
                <div class="entry-row">
                  <span class="entry-key">{t('messageResearch.field.outcome')}</span>
                  <span class="chip {outcomeChipClass(hit.outcome)}">{hit.outcome}</span>
                </div>
                {#if hit.actor_id}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.principal')}</span>
                    <span class="entry-val mono-sm">{hit.actor_id}</span>
                  </div>
                {/if}
                {#if hit.domain}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.domain')}</span>
                    <span class="entry-val mono-sm">{hit.domain}</span>
                  </div>
                {/if}
                {#if hit.message}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.message')}</span>
                    <span class="entry-val">{truncate(hit.message)}</span>
                  </div>
                {/if}
                {#if hit.remote_addr}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.ipAddress')}</span>
                    <span class="entry-val mono-sm">{hit.remote_addr}</span>
                  </div>
                {/if}
              </div>

            {:else if isSendOutcome(hit)}
              <!-- Outbound send outcome -->
              <div class="entry-body">
                <div class="entry-row">
                  <span class="entry-key">{t('messageResearch.field.from')}</span>
                  <span class="entry-val mono-sm">{hit.mail_from}</span>
                </div>
                <div class="entry-row">
                  <span class="entry-key">{t('messageResearch.field.to')}</span>
                  <span class="entry-val mono-sm">{hit.rcpt_to}</span>
                </div>
                <div class="entry-row">
                  <span class="entry-key">{t('messageResearch.field.state')}</span>
                  <span class="chip {stateChipClass(hit.state)}">{hit.state}</span>
                </div>
                <div class="entry-row">
                  <span class="entry-key">{t('messageResearch.field.attempts')}</span>
                  <span class="entry-val">{hit.attempts}</span>
                </div>
                {#if hit.last_error}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.lastError')}</span>
                    <span class="entry-val error-text">{truncate(hit.last_error)}</span>
                  </div>
                {/if}
                {#if hit.envelope_id}
                  <div class="entry-row">
                    <span class="entry-key">{t('messageResearch.field.envelopeId')}</span>
                    <span class="entry-val mono-sm">{hit.envelope_id}</span>
                  </div>
                {/if}
              </div>
            {/if}
          </div>
        {/each}
      </div>

      {#if messageResearch.hasMore}
        <div class="load-more">
          <button
            type="button"
            class="btn-secondary"
            onclick={() => void messageResearch.loadMore()}
            disabled={messageResearch.status === 'loading'}
          >
            {messageResearch.status === 'loading' ? t('common.loading') : t('messageResearch.loadMore')}
          </button>
        </div>
      {/if}
    {/if}
  {:else if messageResearch.status !== 'loading' && messageResearch.status !== 'idle'}
    <p class="empty-state">{t('messageResearch.empty')}</p>
  {/if}
</div>

<style>
  .research-page {
    max-width: 900px;
  }

  /* ---- Page header ---- */
  .page-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-05);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-05);
  }

  .page-header-left {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }

  .page-title {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    color: var(--text-primary);
    margin: 0;
  }

  .spinner {
    width: 18px;
    height: 18px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
    flex-shrink: 0;
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  @media (prefers-reduced-motion: reduce) {
    .spinner { animation: none; }
  }

  /* ---- Filter grid ---- */
  .filter-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--spacing-04);
    margin-bottom: var(--spacing-05);
    padding: var(--spacing-05);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
  }

  .filter-field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .filter-label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
  }

  .input {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }

  .filter-actions {
    grid-column: 1 / -1;
    display: flex;
    gap: var(--spacing-03);
    align-items: center;
  }

  .page-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
    margin-bottom: var(--spacing-05);
  }

  /* ---- Timeline ---- */
  .timeline {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .timeline-entry {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    border-left-width: 4px;
    padding: var(--spacing-04) var(--spacing-05);
  }

  /* Left-border color per source */
  .source-received { border-left-color: var(--support-success); }
  .source-success   { border-left-color: var(--support-success); }
  .source-error     { border-left-color: var(--support-error); }
  .source-warning   { border-left-color: var(--support-warning); }
  .source-info      { border-left-color: var(--interactive); }
  .source-grey      { border-left-color: var(--text-helper); }

  /* ---- Entry header ---- */
  .entry-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    margin-bottom: var(--spacing-03);
  }

  .source-badge {
    display: inline-block;
    padding: 1px var(--spacing-03);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 700;
    line-height: 18px;
    letter-spacing: 0.02em;
    text-transform: uppercase;
  }

  .source-badge.source-received {
    background: color-mix(in srgb, var(--support-success) 18%, transparent);
    color: var(--support-success);
  }
  .source-badge.source-success {
    background: color-mix(in srgb, var(--support-success) 18%, transparent);
    color: var(--support-success);
  }
  .source-badge.source-error {
    background: color-mix(in srgb, var(--support-error) 18%, transparent);
    color: var(--support-error);
  }
  .source-badge.source-warning {
    background: color-mix(in srgb, var(--support-warning) 18%, transparent);
    color: var(--support-warning);
  }
  .source-badge.source-info {
    background: color-mix(in srgb, var(--interactive) 18%, transparent);
    color: var(--interactive);
  }
  .source-badge.source-grey {
    background: color-mix(in srgb, var(--text-helper) 18%, transparent);
    color: var(--text-helper);
  }

  .entry-time {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    cursor: default;
  }

  /* ---- Entry body ---- */
  .entry-body {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .entry-row {
    display: flex;
    align-items: baseline;
    gap: var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
  }

  .entry-key {
    flex: 0 0 120px;
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
    white-space: nowrap;
  }

  .entry-val {
    color: var(--text-primary);
    word-break: break-all;
    min-width: 0;
  }

  .mono-sm {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-primary);
    word-break: break-all;
  }

  .error-text {
    color: var(--support-error);
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    word-break: break-all;
  }

  .confidence {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin-left: var(--spacing-02);
  }

  /* ---- Chips ---- */
  .chip {
    display: inline-block;
    padding: 1px var(--spacing-02);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    line-height: 18px;
  }
  .chip-blue {
    background: color-mix(in srgb, var(--interactive) 15%, transparent);
    color: var(--interactive);
  }
  .chip-amber {
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    color: var(--support-warning);
  }
  .chip-grey {
    background: color-mix(in srgb, var(--text-helper) 15%, transparent);
    color: var(--text-helper);
  }
  .chip-red {
    background: color-mix(in srgb, var(--support-error) 15%, transparent);
    color: var(--support-error);
  }
  .chip-green {
    background: color-mix(in srgb, var(--support-success) 15%, transparent);
    color: var(--support-success);
  }

  .empty-state {
    color: var(--text-helper);
    font-size: var(--type-body-01-size);
    margin-top: var(--spacing-05);
  }

  .load-more {
    margin-top: var(--spacing-05);
    text-align: center;
  }

  /* ---- Buttons ---- */
  .btn-primary {
    padding: var(--spacing-03) var(--spacing-06);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    cursor: pointer;
    border: none;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter),
      opacity var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .btn-primary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-secondary {
    padding: var(--spacing-03) var(--spacing-06);
    background: var(--layer-02);
    color: var(--text-primary);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: var(--touch-min);
    cursor: pointer;
    border: 1px solid var(--border-subtle-01);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-secondary:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .btn-secondary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
</style>
