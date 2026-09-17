/**
 * RFC 8058 one-click unsubscribe (REQ-UNS-20), issue #412.
 *
 * The Suite never POSTs directly to a sender's `List-Unsubscribe` URL
 * from the browser: that POST is cross-origin, and no sender is
 * expected to send `Access-Control-Allow-Origin` for an arbitrary
 * webmail origin, so the browser discards the response even when the
 * POST reached the sender -- the Suite could never observe success.
 * Instead this calls `Email/unsubscribe`
 * (`internal/protojmap/mail/email/unsubscribe.go`), which performs the
 * POST server-side, on the principal's behalf, and reports the
 * upstream outcome back over JMAP, where no CORS policy applies.
 * Gated behind the `https://netzhansa.com/jmap/unsubscribe` capability
 * -- callers check `jmap.hasCapability(Capability.HeroldEmailUnsubscribe)`
 * before offering the one-click affordance.
 */
import { jmap, strict } from '../jmap/client';
import { Capability, type Invocation } from '../jmap/types';

/** `Email/unsubscribe`'s three-way outcome. */
export type OneClickStatus = 'ok' | 'failed' | 'unsupported';

export interface OneClickResult {
  emailId: string;
  status: OneClickStatus;
  /** The upstream HTTP status, present only when a response came back. */
  httpStatus?: number;
  /** Short diagnostic, present on "failed" and "unsupported". */
  error?: string;
}

function invocationArgs<T>(inv: Invocation | undefined): T {
  if (!inv) throw new Error('Expected method invocation, got undefined');
  return inv[1] as T;
}

/**
 * Call `Email/unsubscribe` for one message. Throws (JmapMethodError /
 * JmapTransportError) on a method- or transport-level failure; callers
 * treat a thrown error the same as `status: "failed"`.
 */
export async function postOneClickUnsubscribe(
  accountId: string,
  emailId: string,
): Promise<OneClickResult> {
  const { responses } = await jmap.batch((b) => {
    b.call(
      'Email/unsubscribe',
      { accountId, emailId },
      [Capability.Mail, Capability.HeroldEmailUnsubscribe],
    );
  });
  strict(responses);
  return invocationArgs<OneClickResult>(responses[0]);
}
