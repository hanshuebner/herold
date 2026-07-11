<script lang="ts">
  /**
   * Contacts app router — dispatches to list, detail, edit, or create
   * sub-views based on the hash route (REQ-CONT-10..12).
   *
   *   #/contacts           -> ContactsListView (list / browse / search)
   *   #/contacts/<id>      -> ContactsDetailView (detail / view)
   *   #/contacts/<id>/edit -> ContactsEditView (edit existing contact)
   *   #/contacts/new       -> ContactsEditView (create new contact)
   */

  import { router } from '../lib/router/router.svelte';
  import ContactsListView from './ContactsListView.svelte';
  import ContactsDetailView from './ContactsDetailView.svelte';
  import ContactsEditView from './ContactsEditView.svelte';

  let segment1 = $derived(router.parts[1] ?? '');
  let segment2 = $derived(router.parts[2] ?? '');

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
  <ContactsEditView contactId={null} />
{:else if view === 'edit'}
  <ContactsEditView contactId={decodeURIComponent(segment1)} />
{:else}
  <ContactsDetailView />
{/if}
