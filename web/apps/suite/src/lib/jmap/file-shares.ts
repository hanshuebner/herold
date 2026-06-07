/**
 * JMAP FileShare typed helpers (REQ-SHARE-40..44).
 *
 * FileShare is a herold-specific JMAP object type that represents a
 * blob promoted to a publicly-downloadable share link. All methods
 * require the HeroldFileShares capability.
 *
 * Wire contract: docs/design/server/requirements/25-attachment-shares.md
 */

import { jmap } from './client';
import { Capability } from './types';
import { JmapMethodError } from './errors';
import type { Invocation } from './types';

// ── Types ────────────────────────────────────────────────────────────────────

/** State values a FileShare can be in (REQ-SHARE-20). */
export type FileShareState = 'pending' | 'active' | 'revoked';

/**
 * A FileShare object as returned by FileShare/get and FileShare/query.
 * `password` is never returned; `hasPassword` reports its presence.
 */
export interface FileShare {
  id: string;
  blobId: string;
  name: string;
  type: string;
  size: number;
  /** Publicly-accessible capability URL. */
  url: string;
  state: FileShareState;
  createdAt: string;
  /** ISO-8601 UTC timestamp like "2006-01-02T15:04:05Z". */
  expiresAt: string;
  maxDownloads: number | null;
  downloadCount: number;
  hasPassword: boolean;
  lastDownloadedAt: string | null;
  /**
   * Email id of the message this share was confirmed from (REQ-SHARE-04).
   * null when the share was created from a source other than a compose-send,
   * or when the share is still pending and the source has not been recorded yet.
   */
  sourceMessageId: string | null;
  /**
   * Subject line snapshot taken at confirm time (REQ-SHARE-04 / REQ-ATT-70a).
   * null for pending shares and shares without a source message.
   */
  sourceSubject: string | null;
  /**
   * Envelope recipient email addresses (To + Cc + Bcc) snapshotted at
   * confirm time (REQ-SHARE-04 / REQ-ATT-70a). Omitted or empty for
   * pending shares and shares without a source message.
   */
  sourceRecipients?: string[];
}

/**
 * Arguments for FileShare/set create. The server clamps expiresIn to
 * max_ttl and sets createdAt / expiresAt server-side.
 */
export interface FileShareCreateArgs {
  blobId: string;
  name: string;
  type: string;
  /** Desired lifetime in seconds; clamped server-side to max_ttl. */
  expiresIn: number;
  maxDownloads?: number;
  password?: string;
}

/**
 * Arguments for FileShare/set update. Only state:"active" (confirm),
 * a shortened expiresAt, or a lowered maxDownloads are accepted
 * (REQ-SHARE-42). When confirming (state:"active") from a compose-send
 * path, the three source fields may also be included to record the
 * originating message (REQ-SHARE-04).
 */
export interface FileShareUpdateArgs {
  state?: 'active';
  expiresAt?: string;
  maxDownloads?: number | null;
  /**
   * Email id of the message the share was sent in (REQ-SHARE-04).
   * Only accepted by the server on the confirm transition (state:"active").
   */
  sourceMessageId?: string;
  /**
   * Subject line of the message at send time (REQ-SHARE-04).
   * Only accepted on the confirm transition.
   */
  sourceSubject?: string;
  /**
   * Flattened list of envelope recipient addresses (To + Cc + Bcc)
   * at send time (REQ-SHARE-04). Only accepted on the confirm transition.
   */
  sourceRecipients?: string[];
}

/** Capability value shape advertised in the session descriptor. */
export interface FileShareCapability {
  /** Bytes at or above which the suite offers offload (default 25 MB). */
  offload_threshold_bytes?: number;
  /** Default share lifetime in seconds after confirmation. */
  default_ttl_seconds?: number;
  /** Maximum allowed share lifetime in seconds. */
  max_ttl_seconds?: number;
  /** Principal's current share storage usage in bytes. */
  quota_used_bytes?: number;
  /** Principal's share storage cap in bytes. */
  quota_max_bytes?: number;
}

// ── Defaults ─────────────────────────────────────────────────────────────────

/** Default offload threshold when the capability does not advertise one. */
export const DEFAULT_OFFLOAD_THRESHOLD = 25 * 1024 * 1024; // 25 MB

/** Default share TTL when the capability does not advertise one (30 days). */
export const DEFAULT_TTL_SECONDS = 30 * 24 * 3600;

/** Default max TTL for the expiry picker (90 days). */
export const DEFAULT_MAX_TTL_SECONDS = 90 * 24 * 3600;

// ── Capability helpers ────────────────────────────────────────────────────────

/**
 * Returns true when the server advertises the file-shares capability
 * and hides all offload affordances when false (REQ-ATT-60).
 */
export function hasFileShares(): boolean {
  return jmap.hasCapability(Capability.HeroldFileShares);
}

