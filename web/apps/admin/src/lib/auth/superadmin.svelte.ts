/**
 * Client-side detection of the server:superadmin principal flag.
 *
 * GET /api/v1/auth/me and GET /api/v1/server/status report a `roles` array
 * that today only distinguishes "admin" from "end-user" -- the
 * server-side principalRoles() doc comment in
 * internal/protoadmin/session_auth.go notes that a "superadmin" role
 * string is a future schema addition. Until that lands, the only
 * wire-exposed signal for "is this admin principal also a
 * server:superadmin" is the `flags` array on GET /api/v1/principals/{id}
 * (requireSelfOrAdmin always permits a caller to fetch their own record,
 * so this self-lookup needs no elevated privilege beyond being signed in).
 *
 * Screens that must match a superadmin-only server gate (e.g. REQ-AC-66's
 * authz_trusted flip) call check() before rendering the gated control, so
 * a non-superadmin operator never sees a control the server would 403.
 * The result is cached for the session; call reset() on logout so a
 * future sign-in re-probes rather than reusing a stale answer.
 */
import { apiGet } from '../api/client';
import { auth } from './auth.svelte';

interface PrincipalFlagsResponse {
  flags: string[];
}

class SuperAdminState {
  isSuperAdmin = $state(false);

  /**
   * The principal id the cached isSuperAdmin answer applies to, or null
   * before the first probe. Compared against auth.principal.id on every
   * call so a sign-out/sign-in cycle within the same tab (the auth
   * singleton persists across SPA route changes) never serves a stale
   * answer for a different principal.
   */
  #checkedForPrincipalId: string | null = null;

  /** Probe once per signed-in principal; subsequent calls for the same
   * principal return the cached result. */
  async check(): Promise<boolean> {
    const id = auth.principal?.id ?? null;
    if (id === null) return false;
    if (this.#checkedForPrincipalId === id) return this.isSuperAdmin;
    const result = await apiGet<PrincipalFlagsResponse>(`/api/v1/principals/${id}`);
    if (result.ok && result.data) {
      this.isSuperAdmin = result.data.flags.includes('super_admin');
      this.#checkedForPrincipalId = id;
    }
    return this.isSuperAdmin;
  }
}

export const superAdminState = new SuperAdminState();
