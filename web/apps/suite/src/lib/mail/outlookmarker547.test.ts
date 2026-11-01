/**
 * Corpus for the Outlook marker fold (re #547).
 *
 * The HTML reader folds a reply's quoted original only on Outlook's own
 * markup: OWA's `divRplyFwdMsg` container, and Outlook desktop's header
 * block, a div carrying `border:none;border-top:solid #E1E1E1 1.0pt` (or
 * `#B5C4DF`) around the bold-label header paragraph. The marker block and
 * everything after it fold. HTML without those markers and plain text never
 * fold through this path, so every sender-written labelled list (web forms,
 * itineraries, signatures, newsletter boxes, in several locales) stays
 * visible.
 *
 * The Outlook fixtures are built to match the markup Outlook 2016 / 365
 * desktop and Outlook on the web emit (no genuine Outlook samples exist in
 * the repo). Every fixture sits between an unrelated paragraph before and
 * after, plain and inside an extra wrapper.
 */
import { describe, it, expect } from 'vitest';
import { sanitizeHtml } from './sanitize';
import { splitQuotedText } from './quoted';

function render(html: string): { folds: number; visible: string; folded: string } {
  const out = sanitizeHtml(html, { loadImages: false });
  const body = out.match(/<body>([\s\S]*?)<\/body>/)?.[1] ?? '';
  const host = document.createElement('div');
  host.innerHTML = body;
  const folds = host.querySelectorAll('details.herold-quoted');
  const folded = folds[0]?.textContent ?? '';
  host.querySelectorAll('details.herold-quoted').forEach((d) => d.remove());
  return { folds: folds.length, visible: host.textContent ?? '', folded };
}

const BEFORE = '<p>Unrelated paragraph before.</p>';
const AFTER = '<p>Unrelated paragraph after.</p>';
const wrappers: Record<string, (h: string) => string> = {
  plain: (h) => BEFORE + h + AFTER,
  'extra wrapping div': (h) => `<div class="extra-wrap">${BEFORE}${h}${AFTER}</div>`,
};

type Locale = 'de' | 'en';
const FIELDS: Record<Locale, string[][]> = {
  de: [
    ['Von:', 'Alice Example &lt;alice@example.test&gt;'],
    ['Gesendet:', 'Montag, 12. Oktober 2026 09:15'],
    ['An:', 'Bob Example'],
    ['Cc:', 'Carol Example'],
    ['Betreff:', 'Quarterly figures'],
  ],
  en: [
    ['From:', 'Alice Example &lt;alice@example.test&gt;'],
    ['Sent:', 'Monday, 12 October 2026 09:15'],
    ['To:', 'Bob Example'],
    ['Cc:', 'Carol Example'],
    ['Subject:', 'Quarterly figures'],
  ],
};
const GREY = 'rgb(33, 33, 33)';

/** Outlook desktop: bordered div around one <p class=MsoNormal> of bold-label lines. */
function desktop(locale: Locale, color = 'E1E1E1'): string {
  const f = FIELDS[locale];
  const lines = f
    .map(([l, v], i) =>
      i === 0
        ? `<b><span lang="DE" style="font-size:11.0pt;font-family:'Calibri',sans-serif">${l}</span></b>` +
          `<span style="font-size:11.0pt;font-family:'Calibri',sans-serif"> ${v}<br>`
        : `<b>${l}</b> ${v}${i === f.length - 1 ? '<o:p></o:p></span>' : '<br>'}`,
    )
    .join('');
  return (
    '<div class="WordSection1"><p class="MsoNormal">Fresh reply.<o:p></o:p></p>' +
    '<div><div style="border:none;border-top:solid #' + color + ' 1.0pt;padding:3.0pt 0cm 0cm 0cm">' +
    `<p class="MsoNormal">${lines}</p></div></div>` +
    '<p class="MsoNormal">Original line one.<o:p></o:p></p><p class="MsoNormal">Original line two.</p></div>'
  );
}

