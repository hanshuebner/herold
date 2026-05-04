/**
 * Tests for the label-dialog store. Covers open/decide round-trips,
 * cancellation when a new open supplants a pending one, and the
 * preservation of optional request fields.
 *
 * Also acts as the specification for the dirty-gate behaviour introduced
 * in re #78: the LabelDialog component disables its CTA in edit mode
 * (defaultName provided) until the form is dirty; these tests verify the
 * store contract that enables that — the pending state carries both
 * defaultName and defaultColor so the component can compare.
 */
import { describe, it, expect, beforeEach } from 'vitest';
import { labelDialog } from './label-dialog.svelte';

beforeEach(() => {
  if (labelDialog.pending) {
    labelDialog.decide(null);
  }
});

describe('labelDialog.open', () => {
  it('resolves with name and color when decide is called with a result', async () => {
    const promise = labelDialog.open({ title: 'New label' });
    expect(labelDialog.pending?.title).toBe('New label');
    labelDialog.decide({ name: 'Work', color: '#ff0000' });
    await expect(promise).resolves.toEqual({ name: 'Work', color: '#ff0000' });
    expect(labelDialog.pending).toBeNull();
  });

  it('resolves null when decide(null) is called (cancel)', async () => {
    const promise = labelDialog.open({ title: 'New label' });
    labelDialog.decide(null);
    await expect(promise).resolves.toBeNull();
    expect(labelDialog.pending).toBeNull();
  });

  it('cancels the prior pending request when a new open comes in', async () => {
    const first = labelDialog.open({ title: 'first' });
    const second = labelDialog.open({ title: 'second' });
    await expect(first).resolves.toBeNull();
    expect(labelDialog.pending?.title).toBe('second');
    labelDialog.decide({ name: 'Tag', color: '#00ff00' });
    await expect(second).resolves.toEqual({ name: 'Tag', color: '#00ff00' });
  });

  it('decide is a no-op when nothing is pending', () => {
    labelDialog.decide({ name: 'x', color: '#000000' });
    expect(labelDialog.pending).toBeNull();
  });

  it('preserves optional fields on the pending state', () => {
    void labelDialog.open({
      title: 'Edit label',
      defaultName: 'Work',
      defaultColor: '#aabbcc',
      confirmLabel: 'Change',
      cancelLabel: 'Cancel',
    });
    expect(labelDialog.pending?.defaultName).toBe('Work');
    expect(labelDialog.pending?.defaultColor).toBe('#aabbcc');
    expect(labelDialog.pending?.confirmLabel).toBe('Change');
    expect(labelDialog.pending?.cancelLabel).toBe('Cancel');
  });

  it('exposes defaultName and defaultColor for dirty comparison in edit mode (re #78)', () => {
    // The dialog component compares current field values against these
    // snapshot values to decide whether the CTA should be enabled.
    void labelDialog.open({
      title: 'Edit label',
      defaultName: 'My Label',
      defaultColor: '#123456',
      confirmLabel: 'Change',
    });
    expect(labelDialog.pending?.defaultName).toBe('My Label');
    expect(labelDialog.pending?.defaultColor).toBe('#123456');
  });
});
