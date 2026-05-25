import { describe, it, expect } from 'vitest';
import { emlDownloadFilename } from './download-filename';

describe('emlDownloadFilename', () => {
  it('uses the subject as-is when it is short and safe', () => {
    expect(emlDownloadFilename({ subject: 'Hello world', emailId: 'e1' })).toBe(
      'Hello world.eml',
    );
  });

  it('replaces path-separator and Windows-forbidden chars with underscore', () => {
    expect(
      emlDownloadFilename({
        subject: 'Project / Plan : "Final" <v2>',
        emailId: 'e1',
      }),
    ).toBe('Project _ Plan _ _Final_ _v2_.eml');
  });

  it('replaces control bytes with underscore', () => {
    // Build the input via fromCharCode so the source file does not embed
    // a literal control byte (which the Write tool would normalise away).
    const SOH = String.fromCharCode(0x01);
    const subject = 'before' + SOH + 'after';
    expect(emlDownloadFilename({ subject, emailId: 'e1' })).toBe('before_after.eml');
  });

  it('collapses runs of whitespace and trims the ends', () => {
    expect(
      emlDownloadFilename({ subject: '   hello   world   ', emailId: 'e1' }),
    ).toBe('hello world.eml');
  });

  it('truncates to 80 chars before appending the extension', () => {
    const long = 'A'.repeat(120);
    const out = emlDownloadFilename({ subject: long, emailId: 'e1' });
    expect(out).toBe('A'.repeat(80) + '.eml');
  });

  it('falls back to message-<blobId-prefix>.eml when subject is null', () => {
    expect(
      emlDownloadFilename({ subject: null, blobId: 'BLOB12345xyz', emailId: 'e1' }),
    ).toBe('message-BLOB1234.eml');
  });

  it('falls back to message-<blobId-prefix>.eml when subject is empty after sanitising', () => {
    expect(
      emlDownloadFilename({
        subject: '   :: <<>>   ',
        blobId: 'BLOBabcdefgh',
        emailId: 'e1',
      }),
    ).toBe('message-BLOBabcd.eml');
  });

  it('uses email id when blob id is absent', () => {
    expect(
      emlDownloadFilename({ subject: null, blobId: null, emailId: 'EMAIL12345678' }),
    ).toBe('message-EMAIL123.eml');
  });

  it('returns message-unknown.eml when subject, blobId, and emailId are all empty-ish', () => {
    expect(emlDownloadFilename({ subject: '', blobId: '', emailId: '' })).toBe(
      'message-unknown.eml',
    );
  });
});