/** Outlook on the web: hr + divRplyFwdMsg, original in a grey-text div. */
function owa(locale: Locale, marker = 'id="divRplyFwdMsg"'): string {
  const lines = FIELDS[locale].map(([l, v]) => `<b>${l}</b> ${v}`).join('<br>');
  return (
    '<div style="font-family:Aptos,Calibri,sans-serif;font-size:12pt;color:rgb(0,0,0)">Fresh reply.</div>' +
    '<div id="appendonsend"></div><hr style="display:inline-block;width:98%" tabindex="-1">' +
    `<div ${marker} dir="ltr"><font face="Calibri, sans-serif" style="font-size:11pt" color="#000000">${lines}</font>` +
    '<div>&nbsp;</div></div>' +
    `<div style="color:${GREY}"><p>Original line one.</p><div style="font-size:12pt">Original line two.</div></div>`
  );
}

describe('re #547: Outlook desktop and OWA replies fold from the marker', () => {
  const builds: Record<string, (l: Locale) => string> = {
    'desktop #E1E1E1': (l) => desktop(l),
    'desktop #B5C4DF': (l) => desktop(l, 'B5C4DF'),
    'OWA id': (l) => owa(l),
    'OWA class': (l) => owa(l, 'class="divRplyFwdMsg"'),
  };
  for (const locale of ['de', 'en'] as const) {
    for (const [kind, build] of Object.entries(builds)) {
      for (const [wname, wrap] of Object.entries(wrappers)) {
        it(`${kind} ${locale} / ${wname}`, () => {
          const r = render(wrap(build(locale)));
          expect(r.folds).toBe(1);
          expect(r.visible).toContain('Fresh reply.');
          expect(r.visible).toContain('Unrelated paragraph before.');
          expect(r.visible).not.toContain('Quarterly figures');
          expect(r.visible).not.toContain('Original line one.');
          expect(r.folded).toContain(FIELDS[locale][0]![0]);
          expect(r.folded).toContain('Original line two.');
          // The marker block and everything after it, to the end.
          expect(r.folded).toContain('Unrelated paragraph after.');
        });
      }
    }
  }

  it('a gateway banner and an older blockquote after the marker fold with it', () => {
    const html =
      desktop('de').replace(
        '<p class="MsoNormal">Original line one.',
        '<div><table width="100%"><tbody><tr><td><p>Caution: external sender.</p></td></tr></tbody></table></div>' +
          '<p class="MsoNormal">Original line one.',
      ).replace('</div>', '<blockquote><p>Older quoted text.</p></blockquote></div>');
    const r = render(html);
    expect(r.folds).toBe(1);
    expect(r.visible).toBe('Fresh reply.');
    expect(r.folded).toContain('Caution: external sender.');
    expect(r.folded).toContain('Older quoted text.');
  });
});

describe('re #547: a marker with nothing of the sender before it does not fold', () => {
  it('OWA forward with no text above', () => {
    const r = render(owa('en').replace(/^<div style[^>]*>Fresh reply\.<\/div>/, ''));
    expect(r.folds).toBe(0);
    expect(r.visible).toContain('Original line one.');
  });
  it('desktop forward with no text above', () => {
    const r = render(desktop('en').replace('<p class="MsoNormal">Fresh reply.<o:p></o:p></p>', ''));
    expect(r.folds).toBe(0);
    expect(r.visible).toContain('Original line one.');
  });
});

describe('re #547: lookalikes of the marker do not fold', () => {
  const cases: Record<string, string> = {
    'bordered div in another colour': desktop('en', '999999'),
    'Outlook border without a bold label': desktop('en').replace(/<b>/g, '<i>').replace(/<\/b>/g, '</i>'),
    'the word divRplyFwdMsg in text': '<p>Fresh reply.</p><p>see divRplyFwdMsg here</p><p>Original line one.</p>',
    'id on a non-matching element': '<p>Fresh reply.</p><div id="divRplyFwdMsgX"><b>From:</b> x</div><p>Original line one.</p>',
  };
  for (const [name, html] of Object.entries(cases)) {
    it(name, () => {
      const r = render(html);
      expect(r.folds).toBe(0);
      expect(r.visible).toContain('Original line one.');
    });
  }
});

