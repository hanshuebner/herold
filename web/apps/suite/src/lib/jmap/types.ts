/**
 * JMAP Core types per RFC 8620.
 *
 * Just the wire shapes the suite cares about for bootstrap and method-call
 * dispatch. Full RFC 8621 (Mail) datatypes layer on top in feature-specific
 * modules.
 */

/**
 * RFC 8620 §3.2 — A method call is a triple `[methodName, args, callId]`.
 */
export type Invocation<TArgs = unknown> = [
  methodName: string,
  args: TArgs,
  callId: string,
];

/**
 * RFC 8620 §3.7 — A result reference points at the result of a prior call
 * in the same batch via JSON-Pointer path.
 */
export interface ResultReference {
  resultOf: string;
  name: string;
  path: string;
}

/**
 * RFC 8620 §3.6 — Method-level error envelope. Wrapped as an Invocation
 * with name "error".
 */
export interface MethodError {
  type: string;
  description?: string;
}

/**
 * RFC 8620 §2 — Session resource. Returned from `GET /.well-known/jmap`.
 */
export interface SessionResource {
  capabilities: Record<string, unknown>;
  accounts: Record<string, AccountInfo>;
  primaryAccounts: Record<string, string>;
  username: string;
  apiUrl: string;
  downloadUrl: string;
  uploadUrl: string;
  eventSourceUrl: string;
  state: string;
}

export interface AccountInfo {
  name: string;
  isPersonal: boolean;
  isReadOnly: boolean;
  accountCapabilities: Record<string, unknown>;
}

/**
 * Request to `POST /jmap` per RFC 8620 §3.3.
 */
export interface MethodCallRequest {
  using: string[];
  methodCalls: Invocation[];
  createdIds?: Record<string, string>;
}

export interface MethodCallResponse {
  methodResponses: Invocation[];
  createdIds?: Record<string, string>;
  sessionState: string;
}

/**
 * Standard JMAP capability URIs we use across the suite.
 */
export const Capability = {
  Core: 'urn:ietf:params:jmap:core',
  Mail: 'urn:ietf:params:jmap:mail',
  Submission: 'urn:ietf:params:jmap:submission',
  Sieve: 'urn:ietf:params:jmap:sieve',
  VacationResponse: 'urn:ietf:params:jmap:vacationresponse',
  Calendars: 'urn:ietf:params:jmap:calendars',
  Contacts: 'urn:ietf:params:jmap:contacts',
  WebSocket: 'urn:ietf:params:jmap:websocket',
  // Herold suite-specific capabilities
  HeroldSnooze: 'https://netzhansa.com/jmap/snooze',
  HeroldCategorise: 'https://netzhansa.com/jmap/categorise',
  HeroldChat: 'https://netzhansa.com/jmap/chat',
  HeroldEmailReactions: 'https://netzhansa.com/jmap/email-reactions',
  HeroldShortcutCoach: 'https://netzhansa.com/jmap/shortcut-coach',
  HeroldPush: 'https://netzhansa.com/jmap/push',
  HeroldManagedRules: 'https://netzhansa.com/jmap/managed-rules',
  HeroldLLMTransparency: 'https://netzhansa.com/jmap/llm-transparency',
  /**
   * Per-principal external-image internalization backlog (REQ-EXTIMG-BG-21).
   * The capability value is `{ pending_messages: uint64, as_of: rfc3339 }`
   * (the REQ-EXTIMG-BG-21 spec also reserves `total_messages` but the
   * current backend only emits the two fields above; the SPA reads what
   * is on the wire).
   *
   * Joined wire surface: the Go-side constant lives at
   * internal/protojmap/registry.go CapabilityInternalizeStatus.
   * Both sides MUST be updated together if the URI changes.
   */
  HeroldInternalizeStatus: 'urn:netzhansa:params:jmap:internalize-status',
  /**
   * External SMTP submission per Identity (REQ-AUTH-EXT-SUBMIT-05).
   * Advertised when [server.external_submission].enabled is true.
   * The suite shows the submission-config UI only when this capability
   * is present (REQ-MAIL-SUBMIT-01).
   *
   * Joined wire surface: the Go-side constant lives at
   * internal/protojmap/registry.go CapabilityExternalSubmission.
   * Both sides MUST be updated together if the URI changes.
   */
  HeroldExternalSubmission: 'https://netzhansa.com/jmap/external-submission',
  /**
   * Principal directory autocomplete (REQ-MAIL-11).
   * Advertised when the server supports Directory/search.
   * The capability value is { mode: "all" | "domain" }.
   *
   * Joined wire surface: the Go-side constant lives at
   * internal/protojmap/registry.go CapabilityDirectoryAutocomplete.
   * Both sides MUST be updated together if the URI changes.
   */
  HeroldDirectoryAutocomplete: 'https://netzhansa.com/jmap/directory-autocomplete',
  /**
   * Principal-initiated identity verification (REQ-IDENT-11).
   * Advertised when [server.identity_creation].enabled = true.
   * When the capability is absent, the suite hides the "Add identity"
   * affordance and the verify/resend buttons (REQ-SET-IDENT-05).
   *
   * Joined wire surface: the Go-side constant lives at
   * internal/protojmap/registry.go CapabilityIdentityVerification.
   * Both sides MUST be updated together if the URI changes.
   */
  HeroldIdentityVerification: 'https://netzhansa.com/jmap/identity-verification',
  /**
   * Tagged-address filters and dismissals (REQ-TAG-70).
   * Advertised when [server.tagged_addresses].enabled = true. When
   * absent, the suite hides the per-message banner (REQ-MAIL-12c) and
   * the Settings → Tagged addresses surface.
   *
   * Joined wire surface: the Go-side constant lives at
   * internal/protojmap/registry.go CapabilityTaggedAddresses.
   * Both sides MUST be updated together if the URI changes.
   */
  HeroldTaggedAddresses: 'https://netzhansa.com/jmap/tagged-addresses',
  /**
   * File share offload (REQ-ATT-60..73 / REQ-SHARE-40..44).
   * Advertised when [server.attachment_shares].enabled = true. When
   * absent, the suite hides ALL offload affordances and the "Shared
   * files" settings section.
   *
   * The capability value carries optional server-side knobs:
   *   offload_threshold_bytes: size at which the suite offers offload
   *     (default 25 MB when absent).
   *   default_ttl_seconds: seconds until a confirmed share expires
   *     (surfaced as the default expiry preset).
   *   max_ttl_seconds: ceiling for the expiry picker.
   *   quota_used_bytes: principal's current share storage use.
   *   quota_max_bytes: principal's share storage cap.
   *
   * Joined wire surface: the Go-side constant lives at
   * internal/protojmap/registry.go CapabilityFileShares.
   * Both sides MUST be updated together if the URI changes.
   */
  HeroldFileShares: 'https://netzhansa.com/jmap/file-shares',
  /**
   * Per-identity IMAP import (REQ-IMAP-IMP-61, REQ-SET-IMAPIMP-01).
   * Advertised unconditionally (no sysconfig gate in v1).
   * When present the suite surfaces the "Receiving (IMAP import)" section
   * inside the per-identity edit dialog for external-domain identities.
   *
   * Joined wire surface: the Go-side constant lives at
   * internal/protojmap/mail/imapimport/register.go CapabilityIMAPImport.
   * Both sides MUST be updated together if the URI changes.
   */
  HeroldIMAPImport: 'https://netzhansa.com/jmap/imap-import',
} as const;

export type CapabilityName = (typeof Capability)[keyof typeof Capability];
