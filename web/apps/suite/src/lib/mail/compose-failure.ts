/**
 * Compose failure toast helper for external SMTP submission failures
 * (REQ-MAIL-SUBMIT-06).
 *
 * When `EmailSubmission/set` for an external-submission Identity returns a
 * failure category, this helper surfaces a transient toast with the failure
 * message. For `auth-failed` the toast offers a "Re-authenticate" button that
 * navigates Settings scoped to the Identity.
 *
 * The draft is preserved in Drafts (the existing draft-on-fail flow in
 * compose.svelte.ts handles this independently — the send path leaves the
 * compose open on error with the draft id intact).
 */

import { toast } from '../toast/toast.svelte';
import { router } from '../router/router.svelte';

/** Failure categories surfaced by the server on external submission failure. */
export type ExternalSubmissionFailureCategory =
  | 'auth-failed'
  | 'unreachable'
  | 'permanent'
  | 'transient';

/** Arguments for surfacing an external submission failure. */
export interface ExternalSubmissionFailureArgs {
  /** The failure category from the JMAP EmailSubmission notCreated entry. */
  category: ExternalSubmissionFailureCategory;
  /** The Identity JMAP id, used to scope the Settings route. */
  identityId: string;
  /** Optional diagnostic text from the server. */
  diagnostic?: string;
}

/**
 * Surface a transient toast for an external submission failure.
 *
 * For `auth-failed`: includes a "Re-authenticate" action that navigates
 * to Settings > Account scoped to the Identity.
 *
 * For `unreachable`: shows the toast without the re-auth button (re-auth
 * won't fix transport failures).
 *
 * For `permanent` / `transient`: shows a plain informational toast.
 */
export function showExternalSubmissionFailure(
  args: ExternalSubmissionFailureArgs,
): void {
  const { category, identityId, diagnostic } = args;

  const message = failureMessage(category, diagnostic);

  if (category === 'auth-failed') {
    // Offer a Re-authenticate shortcut for auth failures (REQ-MAIL-SUBMIT-06).
    // The undo callback is a one-shot action button labelled "Re-authenticate".
    toast.show({
      message,
      kind: 'error',
      // Keep the toast until dismissed; the user must act on auth failures.
      timeoutMs: 0,
      actionLabel: 'Re-authenticate',
      undo: () => {
        // Navigate to Settings > Account, scoped to this identity.
        // The route /settings/account opens the identity list; the identity
        // id in the param lets SettingsView open the edit dialog pre-scrolled
        // to the submission section.
        router.navigate(`/settings/account?identity=${encodeURIComponent(identityId)}&action=reauth`);
      },
    });
  } else {
    // For unreachable / permanent / transient failures the user cannot
    // self-service in the app — surface the message with a reasonable timeout.
    toast.show({
      message,
      kind: 'error',
      timeoutMs: 8000,
    });
  }
}

/**
 * Compose a human-readable message for the given failure category.
 */
function failureMessage(
  category: ExternalSubmissionFailureCategory,
  diagnostic: string | undefined,
): string {
  switch (category) {
    case 'auth-failed':
      return diagnostic
        ? `Authentication failed: ${diagnostic}`
        : 'Authentication failed with the external SMTP server. Re-authenticate to continue.';
    case 'unreachable':
      return diagnostic
        ? `External server unreachable: ${diagnostic}`
        : 'The external SMTP server could not be reached. Check your submission config.';
    case 'permanent':
      return diagnostic
        ? `Rejected by external server: ${diagnostic}`
        : 'The external SMTP server permanently rejected the message.';
    case 'transient':
      return diagnostic
        ? `Temporary failure from external server: ${diagnostic}`
        : 'A temporary error occurred at the external SMTP server. The message was not sent.';
    default:
      return 'External submission failed. Check the submission configuration.';
  }
}

/**
 * Recognise an external submission failure from a JMAP EmailSubmission/set
 * `notCreated` or `notUpdated` error entry.
 *
 * The herold server sets `type: "external-submission-failed"` and populates
 * a top-level `category` field with the extsubmit.OutcomeState value
 * (auth-failed / unreachable / permanent / transient). This helper extracts
 * those fields from the raw JMAP error object.
 *
 * Returns null if the error is not an external submission failure.
 */
export function parseExternalSubmissionFailure(error: {
  type: string;
  description?: string;
  [key: string]: unknown;
}): ExternalSubmissionFailureCategory | null {
  if (error.type !== 'external-submission-failed') return null;
  const category = error['category'] as string | undefined;
  if (
    category === 'auth-failed' ||
    category === 'unreachable' ||
    category === 'permanent' ||
    category === 'transient'
  ) {
    return category;
  }
  // Fallback: treat any external-submission-failed as permanent.
  return 'permanent';
}
