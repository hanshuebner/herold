/**
 * TypeScript types for the IMAPImport JMAP datatype.
 *
 * Wire format matches the Go-side `jmapIMAPImportAccount` struct in
 * internal/protojmap/mail/imapimport/types.go.
 *
 * REQ-IMAP-IMP-02, -15, -61 / REQ-SET-IMAPIMP-01..04.
 */

/**
 * Lifecycle states for an upstream IMAP import account.
 * REQ-IMAP-IMP-02 / REQ-IMAP-IMP-90..96.
 */
export type IMAPImportState =
  | 'enabled'
  | 'disabled'
  | 'errored'
  | 'migrating'
  | 'migrated';

/**
 * TLS modes for the upstream IMAP connection.
 * REQ-IMAP-IMP-06: tls_mode = none is rejected server-side.
 */
export type IMAPImportTLSMode = 'starttls' | 'implicit';

/**
 * Authentication methods for the upstream IMAP connection.
 * REQ-IMAP-IMP-03..04: xoauth2 requires operator-registered OAuth app.
 */
export type IMAPImportAuthMethod = 'password' | 'app_password' | 'xoauth2';

/**
 * Wire-form IMAPImport object returned by IMAPImport/get (read-only).
 * The credential is write-only and never included in responses;
 * `hasCredential` reports whether one is stored.
 */
export interface IMAPImportAccount {
  id: string;
  /** Owning JMAP Identity (decision 10, REQ-IMAP-IMP-01). */
  identityId: string;
  /** User/operator-visible label (defaults to the identity's email). */
  accountName: string;
  host: string;
  port: number;
  tlsMode: IMAPImportTLSMode;
  username: string;
  authMethod: IMAPImportAuthMethod;
  /**
   * Absolute backfill floor as "YYYY-MM-DD" or "all" (no floor).
   * Relative values (30d / 90d / 1y) are resolved to an absolute date
   * at create time and never appear on the read side (REQ-IMAP-IMP-16).
   */
  backfillHorizon: string;
  state: IMAPImportState;
  /** RFC 3339 UTC timestamp of the last successful fetch, or null. */
  lastSuccessAt: string | null;
  /** Human-readable error string from the last failure, or "". */
  lastError: string;
  /** Whether herold EXPUNGE propagates upstream on local delete. */
  deletePropagates: boolean;
  /** True when a sealed credential is stored (credential is write-only). */
  hasCredential: boolean;
}

/**
 * Response body for IMAPImport/get.
 */
export interface IMAPImportGetResponse {
  accountId: string;
  state: string;
  list: IMAPImportAccount[];
  notFound: string[];
}

/**
 * Preset values for the backfill horizon picker (REQ-IMAP-IMP-15).
 * "custom" maps to an arbitrary date string in the create call.
 */
export type BackfillHorizonPreset = '30d' | '90d' | '1y' | 'all' | 'custom';

/**
 * Body for IMAPImport/set create.
 * `credential` is sealed server-side (REQ-IMAP-IMP-70).
 */
export interface IMAPImportCreateArgs {
  identityId: string;
  accountName: string;
  host: string;
  port: number;
  tlsMode: IMAPImportTLSMode;
  username: string;
  authMethod: IMAPImportAuthMethod;
  /** "30d" | "90d" | "1y" | "all" | "YYYY-MM-DD" */
  backfillHorizon: string;
  credential: string;
  deletePropagates?: boolean;
}

/**
 * Body for IMAPImport/set update (partial patch; omit unchanged fields).
 * Providing `credential` re-seals a new credential.
 * `state` transitions are validated server-side (REQ-IMAP-IMP-90..95).
 */
export interface IMAPImportUpdateArgs {
  accountName?: string;
  host?: string;
  port?: number;
  tlsMode?: IMAPImportTLSMode;
  username?: string;
  authMethod?: IMAPImportAuthMethod;
  backfillHorizon?: string;
  credential?: string;
  state?: IMAPImportState;
  deletePropagates?: boolean;
}

/**
 * IMAPImport/set request body.
 */
export interface IMAPImportSetRequest {
  accountId: string;
  create?: Record<string, IMAPImportCreateArgs>;
  update?: Record<string, IMAPImportUpdateArgs>;
  destroy?: string[];
  /**
   * Keep-or-delete choice for destroy operations (REQ-IMAP-IMP-102).
   * false (default): keep imported mail (provenance label becomes a
   *   regular user label the user can browse / rename / delete).
   * true: delete imported mail (dedup-safe per REQ-IMAP-IMP-103).
   */
  deleteImportedMail?: boolean;
}

/**
 * Set-error envelope returned per failed create / update / destroy.
 */
export interface IMAPImportSetError {
  type: string;
  description?: string;
  properties?: string[];
}

/**
 * IMAPImport/set response body.
 */
export interface IMAPImportSetResponse {
  accountId: string;
  oldState?: string;
  newState: string;
  created?: Record<string, IMAPImportAccount>;
  updated?: Record<string, IMAPImportAccount | null>;
  destroyed?: string[];
  notCreated?: Record<string, IMAPImportSetError>;
  notUpdated?: Record<string, IMAPImportSetError>;
  notDestroyed?: Record<string, IMAPImportSetError>;
}
