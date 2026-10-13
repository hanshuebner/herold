/**
 * "Download thread" link derivation (REQ-MAIL-143, server REQ-TEXP-10).
 *
 * The server advertises the thread-export endpoint through the session
 * capability `https://netzhansa.com/jmap/thread-export`, whose value is
 * `{ downloadUrl: "/jmap/download-thread/{accountId}/{threadId}/{name}" }`
 * (same variable syntax as the core RFC 8620 `downloadUrl`). The suite
 * fills the template and renders it as an `<a download>` so the browser
 * handles the save natively.
 */
import { Capability, type SessionResource } from '../jmap/types';
import { threadZipFilename } from './download-filename';

export interface ThreadDownloadLink {
  href: string;
  filename: string;
}

type SessionLike = Pick<SessionResource, 'capabilities'> | null | undefined;

/**
 * The thread-export URL template from the session, or null when the
 * capability is absent or carries no usable `downloadUrl`.
 */
export function threadExportUrlTemplate(session: SessionLike): string | null {
  const cap = session?.capabilities[Capability.HeroldThreadExport] as
    | { downloadUrl?: unknown }
    | undefined;
  const tmpl = cap?.downloadUrl;
  return typeof tmpl === 'string' && tmpl.length > 0 ? tmpl : null;
}

/**
 * Substitute `{accountId}`, `{threadId}` and `{name}` in the template
 * with URL-encoded values.
 */
export function expandThreadExportUrl(
  template: string,
  args: { accountId: string; threadId: string; name: string },
): string {
  return template
    .replace('{accountId}', encodeURIComponent(args.accountId))
    .replace('{threadId}', encodeURIComponent(args.threadId))
    .replace('{name}', encodeURIComponent(args.name));
}

/**
 * The download link for one thread, or null when the session does not
 * advertise thread export or the account is not yet known.
 */
export function threadDownloadLink(
  session: SessionLike,
  args: {
    accountId: string | null | undefined;
    threadId: string;
    subject: string | null | undefined;
  },
): ThreadDownloadLink | null {
  const template = threadExportUrlTemplate(session);
  if (!template || !args.accountId) return null;
  const filename = threadZipFilename({ subject: args.subject, threadId: args.threadId });
  return {
    href: expandThreadExportUrl(template, {
      accountId: args.accountId,
      threadId: args.threadId,
      name: filename,
    }),
    filename,
  };
}