/**
 * Read the file-shares capability value from the session descriptor.
 * Returns null when the capability is absent.
 */
export function fileShareCapability(): FileShareCapability | null {
  if (!hasFileShares()) return null;
  return (jmap.session?.capabilities[Capability.HeroldFileShares] as
    | FileShareCapability
    | null
    | undefined) ?? null;
}

/**
 * The offload threshold in bytes. Files at or above this size (and
 * unsafe-type files of any size) become offload candidates (REQ-ATT-60).
 */
export function offloadThresholdBytes(): number {
  return fileShareCapability()?.offload_threshold_bytes ?? DEFAULT_OFFLOAD_THRESHOLD;
}

/**
 * Default TTL in seconds for newly created shares (REQ-ATT-65).
 * Used as the default expiry preset in the offer dialog.
 */
export function defaultTtlSeconds(): number {
  return fileShareCapability()?.default_ttl_seconds ?? DEFAULT_TTL_SECONDS;
}

/**
 * Maximum TTL in seconds; the expiry picker must not offer values above this.
 */
export function maxTtlSeconds(): number {
  return fileShareCapability()?.max_ttl_seconds ?? DEFAULT_MAX_TTL_SECONDS;
}

// ── Unsafe-type detection (REQ-ATT-60 / REQ-ATT-30) ─────────────────────────

/**
 * Executable extensions from REQ-ATT-30 plus archive types from REQ-ATT-60.
 * A file whose name ends in any of these is an offload candidate regardless
 * of size.
 */
const UNSAFE_EXTENSIONS = new Set([
  // REQ-ATT-30 executables / scripts
  '.exe', '.bat', '.cmd', '.com', '.scr', '.pif', '.vbs', '.vbe',
  '.js', '.jse', '.ws', '.wsf', '.wsh', '.msi', '.msp', '.reg',
  '.lnk', '.scf', '.ps1', '.ps1xml', '.ps2', '.ps2xml', '.psc1',
  '.psc2', '.jar', '.dll',
  // REQ-ATT-60 archive types
  '.zip', '.rar', '.7z', '.tar', '.gz', '.tgz', '.dmg', '.iso', '.img',
]);

/**
 * Returns true when the filename ends in an extension that makes it an
 * offload candidate regardless of size (REQ-ATT-60).
 */
export function isUnsafeExtension(filename: string): boolean {
  const lower = filename.toLowerCase();
  for (const ext of UNSAFE_EXTENSIONS) {
    if (lower.endsWith(ext)) return true;
  }
  return false;
}

// ── JMAP methods ─────────────────────────────────────────────────────────────

function getAccountId(): string {
  const id = jmap.session?.primaryAccounts[Capability.Mail];
  if (!id) throw new Error('No mail account on this session');
  return id;
}

function parseCreate<T>(
  responses: Invocation[],
  clientId: string,
): T {
  const res = responses.find(
    (r) => r[0] === 'FileShare/set' || r[0] === 'error',
  );
  if (!res) throw new Error('No FileShare/set response in batch');
  if (res[0] === 'error') {
    const e = res[1] as { type: string; description?: string };
    throw new JmapMethodError(res[2] as string, e);
  }
  const args = res[1] as {
    created?: Record<string, FileShare>;
    notCreated?: Record<string, { type: string; description?: string }>;
    updated?: Record<string, FileShare | null>;
    notUpdated?: Record<string, { type: string; description?: string }>;
    destroyed?: string[];
    notDestroyed?: Record<string, { type: string; description?: string }>;
  };
  if (args.notCreated?.[clientId]) {
    const e = args.notCreated[clientId];
    throw new JmapMethodError(`create:${clientId}`, e);
  }
  const created = args.created?.[clientId];
  if (!created) throw new Error(`FileShare/set created[${clientId}] missing`);
  return created as unknown as T;
}

/**
 * Create a new FileShare from an already-uploaded blob (REQ-SHARE-41).
 * Does NOT re-upload; reuses the blobId returned by Blob/upload.
 * Resolves with the created FileShare object including its public url.
 */
export async function createFileShare(
  args: FileShareCreateArgs,
): Promise<FileShare> {
  const accountId = getAccountId();
  const { responses } = await jmap.batch((b) => {
    b.call(
      'FileShare/set',
      {
        accountId,
        create: { share1: { ...args } },
      },
      [Capability.HeroldFileShares],
    );
  });
  return parseCreate<FileShare>(responses, 'share1');
}

/**
 * Source-message metadata passed to confirm calls (REQ-SHARE-04).
 * All three fields are captured at confirm time and are immutable after that.
 */
export interface FileShareSourceInfo {
  /** Email id of the sent message. */
  sourceMessageId: string;
  /** Subject line at send time. */
  sourceSubject: string;
  /** Flattened envelope recipients (To + Cc + Bcc) as plain address strings. */
  sourceRecipients: string[];
}

