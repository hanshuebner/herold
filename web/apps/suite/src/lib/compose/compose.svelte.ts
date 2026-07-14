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
import { localeTag } from '../i18n/i18n.svelte';
import { applyImage } from './editor';
import { generateImageProxy } from './image-proxy';
import {
  showExternalSubmissionFailure,
  parseExternalSubmissionFailure,
} from '../mail/compose-failure';
import { hasExternalSubmission } from '../auth/capabilities';
import {
  emailHtmlBody,
  emailTextBody,
  type Address,
  type Email,
  type Identity,
} from '../mail/types';
import {
  tryCommit,
  recipientToString,
  type Recipient,
} from './recipient-parse';
import { buildSelfEmailSet, isFromSelf, isOwnSentMessage } from '../mail/identity-match';
import { selectReplyIdentity, localAliasesForCc } from './reply-identity';
import {
  createFileShare,
  destroyFileShares,
  confirmFileShares,
  isUnsafeExtension,
  offloadThresholdBytes,
  defaultTtlSeconds,
  hasFileShares,
  type FileShare,
  type FileShareCreateArgs,
  type FileShareSourceInfo,
} from '../jmap/file-shares';

export type { Recipient };

type ComposeStatus = 'idle' | 'editing' | 'sending';

/**
 * An offloaded file share tracked per compose session (REQ-ATT-64).
 * Created in 'pending' state; confirmed to 'active' on successful send;
 * destroyed on compose discard.
 */
export interface ComposeShare {
  /** Stable per-compose key matching the original attachment key. */
  key: string;
  /** Server-assigned share id. */
  shareId: string;
  name: string;
  size: number;
  type: string;
  /** Public URL inserted into the message body. */
  url: string;
  /** Absolute expiry as returned by the server (ISO-8601 UTC). */
  expiresAt: string;
  maxDownloads: number | null;
  hasPassword: boolean;
}

/**
 * Options chosen in the offload offer dialog (REQ-ATT-65).
 * Consumed by `addAttachments` after the user confirms.
 */
export interface ShareOptions {
  /** Desired lifetime in seconds. */
  expiresIn: number;
  maxDownloads?: number;
  password?: string;
}

/**
 * Callback type injected into compose by ComposeWindow so the UI can
 * display an offer dialog (REQ-ATT-61) and return the user's decision.
 *
 * resolve(true)  => proceed with offload (with opts).
 * resolve(false) => attach normally (or give up when forced).
 * resolve(null)  => user cancelled (abort the whole attachment add).
 */
export type OffloadOfferFn = (args: {
  file: File;
  forced: boolean;
  opts: ShareOptions;
}) => Promise<{ offload: boolean; opts: ShareOptions } | null>;

/**
 * Callback type injected into compose by ComposeWindow so #offloadOne can
 * insert the share-link HTML block directly into the live ProseMirror
 * document (REQ-ATT-63). When null the fallback string-mutation path runs.
 */
export type InsertShareLinkFn = (html: string) => void;

/**
 * Callback type injected into compose by ComposeWindow so removeShare can
 * retract the share-link HTML block from the live ProseMirror document
 * (REQ-ATT-64). When null the fallback string-mutation path runs.
 */
