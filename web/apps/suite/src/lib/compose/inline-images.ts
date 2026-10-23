/**
 * Shared inline-image insertion for the compose surfaces (ComposeWindow
 * and ThreadInlineComposer). Used by both the paste handler and the
 * drag-and-drop handler so a pasted or dropped image takes the same
 * path (re #532).
 */
import type { EditorView } from 'prosemirror-view';
import { applyImage, removeImageBySrc } from './editor';

/** The slice of the compose store this routine drives. */
export interface InlineImageHost {
  startInlineImage(file: File): { key: string; objectURL: string } | null | undefined;
  uploadInlineImage(key: string, file: File): Promise<string | null | undefined | void>;
  attachments: ReadonlyArray<{ key: string; objectURL?: string | null }>;
}

/**
 * Phase 1 (synchronous): register each attachment and insert the image
 * node immediately so a thumbnail shows during upload (issue #74).
 * Insertions are serial so the cursor advances between images.
 * Phase 2 (async): upload in parallel; on failure retract the
 * placeholder, re-reading the current src because a downscaled proxy
 * (issue #243) may have swapped it in while the upload was in flight.
 */
export async function insertInlineImages(
  host: InlineImageHost,
  view: EditorView | null,
  files: File[],
): Promise<void> {
  const pending: Array<{ key: string; objectURL: string; file: File }> = [];
  for (const file of files) {
    const started = host.startInlineImage(file);
    if (!started) continue;
    applyImage(view, started.objectURL, file.name);
    pending.push({ key: started.key, objectURL: started.objectURL, file });
  }
  await Promise.all(
    pending.map(async ({ key, objectURL, file }) => {
      const errMsg = await host.uploadInlineImage(key, file);
      if (errMsg) {
        const current = host.attachments.find((a) => a.key === key);
        removeImageBySrc(view, current?.objectURL ?? objectURL);
      }
    }),
  );
}
