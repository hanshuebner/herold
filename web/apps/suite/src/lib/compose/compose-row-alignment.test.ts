/**
 * Regression guard for issue #286: the From (`Von`) and To/Cc/Bcc (`An`)
 * composer rows must share a common left edge for their values, in both
 * the floating ComposeWindow and the thread-reader inline composer.
 *
 * The columns align only when three CSS declarations, spread across three
 * components, stay locked to the same values:
 *   - ComposeWindow.svelte's `.label` width (the From/Subject/Body rows)
 *   - RecipientField.svelte's `.label` width (the To/Cc/Bcc rows' internal
 *     label, nested inside its own flex row)
 *   - ThreadInlineComposer.svelte's `.field-label` width (its own From row)
 *   plus ThreadInlineComposer.svelte's `.field-row` gap, which must match
 *   RecipientField's internal `.recipient-field` gap so the From value and
 *   the first chip start at the same offset from the shared label width.
 *
 * A getBoundingClientRect() layout assertion needs a real browser (see the
 * issue's puppeteer verification); this source-level check is the
 * happy-dom-reachable half of the regression guard -- it fails the moment
 * any of the three label-width declarations, or ThreadInlineComposer's
 * field-row gap, drifts back to an independently maintained value.
 */

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const __dir = dirname(fileURLToPath(import.meta.url));

const composeWindowSource = readFileSync(resolve(__dir, 'ComposeWindow.svelte'), 'utf-8');
const recipientFieldSource = readFileSync(resolve(__dir, 'RecipientField.svelte'), 'utf-8');
const threadInlineComposerSource = readFileSync(
  resolve(__dir, '../mail/ThreadInlineComposer.svelte'),
  'utf-8',
);

/** Extract the `width` declaration of the first rule matching `selector` (e.g. `.label`). */
function labelWidth(source: string, selector: string): string {
  const escaped = selector.replace(/[.[\]]/g, '\\$&');
  const ruleMatch = source.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`));
  if (!ruleMatch) throw new Error(`selector ${selector} not found`);
  const widthMatch = ruleMatch[1]!.match(/width:\s*([^;]+);/);
  if (!widthMatch) throw new Error(`no width declaration in ${selector}`);
  return widthMatch[1]!.trim();
}

describe('composer row label alignment (re #286)', () => {
  it('ComposeWindow, RecipientField, and ThreadInlineComposer share one label-width token', () => {
    const composeWindowLabelWidth = labelWidth(composeWindowSource, '.label');
    const recipientFieldLabelWidth = labelWidth(recipientFieldSource, '.label');
    const threadInlineLabelWidth = labelWidth(threadInlineComposerSource, '.field-label');

    expect(composeWindowLabelWidth).toBe('var(--compose-label-width)');
    expect(recipientFieldLabelWidth).toBe('var(--compose-label-width)');
    expect(threadInlineLabelWidth).toBe('var(--compose-label-width)');
  });

  it("ThreadInlineComposer's field-row gap matches RecipientField's internal gap", () => {
    const fieldRowRule = threadInlineComposerSource.match(/\.field-row\s*\{([^}]*)\}/);
    const recipientFieldRule = recipientFieldSource.match(/\.recipient-field\s*\{([^}]*)\}/);
    expect(fieldRowRule).not.toBeNull();
    expect(recipientFieldRule).not.toBeNull();

    const fieldRowGap = fieldRowRule![1]!.match(/gap:\s*([^;]+);/)?.[1]?.trim();
    const recipientFieldGap = recipientFieldRule![1]!.match(/gap:\s*([^;]+);/)?.[1]?.trim();

    expect(fieldRowGap).toBe(recipientFieldGap);
  });
});
