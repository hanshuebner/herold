/**
 * Store for the OAuth connection test result modal (re #131, item 2).
 *
 * The modal shows the outcome of POST /api/v1/identities/{id}/submission/test
 * and, for OAuth identities, offers re-authorization via a popup flow when
 * the stored token cannot be refreshed silently.
 *
 * Usage:
 *   await connectionTest.show({ ok, detail, serverHost, selfEmail,
 *                               isOAuth, provider, identityId });
 *   // resolves when the user dismisses the modal
 */

export interface ConnectionTestRequest {
  /** True when the probe succeeded. */
  ok: boolean;
  /** Human-readable diagnostic from the SMTP server (empty on success). */
  detail: string;
  /** SMTP host that was tested, shown in the success message. */
  serverHost?: string;
  /** Identity's own address; shown in the success message. */
  selfEmail?: string;
  /**
   * True when the identity uses OAuth2 auth. When true and the probe
   * fails, the modal offers a "Neu autorisieren" button.
   */
  isOAuth: boolean;
  /** Provider id ('gmail', 'm365', etc.) for the re-auth popup. */
  provider: string | null;
  /** Identity id passed to startOAuthPopup on re-auth. */
  identityId: string;
}

interface PendingState extends ConnectionTestRequest {
  resolve: () => void;
}

class ConnectionTestStore {
  pending = $state<PendingState | null>(null);

  /**
   * Show the result modal. Resolves when the user closes the modal.
   * Any previously pending modal is dismissed first so at most one
   * dialog is visible at a time.
   */
  show(req: ConnectionTestRequest): Promise<void> {
    if (this.pending) {
      this.pending.resolve();
    }
    return new Promise<void>((resolve) => {
      this.pending = { ...req, resolve };
    });
  }

  /** Dismiss the modal programmatically. */
  close(): void {
    const cur = this.pending;
    if (!cur) return;
    this.pending = null;
    cur.resolve();
  }
}

export const connectionTest = new ConnectionTestStore();
