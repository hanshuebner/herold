/**
 * TOTP step-up store for the Suite SPA (REQ-AS-20..25, re #79).
 *
 * Any REST client request that receives a 403 with step_up_required:true
 * calls stepUp.request() (registered via setOnStepUpRequired in client.ts).
 * The returned Promise resolves when the user grants elevation, or rejects
 * (with StepUpCancelledError) when the user cancels.
 *
 * The singleton pattern mirrors confirm.svelte.ts: only one step-up modal
 * can be visible at a time. A second concurrent request waits on the same
 * modal session — both callers resume when elevation is granted.
 *
 * The step-up endpoint itself (POST /api/v1/auth/step-up) is called via
 * a direct fetch rather than client.ts to avoid re-entrant interceptor
 * calls (the step-up endpoint requires only auth1, never authAdmin, so it
 * will never itself trigger a step_up_required 403).
 */

import { auth } from './auth.svelte';
import { readCsrfToken, setOnStepUpRequired } from '../api/client';

export interface StepUpState {
  /** Modal is currently visible. */
  visible: boolean;
  /**
   * True when the server says the principal has no TOTP enrolled.
   * The modal switches from the code-entry form to an enrollment prompt
   * (REQ-AS-25).
   */
  enrollRequired: boolean;
  /** Inline error message, or null when no error. */
  error: string | null;
  /** True while the POST /api/v1/auth/step-up request is in flight. */
  submitting: boolean;
  /** Set when the server returns 429 lockout; drives the countdown. */
  lockoutUntil: Date | null;
}

interface PendingElevation {
  resolve: () => void;
  reject: () => void;
}

class StepUpStore implements StepUpState {
  visible = $state(false);
  enrollRequired = $state(false);
  error = $state<string | null>(null);
  submitting = $state(false);
  lockoutUntil = $state<Date | null>(null);

  #pending: PendingElevation | null = null;

  /**
   * Show the step-up modal and return a Promise<void> that resolves when
   * elevation is granted, or rejects when the user cancels.
   *
   * If a modal is already visible (concurrent callers), the new caller
   * joins the same session: both resolve or reject together.
   */
  request(): Promise<void> {
    if (this.visible) {
      // Coalesce: return a promise that tracks the existing session.
      return new Promise<void>((resolve, reject) => {
        const prev = this.#pending;
        this.#pending = {
          resolve: () => {
            prev?.resolve();
            resolve();
          },
          reject: () => {
            prev?.reject();
            reject();
          },
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
   *
   * On 200: updates elevation in auth state, closes modal, resolves the
   * pending promise (REQ-AS-23).
   * On 401 (wrong code): shows inline error.
   * On 400 enroll_required: switches to enrollment prompt (REQ-AS-25).
   * On 429 (lockout): sets lockoutUntil for the countdown (REQ-AS-23).
   */
  async submitCode(code: string): Promise<void> {
    if (this.submitting) return;
    this.error = null;
    this.submitting = true;

    try {
      const csrf = readCsrfToken();
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
        this.#grant();
        return;
      }

      if (response.status === 400) {
        try {
          const body = (await response.json()) as { enroll_required?: boolean; message?: string };
          if (body.enroll_required) {
            this.enrollRequired = true;
            return;
          }
          this.error = body.message ?? 'Invalid request.';
        } catch {
          this.error = 'Invalid request.';
        }
        return;
      }

      if (response.status === 401) {
        this.error = 'Incorrect code — try again.';
        return;
      }

      if (response.status === 429) {
        // Parse Retry-After header (seconds) to compute lockout deadline.
        const retryAfter = response.headers.get('Retry-After');
        const secs = retryAfter ? parseInt(retryAfter, 10) : 60;
        this.lockoutUntil = new Date(Date.now() + (Number.isNaN(secs) ? 60 : secs) * 1000);
        this.error = null;
        return;
      }

      this.error = `Unexpected response: HTTP ${response.status}.`;
    } catch {
      this.error = 'Network error — please try again.';
    } finally {
      this.submitting = false;
    }
  }

  /** Cancel the step-up modal. Rejects the pending promise. */
  cancel(): void {
    const pending = this.#pending;
    this.#pending = null;
    this.visible = false;
    this.enrollRequired = false;
    this.error = null;
    this.lockoutUntil = null;
    pending?.reject();
  }

  /** Clear lockout state (called when the countdown reaches zero). */
  clearLockout(): void {
    this.lockoutUntil = null;
    this.error = null;
  }

  #grant(): void {
    const pending = this.#pending;
    this.#pending = null;
    this.visible = false;
    this.enrollRequired = false;
    this.error = null;
    this.lockoutUntil = null;
    pending?.resolve();
  }
}

export const stepUp = new StepUpStore();

// Register the step-up callback with the REST client. This runs once at
// module init. The client calls this callback whenever a request returns
// 403 step_up_required. The callback returns a Promise<void> that resolves
// when the user completes elevation and rejects when they cancel.
// Registered here (not in auth.svelte.ts) to avoid a circular import:
// step-up imports auth; auth imports client; client imports nothing from auth.
setOnStepUpRequired(() => stepUp.request());
