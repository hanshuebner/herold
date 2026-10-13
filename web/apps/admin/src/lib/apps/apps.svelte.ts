/**
 * Registered apps (REQ-APPS-30..36, server REQ-APP-10..14).
 *
 * Talks to the admin REST surface under /api/v1/apps. Every route needs
 * admin scope plus elevation; a 403 with step_up_required runs the SPA's
 * step-up flow inside the API client and retries once. Without the data
 * key every route answers 503 `apps/data_key_missing`, which both stores
 * expose as `dataKeyMissing`.
 *
 * The signing secret is returned only by create and rotate-secret. The
 * detail store keeps it in `secret` until the operator dismisses it, so
 * the page shows it exactly once. Create and refresh also return the
 * manifest warnings (dropped extensions with unknown points), which the
 * server does not store; the detail store holds them for the review.
 */

import { apiDelete, apiGet, apiPatch, apiPost, type ApiResponse } from '../api/client';
import { t } from '../i18n/i18n.svelte';

export type LocalizedText = Record<string, string>;

export interface AppExtension {
  point: string;
  id: string;
  label: LocalizedText;
  icon: string;
  scopes: string[];
  endpoint: string;
}

export interface AppManifest {
  herold_app: number;
  app_key: string;
  name: LocalizedText;
  extensions: AppExtension[];
}

export interface ScopeChange {
  extension_id: string;
  scope: string;
}

export interface EndpointChange {
  extension_id: string;
  from: string;
  to: string;
}

export interface ManifestDiff {
  added_extensions: string[] | null;
  removed_extensions: string[] | null;
  added_scopes: ScopeChange[] | null;
  removed_scopes: ScopeChange[] | null;
  changed_endpoints: EndpointChange[] | null;
}

export type AppState = 'pending_approval' | 'active' | 'disabled';

export interface AppPrincipal {
  id: number;
  email: string;
}

export interface AppRecord {
  id: number;
  app_key: string;
  origin: string;
  state: AppState;
  name: LocalizedText;
  all_principals: boolean;
  principals: AppPrincipal[] | null;
  manifest: AppManifest | null;
  manifest_digest: string;
  pending_manifest: AppManifest | null;
  pending_manifest_digest: string;
  pending_diff: ManifestDiff | null;
  created_at: string;
  updated_at: string;
  last_invoked_at: string | null;
  /** Only in create and rotate-secret responses. */
  secret?: string;
  /** Only in create and refresh responses. */
  warnings?: string[];
  /** Only in refresh responses. */
  manifest_changed?: boolean;
}

export type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

export interface OpResult {
  ok: boolean;
  errorMessage: string | null;
  /** Individual problems of a manifest fetch or validation failure. */
  errors?: string[];
}

/** Problem type the apps routes answer with when the data key is missing. */
export const DATA_KEY_MISSING_TYPE = 'https://netzhansa.com/problems/apps/data_key_missing';

/** True when a response is the apps surface's missing-data-key 503. */
export function isDataKeyMissing(res: Pick<ApiResponse<unknown>, 'status' | 'problem'>): boolean {
  if (res.status !== 503) return false;
  const type = res.problem?.type;
  return type === undefined || type === DATA_KEY_MISSING_TYPE;
}

function problemErrors(res: Pick<ApiResponse<unknown>, 'problem'>): string[] {
  const errs = res.problem?.errors;
  return Array.isArray(errs) ? errs.filter((e): e is string => typeof e === 'string') : [];
}

/** Pick a localized-map entry: `lang`, then `en`, then any entry. */
export function localized(map: LocalizedText | null | undefined, lang: string): string {
  if (!map) return '';
  return map[lang] ?? map.en ?? Object.values(map)[0] ?? '';
}

/** The app's display name, falling back to its app_key. */
export function appDisplayName(app: Pick<AppRecord, 'name' | 'app_key'>, lang: string): string {
  return localized(app.name, lang) || app.app_key;
}

/** i18n key of the plain-language explanation of a scope (REQ-APPS-32). */
export function scopeExplanationKey(scope: string): string {
  return scope === 'thread.raw' ? 'apps.scope.threadRaw' : 'apps.scope.unknown';
}

