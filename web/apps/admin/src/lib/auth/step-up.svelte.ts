/**
 * TOTP step-up store for the admin SPA (REQ-AS-20..25, re #79).
 *
 * Two activation paths:
 *
 *  1. Bootstrap step-up (REQ-AS-21): auth.status transitions to
 *     'step_up_pending'. AuthGate renders AdminStepUpModal which calls
 *     auth.afterStepUp() on success; no promise is used.
 *
 *  2. API interceptor step-up (REQ-AS-20): admin client.ts intercepts a
 *     403 step_up_required on a post-bootstrap API call and invokes
 *     adminStepUp.request(). The returned Promise resolves when elevation
 *     is granted, or rejects when the user cancels.
 *
 * The store's visible state is true when either path is active. The
 * AdminStepUpModal consults auth.status to determine which path is in use
 * and calls the correct completion handler.
 *
 * The step-up endpoint (POST /api/v1/auth/step-up) is called via direct
 * fetch to avoid re-entrant interceptor calls (the endpoint only requires
 * auth1, never authAdmin, so it will not itself trigger step_up_required).
 */

import { auth } from './auth.svelte';
import { readAdminCsrfToken, setAdminOnStepUpRequired } from '../api/client';
import { t } from '../i18n/i18n.svelte';

interface PendingElevation {
  resolve: () => void;
  reject: () => void;
}

class AdminStepUpStore {
  /**
   * True when the API interceptor has triggered a step-up (path 2).
   * The bootstrap step-up (path 1) is driven by auth.status === 'step_up_pending'.
   */
  visible = $state(false);
  enrollRequired = $state(false);
  error = $state<string | null>(null);
  submitting = $state(false);
  lockoutUntil = $state<Date | null>(null);

  #pending: PendingElevation | null = null;

  /**
   * Show the step-up modal for path 2 (API interceptor).
   * Returns a Promise<void> that resolves when elevation is granted,
   * or rejects when the user cancels.
   *
   * If the modal is already visible (concurrent callers), the new caller
   * joins the same session: both resolve or reject together.
   */
  request(): Promise<void> {
    if (this.visible) {
      return new Promise<void>((resolve, reject) => {
        const prev = this.#pending;
        this.#pending = {
          resolve: () => { prev?.resolve(); resolve(); },
          reject: () => { prev?.reject(); reject(); },
        };
      });
    }

    this.visible = true;
    this.enrollRequired = false;
    this.error = null;
    this.submitting = false;
    this.lockoutUntil = null;

    return new Promise<void>((resolve, reject) => {
      this.#pending = { resolve, reject };
    });
  }

  /**
   * Submit a TOTP code to POST /api/v1/auth/step-up.
   * isBootstrap distinguishes path 1 (bootstrap) from path 2 (interceptor).
   *
   * On 200: updates auth elevation state.
   *   Path 1: calls auth.afterStepUp() (fetches server/status, transitions ready).
   *   Path 2: resolves the pending Promise so client.ts can retry.
   * On 401 (wrong code): shows inline error.
   * On 400 enroll_required: switches to enrollment prompt (REQ-AS-25).
   * On 429 (lockout): sets lockoutUntil for the countdown (REQ-AS-23).
   */
  async submitCode(code: string, isBootstrap: boolean): Promise<void> {
    if (this.submitting) return;
    this.error = null;
    this.submitting = true;

    try {
      const csrf = readAdminCsrfToken();
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
        Accept: 'application/json',
      };
      if (csrf) headers['X-CSRF-Token'] = csrf;

      const response = await fetch('/api/v1/auth/step-up', {
        method: 'POST',
        credentials: 'include',
        headers,
        body: JSON.stringify({ totp_code: code }),
      });

      if (response.status === 200) {
        const body = (await response.json()) as { elevation_expires_at: string };
        auth.updateElevation(body.elevation_expires_at ?? null);
        if (isBootstrap) {
          this.#closeModal(false);
          await auth.afterStepUp();
        } else {
          this.#grant();
        }
        return;
      }

      if (response.status === 400) {
        try {
          const body = (await response.json()) as { enroll_required?: boolean; message?: string };
          if (body.enroll_required) {
            this.enrollRequired = true;
            return;
          }
          this.error = body.message ?? t('stepup.error.invalidRequest');
        } catch {
          this.error = t('stepup.error.invalidRequest');
        }
        return;
      }

      if (response.status === 401) {
        this.error = t('stepup.error.wrongCode');
        return;
      }

      if (response.status === 429) {
        const retryAfter = response.headers.get('Retry-After');
        const secs = retryAfter ? parseInt(retryAfter, 10) : 60;
        this.lockoutUntil = new Date(Date.now() + (Number.isNaN(secs) ? 60 : secs) * 1000);
        this.error = null;
        return;
      }

      this.error = t('stepup.error.unexpectedResponse', { status: response.status });
    } catch {
      this.error = t('stepup.error.networkError');
    } finally {
      this.submitting = false;
    }
  }

  /** Cancel the step-up. On bootstrap path: transitions to unauthenticated. */
  cancel(isBootstrap: boolean): void {
    this.#closeModal(false);
    if (isBootstrap) {
      auth.handleUnauthorized();
    } else {
      const pending = this.#pending;
      this.#pending = null;
      pending?.reject();
    }
  }

  /** Clear lockout state (called when the countdown reaches zero). */
  clearLockout(): void {
    this.lockoutUntil = null;
    this.error = null;
  }

  #grant(): void {
    const pending = this.#pending;
    this.#pending = null;
    this.#closeModal(false);
    pending?.resolve();
  }

  #closeModal(_keepEnrollRequired: boolean): void {
    this.visible = false;
    this.enrollRequired = false;
    this.error = null;
    this.lockoutUntil = null;
  }
}

export const adminStepUp = new AdminStepUpStore();

// Register the step-up callback with the REST client. Runs once at module
// init. The client calls this callback when a post-bootstrap API request
// returns 403 step_up_required. The callback returns a Promise<void> that
// resolves when the user completes elevation and rejects when they cancel.
setAdminOnStepUpRequired(() => adminStepUp.request());
