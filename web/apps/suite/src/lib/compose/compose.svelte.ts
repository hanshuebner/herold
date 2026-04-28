/**
 * Compose store + send pipeline.
 *
 * One singleton compose-state-machine. Multi-window stacking
 * (REQ-UI-04, max 3 stacked compose windows) is deferred — v1 ships
 * single-window compose.
 *
 * Send pipeline per REQ-MAIL-14 / REQ-OPT-11:
 *   1. Email/set { create } the draft in the drafts mailbox with $draft.
 *   2. EmailSubmission/set { create } in the same batch with
 *      sendAt = now + <undoWindow>, identityId, envelope, and
 *      onSuccessUpdateEmail to flip drafts → sent and clear $draft on
 *      successful send.
 *   3. Show "Message sent" toast with Undo for <undoWindow> seconds.
 *   4. Undo: EmailSubmission/set { destroy: [<id>] } before sendAt;
 *      re-open compose with the saved content per REQ-MAIL-15 /
 *      REQ-DFT-60.
 *
 * The actual SMTP delivery happens server-side at sendAt; the suite's
 * Undo is purely a JMAP-destroy-before-the-deadline. After the toast
 * times out, herold sends.
 */

import { jmap, strict } from '../jmap/client';
import { Capability, type Invocation } from '../jmap/types';
import { mail } from '../mail/store.svelte';
import { settings } from '../settings/settings.svelte';
import { toast } from '../toast/toast.svelte';
import {
  emailHtmlBody,
  emailTextBody,
  type Address,
  type Email,
} from '../mail/types';

type ComposeStatus = 'idle' | 'editing' | 'sending';

/** One queued attachment, owned by a single compose state-machine. */
export interface ComposeAttachment {
  /** Stable per-compose key used by the UI. */
  key: string;
  name: string;
  size: number;
  type: string;
  /** Upload progress / status. */
  status: 'uploading' | 'ready' | 'failed';
  /** Server-assigned blob id once status === 'ready'. */
  blobId: string | null;
  /** Failure reason — surfaced as the row's helper text. */
  error: string | null;
}

interface ReplyContext {
  /** Parent email id — used to flip $answered / $forwarded after send. */
  parentId: string | null;
  /** Which keyword to set on the parent. null = none. */
  parentKeyword: '$answered' | '$forwarded' | null;
  /** RFC 5322 `In-Reply-To` value(s) to write on the new email. */
  inReplyTo: string[] | null;
  /** RFC 5322 `References` chain (parent's references + parent's messageId). */
  references: string[] | null;
}

const EMPTY_REPLY: ReplyContext = {
  parentId: null,
  parentKeyword: null,
  inReplyTo: null,
  references: null,
};

class ComposeStore {
  status = $state<ComposeStatus>('idle');
  to = $state('');
  cc = $state('');
  bcc = $state('');
  subject = $state('');
  body = $state('');
  errorMessage = $state<string | null>(null);

  /**
   * True when the user has opened the Cc/Bcc fields, or when those fields
   * are non-empty (e.g. opened via Undo with prior cc/bcc state). The
   * UI keeps them collapsed by default so the common single-recipient
   * compose stays uncluttered.
   */
  ccBccVisible = $state(false);

  /** Reply / forward context — null fields mean "this is a fresh compose". */
  replyContext = $state<ReplyContext>({ ...EMPTY_REPLY });

  /**
   * Attachments queued for the in-progress message. Each entry tracks
   * its own upload state so the UI can render uploading / failed / ready
   * affordances without blocking the rest of the compose.
   */
  attachments = $state<ComposeAttachment[]>([]);
  #attachmentSeq = 0;

  /**
   * Server-assigned id of the draft this compose is currently editing.
   * null on fresh compose; set when:
   *   1. the auto-save effect creates a draft (Email/set create), or
   *   2. the user opened an existing draft via openDraft.
   * Drives whether send() updates an existing draft or creates one,
   * and whether closeWithConfirm destroys on discard.
   */
  editingDraftId = $state<string | null>(null);

