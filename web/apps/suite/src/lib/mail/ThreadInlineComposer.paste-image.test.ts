/**
 * Pasting an image into the inline reply composer (re #532).
 *
 * ThreadInlineComposer mounted RichEditor without `onImagePaste`, so the
 * editor's paste handler returned early and the paste did nothing, while
 * ComposeWindow (which wires it) uploaded and inlined the image. These
 * tests mount the real component and real ProseMirror editor, dispatch a
 * paste event whose clipboardData carries a File, and assert the image is
 * registered, uploaded and inserted as an <img> node.
 *
 * Not covered: the real OS clipboard / browser paste pipeline (a script-
 * dispatched event reaches the same ProseMirror handler but does not prove
 * what Chrome/Safari put on the clipboard for a screenshot).
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import { tick } from 'svelte';
import type { Identity } from './types';

vi.mock('../auth/capabilities', () => ({
  hasExternalSubmission: vi.fn(() => false),
  hasDirectoryAutocomplete: vi.fn(() => false),
}));

vi.mock('../identities/identity-submission.svelte', () => {
  const handle = {
    status: 'idle',
    data: null,
    error: null,
    load: vi.fn(async () => undefined),
    refresh: vi.fn(async () => undefined),
  };
  return {
    submissionStore: {
      forIdentity: vi.fn(() => handle),
      evict: vi.fn(),
    },
  };
});

vi.mock('../compose/compose.svelte', () => {
  const compose = {
    isOpen: true,
    inlineMode: true,
    status: 'editing' as 'idle' | 'editing' | 'sending',
    to: '',
    cc: '',
    bcc: '',
    body: '',
    errorMessage: null as string | null,
    ccBccVisible: false,
    replyContext: {
      parentId: 'msg-1',
      parentKeyword: null,
      inReplyTo: null,
      references: null,
    },
    toRecipients: [],
    ccRecipients: [],
    bccRecipients: [],
    attachments: [] as Array<{ key: string; objectURL?: string }>,
    shares: [],
    attachmentsBusy: false,
    editingDraftId: null,
    hasContent: false,
    selectedIdentity: null as Identity | null,
    send: vi.fn(async () => {
      compose.isOpen = false;
    }),
    discard: vi.fn(),
    addAttachments: vi.fn(),
    removeAttachment: vi.fn(),
    removeShare: vi.fn(),
    setShareEditorFns: vi.fn(),
    setSwapInlineImageSrcFn: vi.fn(),
    startInlineImage: vi.fn(),
    uploadInlineImage: vi.fn(),
  };
  return { compose };
});

vi.mock('../keyboard/engine.svelte', () => ({
  keyboard: { pushLayer: vi.fn(() => vi.fn()) },
}));

vi.mock('../dialog/confirm.svelte', () => ({
  confirm: { ask: vi.fn(async () => true) },
}));

vi.mock('./navigate-back', () => ({
  navigateBackFromThread: vi.fn(),
}));

vi.mock('./attachment-icon', () => ({
  attachmentBadge: vi.fn(() => ({ bg: '#888', label: 'FILE' })),
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string): string => {
    const map: Record<string, string> = {
      'compose.from': 'From',
      'compose.from.picker.aria': 'From identity',
      'compose.from.picker.open': 'Change From identity',
      'compose.to': 'To',
      'compose.cc': 'Cc',
      'compose.bcc': 'Bcc',
      'compose.toggleCcBcc': 'Cc / Bcc',
      'compose.send': 'Send',
      'compose.sending': 'Sending...',
      'compose.sendAndArchive': 'Send + Archive',
      'compose.attach': 'Attach',
      'compose.discard': 'Discard',
      'compose.close': 'Close',
      'compose.popOut': 'Pop out',
      'compose.title.reply': 'Reply',
      'compose.title.forward': 'Forward',
    };
    return map[key] ?? key;
  },
  localeTag: () => 'en',
  LOCALES: ['en'],
}));

vi.mock('./store.svelte', () => {
  const DEFAULT_IDENTITY: Identity = {
    id: '1',
    name: 'Alice',
    email: 'alice@example.local',
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: false,
    verifiedAt: '2026-01-01T00:00:00Z',
    isDefault: true,
  };
  const mail = {
    primaryIdentity: DEFAULT_IDENTITY as Identity | null,
    identities: new Map<string, Identity>([[DEFAULT_IDENTITY.id, DEFAULT_IDENTITY]]),
    inbox: { id: 'inbox-1' } as { id: string } | null,
    archive: { id: 'archive-1' } as { id: string } | null,
    threadEmails: vi.fn((_tid: string) => []),
    bulkArchive: vi.fn(),
    loadIdentities: vi.fn(),
  };
  return { mail };
});

const { compose } = await import('../compose/compose.svelte');
const composeMock = compose as unknown as {
  body: string;
  isOpen: boolean;
  inlineMode: boolean;
  status: 'idle' | 'editing' | 'sending';
  selectedIdentity: Identity | null;
  startInlineImage: ReturnType<typeof vi.fn>;
  uploadInlineImage: ReturnType<typeof vi.fn>;
};

import ThreadInlineComposer from './ThreadInlineComposer.svelte';

const IDENTITY: Identity = {
  id: '1',
  name: 'Alice',
  email: 'alice@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
  verifiedAt: '2026-01-01T00:00:00Z',
  isDefault: true,
};

function pasteEvent(file: File): ClipboardEvent {
  const ev = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent;
  Object.defineProperty(ev, 'clipboardData', {
    value: { files: [file], items: [], getData: () => '', types: ['Files'] },
  });
  return ev;
}

describe('ThreadInlineComposer image paste (re #532)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    composeMock.body = '';
    composeMock.isOpen = true;
    composeMock.inlineMode = true;
    composeMock.status = 'editing';
    composeMock.selectedIdentity = IDENTITY;
    composeMock.startInlineImage.mockImplementation((file: File) => ({
      key: 'k-' + file.name,
      objectURL: 'blob:test/' + file.name,
    }));
    composeMock.uploadInlineImage.mockResolvedValue(null);
  });

  it('uploads a pasted image and inserts it inline in the editor', async () => {
    const { container } = render(ThreadInlineComposer, { props: { threadId: 'tid-1' } });
    await tick();
    const pm = container.querySelector<HTMLElement>('.ProseMirror');
    expect(pm).not.toBeNull();

    const file = new File([new Uint8Array([1, 2, 3])], 'shot.png', { type: 'image/png' });
    const ev = pasteEvent(file);
    pm!.dispatchEvent(ev);
    await tick();
    await tick();

    expect(ev.defaultPrevented).toBe(true);
    expect(composeMock.startInlineImage).toHaveBeenCalledWith(file);
    expect(composeMock.uploadInlineImage).toHaveBeenCalledWith('k-shot.png', file);
    const img = pm!.querySelector('img');
    expect(img).not.toBeNull();
    expect(img!.getAttribute('src')).toBe('blob:test/shot.png');
  });

  it('retracts the placeholder when the upload fails', async () => {
    composeMock.uploadInlineImage.mockResolvedValue('upload failed');
    const { container } = render(ThreadInlineComposer, { props: { threadId: 'tid-1' } });
    await tick();
    const pm = container.querySelector<HTMLElement>('.ProseMirror')!;
    pm.dispatchEvent(pasteEvent(new File(['x'], 'bad.png', { type: 'image/png' })));
    await new Promise((r) => setTimeout(r, 0));
    await tick();
    expect(pm.querySelector('img')).toBeNull();
  });
});
