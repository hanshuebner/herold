<script lang="ts">
  /**
   * Chat compose box per REQ-CHAT-20..27.
   *
   * Key bindings (REQ-CHAT-27):
   *   Enter       -> send
   *   Shift+Enter -> insert newline
   *   Mod+B/I/U   -> marks
   *   Escape      -> blur
   *
   * Inline image upload on paste or drop (REQ-CHAT-22/23):
   *   - File is uploaded via Blob/upload.
   *   - Image node inserted at cursor with src = JMAP download URL.
   *   - On 413 the error is surfaced inline.
   *
   * Typing indicators are emitted on input via chat.notifyTyping().
   */

  import { untrack } from 'svelte';
  import {
    EditorState,
    type Transaction,
  } from 'prosemirror-state';
  import { EditorView } from 'prosemirror-view';
  import {
    DOMParser as PMDOMParser,
    DOMSerializer,
  } from 'prosemirror-model';
  import { keymap } from 'prosemirror-keymap';
  import { baseKeymap, toggleMark } from 'prosemirror-commands';
  import {
    splitListItem,
    liftListItem,
    sinkListItem,
  } from 'prosemirror-schema-list';
  import { history, undo, redo } from 'prosemirror-history';

  import { chatSchema, chatMarks_, chatNodes_ } from './schema';
  import { chat } from './store.svelte';
  import { jmap } from '../jmap/client';
  import { auth } from '../auth/auth.svelte';
  import { toast } from '../toast/toast.svelte';
  import { Capability } from '../jmap/types';

  interface Props {
    conversationId: string;
    /** Whether the compose box should autofocus on mount. */
    autofocus?: boolean;
  }
  let { conversationId, autofocus = false }: Props = $props();

  let host = $state<HTMLDivElement | null>(null);
  let view: EditorView | null = null;
  let uploading = $state(false);
  let uploadError = $state<string | null>(null);
  // Per-editor-lifetime flag: the first user-driven doc change advances the
  // read pointer to the latest visible message. Bare focus does NOT trigger
  // mark-read — the recipient must actually start composing a reply before
  // the unread badge clears. Reset when the editor remounts (e.g. when the
  // overlay window is closed and reopened).
  let hasMarkedRead = false;

  function docToHtml(): string {
    if (!view) return '';
    const frag = DOMSerializer.fromSchema(chatSchema).serializeFragment(
      view.state.doc.content,
    );
    const wrapper = document.createElement('div');
    wrapper.appendChild(frag);
    return wrapper.innerHTML;
  }

  function docToText(): string {
    if (!view) return '';
    return view.state.doc.textBetween(
      0,
      view.state.doc.content.size,
      '\n',
      ' ',
    ).trim();
  }

  function isEmpty(): boolean {
    if (!view) return true;
    const doc = view.state.doc;
    return (
      doc.textContent.trim() === '' &&
      !doc.content.child(0)?.content.firstChild?.type.isInline
    );
  }

  async function handleSend(): Promise<void> {
    if (!view || isEmpty()) return;
    const html = docToHtml();
    const text = docToText();
    clearEditor();
    chat.stopTyping(conversationId);
    // Defensive: dispatchTransaction normally already fired this on the
    // first keystroke, but a send without any prior input would otherwise
    // leave the unread pointer stale.
    if (!hasMarkedRead) {
      hasMarkedRead = true;
      chat.markReadLatest(conversationId);
    }
    await chat.sendMessage(conversationId, html, text);
  }

  function clearEditor(): void {
    if (!view) return;
    const empty = chatSchema.topNodeType.createAndFill()!;
    const tr = view.state.tr.replaceWith(0, view.state.doc.content.size, empty.content);
    view.dispatch(tr);
  }

  function buildKeymap() {
    return keymap({
      'Mod-z': undo,
      'Mod-y': redo,
      'Mod-Shift-z': redo,
      'Mod-b': toggleMark(chatMarks_.strong),
      'Mod-i': toggleMark(chatMarks_.em),
      'Mod-u': toggleMark(chatMarks_.underline),
      // Enter sends; Shift+Enter inserts hard break.
      Enter: (state, dispatch) => {
        if (dispatch) {
          // Queue the send so the editor state settles first.
          void handleSend();
          return true;
        }
        return false;
      },
      'Shift-Enter': baseKeymap['Enter']!,
      Tab: splitListItem(chatNodes_.listItem),
      'Mod-[': liftListItem(chatNodes_.listItem),
      'Mod-]': sinkListItem(chatNodes_.listItem),
      Escape: () => {
        view?.dom.blur();
        return true;
      },
    });
  }

  $effect(() => {
    if (!host) return;
    const emptyDoc = chatSchema.topNodeType.createAndFill()!;
    const state = EditorState.create({
      schema: chatSchema,
      doc: emptyDoc,
      plugins: [history(), buildKeymap(), keymap(baseKeymap)],
    });

    const editorView = new EditorView(host, {
      state,
      handleDOMEvents: {
        drop: (_v, event) => {
          const dt = (event as DragEvent).dataTransfer;
          if (!dt) return false;
          const files = Array.from(dt.files).filter((f) =>
            f.type.startsWith('image/'),
          );
          if (files.length === 0) return false;
          event.preventDefault();
          for (const file of files) void uploadImage(file);
          return true;
        },
        paste: (_v, event) => {
          const cd = (event as ClipboardEvent).clipboardData;
          if (!cd) return false;
          const files = Array.from(cd.items)
            .filter(
              (item) =>
                item.kind === 'file' && item.type.startsWith('image/'),
            )
            .map((item) => item.getAsFile())
            .filter((f): f is File => f !== null);
          if (files.length === 0) return false;
          event.preventDefault();
          for (const file of files) void uploadImage(file);
          return true;
        },
      },
      dispatchTransaction(tr: Transaction) {
        const next = editorView.state.apply(tr);
        editorView.updateState(next);
        if (tr.docChanged) {
          untrack(() => {
            uploadError = null;
            chat.notifyTyping(conversationId);
            // Engagement-based mark-read: the badge clears only once the
            // recipient actually starts composing (or sends), not when
            // their cursor merely lands in the input. Fire once per
            // editor lifetime to avoid spamming /Email/set on every key.
            if (!hasMarkedRead) {
              hasMarkedRead = true;
              chat.markReadLatest(conversationId);
            }
          });
        }
      },
    });

    view = editorView;
    if (autofocus) requestAnimationFrame(() => editorView.focus());

    // Track focus state in the chat store so MessageList can gate
    // auto-mark-read on "the cursor is actually in this compose box".
    // The ProseMirror editor host receives focus/blur via its
    // contenteditable element; mirror it onto chat.focusedConversationId.
    const onFocus = (): void => {
      chat.focusedConversationId = conversationId;
    };
    const onBlur = (): void => {
      if (chat.focusedConversationId === conversationId) {
        chat.focusedConversationId = null;
      }
    };
    editorView.dom.addEventListener('focus', onFocus);
    editorView.dom.addEventListener('blur', onBlur);

    return () => {
      editorView.dom.removeEventListener('focus', onFocus);
      editorView.dom.removeEventListener('blur', onBlur);
      if (chat.focusedConversationId === conversationId) {
        chat.focusedConversationId = null;
      }
      editorView.destroy();
      view = null;
    };
  });

  // External focus requests: a sidebar click (or other UI affordance)
  // sets chat.focusRequest to ask the compose for this conversation to
  // grab keyboard focus. The epoch bumps on every request so re-clicking
  // the same conversation re-fires the effect. Deferred via
  // requestAnimationFrame because a sibling component (e.g. the parent
  // ChatOverlayWindow that mounts this compose) may still be running
  // its own mount work in the same microtask, and ProseMirror.focus
  // is a no-op against a contenteditable that hasn't yet been laid out.
  $effect(() => {
    const req = chat.focusRequest;
    if (!req || req.conversationId !== conversationId) return;
    untrack(() => {
      requestAnimationFrame(() => view?.focus());
    });
  });

  async function uploadImage(file: File): Promise<void> {
    const session = auth.session;
    if (!session) return;
    const accountId =
      session.primaryAccounts[Capability.HeroldChat] ??
      session.primaryAccounts[Capability.Core];
    if (!accountId) return;

    uploading = true;
    uploadError = null;
    try {
      const result = await jmap.uploadBlob({
        accountId,
        body: file,
        type: file.type,
      });
      insertImageNode(result.blobId, file.name, result.type);
    } catch (err) {
      if (err && typeof err === 'object' && 'status' in err && (err as { status?: number }).status === 413) {
        uploadError = 'Image too large (server limit exceeded)';
      } else {
        uploadError =
          err instanceof Error ? err.message : 'Image upload failed';
      }
    } finally {
      uploading = false;
    }
  }

  function insertImageNode(blobId: string, name: string, mimeType: string): void {
    if (!view) return;
    const session = auth.session;
    if (!session) return;
    const accountId =
      session.primaryAccounts[Capability.HeroldChat] ??
      session.primaryAccounts[Capability.Core];
    if (!accountId) return;

    const downloadUrl =
      jmap.downloadUrl({
        accountId,
        blobId,
        type: mimeType,
        name,
      }) ?? blobId;

    const imageType = chatSchema.nodes.image;
    if (!imageType) return;
    const node = imageType.create({ src: downloadUrl, alt: name });
    const tr = view.state.tr.replaceSelectionWith(node, false);
    view.dispatch(tr);
    view.focus();
  }

  function handleKeydown(ev: KeyboardEvent): void {
    // Keyboard shortcuts that operate outside ProseMirror's scope.
    if (ev.key === 'Escape') {
      ev.currentTarget instanceof HTMLElement && ev.currentTarget.blur();
    }
  }