// ---- Must-stay-visible: sender-written labelled lists, no Outlook marker ----

const GREYRULE = '<div style="border:none;border-top:solid #999999 1.0pt;padding:3.0pt 0cm">';

const forms: Record<string, { html: string; text: string | null; marker: string }> = {
  'German Von/Datum/An/Betreff/Nachricht (bold)': {
    html:
      '<p>Neue Anfrage:</p><hr><p><b>Von:</b> Max Mustermann<br><b>Datum:</b> 12. Oktober 2026<br>' +
      '<b>An:</b> Vertrieb<br><b>Betreff:</b> Anfrage<br><b>Nachricht:</b> Bitte um Angebot FORMBODY</p>',
    text:
      'Neue Anfrage:\n____________\nVon: Max Mustermann\nDatum: 12. Oktober 2026\nAn: Vertrieb\n' +
      'Betreff: Anfrage\nNachricht: Bitte um Angebot FORMBODY',
    marker: 'FORMBODY',
  },
  'German list with an unbolded Nachricht line': {
    html:
      '<p>Neue Anfrage:</p><hr><p><b>Von:</b> Max Mustermann<br><b>Datum:</b> 12. Oktober 2026<br>' +
      '<b>An:</b> Vertrieb<br><b>Betreff:</b> Anfrage<br>Nachricht: Bitte um Angebot FORMBODY</p>',
    text:
      'Neue Anfrage:\n____________\nVon: Max Mustermann\nDatum: 12. Oktober 2026\nAn: Vertrieb\n' +
      'Betreff: Anfrage\n\nNachricht: Bitte um Angebot FORMBODY',
    marker: 'FORMBODY',
  },
  'English From/Date/To/Subject/Message': {
    html:
      '<p>New enquiry:</p><hr><p><b>From:</b> Max Mustermann<br><b>Date:</b> 3 March 2026<br>' +
      '<b>To:</b> Support<br><b>Subject:</b> Enquiry<br><b>Message:</b> Please call me FORMBODY</p>',
    text:
      'New enquiry:\n____________\nFrom: Max Mustermann\nDate: 3 March 2026\nTo: Support\n' +
      'Subject: Enquiry\nMessage: Please call me FORMBODY',
    marker: 'FORMBODY',
  },
  'list ending in Telefon': {
    html:
      '<p>Rueckruf:</p><hr><p><b>Von:</b> Max Mustermann<br><b>Datum:</b> 12. Oktober 2026<br>' +
      '<b>An:</b> Vertrieb<br><b>Betreff:</b> Rueckruf<br><b>Telefon:</b> 0170 555 0100</p>',
    text:
      'Rueckruf:\n____________\nVon: Max Mustermann\nDatum: 12. Oktober 2026\nAn: Vertrieb\n' +
      'Betreff: Rueckruf\nTelefon: 0170 555 0100',
    marker: 'Telefon',
  },
  'German Ort / Bestellnummer list': {
    html:
      '<p>Ihre Bestellung:</p><hr><p><b>Von:</b> Shop Example<br><b>Datum:</b> 12. Oktober 2026<br>' +
      '<b>An:</b> Hans Example<br><b>Betreff:</b> Bestellung<br><b>Ort:</b> Hamburg<br>' +
      '<b>Bestellnummer:</b> 4711 FORMBODY</p>',
    text:
      'Ihre Bestellung:\n____________\nVon: Shop Example\nDatum: 12. Oktober 2026\nAn: Hans Example\n' +
      'Betreff: Bestellung\nOrt: Hamburg\nBestellnummer: 4711 FORMBODY',
    marker: 'FORMBODY',
  },
  'Spanish De/Fecha/Para/Asunto': {
    html:
      '<p>Reserva:</p><hr><p><b>De:</b> Hotel Ejemplo<br><b>Fecha:</b> 12 de octubre de 2026<br>' +
      '<b>Para:</b> Hans<br><b>Asunto:</b> Reserva FORMBODY</p>',
    text:
      'Reserva:\n____________\nDe: Hotel Ejemplo\nFecha: 12 de octubre de 2026\nPara: Hans\nAsunto: Reserva FORMBODY',
    marker: 'FORMBODY',
  },
  'French De/Date/A/Objet': {
    html:
      '<p>Reservation:</p><hr><p><b>De :</b> Hotel Exemple<br><b>Date :</b> 12 octobre 2026<br>' +
      '<b>A :</b> Hans<br><b>Objet :</b> Reservation FORMBODY</p>',
    text:
      'Reservation:\n____________\nDe : Hotel Exemple\nDate : 12 octobre 2026\nA : Hans\nObjet : Reservation FORMBODY',
    marker: 'FORMBODY',
  },
  'Italian Da/Data/A/Oggetto': {
    html:
      '<p>Prenotazione:</p><hr><p><b>Da:</b> Hotel Esempio<br><b>Data:</b> 12 ottobre 2026<br>' +
      '<b>A:</b> Hans<br><b>Oggetto:</b> Prenotazione FORMBODY</p>',
    text:
      'Prenotazione:\n____________\nDa: Hotel Esempio\nData: 12 ottobre 2026\nA: Hans\nOggetto: Prenotazione FORMBODY',
    marker: 'FORMBODY',
  },
  'Dutch Van/Datum/Aan/Onderwerp': {
    html:
      '<p>Reservering:</p><hr><p><b>Van:</b> Hotel Voorbeeld<br><b>Datum:</b> 12 oktober 2026<br>' +
      '<b>Aan:</b> Hans<br><b>Onderwerp:</b> Reservering FORMBODY</p>',
    text:
      'Reservering:\n____________\nVan: Hotel Voorbeeld\nDatum: 12 oktober 2026\nAan: Hans\nOnderwerp: Reservering FORMBODY',
    marker: 'FORMBODY',
  },
  'table form, label and value in separate cells': {
    html:
      '<p>Neue Anfrage:</p><hr><table><tbody>' +
      '<tr><td><b>Von:</b></td><td>Max Mustermann</td></tr>' +
      '<tr><td><b>Datum:</b></td><td>12. Oktober 2026</td></tr>' +
      '<tr><td><b>An:</b></td><td>Vertrieb</td></tr>' +
      '<tr><td><b>Betreff:</b></td><td>Anfrage FORMBODY</td></tr></tbody></table>',
    text: null,
    marker: 'FORMBODY',
  },
  'newsletter box with sender, date and subject': {
    html:
      '<p>Ihr Newsletter:</p><table style="border:1px solid #ccc"><tbody><tr><td>' +
      '<b>Von:</b> Redaktion Example<br><b>Datum:</b> 12. Oktober 2026<br><b>An:</b> Abonnenten<br>' +
      '<b>Betreff:</b> Wochenrueckblick FORMBODY</td></tr></tbody></table>',
    text:
      'Ihr Newsletter:\n____________\nVon: Redaktion Example\nDatum: 12. Oktober 2026\nAn: Abonnenten\n' +
      'Betreff: Wochenrueckblick FORMBODY',
    marker: 'FORMBODY',
  },
  'contact form (Gesendet, Telefon)': {
    html:
      '<p>Kontaktformular:</p><hr><p><b>Von:</b> Max Mustermann<br><b>Gesendet:</b> 12. Oktober 2026 09:15<br>' +
      '<b>Betreff:</b> Anfrage Angebot<br><b>Telefon:</b> 0170 555 0100 FORMBODY</p><p>Bitte um Rueckruf.</p>',
    text:
      'Kontaktformular:\n________\nVon: Max Mustermann\nGesendet: 12. Oktober 2026 09:15\n' +
      'Betreff: Anfrage Angebot\nTelefon: 0170 555 0100 FORMBODY\n\nBitte um Rueckruf.',
    marker: 'FORMBODY',
  },
  'itinerary (Von/An/Datum/Betreff)': {
    html:
      '<p>Ihre Reise:</p><hr><p><b>Von:</b> Hamburg Hbf<br><b>An:</b> Berlin Hbf<br>' +
      '<b>Datum:</b> 12. Oktober 2026<br><b>Betreff:</b> ICE 1234 FORMBODY</p>',
    text:
      'Ihre Reise:\n________\nVon: Hamburg Hbf\nAn: Berlin Hbf\nDatum: 12. Oktober 2026\nBetreff: ICE 1234 FORMBODY',
    marker: 'FORMBODY',
  },
  'bordered signature (English), non-Outlook border': {
    html:
      '<p>Thanks.</p>' + GREYRULE + '<p><b>From:</b> Hans Example, Example GmbH<br>' +
      '<b>Date:</b> 12 October 2026<br><b>Subject:</b> Planning<br><b>Phone:</b> 555 0100 FORMBODY</p></div>',
    text:
      'Thanks.\n________\nFrom: Hans Example, Example GmbH\nDate: 12 October 2026\nSubject: Planning\n' +
      'Phone: 555 0100 FORMBODY',
    marker: 'FORMBODY',
  },
  'bordered list, date without a year, non-Outlook border': {
    html:
      '<p>Termin:</p>' + GREYRULE + '<p><b>Von:</b> Hans Example<br>' +
      '<b>Datum:</b> Montag, 12. Oktober<br><b>Betreff:</b> Planung FORMBODY</p></div>',
    text: null,
    marker: 'FORMBODY',
  },
  'bold signature under a rule': {
    html:
      '<p>Danke.</p><hr><p><b>Von:</b> Hans Example<br><b>Datum:</b> 12.10.<br><b>Betreff:</b> Planung FORMBODY</p>',
    text: null,
    marker: 'FORMBODY',
  },
  'meeting block (English)': {
    html:
      '<p>Meeting details:</p><p><b>From:</b> 9am<br><b>Date:</b> Monday<br><b>Subject:</b> Planning FORMBODY</p>',
    text: null,
    marker: 'FORMBODY',
  },
  'Tel/Fax/Mail signature list': {
    html: '<p>Hallo.</p><hr><p><b>Tel:</b> 555 0100<br><b>Fax:</b> 555 0101<br><b>Mail:</b> sender@example.test FORMBODY</p>',
    text: null,
    marker: 'FORMBODY',
  },
  'complete-looking Outlook-style list without Outlook markup': {
    html:
      '<p>Notiz:</p><hr><div><p><b>Von:</b> Alice Example<br><b>Gesendet:</b> Montag, 12. Oktober 2026 09:15<br>' +
      '<b>An:</b> Bob Example<br><b>Betreff:</b> Zahlen FORMBODY</p></div><p>Weiterer Text.</p>',
    text: null,
    marker: 'FORMBODY',
  },
};

