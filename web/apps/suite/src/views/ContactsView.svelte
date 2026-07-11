<script lang="ts">
  /**
   * Contacts app router — dispatches to list, detail, edit, create,
   * duplicates-review, or merge sub-views based on the hash route.
   *
   *   #/contacts                -> ContactsListView (list / browse / search)
   *   #/contacts/new            -> ContactsEditView (create new contact)
   *   #/contacts/duplicates     -> ContactsDuplicatesView (REQ-CONT-90, 91)
   *   #/contacts/merge?ids=...  -> ContactsMergeView (REQ-CONT-92, 93)
   *   #/contacts/<id>           -> ContactsDetailView (detail / view)
   *   #/contacts/<id>/edit      -> ContactsEditView (edit existing contact)
   *
   * REQ-CONT-10..12, REQ-CONT-90..94.
   */

  import { router } from '../lib/router/router.svelte';
  import ContactsListView from './ContactsListView.svelte';
  import ContactsDetailView from './ContactsDetailView.svelte';
  import ContactsEditView from './ContactsEditView.svelte';
  import ContactsDuplicatesView from './ContactsDuplicatesView.svelte';
  import ContactsMergeView from './ContactsMergeView.svelte';

  let segment1 = $derived(router.parts[1] ?? '');
  let segment2 = $derived(router.parts[2] ?? '');

  type ContactView = 'list' | 'new' | 'duplicates' | 'merge' | 'edit' | 'detail';
  let view = $derived<ContactView>(
    !segment1 ? 'list' :
    segment1 === 'new' ? 'new' :
    segment1 === 'duplicates' ? 'duplicates' :
    segment1 === 'merge' ? 'merge' :
    segment2 === 'edit' ? 'edit' :
    'detail'
  );
</script>

{#if view === 'list'}
  <ContactsListView />
{:else if view === 'new'}
  <ContactsEditView contactId={null} />
{:else if view === 'duplicates'}
  <ContactsDuplicatesView />
{:else if view === 'merge'}
  <ContactsMergeView />
{:else if view === 'edit'}
  <ContactsEditView contactId={decodeURIComponent(segment1)} />
{:else}
  <ContactsDetailView />
{/if}
