/**
 * Tests for the window error listener in capture.ts.
 *
 * The handler is registered via addEventListener, which passes a single
 * ErrorEvent (not the five positional args of the window.onerror property).
 * These tests assert that the emitted CapturedEvent carries the real message
 * and stack -- never the useless "[object ErrorEvent]" or "[object Event]"
 * string that a naive String(event) conversion produces.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { installCapture } from '../capture.js';
import type { CapturedEvent } from '../schema.js';
import { _resetForTest as _resetBreadcrumbs } from '../breadcrumbs.js';

function makeCfg(emit: (event: CapturedEvent) => void) {
  let seq = 0;
  return {
    telemetryEnabled: () => true,
    isLivetailActive: () => false,
    getSeq: () => seq++,
    emit,
  };
}

describe('window error listener', () => {
  let removeHandlers: () => void;
  let emitted: CapturedEvent[];

  beforeEach(() => {
    emitted = [];
    _resetBreadcrumbs();
    ({ removeHandlers } = installCapture(makeCfg((ev) => emitted.push(ev))));
  });

  afterEach(() => {
    removeHandlers();
  });

  it('extracts the real error message and stack from ErrorEvent.error', () => {
    const err = new Error('boom');
    window.dispatchEvent(
      new ErrorEvent('error', { error: err, message: 'boom', bubbles: true }),
    );
    expect(emitted).toHaveLength(1);
    const ev = emitted[0]!;
    expect(ev.kind).toBe('error');
    expect(ev.level).toBe('error');
    expect(ev.msg).toBe('boom');
    // Stack must be present and reference the real Error
    expect(ev.stack).toBeDefined();
    expect(ev.stack).toContain('Error: boom');
    // Must not be the stringified event object
    expect(ev.msg).not.toBe('[object ErrorEvent]');
    expect(ev.msg).not.toContain('[object');
  });

  it('uses ErrorEvent.message as fallback when event.error is absent', () => {
    // Cross-origin scripts and some browser error reports deliver a message
    // string but no Error object (event.error is null).
    window.dispatchEvent(
      new ErrorEvent('error', { message: 'Script error.', bubbles: true }),
    );
    expect(emitted).toHaveLength(1);
    const ev = emitted[0]!;
    expect(ev.kind).toBe('error');
    expect(ev.msg).toBe('Script error.');
    expect(ev.stack).toBeUndefined();
    expect(ev.msg).not.toContain('[object');
  });

  it('never emits "[object ErrorEvent]" for real thrown errors', () => {
    const err = new Error('verify-real-message');
    window.dispatchEvent(
      new ErrorEvent('error', { error: err, message: 'verify-real-message', bubbles: true }),
    );
    expect(emitted).toHaveLength(1);
    expect(emitted[0]!.msg).toBe('verify-real-message');
    expect(emitted[0]!.msg).not.toBe('[object ErrorEvent]');
    expect(emitted[0]!.msg).not.toBe('[object Event]');
  });

  it('does not emit "[object Event]" for a resource-style plain Event on window', () => {
    // A plain Event (not ErrorEvent) dispatched directly on window simulates
    // the defensive case. The handler must not stringify the event object.
    window.dispatchEvent(new Event('error'));
    // Either nothing is emitted (silent skip when target is window, not an
    // element) or the emitted msg is a bounded string, never "[object Event]".
    for (const ev of emitted) {
      expect(ev.msg).not.toBe('[object Event]');
      expect(ev.msg).not.toContain('[object');
    }
  });

  it('emits a bounded description for a resource load failure on an element', () => {
    // Simulate a resource error where the event target is an img element.
    // We dispatch a plain Event on window and override its target to an element,
    // matching the shape the handler sees when a resource error reaches window.
    const img = document.createElement('img');
    const evt = new Event('error');
    Object.defineProperty(evt, 'target', { value: img, configurable: true });
    window.dispatchEvent(evt);
    expect(emitted).toHaveLength(1);
    const ev = emitted[0]!;
    expect(ev.kind).toBe('error');
    expect(ev.msg).toMatch(/^resource load error: img/);
    expect(ev.msg).not.toContain('[object');
    expect(ev.msg.length).toBeLessThanOrEqual(4096);
  });

  it('slices an oversized ErrorEvent.message to 4096 characters', () => {
    const longMsg = 'x'.repeat(5000);
    window.dispatchEvent(
      new ErrorEvent('error', { message: longMsg, bubbles: true }),
    );
    expect(emitted).toHaveLength(1);
    expect(emitted[0]!.msg.length).toBe(4096);
  });

  it('attaches a breadcrumb snapshot on each error event', () => {
    window.dispatchEvent(
      new ErrorEvent('error', { error: new Error('with-crumbs'), message: 'with-crumbs' }),
    );
    expect(emitted).toHaveLength(1);
    // breadcrumbs_snapshot is set for error events (may be empty array)
    expect(emitted[0]!.breadcrumbs_snapshot).toBeDefined();
  });
});