  /**
   * Optional pre-open guard.  When set, every open* path calls it
   * first; a false return aborts the open.  Wired by composeStack to
   * snapshot the current compose into the minimized tray before a
   * fresh compose takes over the modal.
   */
  #beforeOpenHook: (() => boolean) | null = null;
  setBeforeOpenHook(fn: (() => boolean) | null): void {
    this.#beforeOpenHook = fn;
  }
  #runBeforeOpen(): boolean {
    return this.#beforeOpenHook ? this.#beforeOpenHook() : true;
  }

  /** Open a fresh compose. */
  openBlank(): void {
    if (!this.#runBeforeOpen()) return;
    this.to = '';
    this.cc = '';
    this.bcc = '';
    this.subject = '';
    this.body = '';
    this.errorMessage = null;
    this.ccBccVisible = false;
    this.replyContext = { ...EMPTY_REPLY };
    this.editingDraftId = null;
    this.attachments = [];
    this.status = 'editing';
    this.#ensureAccountReady();
  }

  /**
   * Open compose pre-populated (e.g. after Undo). Caller is responsible
   * for resetting / setting the reply context.
   */
  openWith(args: {
    to: string;
    cc?: string;
    bcc?: string;
    subject: string;
    body: string;
    replyContext?: ReplyContext;
    /** When set the compose is editing an existing server draft. */
    draftId?: string | null;
    /** Skip the beforeOpen hook — used when restoring from the minimized tray. */
    skipHook?: boolean;
  }): void {
    if (!args.skipHook && !this.#runBeforeOpen()) return;
    this.to = args.to;
    this.cc = args.cc ?? '';
    this.bcc = args.bcc ?? '';
    this.subject = args.subject;
    this.body = args.body;
    this.replyContext = args.replyContext ?? { ...EMPTY_REPLY };
    this.errorMessage = null;
    this.ccBccVisible = Boolean(this.cc || this.bcc);
    this.editingDraftId = args.draftId ?? null;
    this.attachments = [];
    this.status = 'editing';
    this.#ensureAccountReady();
  }

  /** Open compose as a reply to the given email. */
  openReply(parent: Email): void {
    this.openWith({
      to: addressToString(parent.from?.[0]),
      subject: replySubject(parent.subject),
      body: formatReplyQuote(parent),
      replyContext: {
        parentId: parent.id,
        parentKeyword: '$answered',
        inReplyTo: parent.messageId ?? null,
        references: mergeReferences(parent),
      },
    });
  }

  /**
   * Open compose as a reply-all: To = parent.from, Cc = (parent.to ∪
   * parent.cc) minus every Identity.email this user owns minus the
   * primary recipient. Falls back to a regular reply when no Cc would
   * survive the self-filter.
   */
  openReplyAll(parent: Email): void {
    const selfEmails = new Set<string>();
    for (const id of mail.identities.values()) {
      selfEmails.add(id.email.toLowerCase());
    }
    const cc = computeReplyAllCc(parent, selfEmails);
    this.openWith({
      to: addressToString(parent.from?.[0]),
      cc: cc.map(addressToString).join(', '),
      subject: replySubject(parent.subject),
      body: formatReplyQuote(parent),
      replyContext: {
        parentId: parent.id,
        parentKeyword: '$answered',
        inReplyTo: parent.messageId ?? null,
        references: mergeReferences(parent),
      },
    });
  }

  /**
   * Open compose pre-populated with an existing server draft. The
   * caller has already loaded the email's body via Email/get with
   * fetchHTMLBodyValues; this method only projects it into compose
   * state and pins editingDraftId so subsequent saves update the same
   * row instead of creating a new one.
   */
  openDraft(draft: Email): void {
    const html = emailHtmlBody(draft) ?? '';
    const text = emailTextBody(draft) ?? '';
    const body = html || (text ? plainTextToHtml(text) : '');
    this.openWith({
      to: (draft.to ?? []).map(addressToString).join(', '),
      cc: (draft.cc ?? []).map(addressToString).join(', '),
      bcc: (draft.bcc ?? []).map(addressToString).join(', '),
      subject: draft.subject ?? '',
      body,
      draftId: draft.id,
      replyContext: {
        parentId: null,
        parentKeyword: null,
        inReplyTo: draft.inReplyTo ?? null,
        references: draft.references ?? null,
      },
    });
  }

  /** Open compose as a forward of the given email. */
  openForward(parent: Email): void {
    this.openWith({
      to: '',
      subject: forwardSubject(parent.subject),
      body: formatForwardQuote(parent),
      replyContext: {
        parentId: parent.id,
        parentKeyword: '$forwarded',
        inReplyTo: parent.messageId ?? null,
        references: mergeReferences(parent),
      },
    });
  }

  /** True when the form holds anything the user might want to keep. */
  get hasContent(): boolean {
    if (this.to.trim() || this.cc.trim() || this.bcc.trim()) return true;
    if (this.subject.trim()) return true;
    if (this.attachments.length > 0) return true;
    // Body is HTML; an empty editor renders one or more empty <p> tags.
    // Strip tags and whitespace before deciding it's "empty".
    const textOnly = this.body.replace(/<[^>]+>/g, '').trim();
    return textOnly.length > 0;
  }

  /** True when at least one attachment is still uploading. */
  get attachmentsBusy(): boolean {
    return this.attachments.some((a) => a.status === 'uploading');
  }

  /**
   * Queue one or more files for upload. Each file becomes an attachment
   * row in `uploading` status; the upload runs in parallel and the row
   * flips to `ready` (with `blobId`) or `failed` (with `error`) on its own.
   * Files exceeding the server's advertised maxSizeUpload (when present)
   * are rejected immediately as `failed`.
   */
  async addAttachments(files: File[] | FileList): Promise<void> {
    const accountId = mail.mailAccountId;
    if (!accountId) return;
    const list = Array.from(files);
    if (list.length === 0) return;
    const maxSize = jmap.maxUploadSize;
    const tasks: Promise<void>[] = [];
    for (const file of list) {
      const key = `att-${++this.#attachmentSeq}`;
      const att: ComposeAttachment = {
        key,
        name: file.name || 'file',
        size: file.size,
        type: file.type || 'application/octet-stream',
        status: 'uploading',
        blobId: null,
        error: null,
      };
      if (maxSize !== null && file.size > maxSize) {
        att.status = 'failed';
        att.error = `Too large: ${formatBytes(file.size)} (max ${formatBytes(maxSize)})`;
      }
      this.attachments = [...this.attachments, att];
      if (att.status === 'failed') continue;
      tasks.push(this.#uploadOne(att.key, file, accountId));
    }
    await Promise.all(tasks);
  }

  /** Remove a queued attachment by key. */
  removeAttachment(key: string): void {
    this.attachments = this.attachments.filter((a) => a.key !== key);
  }

  /** Retry a failed upload. */
  async retryAttachment(key: string, file: File): Promise<void> {
    const accountId = mail.mailAccountId;
    if (!accountId) return;
    this.#patchAttachment(key, { status: 'uploading', error: null, blobId: null });
    await this.#uploadOne(key, file, accountId);
  }

  async #uploadOne(key: string, file: File, accountId: string): Promise<void> {
    try {
      const result = await jmap.uploadBlob({
        accountId,
        body: file,
        type: file.type || 'application/octet-stream',
      });
      this.#patchAttachment(key, {
        status: 'ready',
        blobId: result.blobId,
        error: null,
      });
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Upload failed';
      this.#patchAttachment(key, { status: 'failed', error: msg });
    }
  }

  #patchAttachment(key: string, patch: Partial<ComposeAttachment>): void {
    this.attachments = this.attachments.map((a) =>
      a.key === key ? { ...a, ...patch } : a,
    );
  }

  /** Close and clear. */
  close(): void {
    this.status = 'idle';
    this.to = '';
    this.cc = '';
    this.bcc = '';
    this.subject = '';
    this.body = '';
    this.errorMessage = null;
    this.ccBccVisible = false;
    this.replyContext = { ...EMPTY_REPLY };
    this.editingDraftId = null;
    this.attachments = [];
  }

  /**
   * Discard the current compose. When a server-side draft has already
   * been auto-saved, destroy the row so it doesn't accumulate in the
   * Drafts folder; otherwise just reset state. This is what
   * closeWithConfirm calls on user-confirmed discard.
   */
  async discard(): Promise<void> {
    const draftId = this.editingDraftId;
    const accountId = mail.mailAccountId;
    if (draftId && accountId) {
      try {
        const { responses } = await jmap.batch((b) => {
          b.call(
            'Email/set',
            { accountId, destroy: [draftId] },
            [Capability.Mail],
          );
        });
        strict(responses);
      } catch (err) {
        // Discard can race with the auto-save creating the row;
        // surface as a toast rather than blocking the close.
        toast.show({
          message: err instanceof Error
            ? `Could not delete draft: ${err.message}`
            : 'Could not delete draft',
          kind: 'error',
          timeoutMs: 6000,
        });
      }
    }
    this.close();
  }

  /**
   * Persist the current compose state as a draft. Creates the draft
   * row on first call, updates the same row on subsequent calls.
   * No-op when:
   *   - the compose is not editing
   *   - the form has no content (avoid spawning empty drafts)
   *   - mail account or drafts mailbox is unavailable
   * Returns true on a successful round-trip (so callers can stop
   * marking the compose dirty).
   */
  async persistDraft(): Promise<boolean> {
    if (this.status !== 'editing') return false;
    if (!this.hasContent) return false;
    const accountId = mail.mailAccountId;
    if (!accountId) return false;
    if (!mail.primaryIdentity || !mail.drafts) {
      await this.#ensureAccountReady();
    }
    const identity = mail.primaryIdentity;
    const drafts = mail.drafts;
    if (!identity || !drafts) return false;

    const toAddrs = parseAddressList(this.to);
    const ccAddrs = parseAddressList(this.cc);
    const bccAddrs = parseAddressList(this.bcc);
    const bodyHtml = this.body;
    const bodyText = htmlToPlainText(bodyHtml);

    const readyAttachments = this.attachments.filter(
      (a): a is ComposeAttachment & { blobId: string } =>
        a.status === 'ready' && typeof a.blobId === 'string',
    );

    // Body structure is multipart/alternative when there are no
    // attachments; with attachments it becomes multipart/mixed wrapping
    // the alternative body + one part per attachment.
    const altPart = {
      type: 'multipart/alternative',
      subParts: [
        { partId: '1', type: 'text/plain', charset: 'utf-8' },
        { partId: '2', type: 'text/html', charset: 'utf-8' },
      ],
    };
    const bodyStructure: Record<string, unknown> =
      readyAttachments.length === 0
        ? altPart
        : {
            type: 'multipart/mixed',
            subParts: [
              altPart,
              ...readyAttachments.map((a) => ({
                blobId: a.blobId,
                type: a.type,
                disposition: 'attachment',
                name: a.name,
                size: a.size,
              })),
            ],
          };
    const attachmentParts = readyAttachments.map((a) => ({
      blobId: a.blobId,
      type: a.type,
      disposition: 'attachment',
      name: a.name,
      size: a.size,
    }));

    const fields: Record<string, unknown> = {
      from: [{ name: identity.name, email: identity.email }],
      to: toAddrs.map((email) => ({ email, name: null })),
      subject: this.subject,
      bodyValues: {
        '1': { value: bodyText, isTruncated: false, isEncodingProblem: false },
        '2': { value: bodyHtml, isTruncated: false, isEncodingProblem: false },
      },
      textBody: [{ partId: '1', type: 'text/plain', charset: 'utf-8' }],
      htmlBody: [{ partId: '2', type: 'text/html', charset: 'utf-8' }],
      bodyStructure,
      ...(attachmentParts.length > 0 ? { attachments: attachmentParts } : {}),
      hasAttachment: attachmentParts.length > 0,
    };
    if (ccAddrs.length > 0) {
      fields.cc = ccAddrs.map((email) => ({ email, name: null }));
    }
    if (bccAddrs.length > 0) {
      fields.bcc = bccAddrs.map((email) => ({ email, name: null }));
    }
    if (this.replyContext.inReplyTo && this.replyContext.inReplyTo.length > 0) {
      fields.inReplyTo = this.replyContext.inReplyTo;
    }
    if (this.replyContext.references && this.replyContext.references.length > 0) {
      fields.references = this.replyContext.references;
    }

    try {
      if (this.editingDraftId) {
        const { responses } = await jmap.batch((b) => {
          b.call(
            'Email/set',
            {
              accountId,
              update: { [this.editingDraftId!]: fields },
            },
            [Capability.Mail],
          );
        });
        strict(responses);
      } else {
        const create = {
          ...fields,
          mailboxIds: { [drafts.id]: true },
          keywords: { $draft: true, $seen: true },
        };
        const { responses } = await jmap.batch((b) => {
          b.call(
            'Email/set',
            {
              accountId,
              create: { autosave: create },
            },
            [Capability.Mail],
          );
        });
        strict(responses);
        const result = invocationArgs<{
          created?: Record<string, { id: string }>;
        }>(responses[0]);
        const id = result.created?.autosave?.id;
        if (id) this.editingDraftId = id;
      }
      return true;
    } catch (err) {
      // Auto-save errors don't block the user; log and let the next
      // attempt try again. errorMessage is reserved for the explicit
      // Send path so it stays visible there only.
      console.error('compose: auto-save failed', err);
      return false;
    }
  }

  get isOpen(): boolean {
    return this.status !== 'idle';
  }

  async send(): Promise<void> {
    if (this.status === 'sending') return;
    const accountId = mail.mailAccountId;
    if (!accountId) {
      this.errorMessage = 'No Mail account on this session';
      return;
    }
    if (!mail.primaryIdentity || !mail.drafts) {
      await this.#ensureAccountReady();
    }
    const identity = mail.primaryIdentity;
    const drafts = mail.drafts;
    const sentMailbox = mail.sent;
    if (!identity) {
      this.errorMessage = 'No identity available — cannot send';
      return;
    }
    if (!drafts) {
      this.errorMessage = 'No drafts mailbox — cannot send';
      return;
    }

    const toRecipients = parseAddressList(this.to);
    const ccRecipients = parseAddressList(this.cc);
    const bccRecipients = parseAddressList(this.bcc);
    const allRecipients = [...toRecipients, ...ccRecipients, ...bccRecipients];
    if (allRecipients.length === 0) {
      this.errorMessage = 'At least one recipient is required';
      return;
    }
    const subject = this.subject;
    const bodyHtml = this.body;
    const bodyText = htmlToPlainText(bodyHtml);
    const replyContext = this.replyContext;

    // Snapshot for Undo (full state, including reply context).
    const savedTo = this.to;
    const savedCc = this.cc;
    const savedBcc = this.bcc;
    const savedSubject = subject;
    const savedBody = bodyHtml;
    const savedReplyContext: ReplyContext = { ...replyContext };

    this.errorMessage = null;
    this.status = 'sending';

    // Per REQ-SET-06 / REQ-MAIL-14: configurable undo window. 0 disables
    // the hold and sends immediately (sendAt = null).
    const undoWindowSec = settings.undoWindowSec;
    const sendAt =
      undoWindowSec > 0
        ? new Date(Date.now() + undoWindowSec * 1000).toISOString()
        : null;

    const onSuccessUpdate: Record<string, true | null> = {};
    onSuccessUpdate[`mailboxIds/${drafts.id}`] = null;
    if (sentMailbox) onSuccessUpdate[`mailboxIds/${sentMailbox.id}`] = true;
    onSuccessUpdate['keywords/$draft'] = null;
    onSuccessUpdate['keywords/$seen'] = true;

    // Build the new email — multipart/alternative with text/plain + text/html,
    // wrapped in multipart/mixed when there are attachments.
    // Include In-Reply-To / References when this is a reply or forward.
    // Mark the parent $answered / $forwarded in the same batch
    // (REQ-MAIL-33) so the UI reflects the change immediately.
    const readyAttachments = this.attachments.filter(
      (a): a is ComposeAttachment & { blobId: string } =>
        a.status === 'ready' && typeof a.blobId === 'string',
    );
    const altPart = {
      type: 'multipart/alternative',
      subParts: [
        { partId: '1', type: 'text/plain', charset: 'utf-8' },
        { partId: '2', type: 'text/html', charset: 'utf-8' },
      ],
    };
    const sendBodyStructure: Record<string, unknown> =
      readyAttachments.length === 0
        ? altPart
        : {
            type: 'multipart/mixed',
            subParts: [
              altPart,
              ...readyAttachments.map((a) => ({
                blobId: a.blobId,
                type: a.type,
                disposition: 'attachment',
                name: a.name,
                size: a.size,
              })),
            ],
          };
    const sendAttachmentParts = readyAttachments.map((a) => ({
      blobId: a.blobId,
      type: a.type,
      disposition: 'attachment',
      name: a.name,
      size: a.size,
    }));

    const draftEmail: Record<string, unknown> = {
      mailboxIds: { [drafts.id]: true },
      keywords: { $draft: true, $seen: true },
      from: [{ name: identity.name, email: identity.email }],
      to: toRecipients.map((email) => ({ email, name: null })),
      subject,
      bodyValues: {
        '1': { value: bodyText, isTruncated: false, isEncodingProblem: false },
        '2': { value: bodyHtml, isTruncated: false, isEncodingProblem: false },
      },
      textBody: [{ partId: '1', type: 'text/plain', charset: 'utf-8' }],
      htmlBody: [{ partId: '2', type: 'text/html', charset: 'utf-8' }],
      bodyStructure: sendBodyStructure,
      ...(sendAttachmentParts.length > 0 ? { attachments: sendAttachmentParts } : {}),
      hasAttachment: sendAttachmentParts.length > 0,
    };
    if (ccRecipients.length > 0) {
      draftEmail.cc = ccRecipients.map((email) => ({ email, name: null }));
    }
    // Bcc: per RFC 8621 §4.1.2 the Bcc header is set on the draft so the
    // sender's Sent-folder copy retains the blind list, but the envelope
    // recipients (below) are what actually drive delivery — the server
    // strips Bcc from the wire-bound message.
    if (bccRecipients.length > 0) {
      draftEmail.bcc = bccRecipients.map((email) => ({ email, name: null }));
    }
    if (replyContext.inReplyTo && replyContext.inReplyTo.length > 0) {
      draftEmail.inReplyTo = replyContext.inReplyTo;
    }
    if (replyContext.references && replyContext.references.length > 0) {
      draftEmail.references = replyContext.references;
    }

    // When editing an existing draft, update it in place and reference
    // it directly from EmailSubmission/set; otherwise create a fresh
    // draft and use the #draft1 creation reference. The
    // onSuccessUpdate then drops $draft + moves into Sent on the same
    // row in either case.
    const existingDraftId = this.editingDraftId;
    const setArgs: Record<string, unknown> = {
      accountId,
    };
    let submissionEmailId: string;
    if (existingDraftId) {
      // Updating an existing draft: send the same field set as an
      // /update entry.
      const updates: Record<string, unknown> = { [existingDraftId]: draftEmail };
      if (replyContext.parentId && replyContext.parentKeyword) {
        updates[replyContext.parentId] = {
          [`keywords/${replyContext.parentKeyword}`]: true,
        };
      }
      setArgs.update = updates;
      submissionEmailId = existingDraftId;
    } else {
      setArgs.create = { draft1: draftEmail };
      if (replyContext.parentId && replyContext.parentKeyword) {
        setArgs.update = {
          [replyContext.parentId]: {
            [`keywords/${replyContext.parentKeyword}`]: true,
          },
        };
      }
      submissionEmailId = '#draft1';
    }
    try {
      const { responses } = await jmap.batch((b) => {
        b.call('Email/set', setArgs, [Capability.Mail]);
        b.call(
          'EmailSubmission/set',
          {
            accountId,
            create: {
              sub1: {
                emailId: submissionEmailId,
                identityId: identity.id,
                envelope: {
                  mailFrom: { email: identity.email },
                  rcptTo: allRecipients.map((email) => ({ email })),
                },
                sendAt,
              },
            },
            onSuccessUpdateEmail: { '#sub1': onSuccessUpdate },
          },
          [Capability.Submission],
        );
      });
      strict(responses);

      const setResult = invocationArgs<{
        notCreated?: Record<string, { type: string; description?: string }>;
      }>(responses[0]);
      if (setResult.notCreated?.draft1) {
        const e = setResult.notCreated.draft1;
        throw new Error(e.description ?? e.type);
      }

      const subResult = invocationArgs<{
        created?: Record<string, { id: string }>;
        notCreated?: Record<string, { type: string; description?: string }>;
      }>(responses[1]);
      if (subResult.notCreated?.sub1) {
        const e = subResult.notCreated.sub1;
        throw new Error(e.description ?? e.type);
      }
      const submissionId = subResult.created?.sub1?.id;
      if (!submissionId) {
        throw new Error('Submission created but no id returned');
      }

      this.close();

      if (undoWindowSec > 0) {
        toast.show({
          message: 'Message sent',
          timeoutMs: undoWindowSec * 1000,
          undo: async () => {
            try {
              const result = await jmap.batch((b) => {
                b.call(
                  'EmailSubmission/set',
                  {
                    accountId,
                    destroy: [submissionId],
                  },
                  [Capability.Submission],
                );
              });
              strict(result.responses);
              // Re-open compose with the full saved state (including reply context).
              this.openWith({
                to: savedTo,
                cc: savedCc,
                bcc: savedBcc,
                subject: savedSubject,
                body: savedBody,
                replyContext: savedReplyContext,
              });
            } catch (err) {
              toast.show({
                message:
                  err instanceof Error
                    ? `Could not cancel send: ${err.message}`
                    : 'Could not cancel send',
                kind: 'error',
                timeoutMs: 6000,
              });
            }
          },
        });
      } else {
        toast.show({ message: 'Message sent', timeoutMs: 4000 });
      }
    } catch (err) {
      this.status = 'editing';
      this.errorMessage = err instanceof Error ? err.message : String(err);
      return;
    }
  }

  /**
   * In-flight warmup promise so concurrent open/send paths share one
   * load. Cleared on completion so a later call re-checks the store
   * (e.g. after a logout/login or an explicit identities refresh).
   */
  #warmup: Promise<void> | null = null;

  #ensureAccountReady(): Promise<void> {
    if (mail.primaryIdentity && mail.drafts) {
      return Promise.resolve();
    }
    if (!this.#warmup) {
      this.#warmup = this.#warmupAccount().finally(() => {
        this.#warmup = null;
      });
    }
    return this.#warmup;
  }

  async #warmupAccount(): Promise<void> {
    try {
      const tasks: Promise<unknown>[] = [];
      if (mail.mailboxes.size === 0) tasks.push(mail.loadMailboxes());
      if (mail.identities.size === 0) tasks.push(mail.loadIdentities());
      if (tasks.length > 0) await Promise.all(tasks);
    } catch (err) {
      this.errorMessage = err instanceof Error ? err.message : String(err);
    }
  }
}