</script>

<div class="compose-wrap" role="group" aria-label="Chat compose">
  <div
    bind:this={host}
    class="compose-editor"
    class:uploading
    aria-label="Message compose area"
  ></div>

  {#if uploadError}
    <p class="upload-error" role="alert">{uploadError}</p>
  {/if}

  <div class="compose-actions">
    <button
      type="button"
      class="send-btn"
      aria-label="Send message (Enter)"
      onclick={handleSend}
      disabled={uploading}
    >
      Send
    </button>
  </div>
</div>

<style>
  .compose-wrap {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    padding: var(--spacing-03) var(--spacing-04);
    border-top: 1px solid var(--border-subtle-01);
    background: var(--background);
  }

  .compose-editor {
    width: 100%;
    min-height: 40px;
    max-height: 200px;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    overflow-y: auto;
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .compose-editor:focus-within {
    border-color: var(--focus);
    box-shadow: 0 0 0 1px var(--focus);
  }

  .compose-editor.uploading {
    opacity: 0.7;
    pointer-events: none;
  }

  .compose-editor :global(.ProseMirror) {
    padding: var(--spacing-03);
    outline: none;
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    min-height: 36px;
  }

  .compose-editor :global(.ProseMirror p) {
    margin: 0;
  }

  .compose-editor :global(.ProseMirror p + p) {
    margin-top: var(--spacing-02);
  }

  .compose-editor :global(.ProseMirror ul),
  .compose-editor :global(.ProseMirror ol) {
    padding-left: var(--spacing-06);
    margin: 0;
  }

  .compose-editor :global(.ProseMirror code) {
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
    background: var(--layer-02);
    padding: 0 var(--spacing-01);
    border-radius: var(--radius-sm);
  }

  .compose-editor :global(.ProseMirror pre) {
    background: var(--layer-02);
    padding: var(--spacing-03);
    border-radius: var(--radius-md);
    overflow-x: auto;
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
  }

  .compose-editor :global(.ProseMirror img) {
    max-width: 400px;
    max-height: 300px;
    border-radius: var(--radius-sm);
    display: block;
    margin: var(--spacing-02) 0;
  }

  .upload-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    margin: 0;
  }

  .compose-actions {
    display: flex;
    justify-content: flex-end;
  }

  .send-btn {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
  }

  .send-btn:hover:not(:disabled) {
    filter: brightness(1.1);
  }

  .send-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
</style>
