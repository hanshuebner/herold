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