/**
 * Parse a comma- or semicolon-separated string of email addresses. Strips
 * "Name <addr>" wrappers — for v1 we keep only the bare address; structured
 * Address objects come in a follow-up pass.
 */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

function parseAddressList(raw: string): string[] {
  return raw
    .split(/[,;\n]/)
    .map((s) => {
      const trimmed = s.trim();
      // Match a "Name <email>" form; otherwise return the whole string.
      const m = trimmed.match(/<([^>]+)>/);
      return m ? m[1]!.trim() : trimmed;
    })
    .filter((s) => s.length > 0);
}

function invocationArgs<T>(inv: Invocation | undefined): T {
  if (!inv) throw new Error('Expected method invocation, got undefined');
  return inv[1] as T;
}

// ── Reply / forward formatters ────────────────────────────────────────

function addressToString(a: Address | undefined): string {
  if (!a) return '';
  return a.name?.trim() ? `${a.name} <${a.email}>` : a.email;
}

function addressListToString(list: Address[] | null | undefined): string {
  if (!list || list.length === 0) return '';
  return list.map(addressToString).join(', ');
}

function replySubject(orig: string | null): string {
  const s = orig ?? '';
  if (/^re:\s*/i.test(s)) return s;
  return `Re: ${s}`;
}

function forwardSubject(orig: string | null): string {
  const s = orig ?? '';
  if (/^fwd?:\s*/i.test(s)) return s;
  return `Fwd: ${s}`;
}

