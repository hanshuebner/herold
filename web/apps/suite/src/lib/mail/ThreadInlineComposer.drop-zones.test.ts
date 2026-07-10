/**
 * Tests for the G15 drop-zone state logic in ThreadInlineComposer (re #165):
 *   - Container-level dragActive tracking using the depth-counter pattern.
 *   - dragover / drop on the container call preventDefault to suppress the
 *     browser's default open-file behaviour.
 *   - Inline zone only receives files when dragActive is true (models
 *     pointer-events: none → pointer-events: all CSS toggle).
 *   - Attach zone accepts any file; inline zone accepts only images.
 *   - Drag events without Files type are ignored.
 *
 * Models the same state machine as compose-drop-zones.test.ts (ComposeWindow)
 * so both composers stay in sync.
 */
import { describe, it, expect } from 'vitest';

/**
 * Minimal reimplementation of the drop-zone state machine added to
 * ThreadInlineComposer.svelte (re #165) so we can test it without
 * mounting the full component.
 */
function makeInlineComposerDragState() {
  let dragActive = false;
  let dragDepth = 0;
  let inlineZoneHover = false;
  let attachZoneHover = false;

  /** Files routed to each zone on drop. */
  const inlineDroppedFiles: File[] = [];
  const attachDroppedFiles: File[] = [];

  function hasFiles(types: string[]): boolean {
    return types.includes('Files');
  }
  function isRelevant(types: string[]): boolean {
    return hasFiles(types) || types.includes('application/x-herold-compose-part');
  }

  return {
    // Container handlers.
    onContainerDragEnter(types: string[]): boolean {
      if (!isRelevant(types)) return false;
      dragDepth++;
      dragActive = true;
      return true; // models preventDefault()
    },
    onContainerDragOver(types: string[]): boolean {
      if (!isRelevant(types)) return false;
      return true; // models preventDefault()
    },
    onContainerDragLeave() {
      dragDepth = Math.max(0, dragDepth - 1);
      if (dragDepth === 0) {
        dragActive = false;
        inlineZoneHover = false;
        attachZoneHover = false;
      }
    },
    onContainerDrop(): boolean {
      dragDepth = 0;
      dragActive = false;
      inlineZoneHover = false;
      attachZoneHover = false;
      return true; // models preventDefault()
    },

    // Inline zone handlers — only fire when dragActive (pointer-events: none at rest).
    onInlineZoneDragEnter(): boolean {
      if (!dragActive) return false;
      inlineZoneHover = true;
      return true;
    },
    onInlineZoneDragOver(): boolean {
      if (!dragActive) return false;
      return true;
    },
    onInlineZoneDragLeave(relatedTargetInsideZone: boolean) {
      if (!dragActive) return;
      if (!relatedTargetInsideZone) inlineZoneHover = false;
    },
    onInlineZoneDrop(files: File[]): boolean {
      if (!dragActive) return false;
      dragDepth = 0;
      dragActive = false;
      inlineZoneHover = false;
      attachZoneHover = false;
      const images = files.filter((f) => f.type.startsWith('image/'));
      inlineDroppedFiles.push(...images);
      return true; // models preventDefault()
    },

    // Attach zone handlers.
    onAttachZoneDragEnter(): boolean {
      if (!dragActive) return false;
      attachZoneHover = true;
      return true;
    },
    onAttachZoneDragOver(): boolean {
      if (!dragActive) return false;
      return true;
    },
    onAttachZoneDragLeave(relatedTargetInsideZone: boolean) {
      if (!dragActive) return;
      if (!relatedTargetInsideZone) attachZoneHover = false;
    },
    onAttachZoneDrop(files: File[]): boolean {
      if (!dragActive) return false;
      dragDepth = 0;
      dragActive = false;
      inlineZoneHover = false;
      attachZoneHover = false;
      attachDroppedFiles.push(...files);
      return true; // models preventDefault()
    },

    get dragActive() { return dragActive; },
    get inlineZoneHover() { return inlineZoneHover; },
    get attachZoneHover() { return attachZoneHover; },
    get inlineDroppedFiles() { return inlineDroppedFiles; },
    get attachDroppedFiles() { return attachDroppedFiles; },
  };
}