describe('re #547: a sender-written labelled list never folds, HTML and plain text', () => {
  for (const [name, { html, text, marker }] of Object.entries(forms)) {
    for (const [wname, wrap] of Object.entries(wrappers)) {
      it(`HTML ${name} / ${wname}`, () => {
        const r = render(wrap(html));
        expect(r.folds).toBe(0);
        expect(r.visible).toContain(marker);
        expect(r.visible).toContain('Unrelated paragraph before.');
        expect(r.visible).toContain('Unrelated paragraph after.');
      });
    }
    if (text !== null) {
      it(`plain text ${name}`, () => {
        const body = `Unrelated paragraph before.\n\n${text}\n\nUnrelated paragraph after.`;
        const { head, collapsed } = splitQuotedText(body);
        expect(collapsed).toBe('');
        expect(head).toBe(body);
      });
    }
  }

  it('inline replies in blue spans among header-shaped lines stay visible', () => {
    const html =
      '<p>Hallo, siehe unten.</p><hr><div><p><b>Von:</b> Alice Example<br><b>Gesendet:</b> Montag, 12. Oktober 2026 09:15<br>' +
      '<b>An:</b> Bob Example<br><b>Betreff:</b> Fragen</p></div>' +
      '<p>Original frage 1?</p><p><span style="color:#1F497D">Meine Antwort 1</span></p>';
    for (const wrap of Object.values(wrappers)) {
      const r = render(wrap(html));
      expect(r.folds).toBe(0);
      expect(r.visible).toContain('Meine Antwort 1');
    }
  });
});
