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
 * The actual SMTP delivery happens server-side at sendAt; tabard's
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
  subject = $state('');
  body = $state('');
  errorMessage = $state<string | null>(null);

  /** Reply / forward context — null fields mean "this is a fresh compose". */
  replyContext = $state<ReplyContext>({ ...EMPTY_REPLY });

  /** Open a fresh compose. */
  openBlank(): void {
    this.to = '';
    this.subject = '';
    this.body = '';
    this.errorMessage = null;
    this.replyContext = { ...EMPTY_REPLY };
    this.status = 'editing';
    if (!mail.primaryIdentity || !mail.drafts) {
      void this.#warmupAccount();
    }
  }

  /**
   * Open compose pre-populated (e.g. after Undo). Caller is responsible
   * for resetting / setting the reply context.
   */
  openWith(args: {
    to: string;
    subject: string;
    body: string;
    replyContext?: ReplyContext;
  }): void {
    this.to = args.to;
    this.subject = args.subject;
    this.body = args.body;
    this.replyContext = args.replyContext ?? { ...EMPTY_REPLY };
    this.errorMessage = null;
    this.status = 'editing';
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

  /** Close and clear. */
  close(): void {
    this.status = 'idle';
    this.to = '';
    this.subject = '';
    this.body = '';
    this.errorMessage = null;
    this.replyContext = { ...EMPTY_REPLY };
  }

  get isOpen(): boolean {
    return this.status !== 'idle';
  }

  async send(): Promise<void> {
    if (this.status === 'sending') return;
    const accountId = mail.mailAccountId;
    const identity = mail.primaryIdentity;
    const drafts = mail.drafts;
    const sentMailbox = mail.sent;
    if (!accountId) {
      this.errorMessage = 'No Mail account on this session';
      return;
    }
    if (!identity) {
      this.errorMessage = 'No identity available — cannot send';
      return;
    }
    if (!drafts) {
      this.errorMessage = 'No drafts mailbox — cannot send';
      return;
    }

    const recipients = parseAddressList(this.to);
    if (recipients.length === 0) {
      this.errorMessage = 'At least one recipient is required';
      return;
    }
    const subject = this.subject;
    const bodyHtml = this.body;
    const bodyText = htmlToPlainText(bodyHtml);
    const replyContext = this.replyContext;

    // Snapshot for Undo (full state, including reply context).
    const savedTo = this.to;
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

    // Build the new email — multipart/alternative with text/plain + text/html.
    // Include In-Reply-To / References when this is a reply or forward.
    // Mark the parent $answered / $forwarded in the same batch
    // (REQ-MAIL-33) so the UI reflects the change immediately.
    const draftEmail: Record<string, unknown> = {
      mailboxIds: { [drafts.id]: true },
      keywords: { $draft: true, $seen: true },
      from: [{ name: identity.name, email: identity.email }],
      to: recipients.map((email) => ({ email, name: null })),
      subject,
      bodyValues: {
        '1': { value: bodyText, isTruncated: false, isEncodingProblem: false },
        '2': { value: bodyHtml, isTruncated: false, isEncodingProblem: false },
      },
      textBody: [{ partId: '1', type: 'text/plain', charset: 'utf-8' }],
      htmlBody: [{ partId: '2', type: 'text/html', charset: 'utf-8' }],
      bodyStructure: {
        type: 'multipart/alternative',
        subParts: [
          { partId: '1', type: 'text/plain', charset: 'utf-8' },
          { partId: '2', type: 'text/html', charset: 'utf-8' },
        ],
      },
    };
    if (replyContext.inReplyTo && replyContext.inReplyTo.length > 0) {
      draftEmail.inReplyTo = replyContext.inReplyTo;
    }
    if (replyContext.references && replyContext.references.length > 0) {
      draftEmail.references = replyContext.references;
    }

    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Email/set',
          {
            accountId,
            create: { draft1: draftEmail },
            ...(replyContext.parentId && replyContext.parentKeyword
              ? {
                  update: {
                    [replyContext.parentId]: {
                      [`keywords/${replyContext.parentKeyword}`]: true,
                    },
                  },
                }
              : {}),
          },
          [Capability.Mail],
        );
        b.call(
          'EmailSubmission/set',
          {
            accountId,
            create: {
              sub1: {
                emailId: '#draft1',
                identityId: identity.id,
                envelope: {
                  mailFrom: { email: identity.email },
                  rcptTo: recipients.map((email) => ({ email })),
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