/**
 * Compute the Cc address list for a reply-all: every parent To and Cc
 * address, minus those whose lowercase email matches the user's
 * Identity.email set, minus the primary reply target (parent.from).
 * Order preserves the parent's To-then-Cc sequence; duplicates are
 * dropped on the lowercase form.
 */
function computeReplyAllCc(parent: Email, selfEmails: Set<string>): Address[] {
  const out: Address[] = [];
  const primary = parent.from?.[0]?.email?.toLowerCase() ?? '';
  const seen = new Set<string>([primary]);
  for (const list of [parent.to, parent.cc]) {
    if (!list) continue;
    for (const addr of list) {
      const lc = (addr.email ?? '').toLowerCase();
      if (!lc) continue;
      if (selfEmails.has(lc)) continue;
      if (seen.has(lc)) continue;
      seen.add(lc);
      out.push(addr);
    }
  }
  return out;
}

function mergeReferences(parent: Email): string[] {
  const refs: string[] = [];
  if (parent.references) refs.push(...parent.references);
  if (parent.messageId) refs.push(...parent.messageId);
  return refs;
}

/**
 * Get the parent email's plain-text body for quoting. Prefers the textBody
 * if present; falls back to a tag-stripped version of htmlBody.
 */
function parentBodyText(parent: Email): string {
  const text = emailTextBody(parent);
  if (text) return text;
  const html = emailHtmlBody(parent);
  if (!html) return '';
  return htmlToText(html);
}

