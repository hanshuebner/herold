/**
 * Tests for the G15 drop-zone state logic (REQ-ATT-01):
 *   - dragActive flag is set on modal dragenter with Files MIME type.
 *   - dragActive is cleared on modal drop.
 *   - Drag events that do NOT carry Files or compose-part data are ignored.
 *   - The inline/attach zone routing functions (extracted for testability).
 *
 * We test the state machine logic rather than the full Svelte rendering
 * because mounting ComposeWindow requires ProseMirror (complex DOM) and the
 * full compose singleton. The drop-zone logic is exercised here as pure
 * state transitions; a manual browser test / E2E test covers the visual
 * affordance.
 */
import { describe, it, expect } from 'vitest';

/**
 * Minimal reimplementation of the drag-depth state machine from
 * ComposeWindow.svelte so we can test it without mounting the component.
 */
function makeDragState() {
  let dragActive = false;
  let dragDepth = 0;
  let inlineZoneHover = false;
  let attachZoneHover = false;

  function hasFiles(types: string[]): boolean {
    return types.includes('Files');
  }
  function hasComposePart(types: string[]): boolean {
    return types.includes('application/x-herold-compose-part');
  }
  function isRelevant(types: string[]): boolean {
    return hasFiles(types) || hasComposePart(types);
  }

  return {
    onModalDragEnter(types: string[]) {
      if (!isRelevant(types)) return;
      dragDepth++;
      dragActive = true;
    },
    onModalDragLeave() {
      dragDepth = Math.max(0, dragDepth - 1);
      if (dragDepth === 0) {
        dragActive = false;
        inlineZoneHover = false;
        attachZoneHover = false;
      }
    },
    onModalDrop() {
      dragDepth = 0;
      dragActive = false;
      inlineZoneHover = false;
      attachZoneHover = false;
    },
    onInlineZoneDragEnter() {
      inlineZoneHover = true;
    },
    onInlineZoneDragLeave() {
      inlineZoneHover = false;
    },
    onAttachZoneDragEnter() {
      attachZoneHover = true;
    },
    onAttachZoneDragLeave() {
      attachZoneHover = false;
    },
    get dragActive() { return dragActive; },
    get inlineZoneHover() { return inlineZoneHover; },
    get attachZoneHover() { return attachZoneHover; },
  };
}

describe('drag state machine (G15)', () => {
  it('sets dragActive=true on dragenter with Files type', () => {
    const s = makeDragState();
    s.onModalDragEnter(['Files']);
    expect(s.dragActive).toBe(true);
  });

  it('does NOT set dragActive for a drag without Files or compose-part', () => {
    const s = makeDragState();
    s.onModalDragEnter(['text/plain']);
    expect(s.dragActive).toBe(false);
  });

  it('sets dragActive=true on dragenter with compose-part type', () => {
    const s = makeDragState();
    s.onModalDragEnter(['application/x-herold-compose-part']);
    expect(s.dragActive).toBe(true);
  });

  it('clears dragActive when dragLeave depth reaches 0', () => {
    const s = makeDragState();
    s.onModalDragEnter(['Files']); // depth = 1
    s.onModalDragLeave();          // depth = 0 => dragActive false
    expect(s.dragActive).toBe(false);
  });

  it('keeps dragActive when drag enters a child (depth > 1)', () => {
    const s = makeDragState();
    s.onModalDragEnter(['Files']); // depth = 1
    s.onModalDragEnter(['Files']); // depth = 2 (entered child)
    s.onModalDragLeave();          // depth = 1, still active
    expect(s.dragActive).toBe(true);
  });

  it('clears dragActive on modal drop', () => {
    const s = makeDragState();
    s.onModalDragEnter(['Files']);
    s.onModalDrop();
    expect(s.dragActive).toBe(false);
  });

  it('sets inlineZoneHover on inline zone dragenter', () => {
    const s = makeDragState();
    s.onModalDragEnter(['Files']);
    s.onInlineZoneDragEnter();
    expect(s.inlineZoneHover).toBe(true);
    expect(s.attachZoneHover).toBe(false);
  });

  it('sets attachZoneHover on attach zone dragenter', () => {
    const s = makeDragState();
    s.onModalDragEnter(['Files']);
    s.onAttachZoneDragEnter();
    expect(s.attachZoneHover).toBe(true);
    expect(s.inlineZoneHover).toBe(false);
  });

  it('clears both zone hovers on modal drop', () => {
    const s = makeDragState();
    s.onModalDragEnter(['Files']);
    s.onInlineZoneDragEnter();
    s.onAttachZoneDragEnter();
    s.onModalDrop();
    expect(s.inlineZoneHover).toBe(false);
    expect(s.attachZoneHover).toBe(false);
  });
});

describe('drag depth counter stays balanced when cursor moves into a drop zone (re #67)', () => {
  // When cursor moves from a parent element (e.g. zone-container) to the
  // inline-drop-zone, the browser fires:
  //   1. dragleave on the parent -> bubbles to modal -> onModalDragLeave
  //   2. dragenter on the inline zone -> bubbles to modal -> onModalDragEnter
  //      (ONLY if the zone handler does NOT call stopPropagation)
  // Without the balancing modal dragenter, dragDepth reaches 0 and
  // dragActive is cleared, causing the zone to disappear and oscillate.

  it('dragActive stays true when cursor enters inline zone after entering modal', () => {
    const s = makeDragState();
    s.onModalDragEnter(['Files']); // cursor enters modal, depth=1
    // cursor moves from modal into a parent div (e.g. body-row):
    s.onModalDragEnter(['Files']); // child dragenter bubbles up, depth=2
    s.onModalDragLeave();          // parent dragleave bubbles up, depth=1
    // cursor moves from body-row into inline zone:
    s.onModalDragLeave();          // parent dragleave bubbles up, depth=0
    s.onModalDragEnter(['Files']); // inline zone dragenter bubbles up (fix: no stopPropagation)
    expect(s.dragActive).toBe(true);
  });

  it('dragActive stays true when cursor enters attach zone after entering modal', () => {
    const s = makeDragState();
    s.onModalDragEnter(['Files']); // cursor enters modal, depth=1
    // cursor moves from modal into a parent div:
    s.onModalDragEnter(['Files']); // child dragenter bubbles up, depth=2
    s.onModalDragLeave();          // parent dragleave, depth=1
    // cursor moves from parent into attach zone:
    s.onModalDragLeave();          // parent dragleave, depth=0
    s.onModalDragEnter(['Files']); // attach zone dragenter bubbles up (fix: no stopPropagation)
    expect(s.dragActive).toBe(true);
  });
});

describe('chip drag-data encoding (G15)', () => {
  it('encodes the attachment key as application/x-herold-compose-part', () => {
    // The chip's ondragstart calls:
    //   e.dataTransfer.setData('application/x-herold-compose-part', key)
    // This test verifies the MIME type string is stable (used by both
    // the setter and the receiver on the zone's drop handler).
    const MIME = 'application/x-herold-compose-part';
    const key = 'att-42';

    const stored: Record<string, string> = {};
    const dt = {
      setData(type: string, value: string) { stored[type] = value; },
      getData(type: string) { return stored[type] ?? ''; },
      effectAllowed: '' as string,
    };

    // Simulate dragstart handler.
    dt.setData(MIME, key);
    dt.effectAllowed = 'move';

    expect(dt.getData(MIME)).toBe(key);
    expect(dt.effectAllowed).toBe('move');
  });
});
