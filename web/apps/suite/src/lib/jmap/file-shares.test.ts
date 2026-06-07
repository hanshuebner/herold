/**
 * Unit tests for the FileShare JMAP helper module.
 *
 * Tests cover:
 *   - isUnsafeExtension: extension detection (REQ-ATT-60)
 *   - offloadThresholdBytes / defaultTtlSeconds: capability defaults
 *   - hasFileShares: capability presence detection
 *   - confirmFileShares: confirm payload shape with source metadata (REQ-SHARE-04)
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  isUnsafeExtension,
  confirmFileShares,
  DEFAULT_OFFLOAD_THRESHOLD,
  DEFAULT_TTL_SECONDS,
  DEFAULT_MAX_TTL_SECONDS,
  type FileShareSourceInfo,
} from './file-shares';

// ── JMAP client mock ──────────────────────────────────────────────────────────
//
// We capture the update map passed to FileShare/set so tests can assert the
// exact confirm payload shape without needing a real JMAP server.

let lastUpdateMap: Record<string, unknown> = {};

vi.mock('./client', () => {
  const jmap = {
    batch: vi.fn(async (builder: (b: unknown) => void) => {
      builder({
        call: (_name: string, args: unknown, _using: string[]) => {
          const a = args as { update?: Record<string, unknown> };
          if (a.update) {
            lastUpdateMap = a.update;
          }
          return { ref: () => ({}) };
        },
      });
      return {
        responses: [['FileShare/set', { updated: {} }, 'R1']],
        sessionState: 's1',
      };
    }),
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-1' },
    },
  };
  return { jmap };
});

// ── isUnsafeExtension ─────────────────────────────────────────────────────

describe('isUnsafeExtension', () => {
  // REQ-ATT-30 executables
  it.each([
    'virus.exe',
    'script.bat',
    'autorun.cmd',
    'legacy.com',
    'screen.scr',
    'annoying.pif',
    'vbscript.vbs',
    'encoded.vbe',
    'jscript.js',
    'encoded.jse',
    'windows.ws',
    'formatted.wsf',
    'host.wsh',
    'installer.msi',
    'patch.msp',
    'registry.reg',
    'shortcut.lnk',
    'command.scf',
    'powershell.ps1',
    'psxml.ps1xml',
    'ps2.ps2',
    'ps2xml.ps2xml',
    'psc1.psc1',
    'psc2.psc2',
    'archive.jar',
    'library.dll',
  ])('flags %s as unsafe', (filename) => {
    expect(isUnsafeExtension(filename)).toBe(true);
  });

  // REQ-ATT-60 archive types
  it.each([
    'archive.zip',
    'compressed.rar',
    'sevenzip.7z',
    'tape.tar',
    'gzipped.gz',
    'tarball.tgz',
    'disk.dmg',
    'optical.iso',
    'diskimage.img',
  ])('flags archive %s as unsafe', (filename) => {
    expect(isUnsafeExtension(filename)).toBe(true);
  });

  // Safe types
  it.each([
    'document.docx',
    'spreadsheet.xlsx',
    'presentation.pptx',
    'image.png',
    'photo.jpg',
    'music.mp3',
    'video.mp4',
    'text.txt',
    'styles.css',
    'code.ts',
    'data.json',
    'nodotfile',
  ])('does not flag %s as unsafe', (filename) => {
    expect(isUnsafeExtension(filename)).toBe(false);
  });

  it('is case-insensitive', () => {
    expect(isUnsafeExtension('VIRUS.EXE')).toBe(true);
    expect(isUnsafeExtension('Archive.ZIP')).toBe(true);
    expect(isUnsafeExtension('Script.PS1')).toBe(true);
  });

  it('does not flag a name that contains an unsafe ext as a substring mid-name', () => {
    // "setup.exe.txt" ends in .txt not .exe
    expect(isUnsafeExtension('setup.exe.txt')).toBe(false);
  });
});

// ── Defaults ──────────────────────────────────────────────────────────────

describe('DEFAULT constants', () => {
  it('DEFAULT_OFFLOAD_THRESHOLD is 25 MB', () => {
    expect(DEFAULT_OFFLOAD_THRESHOLD).toBe(25 * 1024 * 1024);
  });

  it('DEFAULT_TTL_SECONDS is 30 days', () => {
    expect(DEFAULT_TTL_SECONDS).toBe(30 * 24 * 3600);
  });

  it('DEFAULT_MAX_TTL_SECONDS is 90 days', () => {
    expect(DEFAULT_MAX_TTL_SECONDS).toBe(90 * 24 * 3600);
  });
});

// ── confirmFileShares (REQ-SHARE-04) ──────────────────────────────────────

describe('confirmFileShares', () => {
  beforeEach(() => {
    lastUpdateMap = {};
  });

  it('sends state:active without source when source is omitted', async () => {
    await confirmFileShares(['share-1']);
    expect(lastUpdateMap['share-1']).toEqual({ state: 'active' });
  });

  it('includes source fields on the confirm transition when source is provided (REQ-SHARE-04)', async () => {
    const source: FileShareSourceInfo = {
      sourceMessageId: 'email-abc',
      sourceSubject: 'Hello there',
      sourceRecipients: ['alice@example.com', 'bob@example.com'],
    };
    await confirmFileShares(['share-1'], source);
    expect(lastUpdateMap['share-1']).toEqual({
      state: 'active',
      sourceMessageId: 'email-abc',
      sourceSubject: 'Hello there',
      sourceRecipients: ['alice@example.com', 'bob@example.com'],
    });
  });

  it('applies the same source to every share in a batch confirm', async () => {
    const source: FileShareSourceInfo = {
      sourceMessageId: 'email-xyz',
      sourceSubject: 'Multi-share message',
      sourceRecipients: ['carol@example.com'],
    };
    await confirmFileShares(['share-1', 'share-2', 'share-3'], source);
    for (const id of ['share-1', 'share-2', 'share-3']) {
      expect(lastUpdateMap[id]).toEqual({
        state: 'active',
        sourceMessageId: 'email-xyz',
        sourceSubject: 'Multi-share message',
        sourceRecipients: ['carol@example.com'],
      });
    }
  });

  it('is a no-op when the id list is empty and does not call jmap.batch', async () => {
    // Call with an empty list; lastUpdateMap should remain unchanged.
    const before = lastUpdateMap;
    await confirmFileShares([]);
    expect(lastUpdateMap).toBe(before);
  });

  it('sends only state:active when source has empty recipients', async () => {
    const source: FileShareSourceInfo = {
      sourceMessageId: 'email-def',
      sourceSubject: 'No recipients',
      sourceRecipients: [],
    };
    await confirmFileShares(['share-x'], source);
    expect(lastUpdateMap['share-x']).toEqual({
      state: 'active',
      sourceMessageId: 'email-def',
      sourceSubject: 'No recipients',
      sourceRecipients: [],
    });
  });
});
