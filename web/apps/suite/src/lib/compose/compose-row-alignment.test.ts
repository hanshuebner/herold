/**
 * Regression guard for issue #286: the From (`Von`) value, the An/Cc/Bcc
 * input's typed text and chips, and the message editor's text must all
 * start on one common left edge, in both the floating ComposeWindow and
 * the thread-reader inline composer.
 *
 * Three independent invariants make that true:
 *
 * 1. The *column* start (the label width + gap before the value area)
 *    must be identical across ComposeWindow's From/Subject/Body rows,
 *    RecipientField's internal label, and ThreadInlineComposer's own
 *    From row -- enforced by a single shared `--compose-label-width`
 *    token and matching row gaps (re #286, round 1).
 *
 * 2. Within that column, the *text* itself must start at the same inset
 *    from the column edge -- matching the message editor's own text
 *    inset (its `.rich-editor` border plus `.ProseMirror` padding).
 *    Plain text (`.from-display`, the recipient `<input>`) carries no
 *    border/padding of its own, so it gets that inset directly via
 *    `--compose-value-left`. Bordered/padded controls (the FromPicker
 *    trigger button, a recipient chip) already have their own smaller
 *    built-in inset, so they get a compensating adjustment instead of a
 *    flat inset, landing their text at the same edge (re #286, round 2).
 *
 * 3. In ThreadInlineComposer specifically, the message editor is NOT
 *    itself a field-row with a label -- it is a sibling block below
 *    `.composer-fields` -- so invariant 1 does not reach it. Its
 *    `.composer-body` wrapper must carry an explicit `padding-left`
 *    that reconstructs the field-row's value-column start (the row's
 *    own left padding, plus the label width, plus the row gap), or the
 *    editor's box starts flush at the composer's edge while Von/An sit
 *    past the label column (re #286, round 3). ComposeWindow does not
 *    need this: its Body row already is a field-row with a label, so
 *    invariant 1 places the editor's box correctly on its own.
 *
 * A getBoundingClientRect() layout assertion needs a real browser (see
 * the issue's puppeteer verification); this source-level check is the
 * happy-dom-reachable half of the regression guard -- it fails the
 * moment any of these declarations drifts back to an independently
 * maintained value.
 */

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const __dir = dirname(fileURLToPath(import.meta.url));

const composeWindowSource = readFileSync(resolve(__dir, 'ComposeWindow.svelte'), 'utf-8');
const recipientFieldSource = readFileSync(resolve(__dir, 'RecipientField.svelte'), 'utf-8');
const fromPickerSource = readFileSync(resolve(__dir, 'FromPicker.svelte'), 'utf-8');
const threadInlineComposerSource = readFileSync(
  resolve(__dir, '../mail/ThreadInlineComposer.svelte'),
  'utf-8',
);