export type RemoveShareLinkFn = (url: string) => void;

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
  /**
   * Inline images get disposition: 'inline' on the part and a Content-
   * ID that the body's <img src="cid:..."> references. Issue #20.
   * Regular file attachments leave inline=false / cid=null.
   */
  inline?: boolean;
  cid?: string | null;
  /**
   * The blob: URL currently rendered as this inline image's `<img src>`
   * while the message is being composed. `startInlineImage` sets this to
   * `URL.createObjectURL(file)` — the full-resolution original — so the
   * editor shows a thumbnail immediately (issue #74). When the image
   * exceeds the downscale threshold (issue #243), a generated proxy blob
   * URL replaces it here shortly after and the full-resolution URL is
   * revoked; the outbound upload (`uploadInlineImage`) always uploads the
   * original `File` and is unaffected by this swap. On send/persist the
   * body is rewritten to replace whichever URL is current with
   * `cid:<cid>` so the receiving client resolves it against the inline
   * part. Revoked when the attachment is removed.
   */
  objectURL?: string | null;
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
  /**
   * True when the compose is rendered inline inside ThreadReader rather
   * than as a floating modal. Set by ThreadReplyBar before opening a
   * reply; reset by close() so subsequent opens default to floating.
   * re #38.
   */
  inlineMode = $state(false);
  to = $state('');
  cc = $state('');
  bcc = $state('');
  /**
   * Structured recipient chips for the To field. Kept in sync with
   * `to` (string form). The RecipientField component drives these;
   * openWith / openReply etc. populate both the string and these arrays.
   * REQ-MAIL-11a..d.
   */
  toRecipients = $state<Recipient[]>([]);
  ccRecipients = $state<Recipient[]>([]);
  bccRecipients = $state<Recipient[]>([]);
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
   * The Identity selected as the compose's From. Set by the reply /
   * reply-all / forward entry points via the REQ-MAIL-12a match
   * algorithm (`selectReplyIdentity`), and by openWith for paths that
   * already know the identity. null means "use mail.primaryIdentity"
   * — the persistDraft / send paths fall back to that as the legacy
   * behaviour. The From picker (not yet implemented) will write here
   * when the user changes the From mid-compose.
   */
  selectedIdentity = $state<Identity | null>(null);

  /**
   * Attachments queued for the in-progress message. Each entry tracks
   * its own upload state so the UI can render uploading / failed / ready
   * affordances without blocking the rest of the compose.
   */
  attachments = $state<ComposeAttachment[]>([]);
  #attachmentSeq = 0;

  /**
   * Offloaded file shares for the current compose session (REQ-ATT-64).
   * Each entry corresponds to an attachment that was offloaded via
   * FileShare/set create. Shares are pending until the message is sent;
   * they are destroyed on discard.
   */
  shares = $state<ComposeShare[]>([]);

  /**
   * Callback wired by ComposeWindow so addAttachments can present the
   * offload offer dialog to the user (REQ-ATT-61). When null (e.g.
   * when the capability is absent), offload is never attempted.
   */
  #offloadOfferFn: OffloadOfferFn | null = null;

  setOffloadOfferFn(fn: OffloadOfferFn | null): void {
    this.#offloadOfferFn = fn;
  }

  /**
   * Editor-backed callbacks for share-link insertion and removal (REQ-ATT-63/64).
   * Registered by ComposeWindow when the ProseMirror view mounts; cleared when
   * it unmounts. When null the string-mutation fallback is used (covers unit
   * tests and any not-yet-opened compose path).
   */
  #insertShareLinkFn: InsertShareLinkFn | null = null;
  #removeShareLinkFn: RemoveShareLinkFn | null = null;

  setShareEditorFns(
    insert: InsertShareLinkFn | null,
    remove: RemoveShareLinkFn | null,
  ): void {
    this.#insertShareLinkFn = insert;
    this.#removeShareLinkFn = remove;
  }

  /**
   * Editor-backed callback that swaps an inline image node's `src` in the
   * live ProseMirror document (issue #243). Registered by ComposeWindow /
   * ThreadInlineComposer alongside `setShareEditorFns`, driven off the same
   * mounted EditorView. Used by `#applyImageProxy` to replace the
   * full-resolution blob: URL with a downscaled proxy once it's ready.
   * When null (editor not mounted, or a unit test) the proxy is generated
   * but never swapped in, which is harmless — the attachment record's
   * `objectURL` also does not update, so callers relying on it fall back
   * to the original.
   */
  #swapInlineImageSrcFn: ((oldSrc: string, newSrc: string) => void) | null = null;
  setSwapInlineImageSrcFn(fn: ((oldSrc: string, newSrc: string) => void) | null): void {
    this.#swapInlineImageSrcFn = fn;
  }

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
   * Snapshot of the form fields as they were the moment compose opened.
   * Used by `hasContent` to distinguish "user has actually edited
   * something" from "the form is non-empty because reply / forward
   * pre-filled it" -- discarding an unmodified reply should not prompt.
   * Captured by `openWith` after every field is set; cleared by
   * `close()` so a stale snapshot can't cross-pollute the next compose.
   */
  #snapshot: {
    to: string;
    cc: string;
    bcc: string;
    subject: string;
    body: string;
  } | null = null;

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
    this.toRecipients = [];
    this.ccRecipients = [];
    this.bccRecipients = [];
    this.subject = '';
    this.body = appendSignature('', mail.primaryIdentity);
    this.errorMessage = null;
    this.ccBccVisible = false;
    this.replyContext = { ...EMPTY_REPLY };
    this.editingDraftId = null;
    this.attachments = [];
    this.shares = [];
    this.selectedIdentity = null;
    this.status = 'editing';
    this.#ensureAccountReady();
  }

  /**
   * Open compose pre-populated (e.g. after Undo). Caller is responsible
   * for resetting / setting the reply context.
   *
   * Per REQ-MAIL-101 the From identity's signature is appended to the
   * body separated by `\n-- \n`. `skipSignature` opts out — used by
   * `openDraft` and Undo restores, where the body already contains the
   * signature the user previously authored.
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
    /** Skip signature appending — body already carries the user's edited signature. */
    skipSignature?: boolean;
    /**
     * Pre-selected From identity — set by openReply / openReplyAll /
     * openForward after the REQ-MAIL-12a match. null falls back to
     * `mail.primaryIdentity` at send time, matching the prior
     * behaviour for fresh composes and draft re-opens.
     */
    identity?: Identity | null;
  }): void {
    if (!args.skipHook && !this.#runBeforeOpen()) return;
    this.to = args.to;
    this.cc = args.cc ?? '';
    this.bcc = args.bcc ?? '';
    // Populate structured recipient arrays from the string representation.
    this.toRecipients = parseStringToRecipients(this.to);
    this.ccRecipients = parseStringToRecipients(this.cc);
    this.bccRecipients = parseStringToRecipients(this.bcc);
    this.subject = args.subject;
    this.selectedIdentity = args.identity ?? null;
    // The signature comes from the matched identity (when provided)
    // so a reply / forward picks up the From identity's sig, not the
    // primary identity's. Fresh compose paths still get the primary
    // sig because args.identity is undefined and primaryIdentity is
    // the fallback.
    const sigIdentity = this.selectedIdentity ?? mail.primaryIdentity;
    this.body = args.skipSignature
      ? args.body
      : appendSignature(args.body, sigIdentity);
    this.replyContext = args.replyContext ?? { ...EMPTY_REPLY };
    this.errorMessage = null;
    this.ccBccVisible = Boolean(this.cc || this.bcc);
    this.editingDraftId = args.draftId ?? null;
    this.attachments = [];
    this.shares = [];
    this.status = 'editing';
    this.#snapshot = {
      to: this.to,
      cc: this.cc,
      bcc: this.bcc,
      subject: this.subject,
      body: this.body,
    };
    this.#ensureAccountReady();
  }

  /** Open compose as a reply to the given email. */
  async openReply(parent: Email): Promise<void> {
    // Defensive: if identities have not loaded yet (race between
    // landing on a thread URL and the auth-ready prime), wait for
    // them. Without identities populated, isOwnSentMessage cannot detect
    // an own-sent message and the To field would silently fall back
    // to the user's own address. App.svelte primes them on auth-ready
    // already; this guard catches the degenerate timing.
    if (mail.identities.size === 0) {
      try {
        await mail.loadIdentities();
      } catch (err) {
        console.warn('openReply: identity load failed', err);
      }
    }
    const selfEmails = buildSelfEmailSet(mail.identities.values());
    // When replying to a message the user themselves sent through herold,
    // the logical recipient is the people who received that message
    // (parent.to), not the user's own From address (REQ-MAIL-30). A
    // delivered message whose From coincidentally matches one of the
    // user's identities (it carries X-Herold-Recipient) is not own-sent —
    // see isOwnSentMessage.
    const ownMessage = isOwnSentMessage(parent, selfEmails);
    const to = computeReplyTo(parent, ownMessage);
    // Build the Cc list: collect any "local alias" addresses the original
    // message reached that the user has not registered as a first-class
    // Identity. Two sources are considered by localAliasesForCc:
    //   1. X-Herold-Recipient, when present and not a registered Identity
    //      (REQ-FLOW-34; absent on outbound per REQ-FLOW-35).
    //   2. Visible To/Cc addresses on an identity domain that are not
    //      registered Identities — covers the upstream-expanded-alias case
    //      where the external MX resolves the alias before herold sees the
    //      envelope so X-Herold-Recipient already carries the resolved
    //      mailbox (a registered Identity), not the original alias.
    // When replying to an own-sent message (ownMessage=true), parent.to
    // addresses go into the reply's To field, so the scan skips parent.to.
    const identities = Array.from(mail.identities.values());
    const localAliases = localAliasesForCc(parent, identities, ownMessage);
    this.openWith({
      to,
      cc: localAliases.length > 0 ? localAliases.join(', ') : undefined,
      subject: replySubject(parent.subject),
      body: formatReplyQuote(parent),
      replyContext: {
        parentId: parent.id,
        parentKeyword: '$answered',
        inReplyTo: parent.messageId ?? null,
        references: mergeReferences(parent),
      },
      identity: this.#matchIdentity(parent),
    });
  }

  /**
   * Open compose as a reply to the list, not the sender (REQ-LIST-22):
   * same quoting / threading as `openReply`, but `to` is overridden to
   * the list's `List-Post` address. Used by the list chip's "Reply to
   * list" popover action.
   */
  async openReplyToList(parent: Email, listPostAddress: string): Promise<void> {
    await this.openReply(parent);
    this.to = listPostAddress;
    this.toRecipients = tryCommit(listPostAddress).chips;
    if (this.#snapshot) this.#snapshot.to = this.to;
  }

  /**
   * Open compose as a reply-all: To = parent.from, Cc = (parent.to ∪
   * parent.cc) minus every Identity.email this user owns minus the
   * primary recipient. Falls back to a regular reply when no Cc would
   * survive the self-filter.
   *
   * When the parent message was sent by the user (ownMessage), To =
   * the original To list and Cc = the original Cc list minus self
   * addresses (REQ-MAIL-31).
   */
  openReplyAll(parent: Email): void {
    const selfEmails = buildSelfEmailSet(mail.identities.values());
    const ownMessage = isOwnSentMessage(parent, selfEmails);
    // Reply-all on own sent message: To = original To (the people who
    // received the message). Otherwise: To = the sender.
    const to = computeReplyTo(parent, ownMessage);
    const identities = Array.from(mail.identities.values());
    const cc = computeActualReplyAllCc(parent, selfEmails, identities);
    this.openWith({
      to,
      cc: cc.map(addressToString).join(', '),
      subject: replySubject(parent.subject),
      body: formatReplyQuote(parent),
      replyContext: {
        parentId: parent.id,
        parentKeyword: '$answered',
        inReplyTo: parent.messageId ?? null,
        references: mergeReferences(parent),
      },
      identity: this.#matchIdentity(parent),
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
      // The saved draft already carries whatever signature the user
      // had when it was last persisted; appending again would
      // duplicate it.
      skipSignature: true,
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
      identity: this.#matchIdentity(parent),
    });
  }

  /**
   * Run the REQ-MAIL-12a reply-identity match against `parent` using
   * the current `mail.identities` cache. Returns `mail.primaryIdentity`
   * as the terminal fallback (REQ-MAIL-12); when there is no primary
   * identity at all the result is null and openWith falls through to
   * primaryIdentity at send time anyway.
   *
   * Centralising the call here keeps openReply / openReplyAll /
   * openForward identical in shape — REQ-MAIL-12a applies to all three
   * — and gives unit tests a single seam to mock against the compose
   * singleton when verifying wiring (the algorithm itself is unit-
   * tested directly against `selectReplyIdentity`).
   */
  #matchIdentity(parent: Email): Identity | null {
    const fallback = mail.primaryIdentity;
    if (!fallback) return null;
    const identities = Array.from(mail.identities.values());
    return selectReplyIdentity(parent, identities, fallback);
  }

  /**
   * True when the user has changed the form since `openWith` captured
   * the snapshot. Auto-injected content (signatures, reply quote,
   * pre-filled To/Subject from openReply / openForward / openDraft) is
   * the snapshot baseline -- discarding without further edits is
   * therefore not "losing user content" and should not prompt for
   * confirmation (REQ-DFT-41 spirit). Attachments added after open
   * always count as user content.
   */
  get hasContent(): boolean {
    if (this.attachments.length > 0) return true;
    if (this.shares.length > 0) return true;
    const snap = this.#snapshot;
    if (!snap) {
      // Pre-open or post-close state -- fall back to the original
      // "any non-empty field" semantics so callers that misuse this
      // outside an editing session still get a sensible answer.
      if (this.to.trim() || this.cc.trim() || this.bcc.trim()) return true;
      if (this.subject.trim()) return true;
      return bodyTextWithoutSignature(this.body).length > 0;
    }
    return (
      this.to !== snap.to ||
      this.cc !== snap.cc ||
      this.bcc !== snap.bcc ||
      this.subject !== snap.subject ||
      this.body !== snap.body
    );
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
   *
   * When the file-shares capability is present, files that are offload
   * candidates (REQ-ATT-60) trigger the offer dialog (REQ-ATT-61) via
   * #offloadOfferFn before deciding whether to upload or offload.
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

      // ── Offload candidacy check (REQ-ATT-60) ──────────────────────────
      // Inline images are never candidates (handled by addInlineImage path).
      const canOffload = hasFileShares() && this.#offloadOfferFn !== null;
      if (canOffload) {
        const threshold = offloadThresholdBytes();
        const unsafeType = isUnsafeExtension(file.name);
        const oversized = maxSize !== null && file.size >= maxSize;
        const candidate = file.size >= threshold || unsafeType || oversized;
        if (candidate) {
          // Forced when the file cannot fit as a MIME attachment at all.
          const forced = oversized;
          const defaultOpts: ShareOptions = { expiresIn: defaultTtlSeconds() };
          const decision = await this.#offloadOfferFn!({ file, forced, opts: defaultOpts });
          if (decision === null) {
            // User cancelled — skip this file entirely.
            continue;
          }
          if (decision.offload) {
            // User chose to offload: upload the blob, then create the share.
            tasks.push(this.#offloadOne(key, file, accountId, decision.opts));
            continue;
          }
          // User chose "Attach anyway". If it is forced-over-quota, prevent
          // the attachment anyway (the dialog should have disabled that path,
          // but guard here for safety).
          if (forced) {
            const att: ComposeAttachment = {
              key,
              name: file.name || 'file',
              size: file.size,
              type: file.type || 'application/octet-stream',
              status: 'failed',
              blobId: null,
              error: `File is too large to attach (${formatBytes(file.size)})`,
            };
            this.attachments = [...this.attachments, att];
            continue;
          }
          // Fall through to normal attachment below.
        }
      }

      // ── Normal attachment path ─────────────────────────────────────────
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

  /**
   * Remove a share by key (REQ-ATT-64 remove control). Issues
   * FileShare/set destroy and removes the link block from the body.
   * When the ProseMirror editor is mounted the removal goes through the
   * editor so the live document updates immediately and the `onUpdate`
   * callback writes the new HTML back into `compose.body`. Falls back to
   * string mutation when the editor is not available (unit tests, etc.).
   */
  async removeShare(key: string): Promise<void> {
    const share = this.shares.find((s) => s.key === key);
    if (!share) return;
    this.shares = this.shares.filter((s) => s.key !== key);
    // Remove the link block through the editor when possible, otherwise
    // mutate the body string directly as a fallback.
    if (this.#removeShareLinkFn) {
      this.#removeShareLinkFn(share.url);
    } else {
      this.body = removeShareLinkFromBody(this.body, share.url);
    }
    // Destroy the share server-side (fire and forget; errors are non-fatal
    // since the pending_ttl sweeper is the backstop for abandoned shares).
    try {
      await destroyFileShares([share.shareId]);
    } catch (err) {
      console.warn('compose: could not destroy share', share.shareId, err);
    }
  }

  /** Remove a queued attachment by key. */
  removeAttachment(key: string): void {
    const removed = this.attachments.find((a) => a.key === key);
    if (removed?.objectURL) URL.revokeObjectURL(removed.objectURL);
    this.attachments = this.attachments.filter((a) => a.key !== key);
  }

  /**
   * Synchronously register an inline image attachment and create a blob
   * URL for immediate in-editor preview. Returns the key, cid, and
   * objectURL so the caller can insert the image node before the upload
   * begins. Returns null when the file exceeds the server's size limit or
   * no mail account is available.
   *
   * Pair with uploadInlineImage(key, file) to complete the round-trip.
   * The attachment chip shows 'Uploading...' until that resolves.
   */
  startInlineImage(
    file: File,
  ): { key: string; cid: string; objectURL: string } | null {
    if (!mail.mailAccountId) return null;
    const maxSize = jmap.maxUploadSize;
    if (maxSize !== null && file.size > maxSize) return null;
    const key = `att-${++this.#attachmentSeq}`;
    const cid = generateInlineCID(this.#attachmentSeq);
    const objectURL = URL.createObjectURL(file);
    const att: ComposeAttachment = {
      key,
      name: file.name || 'image',
      size: file.size,
      type: file.type || 'application/octet-stream',
      status: 'uploading',
      blobId: null,
      error: null,
      inline: true,
      cid,
      objectURL,
    };
    this.attachments = [...this.attachments, att];
    // Fire-and-forget: swap in a downscaled proxy once ready (issue #243).
    // Does not block the synchronous return -- the caller inserts the
    // full-resolution objectURL immediately, same as before.
    void this.#applyImageProxy(key, file);
    return { key, cid, objectURL };
  }

  /**
   * Generate a downscaled proxy for a just-started inline image (issue
   * #243) and, once ready, swap it in as the attachment's display URL via
   * `#swapInlineImageSrcFn`. No-ops when the source image is small enough
   * that `generateImageProxy` declines to produce one, or when the
   * attachment has already been removed (upload failed and the editor
   * placeholder was retracted, or the user deleted the image) by the time
   * the proxy finishes generating.
   */
  async #applyImageProxy(key: string, file: File): Promise<void> {
    const proxy = await generateImageProxy(file);
    if (!proxy) return;
    const current = this.attachments.find((a) => a.key === key);
    if (!current || !current.objectURL) {
      // Attachment gone or already lost its display URL -- drop the
      // generated proxy without ever creating a blob: URL for it.
      return;
    }
    const oldSrc = current.objectURL;
    const proxyURL = URL.createObjectURL(proxy.blob);
    // Patch the record BEFORE dispatching the DOM swap. The swap fires a
    // live ProseMirror transaction; editor.ts's onImageRemoved wiring
    // diffs image srcs across that transaction and reports oldSrc as
    // "removed" (it renamed to proxyURL, not literally gone). If the
    // attachment record still carried the stale oldSrc at that moment,
    // ComposeWindow's onImageRemoved handler would match it and delete
    // the whole attachment, mistaking the src-only rename for the user
    // deleting the image.
    this.#patchAttachment(key, { objectURL: proxyURL });
    this.#swapInlineImageSrcFn?.(oldSrc, proxyURL);
    URL.revokeObjectURL(oldSrc);
  }

  /**
   * Complete the JMAP blob upload for an inline image previously
   * registered via startInlineImage. Patches the attachment record to
   * 'ready' on success or 'failed' on error. Returns the error message
   * on failure so the caller can remove the placeholder from the editor,
   * or null on success.
   */
  async uploadInlineImage(key: string, file: File): Promise<string | null> {
    const accountId = mail.mailAccountId;
    if (!accountId) return 'No mail account';
    const att = this.attachments.find((a) => a.key === key);
    if (!att) return 'Attachment not found';
    try {
      const result = await jmap.uploadBlob({
        accountId,
        body: file,
        type: att.type,
      });
      this.#patchAttachment(key, {
        status: 'ready',
        blobId: result.blobId,
        error: null,
      });
      return null;
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Upload failed';
      this.#patchAttachment(key, { status: 'failed', error: msg });
      return msg;
    }
  }

  /**
   * Upload a single image as an inline attachment and return its
   * Content-ID and blob-id once the upload completes. The caller (the
   * compose toolbar Insert image action, issue #20) inserts an
   * <img src="cid:<cid>"> node into the editor body. The inline part
   * is added to the bodyStructure at send time with disposition:
   * 'inline'.
   *
   * Resolves null on upload failure (a toast row already surfaces the
   * error).
   *
   * @deprecated Use startInlineImage + uploadInlineImage instead for
   * immediate in-editor thumbnail feedback (issue #74).
   */
  async addInlineImage(
    file: File,
  ): Promise<{ cid: string; objectURL: string } | null> {
    const started = this.startInlineImage(file);
    if (!started) return null;
    const { key, cid, objectURL } = started;
    const err = await this.uploadInlineImage(key, file);
    if (err) return null;
    return { cid, objectURL };
  }

  /**
   * Flip an existing attachment from inline to regular attachment
   * (disposition: 'attachment'). Removes any cid: reference from the
   * body HTML so the image no longer appears inline. Triggers dirty
   * state for auto-save (REQ-ATT-07, REQ-DFT-03).
   */
  flipToAttachment(key: string): void {
    const att = this.attachments.find((a) => a.key === key);
    if (!att || !att.inline) return;
    // Remove the img tag from the body. The body may reference the image by
    // either its `blob:` objectURL (during composition) or its `cid:` Content-ID
    // (after an earlier rewrite). Remove whichever is present.
    if (this.body) {
      // Try objectURL first (the common path during composition).
      if (att.objectURL) {
        const escapedUrl = att.objectURL.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
        const reUrl = new RegExp(`<img[^>]*src=["']${escapedUrl}["'][^>]*>`, 'gi');
        this.body = this.body.replace(reUrl, '');
      }
      // Also try the cid: form (in case body was persisted/reloaded).
      if (att.cid) {
        const escapedCid = att.cid.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
        const reCid = new RegExp(`<img[^>]*src=["']cid:${escapedCid}["'][^>]*>`, 'gi');
        this.body = this.body.replace(reCid, '');
      }
    }
    this.#patchAttachment(key, { inline: false, cid: null });
  }

  /**
   * Flip an existing regular attachment to an inline image
   * (disposition: 'inline'). Assigns a fresh Content-ID and inserts
   * an <img src="cid:…"> into the editor at the current cursor via
   * the provided EditorView handle. A no-op when the attachment is
   * not image/* or when the view is unavailable (REQ-ATT-07).
   */
  flipToInline(key: string, view: import('prosemirror-view').EditorView | null): void {
    const att = this.attachments.find((a) => a.key === key);
    if (!att || att.inline) return;
    if (!att.type.startsWith('image/')) return;
    const cid = generateInlineCID(++this.#attachmentSeq);
    // Use the objectURL if the flip was triggered from a prior inline
    // that was moved to attachments; otherwise we have no blob URL so
    // we can only use cid: directly. The editor will render a broken
    // image icon for the cid: src before send — acceptable, since
    // the flip path for attachments-to-inline is an uncommon flow
    // (the user already has the file uploaded).
    const src = att.objectURL ?? `cid:${cid}`;
    this.#patchAttachment(key, { inline: true, cid });
    // Insert the image into the editor at the cursor position.
    if (view) {
      applyImage(view, src, att.name);
    }
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

  /**
   * Offload path (REQ-ATT-62): upload the blob (same as normal attachment),
   * then issue FileShare/set create to promote it to a share. On success,
   * insert the share link into the body (REQ-ATT-63) and add a ComposeShare
   * row (REQ-ATT-64). On failure, surface the error as a failed attachment
   * so the user can decide what to do (REQ-ATT-67).
   */
  async #offloadOne(
    key: string,
    file: File,
    accountId: string,
    opts: ShareOptions,
  ): Promise<void> {
    // Phase 1: show a temporary "Uploading…" placeholder in the attachment
    // strip while the blob upload and share creation run.
    const att: ComposeAttachment = {
      key,
      name: file.name || 'file',
      size: file.size,
      type: file.type || 'application/octet-stream',
      status: 'uploading',
      blobId: null,
      error: null,
    };
    this.attachments = [...this.attachments, att];

    try {
      // Phase 2: blob upload (reuse path, same endpoint as normal attachment).
      const uploadResult = await jmap.uploadBlob({
        accountId,
        body: file,
        type: file.type || 'application/octet-stream',
      });

      // Phase 3: FileShare/set create — reuses the blobId, no re-upload.
      const shareArgs: FileShareCreateArgs = {
        blobId: uploadResult.blobId,
        name: file.name || 'file',
        type: file.type || 'application/octet-stream',
        expiresIn: opts.expiresIn,
        ...(opts.maxDownloads !== undefined ? { maxDownloads: opts.maxDownloads } : {}),
        ...(opts.password ? { password: opts.password } : {}),
      };
      const share: FileShare = await createFileShare(shareArgs);

      // Phase 4: remove the placeholder chip (offloaded files appear in the
      // shared links strip, not the attachment strip).
      this.attachments = this.attachments.filter((a) => a.key !== key);

      // Phase 5: register in the shares strip (REQ-ATT-64).
      const composeShare: ComposeShare = {
        key,
        shareId: share.id,
        name: share.name,
        size: share.size,
        type: share.type,
        url: share.url,
        expiresAt: share.expiresAt,
        maxDownloads: share.maxDownloads,
        hasPassword: share.hasPassword,
      };
      this.shares = [...this.shares, composeShare];

      // Phase 6: insert link into the body (REQ-ATT-63).
      // When the ProseMirror editor is mounted, drive the insertion through
      // the editor so the live document updates immediately and the resulting
      // `onUpdate` propagates the new HTML back into `compose.body`. Falls
      // back to string mutation when the editor is not available (unit tests,
      // or a compose that has not yet opened its window).
      if (this.#insertShareLinkFn) {
        // Build the HTML block and pass it to the editor. The editor's
        // onUpdate callback will write the serialised HTML back into
        // this.body, so no direct mutation is needed here.
        const htmlBlock = buildShareLinkHtml(composeShare);
        this.#insertShareLinkFn(htmlBlock);
      } else {
        this.body = insertShareLinkIntoBody(this.body, composeShare);
      }
    } catch (err) {
      // On failure: surface as a failed attachment so the file is not
      // silently dropped (REQ-ATT-67).
      const msg = err instanceof Error ? err.message : 'Offload failed';
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
    this.inlineMode = false;
    this.to = '';
    this.cc = '';
    this.bcc = '';
    this.toRecipients = [];
    this.ccRecipients = [];
    this.bccRecipients = [];
    this.subject = '';
    this.body = '';
    this.errorMessage = null;
    this.ccBccVisible = false;
    this.replyContext = { ...EMPTY_REPLY };
    this.editingDraftId = null;
    this.attachments = [];
    this.shares = [];
    this.selectedIdentity = null;
    this.#snapshot = null;
  }

  /**
   * Discard the current compose. When a server-side draft has already
   * been auto-saved, destroy the row so it doesn't accumulate in the
   * Drafts folder; otherwise just reset state. This is what
   * closeWithConfirm calls on user-confirmed discard.
   *
   * Any pending shares created in this session are destroyed (REQ-ATT-66).
   * The pending_ttl sweeper is the backstop if the client crashes before
   * getting here.
   */
  async discard(): Promise<void> {
    const draftId = this.editingDraftId;
    const accountId = mail.mailAccountId;
    const pendingShareIds = this.shares.map((s) => s.shareId);

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

    // Destroy pending shares (REQ-ATT-66). Fire-and-forget; the
    // pending_ttl sweeper is the backstop.
    if (pendingShareIds.length > 0) {
      void destroyFileShares(pendingShareIds).catch((err) => {
        console.warn('compose: could not destroy pending shares on discard', err);
      });
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
    // Prefer the selected identity (set by the reply-identity match)
    // and fall back to primary. The reply-identity match runs once at
    // compose-open per REQ-MAIL-12a; from then on this field is the
    // authoritative From for the in-progress draft.
    const identity = this.selectedIdentity ?? mail.primaryIdentity;
    const drafts = mail.drafts;
    if (!identity || !drafts) return false;

    // Use structured recipient arrays (preserves display names). Fall back to
    // parsing the string form for any field whose array is empty but the
    // string is non-empty — covers snapshot-restore paths.
    const toAddrs: Recipient[] =
      this.toRecipients.length > 0
        ? this.toRecipients
        : parseAddressList(this.to).map((email) => ({ email }));
    const ccAddrs: Recipient[] =
      this.ccRecipients.length > 0
        ? this.ccRecipients
        : parseAddressList(this.cc).map((email) => ({ email }));
    const bccAddrs: Recipient[] =
      this.bccRecipients.length > 0
        ? this.bccRecipients
        : parseAddressList(this.bcc).map((email) => ({ email }));
    // Rewrite blob: object URLs to cid: (issue #20), then extract any
    // remaining data: image URIs and upload them as proper MIME parts
    // (issue #50). Both steps work on local variables and do not mutate
    // compose state.
    const bodyHtml_blobRewritten = rewriteInlineImageURLs(this.body, this.attachments);
    const { html: bodyHtml, attachments: dataImageAttachments } =
      await processDataUriImages(bodyHtml_blobRewritten, accountId);
    const bodyText = htmlToPlainText(bodyHtml);

    const baseReadyAttachments = this.attachments.filter(
      (a): a is ComposeAttachment & { blobId: string } =>
        a.status === 'ready' && typeof a.blobId === 'string',
    );
    const readyAttachments = [...baseReadyAttachments, ...dataImageAttachments];

    const fields: Record<string, unknown> = {
      from: [{ name: identity.name, email: identity.email }],
      to: toAddrs.map((r) => ({ email: r.email, name: r.name ?? null })),
      subject: this.subject,
      bodyValues: {
        '1': { value: bodyText, isTruncated: false, isEncodingProblem: false },
        '2': { value: bodyHtml, isTruncated: false, isEncodingProblem: false },
      },
      textBody: [{ partId: '1', type: 'text/plain', charset: 'utf-8' }],
      htmlBody: [{ partId: '2', type: 'text/html', charset: 'utf-8' }],
      bodyStructure: buildBodyStructure(readyAttachments),
      ...(readyAttachments.length > 0
        ? { attachments: buildAttachmentParts(readyAttachments) }
        : {}),
      hasAttachment: readyAttachments.length > 0,
    };
    if (ccAddrs.length > 0) {
      fields.cc = ccAddrs.map((r) => ({ email: r.email, name: r.name ?? null }));
    }
    if (bccAddrs.length > 0) {
      fields.bcc = bccAddrs.map((r) => ({ email: r.email, name: r.name ?? null }));
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
    // Prefer the selected identity (set by REQ-MAIL-12a's reply match
    // at compose-open) and fall back to primary. The compose's From
    // picker (future work) writes to the same `selectedIdentity`
    // cell, so this path is the single point that consumes it.
    const identity = this.selectedIdentity ?? mail.primaryIdentity;
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

    // Use structured recipient arrays (preserves display names). Fall back to
    // parsing the string form for any field whose array is empty but the
    // string is non-empty — this covers snapshot-restore paths that only
    // carry the string representation.
    const toRecipients: Recipient[] =
      this.toRecipients.length > 0
        ? this.toRecipients
        : parseAddressList(this.to).map((email) => ({ email }));
    const ccRecipients: Recipient[] =
      this.ccRecipients.length > 0
        ? this.ccRecipients
        : parseAddressList(this.cc).map((email) => ({ email }));
    const bccRecipients: Recipient[] =
      this.bccRecipients.length > 0
        ? this.bccRecipients
        : parseAddressList(this.bcc).map((email) => ({ email }));
    const allRecipients = [...toRecipients, ...ccRecipients, ...bccRecipients];
    if (allRecipients.length === 0) {
      this.errorMessage = 'At least one recipient is required';
      return;
    }
    const subject = this.subject;
    // Rewrite blob: object URLs to cid: (issue #20), then extract any
    // remaining data: image URIs and upload them as proper MIME parts
    // (issue #50). Both steps work on local variables and do not mutate
    // compose state.
    const bodyHtml_blobRewritten = rewriteInlineImageURLs(this.body, this.attachments);
    const { html: bodyHtml, attachments: dataImageAttachments } =
      await processDataUriImages(bodyHtml_blobRewritten, accountId);
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
    const baseReadyAttachments = this.attachments.filter(
      (a): a is ComposeAttachment & { blobId: string } =>
        a.status === 'ready' && typeof a.blobId === 'string',
    );
    const readyAttachments = [...baseReadyAttachments, ...dataImageAttachments];
    const draftEmail: Record<string, unknown> = {
      mailboxIds: { [drafts.id]: true },
      keywords: { $draft: true, $seen: true },
      from: [{ name: identity.name, email: identity.email }],
      to: toRecipients.map((r) => ({ email: r.email, name: r.name ?? null })),
      subject,
      bodyValues: {
        '1': { value: bodyText, isTruncated: false, isEncodingProblem: false },
        '2': { value: bodyHtml, isTruncated: false, isEncodingProblem: false },
      },
      textBody: [{ partId: '1', type: 'text/plain', charset: 'utf-8' }],
      htmlBody: [{ partId: '2', type: 'text/html', charset: 'utf-8' }],
      bodyStructure: buildBodyStructure(readyAttachments),
      ...(readyAttachments.length > 0
        ? { attachments: buildAttachmentParts(readyAttachments) }
        : {}),
      hasAttachment: readyAttachments.length > 0,
    };
    if (ccRecipients.length > 0) {
      draftEmail.cc = ccRecipients.map((r) => ({ email: r.email, name: r.name ?? null }));
    }
    // Bcc: per RFC 8621 §4.1.2 the Bcc header is set on the draft so the
    // sender's Sent-folder copy retains the blind list, but the envelope
    // recipients (below) are what actually drive delivery — the server
    // strips Bcc from the wire-bound message.
    if (bccRecipients.length > 0) {
      draftEmail.bcc = bccRecipients.map((r) => ({ email: r.email, name: r.name ?? null }));
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
                  rcptTo: allRecipients.map((r) => ({ email: r.email })),
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
        notCreated?: Record<string, { type: string; description?: string; [key: string]: unknown }>;
      }>(responses[1]);
      if (subResult.notCreated?.sub1) {
        const e = subResult.notCreated.sub1;
        // Check for external submission failure (REQ-MAIL-SUBMIT-06).
        // When detected, leave the compose open with the draft id intact
        // so the user can retry after re-authenticating, then surface
        // a toast with the Re-authenticate affordance for auth failures.
        if (hasExternalSubmission()) {
          const extCategory = parseExternalSubmissionFailure(e);
          if (extCategory !== null) {
            this.status = 'editing';
            // Preserve the draft id so the compose keeps autosaving.
            if (!this.editingDraftId) {
              // Try to recover the draft id from the set result.
              const setResult2 = invocationArgs<{
                created?: Record<string, { id: string }>;
              }>(responses[0]);
              const newId = setResult2.created?.draft1?.id;
              if (newId) this.editingDraftId = newId;
            }
            showExternalSubmissionFailure({
              category: extCategory,
              identityId: identity.id,
              diagnostic: e.description,
            });
            return;
          }
        }
        throw new Error(e.description ?? e.type);
      }
      const submissionId = subResult.created?.sub1?.id;
      if (!submissionId) {
        throw new Error('Submission created but no id returned');
      }

      // Confirm all pending shares to 'active' now that the message has
      // been successfully submitted (REQ-ATT-66). Fire-and-forget: a
      // failure here means the shares stay pending and expire via
      // pending_ttl, which is safe (the server enforces the TTL).
      // Include source metadata (REQ-SHARE-04 / REQ-ATT-70a) so the
      // management view can display the originating message.
      const pendingShareIds = this.shares.map((s) => s.shareId);
      if (pendingShareIds.length > 0) {
        // Resolve the final Email id. For an existing draft we already have
        // it; for a freshly created draft we read it from the Email/set result
        // (REQ-MAIL-20: the suite reuses the draft Email id on send).
        let sentEmailId: string | null = existingDraftId;
        if (!sentEmailId) {
          const emailSetResult = invocationArgs<{
            created?: Record<string, { id: string }>;
          }>(responses[0]);
          sentEmailId = emailSetResult.created?.draft1?.id ?? null;
        }
        const source: FileShareSourceInfo | undefined = sentEmailId
          ? {
              sourceMessageId: sentEmailId,
              sourceSubject: subject,
              sourceRecipients: allRecipients.map((r) => r.email),
            }
          : undefined;
        void confirmFileShares(pendingShareIds, source).catch((err) => {
          console.warn('compose: could not confirm shares after send', err);
        });
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
              const undoResult = result.responses[0]?.[1] as
                | { notDestroyed?: Record<string, { type: string; description?: string }> }
                | undefined;
              const undoFailure = undoResult?.notDestroyed?.[submissionId];
              if (undoFailure) {
                throw new Error(
                  undoFailure.description ?? `Could not cancel send: ${undoFailure.type}`,
                );
              }
              // Re-open compose with the full saved state (including reply context).
              // savedBody already carries the signature the user had on send;
              // appending again would duplicate it.
              this.openWith({
                to: savedTo,
                cc: savedCc,
                bcc: savedBcc,
                subject: savedSubject,
                body: savedBody,
                replyContext: savedReplyContext,
                skipSignature: true,
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
 * Parse a comma/semicolon-separated address string into structured
 * Recipient objects. Used by openWith to populate the chip arrays when
 * opening a compose that was pre-filled from a reply/forward/draft.
 * Recognized fragments become chips; unrecognized tokens are silently
 * dropped (the string form is still the canonical store value).
 */
function parseStringToRecipients(raw: string): Recipient[] {
  if (!raw.trim()) return [];
  const { chips } = tryCommit(raw);
  return chips;
}

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

/**
 * Compute the To string for a Reply or Reply-All compose open.
 *
 * When the parent was sent by the user (ownMessage=true), the logical
 * reply target is the people in parent.to (REQ-MAIL-30). When parent.to
 * is null, empty, or all addresses stringify to '' (undisclosed recipients,
 * BCC-only delivery, messages forwarded without a visible To), fall back to
 * parent.from[0] so the composer is never left with an empty To field
 * (re #168). The user can edit the fallback address before sending.
 */
function computeReplyTo(
  parent: Pick<Email, 'from' | 'to'>,
  ownMessage: boolean,
): string {
  const fromOwn = ownMessage
    ? (parent.to ?? []).map(addressToString).join(', ')
    : null;
  if (fromOwn?.trim()) return fromOwn;
  // Either !ownMessage or the own-sent To list is empty: reply to the sender.
  return addressToString(parent.from?.[0]);
}

function addressListToString(list: Address[] | null | undefined): string {
  if (!list || list.length === 0) return '';
  return list.map(addressToString).join(', ');
}

/**
 * Localized reply-marker prefixes (re #248). English "Re" plus the German,
 * Nordic, Polish, Portuguese/Spanish, and Italian equivalents commonly seen
 * on mail relayed through non-English clients.
 */
const REPLY_MARKERS = ['re', 'aw', 'antwort', 'antw', 'sv', 'vs', 'odp', 'res', 'rif'];

/**
 * Localized forward-marker prefixes (re #248). English "Fwd"/"Fw" plus the
 * German, French, and Spanish/Portuguese equivalents.
 */
const FORWARD_MARKERS = ['fwd', 'fw', 'wg', 'tr', 'rv', 'vb'];

/**
 * Strips a leading chain of whitelisted marker prefixes ("AW: Re: Foo" ->
 * "Foo"). Only markers in `markers` collapse; a subject that legitimately
 * starts with a non-marker word and a colon (e.g. "Bugfix: Foo") is left
 * untouched — there is no generic "token ending in colon" fallback.
 */
function stripMarkerChain(subject: string, markers: string[]): string {
  const pattern = new RegExp(`^(?:${markers.join('|')})\\s*:\\s*`, 'i');
  let s = subject.trim();
  let prev: string;
  do {
    prev = s;
    s = s.replace(pattern, '').trim();
  } while (s !== prev);
  return s;
}

function replySubject(orig: string | null): string {
  const s = orig ?? '';
  return `Re: ${stripMarkerChain(s, REPLY_MARKERS)}`;
}

function forwardSubject(orig: string | null): string {
  const s = orig ?? '';
  return `Fwd: ${stripMarkerChain(s, FORWARD_MARKERS)}`;
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

/**
 * Compute the Cc address list for reply-all on a message the user sent.
 * In this case the original Cc is preserved as-is, minus own-identity
 * addresses. The To list is NOT included in Cc (it becomes the To field
 * of the new message). Duplicates are dropped on the lowercase form.
 */
function computeOwnMessageReplyAllCc(
  parent: Email,
  selfEmails: Set<string>,
): Address[] {
  const out: Address[] = [];
  const seen = new Set<string>();
  // Seed seen with all To addresses so they do not bleed into Cc.
  for (const addr of parent.to ?? []) {
    const lc = (addr.email ?? '').toLowerCase();
    if (lc) seen.add(lc);
  }
  if (!parent.cc) return out;
  for (const addr of parent.cc) {
    const lc = (addr.email ?? '').toLowerCase();
    if (!lc) continue;
    if (selfEmails.has(lc)) continue;
    if (seen.has(lc)) continue;
    seen.add(lc);
    out.push(addr);
  }
  return out;
}

/**
 * Compute the exact Cc address list openReplyAll would send for `parent`:
 * the branch-selected filter (computeOwnMessageReplyAllCc for a message
 * the user sent, computeReplyAllCc otherwise) with the delivery alias
 * (X-Herold-Recipient, when present and not already covered) prepended.
 * Single source of truth for both the compose action and
 * ThreadReplyBar's Reply-All visibility gate, which shows the button iff
 * this list is non-empty — so the gate can never drift from what
 * clicking Reply All actually produces (re #163).
 */
export function computeActualReplyAllCc(
  parent: Email,
  selfEmails: Set<string>,
  identities: readonly Identity[],
): Address[] {
  const ownMessage = isOwnSentMessage(parent, selfEmails);
  let cc = ownMessage
    ? computeOwnMessageReplyAllCc(parent, selfEmails)
    : computeReplyAllCc(parent, selfEmails);
  // Prepend any local alias addresses not already represented in cc.
  // localAliasesForCc covers both the X-Herold-Recipient path (herold-
  // native alias) and the visible-To/Cc path (upstream-expanded alias
  // where the external MX resolved the alias before herold saw the
  // envelope). The ownMessage flag prevents parent.to addresses from
  // appearing in Cc (they become the reply's To field).
  const aliases = localAliasesForCc(parent, identities, ownMessage);
  if (aliases.length > 0) {
    const existingEmails = new Set(cc.map((a) => (a.email ?? '').toLowerCase()));
    const toAdd = aliases.filter((a) => !existingEmails.has(a));
    if (toAdd.length > 0) {
      cc = [...toAdd.map((a) => ({ name: null, email: a })), ...cc];
    }
  }
  return cc;
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
  return d.toLocaleString(localeTag(), {
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
 * Append the From identity's signature to an HTML body per REQ-MAIL-101.
 *
 * The standard plain-text delimiter is `\n-- \n` (RFC 3676 §4.3); in HTML
 * it becomes a paragraph containing exactly `-- ` (two dashes + space)
 * followed by the signature lines, each their own paragraph so the
 * editor sees them as block content. The signature goes BELOW any
 * existing body content — the cursor lands at the top of the editor by
 * default, which leaves the user above the delimiter as the spec
 * requires.
 *
 * Returns the original body unchanged when the identity has no
 * signature, when no identity is available, or when the signature is
 * blank after trimming. v1 stores signatures as plain text only
 * (REQ-MAIL-100); HTML-authored signatures are out of scope.
 */
/**
 * Generate a Content-ID for an inline image part. Format: a short
 * random suffix plus the per-compose attachment sequence number, both
 * lowercase alphanumeric so RFC 2392 cid: URLs work without escaping.
 */
function generateInlineCID(seq: number): string {
  const rand = Math.random().toString(36).slice(2, 10);
  return `inl-${seq}-${rand}@herold.local`;
}

/**
 * Rewrite the editor body's inline-image src attributes from the
 * `blob:...` previews used during composition to the `cid:<id>`
 * references the receiving client expects (RFC 2392). Returns the
 * input unchanged when there are no inline images.
 */
export function rewriteInlineImageURLs(
  body: string,
  attachments: ComposeAttachment[],
): string {
  if (!body) return body;
  const inlines = attachments.filter(
    (a) => a.inline && a.objectURL && a.cid,
  ) as (ComposeAttachment & { objectURL: string; cid: string })[];
  if (inlines.length === 0) return body;
  let out = body;
  for (const a of inlines) {
    // The src attribute is HTML-attribute-quoted with either " or '.
    // Match both via a non-capturing alternation. The objectURL is
    // a same-origin "blob:https://..." which is a literal substring.
    const escaped = a.objectURL.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const re = new RegExp(`src=(["'])${escaped}\\1`, 'g');
    out = out.replace(re, `src=$1cid:${a.cid}$1`);
  }
  return out;
}

/**
 * A data: image URI extracted from the HTML body (issue #50).
 * Pure value type — carries everything needed to upload the image as a
 * MIME part and rewrite the HTML body.
 */
export interface DataUriImage {
  /** Generated Content-ID (RFC 2392), unique within this extraction pass. */
  cid: string;
  /** Full MIME type, e.g. 'image/png'. */
  mime: string;
  /**
   * Decoded image bytes backed by a plain ArrayBuffer (not SharedArrayBuffer)
   * so the value is directly passable to the Blob constructor.
   */
  bytes: Uint8Array<ArrayBuffer>;
  /** Synthesized filename, e.g. 'inline-image-1.png'. */
  name: string;
  /** Byte count (equals bytes.length). */
  size: number;
  /** Original data: URI — used to revert the cid: if the upload fails. */
  dataUri: string;
}

/**
 * Map a common image MIME subtype to a file extension. Used to build
 * a synthesized filename for extracted data: image parts.
 */
function mimeSubtypeToExt(subtype: string): string {
  switch (subtype.toLowerCase()) {
    case 'jpeg':
      return 'jpg';
    case 'svg+xml':
      return 'svg';
    default:
      return subtype.toLowerCase();
  }
}

/**
 * Scan `html` for base64-encoded `data:image/*;base64,...` URIs in
 * `<img src="...">` and `<img src='...'>` attributes. For each distinct
 * data: URI, decode the bytes, generate a Content-ID, and collect a
 * `DataUriImage` descriptor. Duplicate data: URIs (identical payloads) map
 * to the same CID so one blob covers all occurrences.
 *
 * Returns the rewritten HTML (with data: srcs replaced by `cid:<cid>`
 * references) and the list of images to upload. Non-base64 data: URIs and
 * URIs whose base64 payload cannot be decoded are left in the HTML unchanged.
 *
 * This is a pure, synchronous helper; the async upload step is handled by
 * the callers in `persistDraft` / `send` via `processDataUriImages`.
 */
export function extractDataUriImages(html: string): {
  html: string;
  images: DataUriImage[];
} {
  if (!html) return { html, images: [] };

  // Map from raw data URI to its assigned cid for deduplication.
  const dedup = new Map<string, string>();
  const images: DataUriImage[] = [];
  let seq = 0;

  // Matches src=" or src=' followed by a base64 data: image URI.
  // Capture groups: 1=quote, 2=full dataUri, 3=subtype, 4=base64 payload.
  const DATA_URI_RE =
    /src=(["'])(data:image\/([a-zA-Z0-9+.\-]+);base64,([A-Za-z0-9+/=\s]+))\1/g;

  const rewritten = html.replace(
    DATA_URI_RE,
    (
      match: string,
      quote: string,
      dataUri: string,
      subtype: string,
      b64: string,
    ) => {
      // Normalise whitespace that some editors embed in long base64 lines.
      const b64clean = b64.replace(/\s/g, '');

      let cid: string;
      if (dedup.has(dataUri)) {
        cid = dedup.get(dataUri)!;
      } else {
        seq++;
        const rand = Math.random().toString(36).slice(2, 10);
        cid = `di-${seq}-${rand}@herold.local`;

        let bytes: Uint8Array<ArrayBuffer>;
        try {
          const raw = atob(b64clean);
          bytes = new Uint8Array(raw.length);
          for (let i = 0; i < raw.length; i++) {
            bytes[i] = raw.charCodeAt(i);
          }
        } catch {
          // Base64 decode failed — leave this src untouched.
          return match;
        }

        const ext = mimeSubtypeToExt(subtype);
        images.push({
          cid,
          mime: `image/${subtype}`,
          bytes,
          name: `inline-image-${seq}.${ext}`,
          size: bytes.length,
          dataUri,
        });
        dedup.set(dataUri, cid);
      }
      return `src=${quote}cid:${cid}${quote}`;
    },
  );

  return { html: rewritten, images };
}

/**
 * Upload the images returned by `extractDataUriImages` as JMAP blobs and
 * return ComposeAttachment entries ready to include in the outbound body
 * structure. For any image whose upload fails, the corresponding `cid:`
 * reference in the HTML is reverted to the original data: URI so the
 * recipient's client still sees the image (at the cost of a larger body).
 *
 * Not exported — called only from `persistDraft` and `send`.
 */
async function processDataUriImages(
  html: string,
  accountId: string,
): Promise<{ html: string; attachments: (ComposeAttachment & { blobId: string })[] }> {
  const { html: extracted, images } = extractDataUriImages(html);
  if (images.length === 0) return { html, attachments: [] };

  const attachments: (ComposeAttachment & { blobId: string })[] = [];
  const results = await Promise.allSettled(
    images.map(async (img) => {
      const blob = new Blob([img.bytes], { type: img.mime });
      const r = await jmap.uploadBlob({ accountId, body: blob, type: img.mime });
      return { img, blobId: r.blobId };
    }),
  );

  let finalHtml = extracted;
  for (let i = 0; i < results.length; i++) {
    const r = results[i]!;
    if (r.status === 'fulfilled') {
      const { img, blobId } = r.value;
      attachments.push({
        key: `di-${img.cid}`,
        name: img.name,
        size: img.size,
        type: img.mime,
        status: 'ready',
        blobId,
        error: null,
        inline: true,
        cid: img.cid,
        objectURL: null,
      });
    } else {
      // Upload failed: put the original data: URI back so the message
      // body remains self-contained rather than containing a broken cid:.
      const img = images[i]!;
      const escapedCid = img.cid.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
      finalHtml = finalHtml.replace(
        new RegExp(`src=(["'])cid:${escapedCid}\\1`, 'g'),
        `src=$1${img.dataUri}$1`,
      );
      console.error('compose: data: image upload failed for', img.name);
    }
  }

  return { html: finalHtml, attachments };
}

/**
 * Construct the JMAP bodyStructure from the ready attachments list.
 *
 * Layout per RFC 8621 + RFC 2387:
 * - no attachments: multipart/alternative (text/plain + text/html).
 * - inline-only: multipart/related wrapping the alternative + each
 *   inline part (so the html-body's cid: refs resolve).
 * - attachment-only: multipart/mixed wrapping the alternative + each
 *   attachment part.
 * - both: multipart/mixed wrapping a multipart/related (alt + inline
 *   parts) + each attachment part.
 *
 * Inline parts gain `disposition: 'inline'` and a `cid` matching the
 * <img src="cid:..."> reference in the html body. Attachments stay
 * disposition: 'attachment'.
 */
function buildBodyStructure(
  ready: (ComposeAttachment & { blobId: string })[],
): Record<string, unknown> {
  const altPart: Record<string, unknown> = {
    type: 'multipart/alternative',
    subParts: [
      { partId: '1', type: 'text/plain', charset: 'utf-8' },
      { partId: '2', type: 'text/html', charset: 'utf-8' },
    ],
  };
  const inlines = ready.filter((a) => a.inline);
  const attached = ready.filter((a) => !a.inline);
  const inlineParts = inlines.map((a) => ({
    blobId: a.blobId,
    type: a.type,
    disposition: 'inline',
    name: a.name,
    size: a.size,
    cid: a.cid ?? null,
  }));
  const attachmentParts = attached.map((a) => ({
    blobId: a.blobId,
    type: a.type,
    disposition: 'attachment',
    name: a.name,
    size: a.size,
  }));
  const inner: Record<string, unknown> =
    inlineParts.length === 0
      ? altPart
      : {
          type: 'multipart/related',
          subParts: [altPart, ...inlineParts],
        };
  if (attachmentParts.length === 0) return inner;
  return {
    type: 'multipart/mixed',
    subParts: [inner, ...attachmentParts],
  };
}

/**
 * Flat attachment list mirrored on the JMAP `attachments` field. Both
 * inline and regular attachments are listed here per RFC 8621 §4.1.4.
 */
function buildAttachmentParts(
  ready: (ComposeAttachment & { blobId: string })[],
): Record<string, unknown>[] {
  return ready.map((a) =>
    a.inline
      ? {
          blobId: a.blobId,
          type: a.type,
          disposition: 'inline',
          name: a.name,
          size: a.size,
          cid: a.cid ?? null,
        }
      : {
          blobId: a.blobId,
          type: a.type,
          disposition: 'attachment',
          name: a.name,
          size: a.size,
        },
  );
}

function appendSignature(body: string, identity: Identity | null): string {
  if (!identity) return body;
  const sig = identity.textSignature ?? '';
  if (!sig.trim()) return body;
  const sigHtml = plainTextToHtml(sig);
  // Empty paragraph between body and delimiter so the user has a blank
  // line to type in if the body was an empty editor on openBlank.
  return `${body}<p></p><p>-- </p>${sigHtml}`;
}

/**
 * Strip the standard sig-dash delimiter (`-- ` per RFC 3676 §4.3) and
 * everything below it, then HTML tags, then whitespace. Used by
 * hasContent and the empty-body send check so a fresh compose carrying
 * only the appended signature is treated as having no user-authored
 * body content (issues #21, #22).
 */
export function bodyTextWithoutSignature(html: string): string {
  const text = html.replace(/<[^>]+>/g, '\n').replace(/&nbsp;/g, ' ');
  // Match the sig-dash on its own line: optional whitespace, `--`,
  // a single space, end-of-line. RFC 3676 mandates the trailing space.
  const idx = text.search(/(^|\n)\s*-- (?:\n|$)/);
  const above = idx === -1 ? text : text.slice(0, idx);
  return above.trim();
}

/**
 * Decide whether the compose body has user-visible content for the
 * send-without-content warning (REQ-MAIL-18 / REQ-MAIL-18a).
 *
 * Returns true when the body has either:
 *   - non-empty text after stripping the auto-inserted signature
 *     (REQ-MAIL-19), OR
 *   - at least one `<img>` element (inline images count as body
 *     content per REQ-MAIL-18a; a message of "just pictures" is
 *     intentional and must not trigger the warning).
 *
 * Returns false when the body is "empty" for the warning's purposes.
 */
export function bodyHasContent(html: string): boolean {
  if (/<img\b/i.test(html)) return true;
  return bodyTextWithoutSignature(html).length > 0;
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

// ── Share link body helpers (REQ-ATT-63) ─────────────────────────────────

/**
 * Format a human-readable relative expiry string given an ISO-8601 UTC date.
 * Returns strings like "30 days", "1 day", "2 hours", or a short date for
 * expiries more than 90 days away.
 */
function formatExpiry(expiresAt: string): string {
  const now = Date.now();
  const exp = new Date(expiresAt).getTime();
  const diffMs = exp - now;
  if (diffMs <= 0) return 'expired';
  const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
  if (diffDays >= 2) return `${diffDays} days`;
  if (diffDays === 1) return '1 day';
  const diffHours = Math.floor(diffMs / (1000 * 60 * 60));
  if (diffHours >= 1) return `${diffHours} hour${diffHours > 1 ? 's' : ''}`;
  return 'less than 1 hour';
}

/**
 * Build the data-share-url sentinel value used to locate and remove link
 * blocks from the HTML body. The url is safe to embed as an attribute
 * because the server generates it as a URL-safe base64 token.
 */
function shareBlockSelector(url: string): string {
  return url;
}

/**
 * Build the HTML for a single share-link block (REQ-ATT-63). The block
 * carries a `data-herold-share` attribute whose value is the share URL so
 * removal code can locate it by attribute match rather than fragile text
 * search. Extracted so it can be called standalone when driving the editor
 * via a ProseMirror transaction.
 */
export function buildShareLinkHtml(share: ComposeShare): string {
  const humanSize = formatBytes(share.size);
  const expiryLabel = formatExpiry(share.expiresAt);
  const safeUrl = escapeHtml(share.url);
  const safeName = escapeHtml(share.name);
  const safeSize = escapeHtml(humanSize);
  const safeExpiry = escapeHtml(expiryLabel);
  return `<p data-herold-share="${safeUrl}" style="border-left:3px solid #697077;padding:0.5em 1em;margin:0.5em 0;font-family:monospace,monospace"><strong>${safeName}</strong> (${safeSize})<br><a href="${safeUrl}">${safeUrl}</a><br><small>Expires in ${safeExpiry}</small></p>`;
}

/**
 * Insert a styled share link block into the HTML body (REQ-ATT-63).
 * Appends before the signature delimiter when present, or at the end.
 * Also injects a plain-text fallback line before the HTML block so
 * the text/plain alternative stays useful. Because we store only HTML
 * in compose.body and derive plain-text at send time via htmlToPlainText,
 * the link block only needs to appear in the HTML.
 *
 * This is the string-mutation fallback used when the editor is not mounted
 * (unit tests, pre-open compose). The live path goes through the editor via
 * `InsertShareLinkFn` (see `setShareEditorFns`).
 */
export function insertShareLinkIntoBody(body: string, share: ComposeShare): string {
  const htmlBlock = buildShareLinkHtml(share);

  // Insert before the sig-dash when present; otherwise append.
  const SIG_DELIMITER = /<p>-- <\/p>/i;
  if (SIG_DELIMITER.test(body)) {
    return body.replace(SIG_DELIMITER, `${htmlBlock}<p>-- </p>`);
  }
  return body + htmlBlock;
}

/**
 * Remove the share link block for `url` from the HTML body (REQ-ATT-64).
 * Matches the data-herold-share attribute inserted by insertShareLinkIntoBody.
 * Returns the body unchanged when no matching block is found.
 */
export function removeShareLinkFromBody(body: string, url: string): string {
  const safeUrl = escapeHtml(shareBlockSelector(url));
  // Match the paragraph element with the data-herold-share attribute.
  // The data attribute value is the URL; strip the whole <p>...</p> block.
  const re = new RegExp(
    `<p[^>]*data-herold-share="${safeUrl.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}"[^>]*>.*?<\\/p>`,
    'gis',
  );
  return body.replace(re, '');
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
  parseStringToRecipients,
  htmlToPlainText,
  replySubject,
  forwardSubject,
  mergeReferences,
  addressToString,
  addressListToString,
  escapeHtml,
  plainTextToHtml,
  computeReplyAllCc,
  computeReplyTo,
  isOwnMessage: isFromSelf,
  computeOwnMessageReplyAllCc,
  formatBytes,
  appendSignature,
  bodyTextWithoutSignature,
  bodyHasContent,
  rewriteInlineImageURLs,
  extractDataUriImages,
  buildBodyStructure,
  buildAttachmentParts,
  generateInlineCID,
  insertShareLinkIntoBody,
  removeShareLinkFromBody,
  buildShareLinkHtml,
};