function htmlToText(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  const out = doc.body?.textContent ?? '';
  // Collapse runs of >2 blank lines that result from block elements.
  return out.replace(/\n{3,}/g, '\n\n').trim();
}

/**
 * Convert the editor's HTML to a plain-text projection for the
 * text/plain body part. We can't just strip tags — we need block-level
 * separations to survive (paragraph → newline, list-item → "- ", etc.).
 */
function htmlToPlainText(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  if (!doc.body) return '';
  const out: string[] = [];
  walk(doc.body, out);
  return out.join('').replace(/\n{3,}/g, '\n\n').trim();
}

function walk(node: Node, out: string[]): void {
  if (node.nodeType === Node.TEXT_NODE) {
    out.push(node.textContent ?? '');
    return;
  }
  if (!(node instanceof Element)) return;
  const tag = node.tagName.toLowerCase();
  switch (tag) {
    case 'br':
      out.push('\n');
      return;
    case 'li': {
      out.push('- ');
      for (const c of Array.from(node.childNodes)) walk(c, out);
      out.push('\n');
      return;
    }
    case 'blockquote': {
      const inner: string[] = [];
      for (const c of Array.from(node.childNodes)) walk(c, inner);
      const quoted = inner
        .join('')
        .split('\n')
        .map((l) => (l ? `> ${l}` : '>'))
        .join('\n');
      out.push(quoted);
      out.push('\n');
      return;
    }
    case 'p':
    case 'div':
    case 'h1':
    case 'h2':
    case 'h3':
    case 'h4':
    case 'h5':
    case 'h6':
    case 'ul':
    case 'ol':
    case 'pre':
    case 'hr':
      for (const c of Array.from(node.childNodes)) walk(c, out);
      out.push('\n');
      return;
    case 'a': {
      const href = node.getAttribute('href');
      const text = node.textContent ?? '';
      if (href && href !== text) out.push(`${text} (${href})`);
      else out.push(text);
      return;
    }
    default:
      for (const c of Array.from(node.childNodes)) walk(c, out);
  }
}

