/**
 * Capability-gating helpers for suite feature surfaces.
 *
 * Each helper reads the JMAP session descriptor via `jmap.hasCapability`
 * and returns a boolean. Callers use these to conditionally render UI
 * sections without duplicating the capability URI strings.
 *
 * Capability URIs are defined once in `lib/jmap/types.ts` (Capability.*);
 * this file only imports and re-exports the boolean predicates.
 */

import { jmap } from '../jmap/client';
import { Capability } from '../jmap/types';

/**
 * True when the server advertises the external-submission capability
 * (`https://netzhansa.com/jmap/external-submission`), i.e.
 * `[server.external_submission].enabled = true` in the operator config.
 *
 * When false, the entire external-submission UI surface is hidden:
 *   - the toggle in the Identity edit dialog
 *   - state badges in the Settings list
 *   - the from-picker icon
 *   - the compose failure toast with Re-authenticate
 *
 * REQ-MAIL-SUBMIT-01 / REQ-AUTH-EXT-SUBMIT-05.
 */
export function hasExternalSubmission(): boolean {
  return jmap.hasCapability(Capability.HeroldExternalSubmission);
}

/**
 * True when the server advertises the directory-autocomplete capability
 * (`https://netzhansa.com/jmap/directory-autocomplete`).
 *
 * When true, the compose-window address autocomplete queries
 * Directory/search in addition to JMAP Contacts and SeenAddress entries.
 */
export function hasDirectoryAutocomplete(): boolean {
  return jmap.hasCapability(Capability.HeroldDirectoryAutocomplete);
}

/**
 * Returns the directory-autocomplete mode from the capability value,
 * or null when the capability is absent.
 *
 * The mode is informational for the UI (e.g. placeholder text); the
 * server still enforces the actual filter regardless of what the client
 * reads here.
 *
 *   "all"    - server returns results across all principals.
 *   "domain" - server restricts results to the caller's email domain.
 *   null     - capability not advertised.
 */
export function directoryAutocompleteMode(): 'all' | 'domain' | null {
  if (!hasDirectoryAutocomplete()) return null;
  const cap = jmap.session?.capabilities[Capability.HeroldDirectoryAutocomplete] as
    | { mode?: string }
    | undefined;
  if (cap?.mode === 'all' || cap?.mode === 'domain') return cap.mode;
  return null;
}

/**
 * True when the server advertises the identity-verification capability
 * (`https://netzhansa.com/jmap/identity-verification`), i.e.
 * `[server.identity_creation].enabled = true` in the operator config.
 *
 * When false, the suite hides the Add-identity affordance and the
 * Verify / Resend buttons on pending / unverified rows
 * (REQ-SET-IDENT-05).
 */
export function hasIdentityVerification(): boolean {
  return jmap.hasCapability(Capability.HeroldIdentityVerification);
}

/**
 * True when the server advertises the file-shares capability
 * (`https://netzhansa.com/jmap/file-shares`), i.e.
 * `[server.attachment_shares].enabled = true` in the operator config.
 *
 * When false, the suite hides ALL offload affordances, the "Shared links"
 * compose strip, and the "Shared files" settings section (REQ-ATT-60..73).
 *
 * This function is a thin wrapper over the identical helper in
 * lib/jmap/file-shares.ts and is provided here so callers in
 * capabilities.ts have a consistent import pattern.
 */
export { hasFileShares } from '../jmap/file-shares';

/**
 * True when the server advertises the IMAP-import capability
 * (`https://netzhansa.com/jmap/imap-import`).
 *
 * When true the suite surfaces the optional "Receiving (IMAP import)"
 * section inside the per-identity edit dialog for external-domain
 * identities (REQ-SET-IMAPIMP-01, REQ-IMAP-IMP-61).
 */
export function hasIMAPImport(): boolean {
  return jmap.hasCapability(Capability.HeroldIMAPImport);
}

/**
 * Seconds threshold under which a chat message's timestamp is hidden
 * because the previous message in the same day-group is recent enough.
 * Sourced from the chat capability descriptor; defaults to 120 (2
 * minutes) when the field is missing or non-positive. 0 from the
 * server is honoured as "always show".
 */
export function chatTimestampGroupingSeconds(): number {
  const cap = jmap.session?.capabilities[Capability.HeroldChat] as
    | { messageTimestampGroupingSeconds?: number }
    | undefined;
  const v = cap?.messageTimestampGroupingSeconds;
  if (typeof v === 'number' && v >= 0) return v;
  return 120;
}
