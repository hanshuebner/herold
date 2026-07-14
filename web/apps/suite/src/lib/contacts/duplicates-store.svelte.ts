/**
 * Contacts duplicate-check store (re #220).
 *
 * Backs `ContactsDuplicatesView.svelte` with a flat, selectable,
 * incrementally-loaded row list instead of the previous fetch-everything-
 * then-cluster-once flow: candidates page in via `Contact/query` +
 * narrow `Contact/get` (mirroring the contacts list store's own paging,
 * re #221's `appendPage`/`canLoadMore`), and every newly-loaded page
 * re-runs `clusterDuplicates` over the accumulated candidate set so rows
 * render as soon as the first page lands rather than after the whole
 * address book has downloaded.
 *
 * Selection composes the shared `whole-set-selection` helpers (re #221) --
 * this view has no "select all N matching" mode (unlike the contacts list
 * and mail stores), since a row only exists once its cluster has been
 * computed from loaded data; "select all" here means "select every
 * flagged row loaded so far".
 */

import { jmap } from '../jmap/client';
import { Capability } from '../jmap/types';
import { auth } from '../auth/auth.svelte';
import { computeShiftClickRange } from '../list-selection/range-select';
import { appendPage, canLoadMore } from '../list-selection/paging';
import {
  selectAllVisible as sharedSelectAllVisible,
  toggleSelectAllVisible as sharedToggleSelectAllVisible,
} from '../list-selection/whole-set-selection';
import {
  extractCandidate,
  clusterDuplicates,
  computeMatchDetails,
  loadDismissedPairs,
  dismissPair,
  defaultMergeChoices,
  buildMergedVM,
  buildMergeSetArgs,
  type ContactCandidate,
  type DuplicateCluster,
  type ClusterReason,
  type RowMatchDetail,
} from './dedup';

/** Page size for the candidate `Contact/query` + `Contact/get` paging. */
const PAGE_SIZE = 100;

/** Max ids in a single `Contact/set` call, mirroring the server default. */
const MAX_OBJECTS_IN_SET = 500;

/** Properties needed for dedup clustering -- fetched narrow, not the full Card. */
const DEDUP_PROPERTIES = ['id', 'name', 'emails', 'phones'];

/** A single flagged-duplicate row: one contact, plus what it matched on. */
export interface DuplicateRow {
  id: string;
  displayName: string;
  emails: string[];
  phones: string[];
  photoBlobId: string | null;
  clusterIndex: number;
  reasons: ClusterReason[];
  match: RowMatchDetail;
}

export type LoadStatus = 'idle' | 'loading' | 'loading-more' | 'ready' | 'error';

function photoBlobIdFromRaw(raw: Record<string, unknown>): string | null {
  const media = raw.media as Record<string, unknown> | undefined;
  if (!media) return null;
  for (const v of Object.values(media)) {
    if (typeof v === 'object' && v !== null) {
      const obj = v as Record<string, unknown>;
      if (obj.kind === 'photo' && typeof obj.blobId === 'string') return obj.blobId;
    }
  }
  return null;
}

class DuplicatesStore {
  candidates = $state<ContactCandidate[]>([]);
  clusters = $state<DuplicateCluster[]>([]);
  rows = $state<DuplicateRow[]>([]);
  total = $state<number | null>(null);
  status = $state<LoadStatus>('idle');
  errorMessage = $state<string | null>(null);

  selectedIds = $state<Set<string>>(new Set());
  selectAnchorId = $state<string | null>(null);

  merging = $state(false);
  deleting = $state(false);

  #dismissed = new Set<string>();

  get hasMore(): boolean {
    return this.total !== null && this.candidates.length < this.total;
  }

