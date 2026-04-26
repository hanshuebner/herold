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
import { auth } from '../auth/auth.svelte';
import { Capability, type Invocation } from '../jmap/types';
import { mail } from '../mail/store.svelte';
import { toast } from '../toast/toast.svelte';

const DEFAULT_UNDO_WINDOW_SEC = 5;

type ComposeStatus = 'idle' | 'editing' | 'sending';

class ComposeStore {
  status = $state<ComposeStatus>('idle');
  to = $state('');
  subject = $state('');
  body = $state('');
  errorMessage = $state<string | null>(null);

  /** Open a fresh compose. */
  openBlank(): void {
    this.to = '';
    this.subject = '';
    this.body = '';
    this.errorMessage = null;
    this.status = 'editing';
    // Identity / mailbox prerequisites — kick off the load if not warm.
    if (!mail.primaryIdentity || !mail.drafts) {
      void this.#warmupAccount();
    }
  }

  /** Open compose pre-populated (e.g. after Undo). */
  openWith(args: { to: string; subject: string; body: string }): void {
    this.to = args.to;
    this.subject = args.subject;
    this.body = args.body;
    this.errorMessage = null;
    this.status = 'editing';
  }

  /** Close and clear. */
  close(): void {
    this.status = 'idle';
    this.to = '';
    this.subject = '';
    this.body = '';
    this.errorMessage = null;
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
    const body = this.body;

    // Snapshot for Undo.
    const savedTo = this.to;
    const savedSubject = subject;
    const savedBody = body;

    this.errorMessage = null;
    this.status = 'sending';

    const sendAt = new Date(Date.now() + DEFAULT_UNDO_WINDOW_SEC * 1000)
      .toISOString();

    const onSuccessUpdate: Record<string, true | null> = {};
    onSuccessUpdate[`mailboxIds/${drafts.id}`] = null;
    if (sentMailbox) onSuccessUpdate[`mailboxIds/${sentMailbox.id}`] = true;
    onSuccessUpdate['keywords/$draft'] = null;
    onSuccessUpdate['keywords/$seen'] = true;

    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Email/set',
          {
            accountId,
            create: {
              draft1: {
                mailboxIds: { [drafts.id]: true },
                keywords: { $draft: true, $seen: true },
                from: [{ name: identity.name, email: identity.email }],
                to: recipients.map((email) => ({ email, name: null })),
                subject,
                bodyValues: {
                  '1': {
                    value: body,
                    isTruncated: false,
                    isEncodingProblem: false,
                  },
                },
                textBody: [{ partId: '1', type: 'text/plain', charset: 'utf-8' }],
                bodyStructure: {
                  partId: '1',
                  type: 'text/plain',
                  charset: 'utf-8',
                },
              },
            },
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

      toast.show({
        message: 'Message sent',
        timeoutMs: DEFAULT_UNDO_WINDOW_SEC * 1000,
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
            // Re-open compose with the saved content.
            this.openWith({ to: savedTo, subject: savedSubject, body: savedBody });
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

export const compose = new ComposeStore();
