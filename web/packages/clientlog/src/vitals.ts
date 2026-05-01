/**
 * Web Vitals adapter (REQ-CLOG-08, REQ-OPS-202).
 *
 * Calls into the web-vitals library for LCP, INP, CLS, FCP, TTFB.
 * One report per metric per page load. web-vitals handles the timing of
 * when each metric's final value is settled.
 *
 * The library is bundled inline; no CDN dependency.
 */

import { onLCP, onINP, onCLS, onFCP, onTTFB } from 'web-vitals';
import type { CapturedEvent, VitalPayload } from './schema.js';

export type EmitFn = (event: CapturedEvent) => void;

export function installVitals(getSeq: () => number, emit: EmitFn): void {
  function report(name: VitalPayload['name'], value: number, id: string): void {
    try {
      const ev: CapturedEvent = {
        kind: 'vital',
        level: 'info',
        msg: `web vital: ${name}`,
        client_ts: new Date().toISOString(),
        seq: getSeq(),
        vital: { name, value, id },
      };
      emit(ev);
    } catch { /* never throw */ }
  }

  onLCP((m) => { report('LCP', m.value, m.id); });
  onINP((m) => { report('INP', m.value, m.id); });
  onCLS((m) => { report('CLS', m.value, m.id); });
  onFCP((m) => { report('FCP', m.value, m.id); });
  onTTFB((m) => { report('TTFB', m.value, m.id); });
}
