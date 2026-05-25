import { describe, it, expect, vi, beforeEach } from 'vitest';
import { buildPrintDocument, printMessage } from './print-message';

describe('buildPrintDocument', () => {
  const baseArgs = {
    subject: 'Quarterly review',
    date: 'Mon, 24 May 2026, 21:32',
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: [{ name: null, email: 'bob@example.test' }],
    cc: [] as Array<{ name: string | null; email: string }>,
    html: null,
    text: 'Hi Bob, see you tomorrow.\n— Alice',
  };

  it('builds a complete document with title from the subject', () => {
    const out = buildPrintDocument(baseArgs);
    expect(out).toContain('<!doctype html>');
    expect(out).toContain('<title>Quarterly review</title>');
  });

  it('renders header rows for from / to / date / subject', () => {
    const out = buildPrintDocument(baseArgs);
    expect(out).toContain('Alice &lt;alice@example.test&gt;');
    expect(out).toContain('bob@example.test');
    expect(out).toContain('Mon, 24 May 2026, 21:32');
    expect(out).toContain('<div class="subject">Quarterly review</div>');
  });

  it('omits the Cc row when cc is empty', () => {
    const out = buildPrintDocument(baseArgs);
    expect(out).not.toContain('class="label">Cc:');
  });

  it('renders the Cc row when cc has entries', () => {
    const out = buildPrintDocument({
      ...baseArgs,
      cc: [{ name: 'Carol', email: 'carol@example.test' }],
    });
    expect(out).toContain('Carol &lt;carol@example.test&gt;');
  });

  it('renders text body wrapped in <pre> when html is null', () => {
    const out = buildPrintDocument(baseArgs);
    expect(out).toContain('<pre class="text-body">');
    expect(out).toContain('Hi Bob, see you tomorrow.');
  });

  it('renders sanitised html body verbatim when present', () => {
    const out = buildPrintDocument({
      ...baseArgs,
      text: null,
      html: '<p>hello <strong>world</strong></p>',
    });
    expect(out).toContain('<p>hello <strong>world</strong></p>');
    expect(out).not.toContain('<pre');
  });

  it('escapes subject content', () => {
    const out = buildPrintDocument({ ...baseArgs, subject: '<script>x</script>' });
    expect(out).toContain('&lt;script&gt;x&lt;/script&gt;');
    expect(out).not.toContain('<script>x</script>');
  });

  it('falls back to (no subject) for empty subject', () => {
    const out = buildPrintDocument({ ...baseArgs, subject: '' });
    expect(out).toContain('<title>(no subject)</title>');
  });
});

describe('printMessage', () => {
  const baseArgs = {
    subject: 'Hi',
    date: '2026-05-24',
    from: [{ name: null, email: 'a@x.test' }],
    to: [{ name: null, email: 'b@x.test' }],
    cc: [],
    html: null,
    text: 'body text',
  };

  let docWrite: ReturnType<typeof vi.fn>;
  let docOpen: ReturnType<typeof vi.fn>;
  let docClose: ReturnType<typeof vi.fn>;
  let focus: ReturnType<typeof vi.fn>;
  let printSpy: ReturnType<typeof vi.fn>;
  let popup: {
    document: { open: typeof docOpen; write: typeof docWrite; close: typeof docClose };
    focus: typeof focus;
    print: typeof printSpy;
  };

  beforeEach(() => {
    docWrite = vi.fn();
    docOpen = vi.fn();
    docClose = vi.fn();
    focus = vi.fn();
    printSpy = vi.fn();
    popup = {
      document: { open: docOpen, write: docWrite, close: docClose },
      focus,
      print: printSpy,
    };
    vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window);
    vi.useFakeTimers();
  });

  it('opens a popup, writes the document, focuses, and prints', () => {
    printMessage(baseArgs);
    expect(window.open).toHaveBeenCalledWith(
      '',
      '_blank',
      'noopener,noreferrer,width=820,height=900',
    );
    expect(docOpen).toHaveBeenCalled();
    expect(docWrite).toHaveBeenCalledTimes(1);
    const firstCall = docWrite.mock.calls[0];
    expect(firstCall).toBeDefined();
    const written = firstCall![0] as string;
    expect(written).toContain('<title>Hi</title>');
    expect(docClose).toHaveBeenCalledAfter(docWrite);
    expect(focus).toHaveBeenCalled();
    // print is scheduled in a 0-ms setTimeout to allow the popup to render.
    expect(printSpy).not.toHaveBeenCalled();
    vi.runAllTimers();
    expect(printSpy).toHaveBeenCalled();
  });

  it('returns null and does not throw when the popup is blocked', () => {
    vi.spyOn(window, 'open').mockReturnValue(null);
    expect(printMessage(baseArgs)).toBeNull();
  });
});
