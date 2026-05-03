/**
 * Web Push subscription management — REQ-PUSH-30..34, REQ-PUSH-80..84.
 *
 * Manages:
 *   - Service worker registration (push-only SW; no caching per NG2).
 *   - PushManager.subscribe() using the VAPID key from the JMAP session
 *     capability descriptor (REQ-PUSH-33).
 *   - PushSubscription/set { create } to register with herold.
 *   - PushSubscription/set { destroy } to unsubscribe.
 *   - localStorage-backed "denied" memory to avoid re-prompting within 30 days
 *     (REQ-PUSH-31).
 *
 * The VAPID key is read from the session capability descriptor at
 * `capabilities["https://netzhansa.com/jmap/push"].applicationServerKey`.
 * If absent, `vapidKey` is null and subscribe() resolves with a no-op.
 */

import { jmap, strict } from '../jmap/client';
import { auth } from '../auth/auth.svelte';
import { Capability } from '../jmap/types';

const DENIED_KEY = 'herold:push:denied_until';
const SW_PATH = '/sw.js';
const DEVICE_CLIENT_ID_KEY = 'herold:push:device_client_id';

/** Permission state as reported by the browser or as inferred from stored state. */
export type PushPermissionState = 'default' | 'granted' | 'denied' | 'unavailable';