/**
 * Confirm a pending share to active after the message is submitted
 * (REQ-SHARE-20 / REQ-ATT-66). Idempotent per REQ-SHARE-21.
 * When `source` is provided the server records the originating message
 * metadata on the share (REQ-SHARE-04).
 */
export async function confirmFileShare(
  id: string,
  source?: FileShareSourceInfo,
): Promise<void> {
  const accountId = getAccountId();
  const patch: FileShareUpdateArgs = {
    state: 'active',
    ...(source ?? {}),
  };
  await jmap.batch((b) => {
    b.call(
      'FileShare/set',
      {
        accountId,
        update: { [id]: patch },
      },
      [Capability.HeroldFileShares],
    );
  });
}

/**
 * Batch-confirm multiple pending shares (REQ-ATT-66). Sends one
 * FileShare/set request with all ids in the update map. When `source`
 * is provided each share in the batch gets the same originating-message
 * metadata (REQ-SHARE-04).
 */
export async function confirmFileShares(
  ids: string[],
  source?: FileShareSourceInfo,
): Promise<void> {
  if (ids.length === 0) return;
  const accountId = getAccountId();
  const patch: FileShareUpdateArgs = {
    state: 'active',
    ...(source ?? {}),
  };
  const update: Record<string, FileShareUpdateArgs> = {};
  for (const id of ids) update[id] = patch;
  await jmap.batch((b) => {
    b.call(
      'FileShare/set',
      { accountId, update },
      [Capability.HeroldFileShares],
    );
  });
}

/**
 * Destroy a share (REQ-ATT-66 discard path / REQ-ATT-64 remove).
 * If the share is still pending, it is deleted immediately; the
 * pending_ttl sweeper is the backstop if the client crashes.
 */
export async function destroyFileShare(id: string): Promise<void> {
  const accountId = getAccountId();
  await jmap.batch((b) => {
    b.call(
      'FileShare/set',
      { accountId, destroy: [id] },
      [Capability.HeroldFileShares],
    );
  });
}

/**
 * Batch-destroy multiple shares. Uses a single FileShare/set call.
 */
export async function destroyFileShares(ids: string[]): Promise<void> {
  if (ids.length === 0) return;
  const accountId = getAccountId();
  await jmap.batch((b) => {
    b.call(
      'FileShare/set',
      { accountId, destroy: ids },
      [Capability.HeroldFileShares],
    );
  });
}

/**
 * Update a share (shorten expiry, lower maxDownloads, or revoke via
 * state:"revoked"). REQ-SHARE-42 restricts what is mutable.
 */
export async function updateFileShare(
  id: string,
  patch: FileShareUpdateArgs | { state: 'revoked' },
): Promise<void> {
  const accountId = getAccountId();
  await jmap.batch((b) => {
    b.call(
      'FileShare/set',
      { accountId, update: { [id]: patch } },
      [Capability.HeroldFileShares],
    );
  });
}

/**
 * Fetch one or more FileShare objects by id (REQ-SHARE-40).
 * Returns an array of the found shares; missing ids are silently absent.
 */
export async function getFileShares(ids: string[]): Promise<FileShare[]> {
  if (ids.length === 0) return [];
  const accountId = getAccountId();
  const { responses } = await jmap.batch((b) => {
    b.call(
      'FileShare/get',
      { accountId, ids },
      [Capability.HeroldFileShares],
    );
  });
  const res = responses[0];
  if (!res || res[0] === 'error') return [];
  const args = res[1] as { list?: FileShare[] };
  return args.list ?? [];
}

/**
 * Query the principal's shares (REQ-SHARE-40 / REQ-ATT-70).
 * Returns all shares, optionally filtered by state, sorted by createdAt
 * descending (newest first). The result is the full list, not paginated.
 */
export async function queryFileShares(opts?: {
  state?: FileShareState;
}): Promise<FileShare[]> {
  const accountId = getAccountId();
  const filter = opts?.state ? { state: opts.state } : undefined;
  const { responses } = await jmap.batch((b) => {
    const q = b.call(
      'FileShare/query',
      {
        accountId,
        ...(filter ? { filter } : {}),
        sort: [{ property: 'createdAt', isAscending: false }],
      },
      [Capability.HeroldFileShares],
    );
    b.call(
      'FileShare/get',
      {
        accountId,
        '#ids': q.ref('/ids'),
      },
      [Capability.HeroldFileShares],
    );
  });
  // responses[0] = FileShare/query, responses[1] = FileShare/get
  const getRes = responses[1];
  if (!getRes || getRes[0] === 'error') return [];
  const args = getRes[1] as { list?: FileShare[] };
  return args.list ?? [];
}
