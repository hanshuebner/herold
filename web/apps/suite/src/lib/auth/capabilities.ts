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