function getOrCreateDeviceClientId(): string {
  let id = localStorage.getItem(DEVICE_CLIENT_ID_KEY);
  if (!id) {
    id = `herold-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
    localStorage.setItem(DEVICE_CLIENT_ID_KEY, id);
  }
  return id;
}

class PushSubscriptionStore {
  /** Current browser Notification permission state. */
  permissionState = $state<PushPermissionState>('default');

  /** True when there is an active PushSubscription registered with herold. */
  subscribed = $state(false);

  /** True while a subscribe/unsubscribe operation is in flight. */
  busy = $state(false);

  /** Error from the last operation, if any. */
  errorMessage = $state<string | null>(null);

  /** The JMAP subscription id herold assigned, for subsequent destroy calls. */
  #jmapSubscriptionId = $state<string | null>(null);

  /** The browser PushSubscription object, kept alive for unsubscribe. */
  #browserSub = $state<PushSubscription | null>(null);

  constructor() {
    // Detect permission state from the browser.
    if (typeof Notification !== 'undefined') {
      this.permissionState = Notification.permission as PushPermissionState;
    } else {
      this.permissionState = 'unavailable';
    }
  }

  /**
   * True when the push capability is available and the VAPID key is present.
   */
  get available(): boolean {
    return (
      jmap.hasCapability(Capability.HeroldPush) &&
      Boolean(this.#vapidKey) &&
      typeof ServiceWorkerRegistration !== 'undefined'
    );
  }

  get #vapidKey(): string | null {
    const cap = auth.session?.capabilities[Capability.HeroldPush] as
      | { applicationServerKey?: string }
      | undefined;
    return cap?.applicationServerKey ?? null;
  }

  /**
   * Register the service worker if not already registered and subscribe to
   * Web Push. Updates the JMAP server via PushSubscription/set { create }.
   */
  async subscribe(): Promise<void> {
    if (this.busy) return;
    if (!('serviceWorker' in navigator) || !('PushManager' in window)) {
      this.errorMessage = 'Web Push is not supported in this browser.';
      return;
    }
    const vapidKey = this.#vapidKey;
    if (!vapidKey) {
      this.errorMessage = 'VAPID key not available — push is not configured on this server.';
      return;
    }

    this.busy = true;
    this.errorMessage = null;
    try {
      // 1. Request notification permission.
      const permission = await Notification.requestPermission();
      this.permissionState = permission as PushPermissionState;
      if (permission !== 'granted') {
        if (permission === 'denied') {
          // Store a 30-day cooldown per REQ-PUSH-31.
          const until = Date.now() + 30 * 24 * 60 * 60 * 1000;
          localStorage.setItem(DENIED_KEY, String(until));
        }
        return;
      }

      // 2. Ensure service worker is registered.
      const reg = await navigator.serviceWorker.register(SW_PATH, { scope: '/' });
      await navigator.serviceWorker.ready;

      // 3. Subscribe to push.
      let browserSub = await reg.pushManager.getSubscription();
      if (!browserSub) {
        browserSub = await reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(vapidKey).buffer as ArrayBuffer,
        });
      }
      this.#browserSub = browserSub;

      // 4. Register with herold via PushSubscription/set { create }.
      await this.#registerWithHerold(browserSub);
      this.subscribed = true;
    } catch (err) {
      this.errorMessage = err instanceof Error ? err.message : 'Subscribe failed';
    } finally {
      this.busy = false;
    }
  }

  /**
   * Unsubscribe from Web Push: destroys the JMAP subscription and the browser
   * push subscription.
   */
  async unsubscribe(): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    this.errorMessage = null;
    try {
      if (this.#jmapSubscriptionId) {
        await this.#destroyJmapSubscription(this.#jmapSubscriptionId);
        this.#jmapSubscriptionId = null;
      }
      if (this.#browserSub) {
        await this.#browserSub.unsubscribe();
        this.#browserSub = null;
      } else {
        // Try to find and unsubscribe the current browser sub.
        if ('serviceWorker' in navigator) {
          const reg = await navigator.serviceWorker.getRegistration('/');
          if (reg) {
            const sub = await reg.pushManager.getSubscription();
            if (sub) await sub.unsubscribe();
          }
        }
      }
      this.subscribed = false;
    } catch (err) {
      this.errorMessage = err instanceof Error ? err.message : 'Unsubscribe failed';
    } finally {
      this.busy = false;
    }
  }

  /**
   * Destroy ALL push subscriptions for this account on herold.
   * Per REQ-PUSH-94 / REQ-PUSH-84 "Forget all my notification subscriptions".
   */
  async destroyAll(): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    this.errorMessage = null;
    try {
      // Fetch all subscriptions, then destroy each.
      const { responses } = await jmap.batch((b) => {
        b.call('PushSubscription/get', {}, [Capability.Core]);
      });
      strict(responses);
      const args = responses[0]?.[1] as
        | { list: Array<{ id: string }> }
        | undefined;
      const ids = args?.list?.map((s) => s.id) ?? [];
      if (ids.length > 0) {
        const { responses: resp2 } = await jmap.batch((b) => {
          b.call('PushSubscription/set', { destroy: ids }, [Capability.Core]);
        });
        strict(resp2);
      }
      // Also unsubscribe the browser subscription.
      if ('serviceWorker' in navigator) {
        const reg = await navigator.serviceWorker.getRegistration('/');
        if (reg) {
          const sub = await reg.pushManager.getSubscription();
          if (sub) await sub.unsubscribe();
        }
      }
      this.#browserSub = null;
      this.#jmapSubscriptionId = null;
      this.subscribed = false;
    } catch (err) {
      this.errorMessage = err instanceof Error ? err.message : 'Failed to remove subscriptions';
    } finally {
      this.busy = false;
    }
  }

  /**
   * Clear the "denied" cooldown so the next notification opportunity
   * can prompt again (REQ-PUSH-84).
   */
  forgetDenial(): void {
    localStorage.removeItem(DENIED_KEY);
    this.permissionState = 'default';
    this.errorMessage = null;
  }

  async #registerWithHerold(sub: PushSubscription): Promise<void> {
    const json = sub.toJSON();
    const keys = json.keys;
    if (!keys?.p256dh || !keys.auth) {
      throw new Error('Browser PushSubscription is missing encryption keys.');
    }
    const vapidKey = this.#vapidKey ?? '';

    const { responses } = await jmap.batch((b) => {
      b.call(
        'PushSubscription/set',
        {
          create: {
            push0: {
              deviceClientId: getOrCreateDeviceClientId(),
              url: sub.endpoint,
              keys: { p256dh: keys.p256dh, auth: keys.auth },
              types: ['Email', 'Message', 'Conversation'],
              vapidKeyAtRegistration: vapidKey,
            },
          },
        },
        [Capability.Core],
      );
    });
    strict(responses);

    const result = responses[0]?.[1] as
      | {
          created?: Record<string, { id: string }>;
          notCreated?: Record<string, { type: string; description?: string }>;
        }
      | undefined;
    const notCreated = result?.notCreated?.['push0'];
    if (notCreated) {
      const desc = notCreated.description ?? `Push registration failed: ${notCreated.type}`;
      throw new Error(desc);
    }
    const created = result?.created?.['push0'];
    if (created?.id) {
      this.#jmapSubscriptionId = created.id;
    }
  }

  async #destroyJmapSubscription(id: string): Promise<void> {
    const { responses } = await jmap.batch((b) => {
      b.call('PushSubscription/set', { destroy: [id] }, [Capability.Core]);
    });
    strict(responses);
  }
}

export const pushSubscription = new PushSubscriptionStore();

/**
 * Convert a base64url-encoded VAPID key (as returned by herold in the session
 * descriptor) to the Uint8Array that pushManager.subscribe() requires.
 */
export function urlBase64ToUint8Array(base64String: string): Uint8Array {
  const padding = '='.repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/');
  const rawData = atob(base64);
  const outputArray = new Uint8Array(rawData.length);
  for (let i = 0; i < rawData.length; ++i) {
    outputArray[i] = rawData.charCodeAt(i);
  }
  return outputArray;
}

export const _internals_forTest = { urlBase64ToUint8Array };