/** Extract the body of the first CSS rule whose selector is exactly `selector`. */
function ruleBody(source: string, selector: string): string {
  const escaped = selector.replace(/[.>:[\]]/g, '\\$&');
  const ruleMatch = source.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`));
  if (!ruleMatch) throw new Error(`selector ${selector} not found`);
  return ruleMatch[1]!;
}

/** Extract a single declaration's value (e.g. `width`, `padding-left`) from a rule body. */
function declaration(body: string, property: string): string {
  const escaped = property.replace(/[.[\]]/g, '\\$&');
  const match = body.match(new RegExp(`${escaped}:\\s*([^;]+);`));
  if (!match) throw new Error(`no ${property} declaration found`);
  return match[1]!.trim().replace(/\s+/g, ' ');
}

/** Resolve the left value out of a `padding` shorthand (1, 2, 3, or 4 value form). */
function paddingLeftFromShorthand(shorthand: string): string {
  const parts = shorthand.split(/\s+/);
  switch (parts.length) {
    case 1:
      return parts[0]!;
    case 2:
    case 3:
      return parts[1]!;
    case 4:
      return parts[3]!;
    default:
      throw new Error(`unexpected padding shorthand: ${shorthand}`);
  }
}

describe('composer row label alignment (re #286)', () => {
  it('ComposeWindow, RecipientField, and ThreadInlineComposer share one label-width token', () => {
    const composeWindowLabelWidth = declaration(ruleBody(composeWindowSource, '.label'), 'width');
    const recipientFieldLabelWidth = declaration(
      ruleBody(recipientFieldSource, '.label'),
      'width',
    );
    const threadInlineLabelWidth = declaration(
      ruleBody(threadInlineComposerSource, '.field-label'),
      'width',
    );

    expect(composeWindowLabelWidth).toBe('var(--compose-label-width)');
    expect(recipientFieldLabelWidth).toBe('var(--compose-label-width)');
    expect(threadInlineLabelWidth).toBe('var(--compose-label-width)');
  });

  it("ThreadInlineComposer's field-row gap matches RecipientField's internal gap", () => {
    const fieldRowGap = declaration(ruleBody(threadInlineComposerSource, '.field-row'), 'gap');
    const recipientFieldGap = declaration(
      ruleBody(recipientFieldSource, '.recipient-field'),
      'gap',
    );

    expect(fieldRowGap).toBe(recipientFieldGap);
  });
});

describe('composer value-text inset (re #286)', () => {
  it('the From text in both composers is inset by --compose-value-left, like the editor', () => {
    const composeWindowFromInset = declaration(
      ruleBody(composeWindowSource, '.from-display'),
      'padding-left',
    );
    const threadInlineFromInset = declaration(
      ruleBody(threadInlineComposerSource, '.from-display'),
      'padding-left',
    );

    expect(composeWindowFromInset).toBe('var(--compose-value-left)');
    expect(threadInlineFromInset).toBe('var(--compose-value-left)');
  });

  it('the Subject input in ComposeWindow is inset by --compose-value-left', () => {
    const subjectPadding = declaration(
      ruleBody(composeWindowSource, "input[type='text']"),
      'padding',
    );

    expect(subjectPadding).toBe('0 0 0 var(--compose-value-left)');
  });

  it("the FromPicker trigger's own border cancels out, landing its text at --compose-value-left", () => {
    const triggerPadding = declaration(ruleBody(fromPickerSource, '.trigger'), 'padding');
    const triggerBorder = declaration(ruleBody(fromPickerSource, '.trigger'), 'border');

    // border-width + padding-left must sum to --compose-value-left.
    expect(triggerBorder).toMatch(/^1px\b/);
    expect(triggerPadding.endsWith('calc(var(--compose-value-left) - 1px)')).toBe(true);
  });

  it("RecipientField's chip-row is inset by --compose-value-left, and the first chip's own border/padding cancels out", () => {
    const chipRowPadding = declaration(ruleBody(recipientFieldSource, '.chip-row'), 'padding-left');
    const chipPadding = declaration(ruleBody(recipientFieldSource, '.chip'), 'padding');
    const chipBorder = declaration(ruleBody(recipientFieldSource, '.chip'), 'border');
    const firstChipMargin = declaration(
      ruleBody(recipientFieldSource, '.chip-row > .chip:first-child'),
      'margin-left',
    );

    expect(chipRowPadding).toBe('var(--compose-value-left)');
    // .chip's padding is `1px var(--spacing-02) 1px var(--spacing-03)` (top/right/bottom/left);
    // its border is 1px -- together they must be exactly cancelled by the first chip's margin.
    expect(chipPadding.endsWith('var(--spacing-03)')).toBe(true);
    expect(chipBorder).toMatch(/^1px\b/);
    expect(firstChipMargin).toBe('calc(-1px - var(--spacing-03))');
  });

  it("ThreadInlineComposer's composer-body reconstructs the field-row value-column start, so the editor lands on Von/An's edge", () => {
    const fieldRowPaddingLeft = paddingLeftFromShorthand(
      declaration(ruleBody(threadInlineComposerSource, '.field-row'), 'padding'),
    );
    const fieldRowGap = declaration(ruleBody(threadInlineComposerSource, '.field-row'), 'gap');
    const fieldLabelWidth = declaration(
      ruleBody(threadInlineComposerSource, '.field-label'),
      'width',
    );
    const composerBodyPaddingLeft = declaration(
      ruleBody(threadInlineComposerSource, '.composer-body'),
      'padding-left',
    );

    // The three inputs composer-body's padding-left formula is built from
    // must stay what it assumes they are.
    expect(fieldRowPaddingLeft).toBe('var(--spacing-04)');
    expect(fieldRowGap).toBe('var(--spacing-04)');
    expect(fieldLabelWidth).toBe('var(--compose-label-width)');
    expect(composerBodyPaddingLeft).toBe(
      'calc(var(--spacing-04) * 2 + var(--compose-label-width))',
    );
  });
});
