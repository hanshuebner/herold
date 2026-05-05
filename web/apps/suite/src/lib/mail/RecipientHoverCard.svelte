<script lang="ts">
  /**
   * Gmail-style recipient hover card (REQ-MAIL-46 / REQ-MAIL-46a..f).
   *
   * Driven by the `recipientHover` singleton: when its `open` state
   * names an anchor element, this component positions itself below the
   * anchor and renders the card. The card resolves its payload through
   * the person-resolver so it appears populated synchronously when a
   * cached entry exists, then re-validates in the background.
   */

  import { onMount } from 'svelte';
  import { recipientHover } from './recipient-hover.svelte';
  import {
    peekPerson,
    resolvePerson,
    type PersonRecord,
  } from './person-resolver.svelte';
  import { mail } from './store.svelte';
  import { contacts } from '../contacts/store.svelte';
  import { compose } from '../compose/compose.svelte';
  import { jmap, strict } from '../jmap/client';
  import { Capability } from '../jmap/types';
  import { auth } from '../auth/auth.svelte';
  import { newChatPicker } from '../chat/new-chat-picker.svelte';
  import { chatOverlay } from '../chat/overlay-store.svelte';
  import { chat } from '../chat/store.svelte';
  import { router } from '../router/router.svelte';
  import { t } from '../i18n/i18n.svelte';
  import { toast } from '../toast/toast.svelte';
  import Avatar from '../avatar/Avatar.svelte';
  import CopyIcon from '../icons/CopyIcon.svelte';
  import PhoneIcon from '../icons/PhoneIcon.svelte';
  import ChatIcon from '../icons/ChatIcon.svelte';
  import VideoCallIcon from '../icons/VideoCallIcon.svelte';
  import CalendarIcon from '../icons/CalendarIcon.svelte';
  import AddPersonIcon from '../icons/AddPersonIcon.svelte';
  import EditIcon from '../icons/EditIcon.svelte';

  let open = $derived(recipientHover.open);
  let person = $state<PersonRecord | null>(null);
  let copied = $state(false);
  let saving = $state(false);

  // Reposition state — anchored below the trigger; flipped above when
  // the card would overflow the viewport.
  let cardEl = $state<HTMLDivElement | null>(null);
  let position = $state<{ top: number; left: number; placement: 'below' | 'above' }>({
    top: 0,
    left: 0,
    placement: 'below',
  });

  // Re-resolve whenever the open record changes. peek() fills the card
  // synchronously from cache; resolvePerson() runs in the background to
  // refresh stale fields per REQ-MAIL-46f.
  $effect(() => {
    const current = open;
    if (!current) {
      person = null;
      copied = false;
      return;
    }
    const cached = peekPerson(current.email, current.capturedName);
    person = cached ?? {
      email: current.email,
      displayName: current.capturedName?.trim() || localPart(current.email),
      avatarUrl: null,
      phones: [],
      contactId: null,
      principalId: null,
    };
    void (async () => {
      const fresh = await resolvePerson(
        current.email,
        Array.from(mail.identities.values()),
        current.capturedName,
        current.messageHeaders,
      );
      // Ignore the result if the user has moved to a different card.
      if (recipientHover.open?.anchor === current.anchor) {
        person = fresh;
      }
    })();
  });

  // Reposition the card under the anchor whenever the open state or
  // viewport changes. position(): pick "below" by default; flip to
  // "above" when the card would overflow.
  $effect(() => {
    const current = open;
    if (!current) return;
    const update = (): void => layout(current.anchor);
    update();
    const ro = new ResizeObserver(update);
    if (cardEl) ro.observe(cardEl);
    window.addEventListener('scroll', update, true);
    window.addEventListener('resize', update);
    return () => {
      ro.disconnect();
      window.removeEventListener('scroll', update, true);
      window.removeEventListener('resize', update);
    };
  });

  function layout(anchor: HTMLElement): void {
    const rect = anchor.getBoundingClientRect();
    const cardHeight = cardEl?.offsetHeight ?? 240;
    const cardWidth = cardEl?.offsetWidth ?? 320;
    // Horizontal viewport margin only; vertical gap is zero so the card
    // abuts the trigger directly. A zero gap prevents the mouse from
    // leaving the hover surface while crossing from trigger to card,
    // which would otherwise fire requestClose and allow a different
    // trigger below the gap to open (re #75).
    const hMargin = 8;
    const spaceBelow = window.innerHeight - rect.bottom;
    const placement: 'below' | 'above' =
      spaceBelow < cardHeight && rect.top > cardHeight
        ? 'above'
        : 'below';
    const top =
      placement === 'below' ? rect.bottom : rect.top - cardHeight;
    const left = Math.max(
      hMargin,
      Math.min(window.innerWidth - cardWidth - hMargin, rect.left),
    );
    position = { top, left, placement };
  }

  function handlePointerEnter(): void {
    recipientHover.cancelClose();
  }

  function handlePointerLeave(): void {
    recipientHover.requestClose();
  }

  function handleKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      recipientHover.closeNow();
      open?.anchor.focus?.();
    }
  }

  // Capability gating for the secondary action row (REQ-MAIL-46c).
  let hasChatCap = $derived(jmap.hasCapability(Capability.HeroldChat));
  let hasVideoCap = $derived(
    jmap.hasCapability('https://netzhansa.com/jmap/calls'),
  );
  let hasCalendarCap = $derived(jmap.hasCapability(Capability.Calendars));

  let chatEnabled = $derived(Boolean(person?.principalId) && hasChatCap);
  let videoEnabled = $derived(Boolean(person?.principalId) && hasVideoCap);
  let calendarEnabled = $derived(
    Boolean(person?.principalId) && hasCalendarCap,
  );

  async function handleCopy(): Promise<void> {
    if (!person) return;
    try {
      await navigator.clipboard.writeText(person.email);
      copied = true;
      setTimeout(() => {
        copied = false;
      }, 1500);
    } catch {
      toast.show({ message: 'Could not copy address', kind: 'error' });
    }
  }

  function handleSendEmail(): void {
    if (!person) return;
    compose.openWith({
      to: person.email,
      subject: '',
      body: '',
    });
    recipientHover.closeNow();
  }

  async function handleChat(): Promise<void> {
    if (!person?.principalId || !chatEnabled) return;
    recipientHover.closeNow();
    const existing = chat.findExistingDM(person.principalId);
    if (existing) {
      chatOverlay.openWindow(existing.id);
      chat.requestComposeFocus(existing.id);
    } else {
      // No existing DM: create one directly with this principal rather
      // than opening the generic new-chat picker (re #61).
      const myId = auth.principalId;
      if (!myId) return;
      try {
        const { id } = await chat.createConversation({
          kind: 'dm',
          members: [myId, person.principalId],
        });
        chatOverlay.openWindow(id);
        chat.requestComposeFocus(id);
      } catch (err) {
        console.error('handleChat: createConversation failed', err);
        toast.show({ message: 'Could not open chat', kind: 'error' });
      }
    }
  }

  function handleVideo(): void {
    if (!person?.principalId || !videoEnabled) return;
    // Video starts a call against the principal — surfaced through chat
    // overlay's call surface (21-video-calls.md). For now, navigate to
    // the chat conversation; the call control lives there.
    const existing = chat.findExistingDM(person.principalId);
    if (existing) {
      router.navigate(`/chat/conversation/${existing.id}?call=start`);
    } else {
      newChatPicker.open({ mode: 'dm' });
    }
    recipientHover.closeNow();
  }

  function handleCalendar(): void {
    if (!person?.principalId || !calendarEnabled) return;
    // Calendar opens the event composer with this address pre-filled —
    // the calendar surface itself is out of scope of this iteration; we
    // route to the calendar app and let it pick up an `attendee` param.
    router.navigate(`/calendar?compose=1&attendee=${encodeURIComponent(person.email)}`);
    recipientHover.closeNow();
  }

  function handleEditContact(): void {
    if (!person?.contactId) return;
    router.navigate(`/contacts/${encodeURIComponent(person.contactId)}`);
    recipientHover.closeNow();
  }

  async function handleAddContact(): Promise<void> {
    const current = person;
    if (!current || current.contactId) return;
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) {
      toast.show({ message: 'Contacts unavailable', kind: 'error' });
      return;
    }
    saving = true;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/set',
          {
            accountId,
            create: {
              new1: {
                name: current.displayName
                  ? {
                      full: current.displayName,
                      components: [
                        { type: 'personal', value: current.displayName },
                      ],
                    }
                  : undefined,
                emails: { primary: { address: current.email } },
              },
            },
          },
          [Capability.Contacts],
        );
      });
      strict(responses);
      const args = responses[0]![1] as {
        created?: Record<string, { id: string } | null>;
        notCreated?: Record<string, { type: string; description?: string } | null>;
      };
      const notCreated = args.notCreated?.['new1'];
      if (notCreated) {
        const desc = notCreated.description ?? notCreated.type;
        console.error('Add contact rejected by server', notCreated);
        toast.show({ message: `Could not add contact: ${desc}`, kind: 'error' });
        return;
      }
      const created = args.created?.['new1'];
      if (created?.id) {
        person = { ...current, contactId: created.id };
        // Force a full reload so the suggestions cache reflects the newly
        // created contact; contacts.load() is idempotent and would be a
        // no-op here because the store is already in 'ready' state (re #75).
        void contacts.reload();
      }
    } catch (err) {
      console.error('Add contact failed', err);
      toast.show({ message: 'Could not add contact', kind: 'error' });
    } finally {
      saving = false;
    }
  }

  // Phone-row label localisation: map the JSContact / Principal type
  // string to a translation key (REQ-MAIL-46a). Unknown types fall back
  // to the `contact.phone.other` label.
  function phoneLabel(type: string): string {
    const lc = type.trim().toLowerCase();
    const known: Record<string, string> = {
      mobile: 'contact.phone.mobile',
      cell: 'contact.phone.mobile',
      work: 'contact.phone.work',
      home: 'contact.phone.home',
      fax: 'contact.phone.fax',
    };
    const key = known[lc];
    if (key) return t(key);
    if (lc) return type;
    return t('contact.phone.other');
  }

  function localPart(email: string): string {
    return email.split('@')[0] ?? email;
  }

  // Close on a click anywhere outside the card or trigger.
  onMount(() => {
    const onPointerDown = (e: MouseEvent): void => {
      const card = cardEl;
      const anchor = recipientHover.open?.anchor;
      const target = e.target as Node | null;
      if (!card || !target) return;
      if (card.contains(target)) return;
      if (anchor && anchor.contains(target)) return;
      recipientHover.closeNow();
    };
    window.addEventListener('mousedown', onPointerDown, true);
    return () => {
      window.removeEventListener('mousedown', onPointerDown, true);
    };
  });
