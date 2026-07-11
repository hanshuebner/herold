<script lang="ts">
  /**
   * Contacts app router — dispatches to list, detail, edit, or new sub-views
   * based on the hash route (REQ-CONT-10..12).
   *
   *   #/contacts           → ContactsListView (list / browse / search)
   *   #/contacts/<id>      → ContactsDetailView (detail / view)
   *   #/contacts/<id>/edit → placeholder (edit form, issue #170)
   *   #/contacts/new       → placeholder (create form, issue #170)
   */

  import { router } from '../lib/router/router.svelte';
  import { t } from '../lib/i18n/i18n.svelte';
  import ContactsListView from './ContactsListView.svelte';
  import ContactsDetailView from './ContactsDetailView.svelte';
  import Button from '@herold/design-system/Button.svelte';

  let segment1 = $derived(router.parts[1] ?? '');
  let segment2 = $derived(router.parts[2] ?? '');

  // Determine which sub-view to render based on route segments.
  type ContactView = 'list' | 'new' | 'edit' | 'detail';
  let view = $derived<ContactView>(
    !segment1 ? 'list' :
    segment1 === 'new' ? 'new' :
    segment2 === 'edit' ? 'edit' :
    'detail'
  );
</script>

{#if view === 'list'}
  <ContactsListView />
{:else if view === 'new'}
  <!-- Create contact — implemented in issue #170 -->
  <div class="placeholder-view">
    <p class="placeholder-msg">{t('contacts.create.placeholder')}</p>
    <Button variant="secondary" onclick={() => router.navigate('/contacts')}>
      {t('contacts.list.title')}
    </Button>
  </div>
{:else if view === 'edit'}
  <!-- Edit contact — implemented in issue #170 -->
  <div class="placeholder-view">
    <p class="placeholder-msg">{t('contacts.edit.placeholder')}</p>
    <Button variant="secondary" onclick={() => router.navigate(`/contacts/${encodeURIComponent(segment1)}`)}>
      {t('contacts.detail.backToDetail')}
    </Button>
  </div>
{:else}
  <ContactsDetailView />
{/if}

<style>
  .placeholder-view {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--spacing-05);
    height: 100%;
    padding: var(--spacing-08);
  }

  .placeholder-msg {
    font-size: var(--type-body-01-size);
    color: var(--text-secondary);
    text-align: center;
    margin: 0;
  }
</style>
