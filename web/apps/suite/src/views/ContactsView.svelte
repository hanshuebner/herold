<script lang="ts">
  /**
   * Contact detail view — reached via /contacts/<contactId>.
   *
   * Fetches the full JSContact card for the given id and renders a
   * read-only summary.  For now this is a minimal stub that shows the
   * name, email addresses, and phone numbers stored in the contact so
   * the "Detailierte Ansicht zeigen" link in the hover card does not
   * route to NotFoundView (re #75).
   */

  import { jmap } from '../lib/jmap/client';
  import { Capability } from '../lib/jmap/types';
  import { auth } from '../lib/auth/auth.svelte';
  import { router } from '../lib/router/router.svelte';
  import { t } from '../lib/i18n/i18n.svelte';

  // The contact id is the second route segment: /contacts/<id>.
  let contactId = $derived(decodeURIComponent(router.parts[1] ?? ''));

  interface ContactCard {
    id: string;
    name?: string;
    emails: string[];
    phones: { type: string; number: string }[];
  }

  let status = $state<'idle' | 'loading' | 'ready' | 'error'>('idle');
  let contact = $state<ContactCard | null>(null);

  $effect(() => {
    const id = contactId;
    if (!id) return;
    void loadContact(id);
  });

  async function loadContact(id: string): Promise<void> {
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) {
      status = 'error';
      return;
    }
    status = 'loading';
    contact = null;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/get',
          { accountId, ids: [id] },
          [Capability.Contacts],
        );
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') {
        status = 'error';
        return;
      }
      const args = resp[1] as { list: unknown[] };
      const raw = args.list[0];
      if (!raw || typeof raw !== 'object') {
        status = 'error';
        return;
      }
      const obj = raw as Record<string, unknown>;
      const nameObj = obj.name as Record<string, unknown> | undefined;
      let name: string | undefined;
      if (nameObj && typeof nameObj.full === 'string' && nameObj.full.trim()) {
        name = nameObj.full.trim();
      } else if (nameObj && Array.isArray(nameObj.components)) {
        const parts: string[] = [];
        for (const c of nameObj.components) {
          if (typeof c === 'object' && c !== null) {
            const v = (c as Record<string, unknown>).value;
            if (typeof v === 'string') parts.push(v);
          }
        }
        const joined = parts.join(' ').trim();
        if (joined) name = joined;
      }

      const emailMap = obj.emails as Record<string, unknown> | undefined;
      const emails: string[] = [];
      if (emailMap) {
        for (const v of Object.values(emailMap)) {
          if (typeof v === 'object' && v !== null) {
            const addr = (v as Record<string, unknown>).address;
            if (typeof addr === 'string' && addr.includes('@')) emails.push(addr);
          }
        }
      }

      const phoneArr = obj.phones as Array<{ type?: string; number?: string }> | undefined;
      const phones: { type: string; number: string }[] = [];
      if (Array.isArray(phoneArr)) {
        for (const p of phoneArr) {
          if (typeof p?.number === 'string' && p.number.trim()) {
            phones.push({ type: p.type ?? '', number: p.number.trim() });
          }
        }
      }

      contact = { id, name, emails, phones };
      status = 'ready';
    } catch {
      status = 'error';
    }
  }
</script>

<div class="contacts-view">
  <div class="toolbar">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/mail')}
    >
      {t('sidebar.inbox')}
    </button>
  </div>

  {#if status === 'loading'}
    <p class="state-msg">Loading&hellip;</p>
  {:else if status === 'error'}
    <p class="state-msg error">Could not load contact.</p>
    <button type="button" class="back-link" onclick={() => router.navigate('/mail')}>
      {t('sidebar.inbox')}
    </button>
  {:else if status === 'ready' && contact}
    <div class="card">
      <h1 class="contact-name">{contact.name ?? contact.emails[0] ?? 'Contact'}</h1>
      {#if contact.emails.length > 0}
        <section class="section">
          <h2 class="section-title">Email</h2>
          <ul class="addr-list">
            {#each contact.emails as email (email)}
              <li><a href="mailto:{email}">{email}</a></li>
            {/each}
          </ul>
        </section>
      {/if}
      {#if contact.phones.length > 0}
        <section class="section">
          <h2 class="section-title">{t('contact.phone.other')}</h2>
          <ul class="addr-list">
            {#each contact.phones as p (`${p.type}:${p.number}`)}
              <li>
                {#if p.type}<span class="phone-type">{p.type} &mdash; </span>{/if}
                <a href="tel:{p.number}">{p.number}</a>
              </li>
            {/each}
          </ul>
        </section>
      {/if}
    </div>
  {/if}
</div>

<style>
  .contacts-view {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: auto;
    padding: var(--spacing-05);
    box-sizing: border-box;
  }
  .toolbar {
    margin-bottom: var(--spacing-05);
  }
  .back-btn {
    color: var(--interactive);
    font-weight: 500;
    font-size: var(--type-body-compact-01-size);
    padding: 0;
  }
  .back-btn::before {
    content: '\2190\00a0';
  }
  .state-msg {
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
  }
  .state-msg.error {
    color: var(--support-error);
  }
  .card {
    max-width: 480px;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-05);
  }
  .contact-name {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    margin: 0;
    color: var(--text-primary);
  }
  .section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }
  .section-title {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-helper);
    margin: 0;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .addr-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .addr-list a {
    color: var(--text-primary);
    text-decoration: none;
    font-size: var(--type-body-01-size);
  }
  .addr-list a:hover {
    text-decoration: underline;
    color: var(--interactive);
  }
  .phone-type {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }
  .back-link {
    color: var(--interactive);
    font-weight: 500;
    margin-top: var(--spacing-04);
  }
</style>