</script>

{#if open && person}
  <div
    bind:this={cardEl}
    class="hover-card"
    role="dialog"
    aria-label={person.displayName || person.email}
    style:top="{position.top}px"
    style:left="{position.left}px"
    onpointerenter={handlePointerEnter}
    onpointerleave={handlePointerLeave}
    onkeydown={handleKeydown}
    tabindex="-1"
  >
    <header class="card-head">
      <Avatar
        email={person.email}
        fallbackInitial={(person.displayName || person.email)
          .slice(0, 1)
          .toUpperCase()}
        size={56}
      />
      <div class="head-text">
        <div class="display-name">{person.displayName || localPart(person.email)}</div>
        <div class="email-row">
          <span class="email" title={person.email}>{person.email}</span>
          <button
            type="button"
            class="copy-btn"
            aria-label={t('contact.card.copy')}
            title={copied ? t('contact.card.copied') : t('contact.card.copy')}
            onclick={() => void handleCopy()}
          >
            <CopyIcon size={16} />
          </button>
          {#if copied}
            <span class="copied-tip" role="status">{t('contact.card.copied')}</span>
          {/if}
        </div>
      </div>
      <div class="corner">
        {#if person.contactId}
          <button
            type="button"
            class="corner-btn"
            aria-label={t('contact.card.edit')}
            title={t('contact.card.edit')}
            onclick={handleEditContact}
          >
            <EditIcon size={18} />
          </button>
        {:else}
          <button
            type="button"
            class="corner-btn"
            aria-label={t('contact.card.add')}
            title={t('contact.card.add')}
            disabled={saving}
            onclick={() => void handleAddContact()}
          >
            <AddPersonIcon size={18} />
          </button>
        {/if}
      </div>
    </header>

    {#if person.phones.length > 0}
      <ul class="phones">
        {#each person.phones as p (p.number)}
          <li class="phone-row">
            <PhoneIcon size={16} />
            <span class="phone-type">{phoneLabel(p.type)}</span>
            <a class="phone-number" href={`tel:${p.number}`}>{p.number}</a>
          </li>
        {/each}
      </ul>
    {/if}

    <div class="actions">
      <button type="button" class="primary" onclick={handleSendEmail}>
        {t('contact.card.sendEmail')}
      </button>
      <button
        type="button"
        class="secondary"
        aria-label={t('contact.card.chat')}
        title={t('contact.card.chat')}
        disabled={!chatEnabled}
        tabindex={chatEnabled ? 0 : -1}
        onclick={() => void handleChat()}
      >
        <ChatIcon size={18} />
      </button>
      <button
        type="button"
        class="secondary"
        aria-label={t('contact.card.video')}
        title={t('contact.card.video')}
        disabled={!videoEnabled}
        tabindex={videoEnabled ? 0 : -1}
        onclick={handleVideo}
      >
        <VideoCallIcon size={18} />
      </button>
      <button
        type="button"
        class="secondary"
        aria-label={t('contact.card.calendar')}
        title={t('contact.card.calendar')}
        disabled={!calendarEnabled}
        tabindex={calendarEnabled ? 0 : -1}
        onclick={handleCalendar}
      >
        <CalendarIcon size={18} />
      </button>
    </div>
  </div>
{/if}

<style>
  .hover-card {
    position: fixed;
    z-index: 400;
    width: 320px;
    max-width: calc(100vw - 16px);
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-md, 0 8px 24px rgba(0, 0, 0, 0.18));
    padding: var(--spacing-04);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    outline: none;
  }
  .card-head {
    display: grid;
    grid-template-columns: auto 1fr auto;
    gap: var(--spacing-03);
    align-items: flex-start;
  }
  .head-text {
    overflow: hidden;
  }
  .display-name {
    font-weight: 600;
    font-size: var(--type-heading-04-size, 1rem);
    line-height: 1.25;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .email-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin-top: var(--spacing-01);
    overflow: hidden;
  }
  .email {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .copy-btn {
    flex: 0 0 auto;
    width: 24px;
    height: 24px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--text-helper);
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .copy-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .copied-tip {
    color: var(--support-success, #28a745);
    font-size: var(--type-body-compact-01-size);
    margin-left: var(--spacing-02);
  }
  .corner-btn {
    width: 32px;
    height: 32px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-pill);
    color: var(--text-secondary);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .corner-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .corner-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  .phones {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }
  .phone-row {
    display: grid;
    grid-template-columns: auto auto 1fr;
    align-items: center;
    gap: var(--spacing-02);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }
  .phone-type {
    color: var(--text-helper);
  }
  .phone-number {
    color: var(--text-primary);
    text-decoration: none;
  }
  .phone-number:hover {
    text-decoration: underline;
  }
  .actions {
    display: flex;
    gap: var(--spacing-02);
    align-items: center;
    margin-top: var(--spacing-02);
  }
  .primary {
    flex: 1 1 auto;
    height: 36px;
    padding: 0 var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .primary:hover {
    background: var(--interactive-hover, var(--interactive));
  }
  .secondary {
    width: 36px;
    height: 36px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-pill);
    background: var(--layer-02);
    color: var(--text-secondary);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .secondary:hover:not(:disabled) {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .secondary:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
</style>