describe('ThreadInlineComposer drag state machine (re #165)', () => {
  it('sets dragActive=true on container dragenter with Files type', () => {
    const s = makeInlineComposerDragState();
    s.onContainerDragEnter(['Files']);
    expect(s.dragActive).toBe(true);
  });

  it('does NOT set dragActive for non-file drags', () => {
    const s = makeInlineComposerDragState();
    s.onContainerDragEnter(['text/plain']);
    expect(s.dragActive).toBe(false);
  });

  it('container dragover returns true (models preventDefault) for Files type', () => {
    const s = makeInlineComposerDragState();
    expect(s.onContainerDragOver(['Files'])).toBe(true);
  });

  it('container dragover returns false for irrelevant type', () => {
    const s = makeInlineComposerDragState();
    expect(s.onContainerDragOver(['text/plain'])).toBe(false);
  });

  it('container drop always calls preventDefault (clears state)', () => {
    const s = makeInlineComposerDragState();
    s.onContainerDragEnter(['Files']);
    const prevented = s.onContainerDrop();
    expect(prevented).toBe(true);
    expect(s.dragActive).toBe(false);
  });

  it('clears dragActive when depth counter reaches 0', () => {
    const s = makeInlineComposerDragState();
    s.onContainerDragEnter(['Files']); // depth=1
    s.onContainerDragLeave();          // depth=0
    expect(s.dragActive).toBe(false);
  });

  it('maintains dragActive while cursor is still inside (depth > 0)', () => {
    const s = makeInlineComposerDragState();
    s.onContainerDragEnter(['Files']); // depth=1
    s.onContainerDragEnter(['Files']); // depth=2 (entered child)
    s.onContainerDragLeave();          // depth=1 — still active
    expect(s.dragActive).toBe(true);
  });

  it('drag depth stays balanced when cursor crosses into inline zone (re #67)', () => {
    const s = makeInlineComposerDragState();
    s.onContainerDragEnter(['Files']); // depth=1
    s.onContainerDragEnter(['Files']); // child enters, depth=2
    s.onContainerDragLeave();          // parent leaves, depth=1
    // cursor moves from parent into inline zone:
    s.onContainerDragLeave();          // parent leaves, depth=0
    // dragenter from inline zone bubbles (no stopPropagation in zone enter):
    s.onContainerDragEnter(['Files']); // depth=1 — dragActive recovers
    expect(s.dragActive).toBe(true);
  });

  it('inline zone hover set on dragenter while dragActive', () => {
    const s = makeInlineComposerDragState();
    s.onContainerDragEnter(['Files']);
    s.onInlineZoneDragEnter();
    expect(s.inlineZoneHover).toBe(true);
    expect(s.attachZoneHover).toBe(false);
  });

  it('inline zone does not respond before dragActive is set', () => {
    const s = makeInlineComposerDragState();
    // No dragenter on container — zone stays inert.
    const entered = s.onInlineZoneDragEnter();
    expect(entered).toBe(false);
    expect(s.inlineZoneHover).toBe(false);
  });

  it('attach zone hover set on dragenter while dragActive', () => {
    const s = makeInlineComposerDragState();
    s.onContainerDragEnter(['Files']);
    s.onAttachZoneDragEnter();
    expect(s.attachZoneHover).toBe(true);
    expect(s.inlineZoneHover).toBe(false);
  });

  it('attach zone drop attaches any file and clears state', () => {
    const s = makeInlineComposerDragState();
    const pdf = new File(['x'], 'doc.pdf', { type: 'application/pdf' });
    s.onContainerDragEnter(['Files']);
    const prevented = s.onAttachZoneDrop([pdf]);
    expect(prevented).toBe(true);
    expect(s.attachDroppedFiles).toHaveLength(1);
    expect(s.attachDroppedFiles[0]?.name).toBe('doc.pdf');
    expect(s.dragActive).toBe(false);
  });

  it('inline zone drop only routes image files and ignores PDFs', () => {
    const s = makeInlineComposerDragState();
    const pdf = new File(['x'], 'doc.pdf', { type: 'application/pdf' });
    const png = new File(['x'], 'img.png', { type: 'image/png' });
    s.onContainerDragEnter(['Files']);
    s.onInlineZoneDrop([pdf, png]);
    expect(s.inlineDroppedFiles).toHaveLength(1);
    expect(s.inlineDroppedFiles[0]?.name).toBe('img.png');
    expect(s.attachDroppedFiles).toHaveLength(0);
  });

  it('inline zone drop clears all state', () => {
    const s = makeInlineComposerDragState();
    const png = new File(['x'], 'img.png', { type: 'image/png' });
    s.onContainerDragEnter(['Files']);
    s.onInlineZoneDragEnter();
    s.onInlineZoneDrop([png]);
    expect(s.dragActive).toBe(false);
    expect(s.inlineZoneHover).toBe(false);
    expect(s.attachZoneHover).toBe(false);
  });
});