/** Full URL an extension endpoint is reached at. */
export function endpointUrl(origin: string, endpoint: string): string {
  return origin.replace(/\/+$/, '') + endpoint;
}

/**
 * The manifest the review view presents for approval: the pending one
 * while there is one, else the approved one.
 */
export function manifestUnderReview(app: AppRecord): AppManifest | null {
  return app.pending_manifest ?? app.manifest;
}

/** True when an approved manifest has a pending update (REQ-APPS-33). */
export function hasManifestUpdate(app: Pick<AppRecord, 'manifest' | 'pending_manifest'>): boolean {
  return app.manifest !== null && app.pending_manifest !== null;
}

/** Per-extension change marks of a pending manifest update. */
export interface ExtensionMarks {
  added: boolean;
  removed: boolean;
  addedScopes: string[];
  removedScopes: string[];
  endpointChange: EndpointChange | null;
}

export function extensionMarks(diff: ManifestDiff | null, extensionId: string): ExtensionMarks {
  const scopes = (list: ScopeChange[] | null | undefined) =>
    (list ?? []).filter((s) => s.extension_id === extensionId).map((s) => s.scope);
  return {
    added: (diff?.added_extensions ?? []).includes(extensionId),
    removed: (diff?.removed_extensions ?? []).includes(extensionId),
    addedScopes: scopes(diff?.added_scopes),
    removedScopes: scopes(diff?.removed_scopes),
    endpointChange: (diff?.changed_endpoints ?? []).find((c) => c.extension_id === extensionId) ?? null,
  };
}

function appPath(id: number | string, suffix = ''): string {
  return `/api/v1/apps/${encodeURIComponent(String(id))}${suffix}`;
}

class AppsListState {
  status = $state<LoadStatus>('idle');
  items = $state<AppRecord[]>([]);
  errorMessage = $state<string | null>(null);
  dataKeyMissing = $state(false);

  async load(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    const res = await apiGet<{ items: AppRecord[]; next: string | null }>('/api/v1/apps');
    if (!res.ok || res.data === null) {
      this.dataKeyMissing = isDataKeyMissing(res);
      this.errorMessage = res.errorMessage ?? t('apps.error.loadFailed');
      this.status = 'error';
      return;
    }
    this.dataKeyMissing = false;
    this.items = res.data.items ?? [];
    this.status = 'ready';
  }

  /**
   * Register an app by origin. On success the new app, its secret and
   * the manifest warnings are handed to the detail store for display.
   */
  async create(origin: string): Promise<OpResult & { app?: AppRecord }> {
    const res = await apiPost<AppRecord>('/api/v1/apps', { origin });
    if (!res.ok || res.data === null) {
      if (isDataKeyMissing(res)) this.dataKeyMissing = true;
      return {
        ok: false,
        errorMessage: res.errorMessage ?? t('apps.error.createFailed'),
        errors: problemErrors(res),
      };
    }
    appDetail.adopt(res.data);
    void this.load();
    return { ok: true, errorMessage: null, app: res.data };
  }
}

class AppDetailState {
  status = $state<LoadStatus>('idle');
  app = $state<AppRecord | null>(null);
  errorMessage = $state<string | null>(null);
  dataKeyMissing = $state(false);
  /** Signing secret of the last create or rotate, until dismissed. */
  secret = $state<string | null>(null);
  /** Manifest warnings of the last create or refresh for this app. */
  warnings = $state<string[]>([]);
  /** One-line outcome of the last action, e.g. a stale-digest reload. */
  notice = $state<string | null>(null);

  #id: string | null = null;

  /**
   * Take a create response as the current app, keeping its secret and
   * warnings for the review that follows.
   */
  adopt(app: AppRecord): void {
    this.#id = String(app.id);
    this.app = app;
    this.secret = app.secret ?? null;
    this.warnings = app.warnings ?? [];
    this.notice = null;
    this.errorMessage = null;
    this.status = 'ready';
  }

