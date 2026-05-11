/**
 * Pure validators for the add-identity wizard (REQ-SET-IDENT-30..33).
 *
 * Kept free of Svelte / store imports so the email / port / code shapes
 * are trivially unit-testable from plain vitest. The wizard component
 * uses these via `$derived` to surface inline error messages.
 *
 * Cross-reference: docs/design/web/requirements/20-settings.md §
 * "Add-identity wizard" and server-side REQ-IDENT-20..32 for the wire-
 * level shape of the code (6 decimal digits) and the domain policy
 * (hosted vs. external).
 */

/**
 * Minimal RFC 5321 syntactic gate for an email address. Mirrors the
 * gate already used in IdentityEditDialog so the wizard's error
 * message is consistent with the rest of Settings. The server still
 * validates the full grammar — this helper is for inline UX only.
 *
 * Rejects: empty string, anything without a single '@', no dot in the
 * domain, or whitespace anywhere.
 */
export function isValidEmail(value: string): boolean {
  const trimmed = value.trim();
  if (trimmed === '') return false;
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed);
}

/**
 * Lower-cased domain part of an email address, or null when the
 * address does not match the syntactic gate. Used to decide whether
 * the wizard's Step 2 (external SMTP) is needed by the operator's
 * domain policy.
 */
export function domainOf(email: string): string | null {
  if (!isValidEmail(email)) return null;
  const at = email.lastIndexOf('@');
  return email.slice(at + 1).toLowerCase();
}

/**
 * Whether the supplied email's domain is hosted on this herold (i.e.
 * appears in the server-advertised local-domain list). Hosted-domain
 * identities skip the external-SMTP step of the wizard
 * (REQ-SET-IDENT-32).
 *
 * `hostedDomains` is the lower-cased set sourced from the JMAP session
 * descriptor's local-domains list. When the descriptor does not yet
 * advertise the list (legacy server), the function falls back to
 * `false` (treat every domain as external) so the wizard never
 * skips the SMTP step prematurely.
 */
export function isHostedDomain(
  email: string,
  hostedDomains: ReadonlySet<string>,
): boolean {
  const dom = domainOf(email);
  if (dom === null) return false;
  return hostedDomains.has(dom);
}

/**
 * Whether a port number is in the IANA user-port range (1..65535).
 * Used by the wizard's Step 2 SMTP form (REQ-MAIL-SUBMIT-03 shape).
 * Browsers reject sub-1024 in some sandbox modes, but the server
 * accepts anything in 1..65535, so we mirror the server-side gate.
 */
export function isValidPort(port: number): boolean {
  if (!Number.isFinite(port)) return false;
  if (!Number.isInteger(port)) return false;
  if (port < 1 || port > 65535) return false;
  return true;
}

/**
 * Whether a string is exactly 6 ASCII decimal digits, matching the
 * server-side `verification_code` shape (REQ-IDENT-32). Leading zeros
 * are preserved.
 */
export function isValidCode(value: string): boolean {
  if (value.length !== 6) return false;
  for (let i = 0; i < value.length; i++) {
    const c = value.charCodeAt(i);
    if (c < 48 || c > 57) return false; // '0'..'9'
  }
  return true;
}

/**
 * Parse a `Retry-After` header value (RFC 7231 §7.1.3). Accepts both
 * the integer-seconds form ("60") and the HTTP-date form ("Tue, 14
 * May 2026 12:00:00 GMT"). Returns the number of seconds to wait, or
 * null when the header is missing / unparseable.
 *
 * Used by the wizard's Step 2 / verify-dialog Resend button to render
 * the "Try again in 47 s" countdown per REQ-SET-IDENT-21.
 */
export function parseRetryAfter(header: string | null, nowMs: number): number | null {
  if (header === null) return null;
  const trimmed = header.trim();
  if (trimmed === '') return null;
  // Integer-seconds form.
  if (/^\d+$/.test(trimmed)) {
    const seconds = parseInt(trimmed, 10);
    if (Number.isFinite(seconds) && seconds >= 0) return seconds;
    return null;
  }
  // Reject signed numeric forms before falling through to Date.parse,
  // which would accept "-5" as a 5-BC year and silently return a wildly
  // wrong delta.
  if (/^[+-]\d+$/.test(trimmed)) return null;
  // HTTP-date form.
  const target = Date.parse(trimmed);
  if (!Number.isFinite(target)) return null;
  const deltaMs = target - nowMs;
  if (deltaMs <= 0) return 0;
  return Math.ceil(deltaMs / 1000);
}