function formatDateForQuote(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  return d.toLocaleString(undefined, {
    weekday: 'short',
    day: 'numeric',
    month: 'short',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

function quoteBlock(text: string): string {
  return text
    .split('\n')
    .map((line) => `> ${line}`)
    .join('\n');
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

/**
 * Convert plain-text body content to a paragraph-broken HTML block. Used
 * when quoting parent bodies in reply / forward — ProseMirror parses the
 * resulting HTML cleanly.
 */
function plainTextToHtml(text: string): string {
  if (!text) return '';
  const paragraphs = text.split(/\n{2,}/);
  return paragraphs
    .map((p) => {
      const escaped = escapeHtml(p).replace(/\n/g, '<br>');
      return `<p>${escaped}</p>`;
    })
    .join('');
}

function formatReplyQuote(parent: Email): string {
  const senderLabel = addressToString(parent.from?.[0]) || '(unknown sender)';
  const dateStr = formatDateForQuote(parent.sentAt ?? parent.receivedAt);
  const body = parentBodyText(parent);
  const header = dateStr
    ? `On ${dateStr}, ${senderLabel} wrote:`
    : `${senderLabel} wrote:`;
  const quotedHtml = body ? plainTextToHtml(body) : '<p>(no quoted body)</p>';
  return `<p></p><p></p><p>${escapeHtml(header)}</p><blockquote>${quotedHtml}</blockquote><p></p>`;
}

function formatForwardQuote(parent: Email): string {
  const fromStr = addressListToString(parent.from);
  const toStr = addressListToString(parent.to);
  const dateStr = formatDateForQuote(parent.sentAt ?? parent.receivedAt);
  const subject = parent.subject ?? '';
  const body = parentBodyText(parent);
  const headerLines = [
    '---------- Forwarded message ----------',
    `From: ${fromStr}`,
    `Date: ${dateStr}`,
    `Subject: ${subject}`,
    `To: ${toStr}`,
  ]
    .map((l) => `<p>${escapeHtml(l)}</p>`)
    .join('');
  const quotedHtml = body ? plainTextToHtml(body) : '<p>(no quoted body)</p>';
  return `<p></p><p></p>${headerLines}<p></p>${quotedHtml}<p></p>`;
}

export const compose = new ComposeStore();

/**
 * Pure helpers that compose() exercises internally; named-exported
 * here so unit tests can target them without spinning up the whole
 * ComposeStore or its singleton-store dependencies. Not part of the
 * stable public surface — name suffix _forTest signals that.
 */
export const _internals_forTest = {
  parseAddressList,
  htmlToPlainText,
  replySubject,
  forwardSubject,
  mergeReferences,
  addressToString,
  addressListToString,
  escapeHtml,
  plainTextToHtml,
  computeReplyAllCc,
  formatBytes,
};