  async load(id: string): Promise<void> {
    if (this.#id !== id) {
      this.secret = null;
      this.warnings = [];
      this.notice = null;
      this.app = null;
    }
    this.#id = id;
    this.status = this.app ? 'ready' : 'loading';
    this.errorMessage = null;
    const res = await apiGet<AppRecord>(appPath(id));
    if (this.#id !== id) return;
    if (!res.ok || res.data === null) {
      this.dataKeyMissing = isDataKeyMissing(res);
      this.errorMessage = res.errorMessage ?? t('apps.error.loadFailed');
      this.status = 'error';
      return;
    }
    this.dataKeyMissing = false;
    this.app = res.data;
    this.status = 'ready';
  }

  dismissSecret(): void {
    this.secret = null;
  }

  async #mutate(
    res: Promise<ApiResponse<AppRecord>>,
    fallback: string,
    after?: (app: AppRecord) => void,
  ): Promise<OpResult> {
    const r = await res;
    if (!r.ok || r.data === null) {
      if (isDataKeyMissing(r)) this.dataKeyMissing = true;
      return { ok: false, errorMessage: r.errorMessage ?? t(fallback), errors: problemErrors(r) };
    }
    this.app = r.data;
    after?.(r.data);
    return { ok: true, errorMessage: null };
  }

  /**
   * Approve the reviewed manifest by its digest (REQ-APPS-32). A 409
   * means the manifest changed after the page loaded: the app is
   * reloaded and a notice asks for a new review.
   */
  async approve(digest: string): Promise<OpResult> {
    const app = this.app;
    if (!app) return { ok: false, errorMessage: null };
    this.notice = null;
    const r = await apiPost<AppRecord>(appPath(app.id, '/approve'), { manifest_digest: digest });
    if (r.status === 409) {
      await this.load(String(app.id));
      this.notice = t('apps.review.manifestChanged');
      return { ok: false, errorMessage: null };
    }
    return this.#mutate(Promise.resolve(r), 'apps.error.approveFailed', () => {
      this.warnings = [];
      this.notice = t('apps.review.approved');
    });
  }

  async refresh(): Promise<OpResult> {
    const app = this.app;
    if (!app) return { ok: false, errorMessage: null };
    this.notice = null;
    return this.#mutate(apiPost<AppRecord>(appPath(app.id, '/refresh')), 'apps.error.refreshFailed', (a) => {
      this.warnings = a.warnings ?? [];
      this.notice = a.pending_manifest ? t('apps.refresh.pending') : t('apps.refresh.unchanged');
    });
  }

  async rotateSecret(): Promise<OpResult> {
    const app = this.app;
    if (!app) return { ok: false, errorMessage: null };
    return this.#mutate(apiPost<AppRecord>(appPath(app.id, '/rotate-secret')), 'apps.error.rotateFailed', (a) => {
      this.secret = a.secret ?? null;
    });
  }

  async setEnabled(enabled: boolean): Promise<OpResult> {
    const app = this.app;
    if (!app) return { ok: false, errorMessage: null };
    return this.#mutate(
      apiPost<AppRecord>(appPath(app.id, enabled ? '/enable' : '/disable')),
      enabled ? 'apps.error.enableFailed' : 'apps.error.disableFailed',
    );
  }

  /** Update access (REQ-APPS-34); `principals` holds ids or emails. */
  async setAccess(change: { all_principals?: boolean; principals?: Array<number | string> }): Promise<OpResult> {
    const app = this.app;
    if (!app) return { ok: false, errorMessage: null };
    return this.#mutate(apiPatch<AppRecord>(appPath(app.id), change), 'apps.error.accessFailed');
  }

  async remove(): Promise<OpResult> {
    const app = this.app;
    if (!app) return { ok: false, errorMessage: null };
    const r = await apiDelete<unknown>(appPath(app.id));
    if (!r.ok) {
      return { ok: false, errorMessage: r.errorMessage ?? t('apps.error.removeFailed') };
    }
    this.app = null;
    this.#id = null;
    this.status = 'idle';
    return { ok: true, errorMessage: null };
  }
}

export const apps = new AppsListState();
export const appDetail = new AppDetailState();