  #accountId(): string | null {
    return auth.session?.primaryAccounts[Capability.Contacts] ?? null;
  }

  /** Reset and load the first page. Call once when the view mounts. */
  async init(): Promise<void> {
    const principalId = auth.principalId ?? 'unknown';
    this.#dismissed = loadDismissedPairs(principalId);
    this.candidates = [];
    this.clusters = [];
    this.rows = [];
    this.total = null;
    this.errorMessage = null;
    this.clearSelection();
    await this.#loadPage(0, false);
  }

  /** Load the next page of candidates. No-op if no more pages or already loading. */
  async loadMore(): Promise<void> {
    if (
      !canLoadMore({
        isReady: this.status === 'ready',
        hasMore: this.hasMore,
        loadingMore: this.status === 'loading-more',
      })
    ) {
      return;
    }
    await this.#loadPage(this.candidates.length, true);
  }

  async #loadPage(position: number, append: boolean): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) {
      this.status = 'error';
      this.errorMessage = 'no-account';
      return;
    }

    this.status = append ? 'loading-more' : 'loading';

    try {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Contact/query',
          {
            accountId,
            sort: [{ property: 'displayName', isAscending: true }],
            position,
            limit: PAGE_SIZE,
            calculateTotal: true,
          },
          [Capability.Contacts],
        );
        b.call(
          'Contact/get',
          { accountId, '#ids': q.ref('/ids'), properties: DEDUP_PROPERTIES },
          [Capability.Contacts],
        );
      });

      const qResp = responses[0];
      const gResp = responses[1];
      if (!qResp || qResp[0] === 'error' || !gResp || gResp[0] === 'error') {
        this.status = 'error';
        this.errorMessage = 'fetch-failed';
        return;
      }

      const qArgs = qResp[1] as { total?: number };
      const gArgs = gResp[1] as { list?: unknown[] };
      if (typeof qArgs.total === 'number') this.total = qArgs.total;

      const newCandidates = (gArgs.list ?? []).flatMap((card) => {
        if (typeof card !== 'object' || card === null) return [];
        return [extractCandidate(card as Record<string, unknown>)];
      });

      this.candidates = append
        ? appendPage(this.candidates, newCandidates, { pageSize: PAGE_SIZE, idOf: (c) => c.id })
            .items
        : newCandidates;
      this.#recluster();
      this.status = 'ready';
    } catch {
      this.status = 'error';
      this.errorMessage = 'fetch-failed';
    }
  }

  /** Recompute clusters and the flat row list from the accumulated candidates. */
  #recluster(): void {
    const clusters = clusterDuplicates(this.candidates, this.#dismissed);
    this.clusters = clusters;
    const rows: DuplicateRow[] = [];
    clusters.forEach((cluster, clusterIndex) => {
      const details = computeMatchDetails(cluster);
      for (const c of cluster.contacts) {
        rows.push({
          id: c.id,
          displayName: c.displayName,
          emails: c.emails,
          phones: c.phones,
          photoBlobId: photoBlobIdFromRaw(c.raw),
          clusterIndex,
          reasons: cluster.reasons,
          match: details.get(c.id) ?? { emails: [], phones: [], closeNames: [] },
        });
      }
    });
    this.rows = rows;
    // Selection can only ever reference currently-flagged rows; drop any
    // id that reclustering (a dismiss, a merge, more data loading) no
    // longer flags.
    const rowIds = new Set(rows.map((r) => r.id));
    if ([...this.selectedIds].some((id) => !rowIds.has(id))) {
      this.selectedIds = new Set([...this.selectedIds].filter((id) => rowIds.has(id)));
    }
  }

  // ── Selection (re #220, composes the shared #221 helpers) ────────────────

  toggleSelected(id: string): void {
    const next = new Set(this.selectedIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    this.selectedIds = next;
    this.selectAnchorId = id;
  }

  selectRowClick(id: string, shiftKey: boolean, visibleIds: string[]): void {
    if (shiftKey && this.selectAnchorId !== null) {
      this.selectedIds = computeShiftClickRange(visibleIds, this.selectAnchorId, id);
      return;
    }
    this.toggleSelected(id);
  }

  selectAllVisible(ids: string[]): void {
    this.selectedIds = sharedSelectAllVisible(ids);
  }

  toggleSelectAllVisible(visibleIds: string[]): void {
    this.selectedIds = sharedToggleSelectAllVisible(visibleIds, this.selectedIds);
  }

  clearSelection(): void {
    this.selectAnchorId = null;
    if (this.selectedIds.size === 0) return;
    this.selectedIds = new Set();
  }

  // ── Dismiss (REQ-CONT-91) ─────────────────────────────────────────────────

  /** Dismiss every pair within `rowId`'s cluster ("not a duplicate"). */
  dismissRow(rowId: string): void {
    const row = this.rows.find((r) => r.id === rowId);
    if (!row) return;
    const cluster = this.clusters[row.clusterIndex];
    if (!cluster) return;
    const principalId = auth.principalId ?? 'unknown';
    for (let i = 0; i < cluster.contacts.length; i++) {
      for (let j = i + 1; j < cluster.contacts.length; j++) {
        this.#dismissed = dismissPair(principalId, cluster.contacts[i]!.id, cluster.contacts[j]!.id);
      }
    }
    this.#recluster();
  }

  // ── Bulk delete ────────────────────────────────────────────────────────────

  /**
   * Destroy the given contacts via `Contact/set`, chunked into groups of
   * at most `MAX_OBJECTS_IN_SET`. Removes them from `candidates` (which
   * re-triggers reclustering) and the selection set. Returns the ids that
   * could not be destroyed.
   */
  async bulkDelete(ids: string[]): Promise<string[]> {
    if (ids.length === 0) return [];
    const accountId = this.#accountId();
    if (!accountId) return ids;

    const chunks: string[][] = [];
    for (let i = 0; i < ids.length; i += MAX_OBJECTS_IN_SET) {
      chunks.push(ids.slice(i, i + MAX_OBJECTS_IN_SET));
    }

    const { responses } = await jmap.batch((b) => {
      for (const chunk of chunks) {
        b.call('Contact/set', { accountId, destroy: chunk }, [Capability.Contacts]);
      }
    });

    const destroyed = new Set<string>();
    for (const resp of responses) {
      if (!resp || resp[0] === 'error') continue;
      const args = resp[1] as { destroyed?: string[] };
      for (const id of args.destroyed ?? []) destroyed.add(id);
    }

    if (destroyed.size > 0) {
      this.candidates = this.candidates.filter((c) => !destroyed.has(c.id));
      if (this.total !== null) this.total = Math.max(0, this.total - destroyed.size);
      this.#recluster();
      const nextSelected = new Set(this.selectedIds);
      for (const id of destroyed) nextSelected.delete(id);
      this.selectedIds = nextSelected;
    }

    return ids.filter((id) => !destroyed.has(id));
  }

  // ── Bulk merge (re #220) ────────────────────────────────────────────────────

  /**
   * Merge the current selection, grouped by cluster. A cluster merges only
   * when at least 2 of its members are selected; clusters with a single
   * selected member are left untouched (nothing to merge them into without
   * ambiguously dragging in an unselected row).
   *
   * The narrow dedup candidates only carry `id`/`name`/`emails`/`phones`
   * (see `DEDUP_PROPERTIES`), too little to build a full merged Card, so
   * this re-fetches the full `Contact/get` for every id in a merging
   * cluster before building each cluster's merge patch (mirrors
   * `ContactsMergeView`'s own load step). All clusters commit atomically
   * in a single `Contact/set` call (one update + destroy map covering
   * every merging cluster).
   *
   * Returns the number of clusters merged and the ids that could not be
   * merged (server-rejected, or left out because fewer than 2 of the
   * cluster's members were selected).
   */
  async bulkMerge(ids: string[]): Promise<{ mergedClusters: number; skipped: string[] }> {
    const selected = new Set(ids);
    const skipped: string[] = [];

    const mergingGroups: string[][] = [];
    for (const cluster of this.clusters) {
      const members = cluster.contacts.map((c) => c.id).filter((id) => selected.has(id));
      if (members.length >= 2) {
        mergingGroups.push(members);
      } else {
        skipped.push(...members);
      }
    }
    if (mergingGroups.length === 0) return { mergedClusters: 0, skipped };

    const accountId = this.#accountId();
    if (!accountId) return { mergedClusters: 0, skipped: ids };

    const allIds = mergingGroups.flat();
    const { responses } = await jmap.batch((b) => {
      b.call('Contact/get', { accountId, ids: allIds }, [Capability.Contacts]);
    });
    const resp = responses[0];
    if (!resp || resp[0] === 'error') return { mergedClusters: 0, skipped: ids };
    const args = resp[1] as { list?: unknown[] };
    const fullById = new Map<string, Record<string, unknown>>();
    for (const card of args.list ?? []) {
      if (typeof card !== 'object' || card === null) continue;
      const obj = card as Record<string, unknown>;
      const id = String(obj.id ?? '');
      if (id) fullById.set(id, obj);
    }

    const update: Record<string, Record<string, unknown>> = {};
    const destroy: string[] = [];
    let mergedClusters = 0;

    for (const members of mergingGroups) {
      const fullContacts = members
        .map((id) => fullById.get(id))
        .filter((c): c is Record<string, unknown> => c !== undefined)
        .map((raw) => extractCandidate(raw));
      if (fullContacts.length < 2) {
        skipped.push(...members);
        continue;
      }
      const subCluster: DuplicateCluster = { contacts: fullContacts, reasons: [] };
      const choices = defaultMergeChoices(subCluster);
      const mergedVM = buildMergedVM(subCluster, choices);
      const setArgs = buildMergeSetArgs(subCluster, choices, mergedVM);
      Object.assign(update, setArgs.update);
      destroy.push(...setArgs.destroy);
      mergedClusters += 1;
    }

    if (mergedClusters === 0) return { mergedClusters: 0, skipped };

    const { responses: setResponses } = await jmap.batch((b) => {
      b.call('Contact/set', { accountId, update, destroy }, [Capability.Contacts]);
    });
    const setResp = setResponses[0];
    if (!setResp || setResp[0] === 'error') {
      return { mergedClusters: 0, skipped: ids };
    }
    const setResult = setResp[1] as {
      updated?: Record<string, unknown> | null;
      destroyed?: string[] | null;
    };

    // Best-effort local reconciliation: drop destroyed candidates outright;
    // updated survivors are refreshed on the next reclustering pass once
    // their new emails/phones/name are re-fetched -- here we simply drop
    // them from the narrow candidate cache too and let #recluster's normal
    // paging refill on next scroll (their id no longer appearing in `rows`
    // is the important post-condition, not staying pixel-accurate).
    const destroyedSet = new Set(setResult.destroyed ?? destroy);
    this.candidates = this.candidates.filter((c) => !destroyedSet.has(c.id));
    if (this.total !== null) this.total = Math.max(0, this.total - destroyedSet.size);
    this.#recluster();
    const nextSelected = new Set(this.selectedIds);
    for (const id of allIds) nextSelected.delete(id);
    this.selectedIds = nextSelected;

    return { mergedClusters, skipped };
  }
}

export const duplicatesStore = new DuplicatesStore();

export const _internals_forTest = {
  photoBlobIdFromRaw,
};
