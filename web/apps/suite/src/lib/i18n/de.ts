/**
 * German message catalogue. Mirrors keys from `en.ts`; missing keys
 * fall back to English at runtime.
 */
export const de = {
  // ── Mail sidebar ─────────────────────────────────────────────────────
  'sidebar.compose': 'Verfassen',
  'sidebar.inbox': 'Posteingang',
  'sidebar.snoozed': 'Zurückgestellt',
  'sidebar.important': 'Wichtig',
  'sidebar.sent': 'Gesendet',
  'sidebar.drafts': 'Entwürfe',
  'sidebar.trash': 'Papierkorb',
  'sidebar.allMail': 'Alle Nachrichten',
  'sidebar.more': 'Mehr',
  'sidebar.labels': 'Labels',
  'sidebar.newMailbox': 'Neues Label',
  'sidebar.noCustom': 'Keine eigenen Labels.',
  'sidebar.rename': 'Umbenennen',
  'sidebar.delete': 'Löschen',
  'sidebar.chats': 'Chats',
  'sidebar.newChat': 'Neuer Chat',
  'sidebar.createFolder.title': 'Neues Label',
  'sidebar.createFolder.label': 'Labelname',
  'sidebar.createFolder.confirm': 'Erstellen',
  'sidebar.renameFolder.title': 'Label umbenennen',
  'sidebar.renameFolder.label': 'Neuer Name',
  'sidebar.renameFolder.confirm': 'Umbenennen',
  'sidebar.editFolder.title': 'Label bearbeiten',
  'sidebar.editFolder.confirm': 'Ändern',
  'sidebar.editFolder.toastChanged': 'Geändert',
  'sidebar.deleteFolder.title': 'Label "{name}" löschen?',
  'sidebar.deleteFolder.message':
    'Enthaltene Nachrichten bleiben in anderen Labels erhalten (andernfalls werden sie in den Papierkorb verschoben).',
  'sidebar.deleteFolder.confirm': 'Löschen',

  // ── Mail list ───────────────────────────────────────────────────────
  'list.refresh': 'Aktualisieren',
  'list.emptyTrash': 'Papierkorb leeren',
  'list.loading': 'Laden...',
  'list.retry': 'Erneut versuchen',
  'list.empty.inbox': 'Posteingang ist leer.',
  'list.empty.allMail': 'Keine Nachrichten.',
  'list.empty.folder': '{name} ist leer.',
  'list.couldNotLoad': '{name} konnte nicht geladen werden.',
  'list.dragMessageCount': '{count} Nachrichten verschieben',
  // Hinweis "Bilder werden verarbeitet" (REQ-EXTIMG-BG-30).
  'mail.list.internalizePending.tooltip':
    'Bilder werden im Hintergrund verarbeitet. Aktualisieren Sie kurz, um sie zu sehen.',
  // Banner im Konversationslesefenster (REQ-EXTIMG-BG-31).
  'mail.threadReader.internalizePending.heading': 'Bilder werden verarbeitet',
  'mail.threadReader.internalizePending.body':
    'Externe Bilder in dieser Nachricht werden noch im Hintergrund internalisiert. Sie erscheinen vorerst als Platzhalter; aktualisieren Sie kurz, um sie zu sehen.',

  // Banner für neu eingetroffene Antwort (issue #118): inline angezeigt,
  // damit der Leser nicht beim Verfassen einer eigenen Antwort von einer
  // neu eingetroffenen Nachricht überrascht wird. Bleibt sichtbar bis
  // ausdrücklich bestätigt.
  'mail.threadReader.newReply.aria': 'Hinweis auf neue Antwort',
  'mail.threadReader.newReply.one.heading': 'Neue Antwort von {sender}',
  'mail.threadReader.newReply.many.heading': '{count} neue Antworten',
  'mail.threadReader.newReply.show': 'Neue Antwort anzeigen',
  'mail.threadReader.newReply.dismiss': 'Verstanden',
  'mail.threadReader.newReply.more': '+{count} weitere',

  // ── Banner für getaggte Adressen (REQ-MAIL-12c) ──────────────────────
  // Banner über dem Nachrichtentext bietet vier Aktionen für erstmals
  // gesehene Sub-Adressen.
  'mail.taggedBanner.heading': 'Post an +{suffix}.',
  'mail.taggedBanner.lead': 'Auto-Sortierung für künftige Nachrichten einrichten?',
  'mail.taggedBanner.action.label': 'Etikett',
  'mail.taggedBanner.action.labelArchive': 'Etikett + archivieren',
  'mail.taggedBanner.action.labelArchiveRead': 'Etikett + archivieren + als gelesen markieren',
  'mail.taggedBanner.action.dismiss': 'Künftige Post an +{suffix} ignorieren',
  'mail.taggedBanner.dialog.title.label': 'Post an +{suffix} etikettieren',
  'mail.taggedBanner.dialog.title.labelArchive':
    'Post an +{suffix} etikettieren und archivieren',
  'mail.taggedBanner.dialog.title.labelArchiveRead':
    'Post an +{suffix} etikettieren, archivieren und als gelesen markieren',
  'mail.taggedBanner.dialog.confirm': 'Regel anlegen',
  'mail.taggedBanner.toast.filterCreated': 'Regel für +{suffix} angelegt',
  'mail.taggedBanner.toast.dismissed': 'Künftige Post an +{suffix} fragt nicht erneut',
  'mail.taggedBanner.toast.filterFailed': 'Regel konnte nicht angelegt werden',
  'mail.taggedBanner.toast.dismissFailed': 'Ignorier-Markierung konnte nicht gespeichert werden',

  // ── MailView-Oberflächen (re #97) ────────────────────────────────────
  // Suchroute: Überschrift + Echo der leeren Anfrage, Zurück-Link,
  // ARIA-Bezeichnungen für Erkennungs-Strip / Verlaufs-Strip /
  // Ergebnisliste sowie den "Keine Treffer"-Zustand.
  'mail.search.heading': 'Suche:',
  'mail.search.empty': '(leer)',
  'mail.search.backToInbox': '← Zurück zum Posteingang',
  'mail.search.recognisedQueryAria': 'Erkannte Suchanfrage',
  'mail.search.recentSearchesAria': 'Letzte Suchen',
  'mail.search.resultsAria': 'Suchergebnisse',
  'mail.search.noMatches': 'Keine Treffer.',
  // Aktionsleiste / Kategorien-Tab-Streifen / Thread-Listbox sowie
  // dazugehörige Zähler-Badges.
  'mail.list.actionsAria': 'Listen-Aktionen',
  'mail.list.threadsAria': 'Konversationen in {name}',
  'mail.list.tabsAria': 'Posteingang-Kategorien',
  'mail.list.tabUnreadAria': '{count} ungelesen',
  'mail.list.tabPrimary': 'Primär',
  'mail.list.emptyTab': 'Keine Nachrichten in {name}.',
  // Steuerelemente in der Zeile: Auswahl-Checkbox, Stern-Schalter,
  // Thread-Anzahl-Badge und der inline-Anhangshinweis (Büroklammer
  // neben dem Betreff). Der Thread-Anzahl-Badge wird über .other
  // pluralisiert.
  'mail.row.selectAria': 'Nachricht auswählen',
  'mail.row.starAria': 'Markieren',
  'mail.row.unstarAria': 'Markierung aufheben',
  'mail.row.threadCountAria': '{count} Nachricht',
  'mail.row.threadCountAria.other': '{count} Nachrichten',
  // Label-Route Platzhalter (Label-Ansicht-Abfragen kommen nach dem
  // Posteingang).
  'mail.label.heading': 'Label: {name}',
  'mail.label.lead': 'Die Label-Ansicht folgt nach dem Posteingang.',

  // ── Suche (re #97) ───────────────────────────────────────────────────
  'search.history.label': 'Zuletzt:',
  'search.history.rerun': 'Suche erneut ausführen',
  'search.history.clear': 'Löschen',
  'search.history.clearTitle': 'Verlauf löschen',
  'search.history.clearAria': 'Suchverlauf löschen',
  'search.searching': 'Suche läuft…',
  'search.failed': 'Suche fehlgeschlagen.',
  'search.advanced.from.placeholder': 'Absendername oder Adresse',
  'search.advanced.to.placeholder': 'Empfängername oder Adresse',
  'search.advanced.subject.placeholder': 'Betreff enthält',
  'search.advanced.body.placeholder': 'Text enthält',

  // ── Postfach-Auswahl ─────────────────────────────────────────────────
  'mailbox.filter.placeholder': 'Postfächer filtern…',
  'mailbox.filter.aria': 'Postfächer filtern',

  // ── Fallback bei nicht gefundenem Postfach ───────────────────────────
  'mail.notFound.title': 'Nicht gefunden',
  'mail.notFound.lead': 'Unter dieser URL gibt es kein Postfach.',

  // ── JMAP setError-Typen (issue #17) ─────────────────────────────────
  // Wird angezeigt, wenn ein Email/set-Update als `notUpdated`
  // zurückkommt. Ohne diese Strings würde der Server-Fehlercode
  // (`notFound`, `forbidden`, …) wortwörtlich im Toast erscheinen.
  'mail.setError.notFound':           'Diese Nachricht gibt es nicht mehr.',
  'mail.setError.forbidden':          'Dafür fehlen dir die Rechte.',
  'mail.setError.invalidProperties':  'Die Änderung wurde als ungültig abgelehnt.',
  'mail.setError.overQuota':          'Dein Postfach hat das Speicher­limit erreicht.',
  'mail.setError.tooManyMailboxes':   'Zu viele Postfächer für diese Nachricht.',
  'mail.setError.mailboxHasChild':    'Dieser Ordner hat Unter­ordner; bitte erst löschen.',
  'mail.setError.serverFail':         'Der Server hat einen Fehler gemeldet.',
  'mail.setError.unknown':            'Da ging etwas schief.',

  // ── Selection chooser (issue #10) ────────────────────────────────────
  'select.all': 'Alle',
  'select.none': 'Keine',
  'select.read': 'Gelesen',
  'select.unread': 'Ungelesen',
  'select.starred': 'Markiert',
  'select.unstarred': 'Nicht markiert',
  'select.openMenu': 'Auswahl...',
  'select.deselectAll': 'Auswahl aufheben',
  'select.clearSelection': 'Auswahl löschen',
  'select.selectAll': 'Alle auswählen',
  'select.options': 'Auswahloptionen',

  // ── Bulk actions ────────────────────────────────────────────────────
  'bulk.selected': '{count} ausgewählt',
  'bulk.archive': 'Archivieren',
  'bulk.markRead': 'Als gelesen markieren',
  'bulk.markUnread': 'Als ungelesen markieren',
  'bulk.move': 'Verschieben...',
  'bulk.label': 'Labels...',
  'bulk.category': 'Kategorie...',
  'bulk.delete': 'Löschen',

  // ── Thread reader ───────────────────────────────────────────────────
  'thread.loading': 'Konversation wird geladen...',
  'thread.couldNotLoad': 'Konversation konnte nicht geladen werden.',
  'thread.retry': 'Erneut versuchen',
  'thread.empty': 'Diese Konversation enthält keine Nachrichten.',
  'thread.print': 'Drucken',
  'thread.subject.none': '(kein Betreff)',
  'thread.back': 'Zurück zur Liste',
  // Thread-scope toolbar actions (re #60, re #98). Labels intentionally
  // do NOT repeat "Konversation" — the toolbar is the thread toolbar, the
  // prefix is redundant and noisy in the UI.
  'thread.archive': 'Archivieren',
  'thread.delete': 'Löschen',
  'thread.restore': 'Aus Papierkorb wiederherstellen',
  'thread.markUnread': 'Als ungelesen markieren',
  'thread.snooze': 'Zurückstellen',
  'thread.move': 'Verschieben',
  'thread.label': 'Labels',
  'thread.moreActions': 'Weitere Aktionen',

  // ── Thread reader: selbst verfasste Karte ────────────────────────────
  'mail.thread.fromYou': 'Du',

  // ── Message-scope strings ────────────────────────────────────────────
  // Siehe re #98: die gesamte per-Nachricht-Aktionsfläche wurde entfernt.
  // Antworten / Weiterleiten leben in der festen Antwortleiste; Konversa-
  // tions-Verben (Stummschalten, Spam, Phishing, blockieren, Wiederher-
  // stellen, archivieren, ungelesen markieren, Snooze, verschieben, Label,
  // Drucken) leben in der Konversations-Symbolleiste; Reaktionen sind in
  // der Titelzeile der Nachricht.
  'msg.reply': 'Antworten',
  'msg.replyAll': 'Allen antworten',
  'msg.forward': 'Weiterleiten',
  'msg.react': 'Reagieren',
  'msg.muteThread': 'Konversation stummschalten',
  'msg.unmuteThread': 'Stummschaltung aufheben',
  'msg.reportSpam': 'Spam melden',
  'msg.reportPhishing': 'Phishing melden',
  'msg.blockSender': 'Absender blockieren',
  'msg.imagesBlocked': 'Externe Bilder werden blockiert.',
  'msg.loadImages': 'Bilder laden',
  'msg.alwaysFrom': 'Immer von {sender}',
  'msg.noBody': '(kein Inhalt)',
  'msg.noSender': '(kein Absender)',
  'msg.recipientsTo': 'an {first}',
  'msg.recipientsToMany': 'an {first} und {others} weitere Person',
  'msg.recipientsToManyOther': 'an {first} und {others} weitere Personen',

  // ── Per-message kebab menu (REQ-UI-21b, REQ-MAIL-130..142) ──────────
  'msg.kebab.openLabel': 'Weitere Aktionen',
  'msg.kebab.markUnread': 'Als ungelesen markieren',
  'msg.kebab.markUnreadFromHere': 'Ab hier als ungelesen markieren',
  'msg.kebab.delete': 'Diese Nachricht löschen',

  // ── Compose ─────────────────────────────────────────────────────────
  'compose.title.new': 'Neue Nachricht',
  'compose.title.reply': 'Antwort',
  'compose.title.forward': 'Weiterleitung',
  'compose.minimize': 'Minimieren',
  'compose.close': 'Verfassen schließen',
  'compose.from': 'Von',
  'compose.to': 'An',
  'compose.cc': 'Cc',
  'compose.bcc': 'Bcc',
  'compose.subject': 'Betreff',
  'compose.body': 'Nachricht',
  'compose.toggleCcBcc': 'Cc / Bcc',
  'compose.send': 'Senden',
  'compose.sending': 'Senden...',
  'compose.discard': 'Verwerfen',
  'compose.attach': 'Anhängen',
  'compose.attached': 'Anhänge',
  'compose.dropToAttach': 'Hier ablegen zum Anhängen',
  'compose.dropInline': 'Bild hier ablegen zum Einbetten',
  'compose.dropAttach': 'Datei hier ablegen zum Anhängen',
  'compose.moveToAttachments': 'Als Anhang verschieben',
  'compose.discardConfirm.title': 'Diese Nachricht verwerfen?',
  'compose.discardConfirm.message': 'Der Entwurf geht verloren.',
  'compose.discardConfirm.confirm': 'Verwerfen',
  'compose.discardConfirm.cancel': 'Weiter bearbeiten',
  'compose.from.picker.aria': 'Absender-Identität',
  'compose.from.picker.open': 'Absender-Identität ändern',
  'compose.from.chip.verifying': 'Bestätigung ausstehend',
  'compose.from.chip.unverified': 'Nicht bestätigt',
  'compose.from.chip.external': 'Externer SMTP fehlt',
  'compose.from.disabled.unverified': 'Bestätigen Sie diese Adresse, bevor Sie damit senden können.',
  'compose.from.disabled.verifying': 'Warte auf Bestätigung dieser Adresse.',
  'compose.from.disabled.external': 'Richten Sie externe SMTP-Übermittlung ein, bevor Sie von dieser Adresse senden.',
  'compose.from.sendDisabled.unverified': 'Bestätigen Sie diese Identität, bevor Sie senden.',
  'compose.from.sendDisabled.verifying': 'Diese Identität wartet auf Bestätigung — Senden noch nicht möglich.',
  'compose.from.sendDisabled.external': 'Externe SMTP-Übermittlung ist für diese Identität nicht eingerichtet.',
  'compose.from.sendDisabled.noIdentity': 'Keine Identität zum Senden verfügbar.',

  // ── Formatierungsleiste im Editor (re #97) ──────────────────────────
  'composeToolbar.aria': 'Formatierung',
  'composeToolbar.bold': 'Fett',
  'composeToolbar.italic': 'Kursiv',
  'composeToolbar.underline': 'Unterstrichen',
  'composeToolbar.bulletList': 'Aufzählung',
  'composeToolbar.orderedList': 'Nummerierte Liste',
  'composeToolbar.blockquote': 'Zitatblock',
  'composeToolbar.linkAdd': 'Link einfügen',
  'composeToolbar.linkRemove': 'Link entfernen',
  'composeToolbar.linkPrompt.title': 'Link einfügen',
  'composeToolbar.linkPrompt.confirm': 'Einfügen',
  'composeToolbar.image': 'Bild einfügen',
  'composeToolbar.imageBadType': 'Bitte Bilddateien auswählen (PNG, JPEG, GIF, WebP).',
  'composeToolbar.imageUploadFailed': 'Hochladen fehlgeschlagen: {name}',

  // ── Attachment list ──────────────────────────────────────────────────
  'att.attachments': '{count} Anhang',
  'att.attachments.other': '{count} Anhänge',
  'att.downloadAll': 'Alle herunterladen ({count})',
  'att.attachmentsOnly': 'Nur Anhänge',
  'att.download': 'Herunterladen',
  'att.noUrl': 'Keine URL',
  'att.view': 'Anzeigen',
  'att.close': 'Schließen',
  'att.headerIcon.label': 'Hat Anhang',
  'att.aria.open': '{name} öffnen',
  'att.aria.download': '{name} herunterladen',
  'mail.trimmed.hide': 'Zitat ausblenden',

  // ── Pickers (move / label / etc.) ────────────────────────────────────
  'picker.close': 'Schließen',
  'movePicker.title.single': 'Label wählen',
  'movePicker.title.bulk': '{count} Nachricht verschieben nach',
  'movePicker.title.bulk.other': '{count} Nachrichten verschieben nach',
  'movePicker.filter': 'Label filtern...',
  'movePicker.empty': 'Keine anderen Labels verfügbar.',
  'movePicker.empty.filter': 'Keine Labels passen zu "{filter}".',
  'labelPicker.title.single': 'Labels zuweisen',
  'labelPicker.title.bulk': '{count} Nachricht mit Labels versehen',
  'labelPicker.title.bulk.other': '{count} Nachrichten mit Labels versehen',
  'labelPicker.filter': 'Label filtern oder anlegen...',
  'labelPicker.empty':
    'Noch keine Labels. Tippe oben einen Namen ein, um ein Label anzulegen.',
  'labelPicker.empty.filter': 'Keine Labels passen zu "{filter}".',
  'labelPicker.create': 'Label "{name}" anlegen',
  'labelPicker.done': 'Fertig',

  // ── Categories settings ──────────────────────────────────────────────
  'cat.currentCategories': 'Aktuelle Kategorien',
  'cat.currentCategories.hint':
    'Dies sind die Kategorien, die das Sprachmodell aktuell verwendet, abgeleitet aus dem Prompt oben. Bearbeite den Prompt, um sie zu ändern.',
  'cat.currentCategories.empty':
    'Noch keine Kategorien. Kategorien erscheinen hier, sobald die nächste Nachricht klassifiziert wurde.',
  'cat.prompt.heading': 'Klassifizierungs-Prompt',
  'cat.prompt.hint':
    'Der Prompt, den das Sprachmodell zur Klassifizierung deiner Nachrichten in Kategorien verwendet. Anpassungen wirken sich auf zukünftige (und neu klassifizierte) Nachrichten aus. Max. 32 KB.',
  'cat.prompt.reset': 'Auf Standard zurücksetzen',
  'cat.prompt.save': 'Prompt speichern',
  'cat.disclosure.heading': 'Wie deine Nachrichten klassifiziert werden',
  'cat.disclosure.hint':
    'Dies ist der Prompt, der zur Kategorisierung deiner E-Mails verwendet wird. Deine Nachrichten werden zusammen mit diesem Prompt an den konfigurierten Klassifizierungs-Endpunkt von herold gesendet.',
  'cat.recategorise.heading': 'Posteingang neu klassifizieren',
  'cat.recategorise.hint':
    'Führt die Klassifizierung für deinen letzten Posteingang erneut aus (bis zu 1000 Nachrichten). Die Ergebnisse werden im Hintergrund aktualisiert.',
  'cat.recategorise.run': 'Posteingang neu klassifizieren',
  'cat.recategorise.running': 'Läuft...',
  'cat.recategorise.notAvailable': 'Auf diesem Server nicht verfügbar.',
  'cat.recategorise.inProgress':
    'Neu-Klassifizierung läuft -- Ergebnisse werden automatisch aktualisiert.',
  'cat.recategorise.runTitle': 'Aktuellen Posteingang neu klassifizieren',
  'cat.recategorise.disabledTitle':
    'Massen-Neu-Klassifizierung ist auf diesem Server nicht aktiviert',
  'cat.disclosure.defaultNotLoaded': '(Standard-Prompt -- noch nicht geladen.)',
  'cat.prompt.resetTitle': 'Auf den ausgelieferten Standard-Prompt zurücksetzen',

  // ── Settings ────────────────────────────────────────────────────────
  'settings.title': 'Einstellungen',
  'settings.account': 'Konto',
  'settings.security': 'Sicherheit',
  'settings.appearance': 'Darstellung',
  'settings.mail': 'E-Mail',
  'settings.theme': 'Erscheinungsbild',
  'settings.theme.system': 'System',
  'settings.theme.light': 'Hell',
  'settings.theme.dark': 'Dunkel',
  'settings.language': 'Sprache',
  'settings.language.en': 'Englisch',
  'settings.language.de': 'Deutsch',
  'settings.displayName.label': 'Anzeigename',
  'settings.displayName.helper':
    "Wird im Von-Feld ausgehender E-Mails als 'Name' <Adresse> angezeigt.",
  'settings.save': 'Speichern',
  'settings.saved': 'Einstellungen gespeichert',
  'settings.saveFailed': 'Einstellungen konnten nicht gespeichert werden',
  'settings.avatar.title': 'Profilbild',
  'settings.avatar.change': 'Ändern…',
  'settings.avatar.remove': 'Entfernen',
  'settings.avatar.pickNew': 'Datei auswählen',
  'settings.avatar.applyToAll.title':
    'Dieses Profilbild für alle Identitäten verwenden?',
  'settings.avatar.applyToAll.message':
    'Sie haben {count} Identitäten. Dieses Bild auf alle anwenden?',
  'settings.avatar.applyToAll.confirm': 'Ja, auf alle anwenden',
  'settings.avatar.applyToAll.cancel': 'Nur diese Identität',
  'settings.avatar.xface.title': 'Profilbild in ausgehender Post einbetten?',
  'settings.avatar.xface.message':
    'Wenn aktiviert, wird Ihr Profilbild als X-Face / Face-Header an jede Nachricht angehängt, die Sie von dieser Identität senden. Empfänger mit E-Mail-Clients, die diese Header unterstützen, sehen Ihr Bild neben Ihrem Namen.',
  'settings.avatar.xface.confirm': 'Ja, aktivieren',
  'settings.avatar.xface.cancel': 'Nein, danke',
  'settings.avatar.upload.failed': 'Hochladen fehlgeschlagen: {reason}',
  'settings.avatar.upload.tooLarge':
    'Bild ist nach Komprimierung zu groß. Bitte ein kleineres wählen.',
  // Profilbild-Aufnahme / Zuschnitt-Dialog (re #97)
  'settings.avatar.capture.title': 'Profilbild auswählen',
  'settings.avatar.capture.dropPrompt': 'Bild hierher ziehen oder eine Quelle wählen:',
  'settings.avatar.capture.chooseFile': 'Datei wählen',
  'settings.avatar.capture.takePhoto': 'Foto aufnehmen',
  'settings.avatar.capture.cameraUnavailable': 'Kamera nicht verfügbar',
  'settings.avatar.capture.snapshotFailedNoCtx': 'Aufnahme fehlgeschlagen: kein 2D-Kontext',
  'settings.avatar.capture.snapshotFailedEmpty': 'Aufnahme fehlgeschlagen: leeres Canvas',
  'settings.avatar.capture.decodeFailed': 'Bild konnte nicht dekodiert werden',
  'settings.avatar.capture.decodeFailedReason': 'Bild konnte nicht dekodiert werden: {reason}',
  'settings.avatar.capture.emptyFile': 'Bild konnte nicht dekodiert werden: die Datei ist leer',
  'settings.avatar.capture.takePhotoTitle': 'Foto aufnehmen',
  'settings.avatar.capture.back': 'Zurück',
  'settings.avatar.capture.snapshot': 'Aufnehmen',
  'settings.avatar.capture.cropTitle': 'Bild zuschneiden',
  'settings.avatar.capture.reset': 'Zurücksetzen',
  'settings.avatar.capture.useThis': 'Übernehmen',
  'settings.avatar.capture.sourceAlt': 'Quelle',

  // Identitäts-Bearbeitungs-Dialog (re #97)
  'settings.identityEdit.title': 'Identität bearbeiten',
  'settings.identityEdit.discardConfirm':
    'Ungespeicherte Änderungen verwerfen?',
  'settings.identityEdit.saveAll': 'Speichern',
  'settings.identityEdit.cancel': 'Abbrechen',
  'settings.identityEdit.replyTo': 'Antwort-an-Adresse',
  'settings.identityEdit.replyToHelper':
    'Optional. Wenn gesetzt, gehen Antworten an diese Adresse statt an die Von-Adresse.',
  'settings.identityEdit.bcc': 'Bcc-Adresse (Auto-Bcc beim Senden)',
  'settings.identityEdit.bccHelper':
    'Optional. Jede aus dieser Identität gesendete Nachricht wird still an diese Adresse kopiert.',
  'settings.identityEdit.signaturePlain': 'Signatur (Klartext)',
  'settings.identityEdit.signatureHtml': 'Signatur (HTML)',
  'settings.identityEdit.signatureHtmlHelper':
    'Rohes HTML, das an ausgehende HTML-Nachrichten angehängt wird. Leer lassen, um auf die Klartext-Signatur zurückzugreifen.',
  'settings.identityEdit.invalidEmail':
    'Gültige E-Mail-Adresse eingeben oder leer lassen.',
  'settings.identityEdit.back': 'Zurück zu den Identitäten',
  'settings.identityEdit.notFound': 'Diese Identität wurde nicht gefunden.',
  'settings.identityEdit.saving': 'Wird gespeichert…',
  'settings.identityEdit.saved': 'Gespeichert',
  'settings.identityEdit.saveFailed': 'Speichern fehlgeschlagen',
  'settings.identityEdit.replyToHeading': 'Antwort-an und Bcc',

  // Identitäten-Liste (REQ-SET-IDENT-01..08, re #20)
  'settings.identityList.heading': 'Identitäten',
  'settings.identityList.addBtn': 'Identität hinzufügen',
  'settings.identityList.addTooltip': 'Neue Absende-Identität hinzufügen',
  'settings.identityList.defaultRadioAria':
    '{email} als Standard festlegen',
  'settings.identityList.defaultRadioDisabledTitle':
    'Nur verifizierte Identitäten können Standard sein.',
  'settings.identityList.editRowAria': '{email} bearbeiten',
  'settings.identityList.editBtn': 'Bearbeiten',
  'settings.identityList.defaultColumn': 'Standard',
  'settings.identityList.identityColumn': 'Identität',
  'settings.identityList.defaultBadge': 'Standard',
  'settings.identityList.chip.verified': 'Verifiziert',
  'settings.identityList.chip.verifying': 'Verifikation ausstehend',
  'settings.identityList.chip.unverified': 'Nicht verifiziert',
  'settings.identityList.verifyBtn': 'Verifizieren',
  'settings.identityList.verifyTooltip': 'Bestätigungscode eingeben',
  'settings.identityList.resendBtn': 'Erneut senden',
  'settings.identityList.resendTooltip': 'Bestätigungs-E-Mail erneut senden',
  'settings.identityList.externalDisabledTitle':
    'Externer SMTP-Versand ist für diese Identität nicht konfiguriert.',
  'settings.identityList.defaultChanged': 'Standard-Identität aktualisiert',
  'settings.identityList.defaultChangeFailed':
    'Standard-Identität konnte nicht geändert werden',
  'settings.identityList.rowMenuAria': 'Aktionen für {email}',
  'settings.identityList.deleteBtn': 'Löschen',
  'settings.identityList.deleteConfirmTitle': 'Diese Identität löschen?',
  'settings.identityList.deleteConfirmMessage':
    'Die Identität {email} löschen? Sie können dann nicht mehr von dieser Adresse senden. Dies kann nicht rückgängig gemacht werden.',
  'settings.identityList.deleteConfirm': 'Identität löschen',
  'settings.identityList.deleted': 'Identität {email} gelöscht',
  'settings.identityList.deleteFailed': 'Identität konnte nicht gelöscht werden',

  // Identitäten-Assistent (REQ-SET-IDENT-30..33, re #21)
  'settings.identityWizard.title': 'Identität hinzufügen',
  'settings.identityWizard.close': 'Schließen',
  'settings.identityWizard.cancel': 'Abbrechen',
  'settings.identityWizard.next': 'Weiter',
  'settings.identityWizard.back': 'Zurück',
  'settings.identityWizard.done': 'Fertig',
  'settings.identityWizard.skip': 'Überspringen',
  'settings.identityWizard.create': 'Erstellen',
  'settings.identityWizard.creating': 'Erstelle…',
  'settings.identityWizard.step1Title': 'Adresse',
  'settings.identityWizard.step1Intro':
    'Geben Sie die E-Mail-Adresse an, für die diese Identität gelten soll. Wir senden eine Bestätigungsnachricht; Sie geben den 6-stelligen Code ein oder folgen dem Link in der Nachricht.',
  'settings.identityWizard.emailLabel': 'E-Mail-Adresse',
  'settings.identityWizard.emailHelper':
    'Die Adresse, von der aus Sie senden möchten.',
  'settings.identityWizard.emailInvalid': 'Gültige E-Mail-Adresse eingeben.',
  'settings.identityWizard.displayNameLabel': 'Anzeigename (optional)',
  'settings.identityWizard.displayNameHelper':
    'Wird Empfängern neben der E-Mail-Adresse angezeigt.',
  'settings.identityWizard.domainBlocked':
    'Dieser Server erlaubt keine Identitäten für {domain} — bitte wenden Sie sich an Ihre Administrator:in.',
  'settings.identityWizard.emailExists':
    'Für diese E-Mail-Adresse existiert bereits eine Identität.',
  'settings.identityWizard.createFailed':
    'Identität konnte nicht erstellt werden. Bitte erneut versuchen.',
  'settings.identityWizard.step2Title': 'Adresse bestätigen',
  'settings.identityWizard.step2Intro':
    'Wir haben eine Bestätigungs-E-Mail an {email} gesendet. Klicken Sie auf den Link in der Nachricht oder geben Sie den 6-stelligen Code unten ein.',
  'settings.identityWizard.codeLabel': 'Bestätigungscode',
  'settings.identityWizard.codeHelper':
    '6 Ziffern — führende Nullen einschließen. Codes laufen nach 24 Stunden ab.',
  'settings.identityWizard.codeInvalid': 'Genau 6 Ziffern eingeben.',
  'settings.identityWizard.codeWrong': 'Code stimmt nicht. Bitte E-Mail prüfen und erneut versuchen.',
  'settings.identityWizard.verify': 'Verifizieren',
  'settings.identityWizard.verifying': 'Verifiziere…',
  'settings.identityWizard.resend': 'E-Mail erneut senden',
  'settings.identityWizard.resending': 'Sende erneut…',
  'settings.identityWizard.resendOk': 'Bestätigungs-E-Mail erneut gesendet.',
  'settings.identityWizard.resendRateLimited': 'In {seconds} s erneut versuchen.',
  'settings.identityWizard.resendRateLimitedShort':
    'Bitte vor erneutem Senden warten.',
  'settings.identityWizard.cancelPendingNotice':
    'Beim Schließen bleibt die Identität in der „Verifikation ausstehend"-Liste — Sie können die Verifikation später in den Einstellungen abschließen.',
  'settings.identityWizard.discardPending': 'Verwerfen',
  'settings.identityWizard.keepPending': 'Ausstehend lassen',
  'settings.identityWizard.step3Title': 'Externes SMTP konfigurieren',
  'settings.identityWizard.step3Intro':
    'Diese Identität nutzt eine externe Domain. Konfigurieren Sie SMTP-Zugangsdaten, damit ausgehende E-Mails über {domain} gesendet werden.',
  'settings.identityWizard.step3SkipNote':
    'Sie können den SMTP-Versand später im Identitäten-Editor konfigurieren.',
  'settings.identityWizard.smtpHost': 'SMTP-Host',
  'settings.identityWizard.smtpPort': 'Port',
  'settings.identityWizard.smtpUser': 'Benutzername',
  'settings.identityWizard.smtpPassword': 'Passwort',
  'settings.identityWizard.smtpSecurity': 'Sicherheit',
  'settings.identityWizard.smtpSecurityStartTLS': 'STARTTLS',
  'settings.identityWizard.smtpSecurityImplicit': 'Implizites TLS',
  'settings.identityWizard.smtpSecurityNone': 'Keine (Klartext)',
  'settings.identityWizard.smtpHostRequired': 'Host wird benötigt.',
  'settings.identityWizard.smtpUserRequired': 'Benutzername wird benötigt.',
  'settings.identityWizard.smtpPasswordRequired': 'Passwort wird benötigt.',
  'settings.identityWizard.smtpPortInvalid':
    'Port muss zwischen 1 und 65535 liegen.',
  'settings.identityWizard.smtpSaveError':
    'SMTP-Einstellungen konnten nicht gespeichert werden: {message}',
  'settings.identityWizard.successToast': '{email} verifiziert',
  'settings.identityWizard.createdToast':
    'Bestätigungs-E-Mail an {email} gesendet',

  // Verifizierungs-Dialog (REQ-SET-IDENT-20..21, re #21)
  'settings.identityVerify.title': 'Identität verifizieren',
  'settings.identityVerify.close': 'Schließen',
  'settings.identityVerify.intro':
    'Wir haben eine Bestätigungsnachricht an {email} gesendet. Klicken Sie auf den Link in der Nachricht oder geben Sie den 6-stelligen Code unten ein.',
  'settings.identityVerify.codeLabel': 'Bestätigungscode',
  'settings.identityVerify.verify': 'Verifizieren',
  'settings.identityVerify.verifying': 'Verifiziere…',
  'settings.identityVerify.resend': 'E-Mail erneut senden',
  'settings.identityVerify.resending': 'Sende erneut…',
  'settings.identityVerify.resendOk': 'Bestätigungs-E-Mail erneut gesendet.',
  'settings.identityVerify.resendRateLimited': 'In {seconds} s erneut versuchen.',
  'settings.identityVerify.resendRateLimitedShort':
    'Bitte vor erneutem Senden warten.',
  'settings.identityVerify.successToast': '{email} verifiziert',
  'settings.identityVerify.cancel': 'Abbrechen',

  // Filter-Formular (re #97)
  'settings.filters.loading': 'Filter werden geladen…',
  'settings.filters.heading2': 'Filter',
  'settings.filters.create': 'Filter erstellen',
  'settings.filters.edit': 'Filter bearbeiten',
  'settings.filters.save': 'Filter speichern',
  'settings.filters.created': 'Filter erstellt',
  'settings.filters.updated': 'Filter aktualisiert',
  'settings.filters.intro':
    'Filter werden auf eingehende E-Mails in ihrer Reihenfolge angewendet (kleinste Ordnungsnummer zuerst). Mehrere Bedingungen werden mit UND verknüpft. Der rohe Sieve-Editor in den "Mail"-Einstellungen funktioniert parallel zu diesen strukturierten Filtern; beide Quellen koexistieren ohne Konflikt.',
  'settings.filters.empty': 'Noch keine Filter.',
  'settings.filters.unnamed': '(unbenannt)',
  'settings.filters.summaryIf': 'Wenn {conditions} → {actions}',
  'settings.filters.moveUp': 'Filter nach oben',
  'settings.filters.moveDown': 'Filter nach unten',
  'settings.filters.enabled': 'Aktiviert',
  'settings.filters.disabled': 'Deaktiviert',
  'settings.filters.editBtn': 'Bearbeiten',
  'settings.filters.deleteBtn': 'Löschen',
  'settings.filters.deleteAria': 'Filter löschen',
  'settings.filters.confirmDelete': 'Diesen Filter löschen?',
  'settings.filters.nameLabel': 'Name',
  'settings.filters.nameOptional': '(optional)',
  'settings.filters.namePlaceholder': 'Mein Filter',
  'settings.filters.enabledCheckbox': 'Aktiviert',
  'settings.filters.conditionsHeading': 'Bedingungen (UND verknüpft)',
  'settings.filters.addCondition': 'Bedingung hinzufügen',
  'settings.filters.noConditions': 'Keine Bedingungen — passt auf alle E-Mails.',
  'settings.filters.actionsHeading': 'Aktionen',
  'settings.filters.addAction': 'Aktion hinzufügen',
  'settings.filters.noActions': 'Keine Aktionen — der Filter trifft zu, tut aber nichts.',
  'settings.filters.removeCondition': 'Bedingung entfernen',
  'settings.filters.removeAction': 'Aktion entfernen',
  'settings.filters.conditionField': 'Bedingungs-Feld',
  'settings.filters.conditionOperator': 'Bedingungs-Operator',
  'settings.filters.conditionValue': 'Bedingungs-Wert',
  'settings.filters.actionKind': 'Aktions-Typ',
  'settings.filters.labelName': 'Label-Name',
  'settings.filters.forwardAddress': 'Weiterleitungs-Adresse',
  'settings.filters.forwardPlaceholder': 'weiterleiten@example.com',
  'settings.filters.fromPlaceholder': 'user@example.com oder *@example.com',
  'settings.filters.boolHasAttachment': '(Boolesch — passt auf Nachrichten mit Anhängen)',
  'settings.filters.test': 'Mit vorhandener Post testen',
  'settings.filters.testing': 'Wird getestet…',
  'settings.filters.testResult': 'Trifft auf {count} Konversationen in Ihrem Postfach zu.',
  'settings.filters.testResultOne': 'Trifft auf {count} Konversation in Ihrem Postfach zu.',
  'settings.filters.errMinCondition': 'Mindestens eine Bedingung ist erforderlich.',
  'settings.filters.errMinAction': 'Mindestens eine Aktion ist erforderlich.',
  'settings.filters.errDeleteApplyLabel':
    '"In Papierkorb verschieben" und "Label zuweisen" können nicht kombiniert werden: Die Lösch-Aktion bricht ab und das Label wird nie zugewiesen.',
  'settings.filters.errEmptyValue': 'Alle Bedingungs-Werte müssen ausgefüllt sein.',
  'settings.filters.errForwardRequired': 'Eine Weiterleitungs-Adresse ist erforderlich.',
  'settings.filters.errLabelRequired': 'Label-Name ist für die Aktion "Label zuweisen" erforderlich.',
  'settings.filters.summaryHasAttachment': 'hat Anhang',
  'settings.filters.summaryNoConditions': '(keine Bedingungen)',
  'settings.filters.summaryNoActions': '(keine Aktionen)',
  'settings.filters.summaryApplyLabel': 'Label zuweisen: {label}',
  'settings.filters.summaryForward': 'Weiterleiten an: {to}',
  'settings.filters.blockedHeading': 'Blockierte Absender',
  'settings.filters.blockedHint':
    'Nachrichten von blockierten Absendern werden bei Eintreffen in den Papierkorb verschoben. Verwenden Sie die Option "Absender blockieren" im Menü einer Nachricht, um einen Absender zu blockieren.',
  'settings.filters.blockedEmpty': 'Keine Absender blockiert.',
  'settings.filters.unblock': 'Entsperren',
  'settings.filters.field.from': 'Von',
  'settings.filters.field.to': 'An',
  'settings.filters.field.subject': 'Betreff',
  'settings.filters.field.fromDomain': 'Absender-Domain',
  'settings.filters.field.hasAttachment': 'Hat Anhang',
  'settings.filters.field.threadId': 'Konversations-ID',
  'settings.filters.op.contains': 'enthält',
  'settings.filters.op.equals': 'gleich',
  'settings.filters.op.wildcard': 'Platzhalter-Übereinstimmung',
  'settings.filters.action.skipInbox': 'Posteingang überspringen (bei Eingang archivieren)',
  'settings.filters.action.markRead': 'Als gelesen markieren',
  'settings.filters.action.applyLabel': 'Label zuweisen',
  'settings.filters.action.delete': 'In Papierkorb verschieben',
  'settings.filters.action.forward': 'An Adresse weiterleiten',

  // Sieve-Formular (re #97)
  'settings.sieve.noAccount': 'Kein Mail-Konto in dieser Sitzung',
  'settings.sieve.loadFailed': 'Sieve-Skript konnte nicht geladen werden',
  'settings.sieve.saveFailedReason': 'Speichern fehlgeschlagen: {reason}',
  'settings.sieve.saveFailed': 'Speichern fehlgeschlagen',
  'settings.sieve.saved': 'Sieve-Skript gespeichert',
  'settings.sieve.intro':
    'Sieve-Skripte werden serverseitig auf eingehende E-Mails angewendet. Phase 1 liefert ein einziges aktives Skript pro Konto; der Editor unten zeigt den rohen RFC-5228-/9007-Quelltext. Siehe {rfcLink} für die Sprache.',
  'settings.sieve.rfcLinkLabel': 'RFC 5228',
  'settings.sieve.name': 'Name',
  'settings.sieve.namePlaceholder': 'default',
  'settings.sieve.script': 'Skript',
  'settings.sieve.validationHeading': 'Sieve-Validierungsfehler:',

  // ── Diagnoseeinstellungen (REQ-CLOG-06) ──────────────────────────────
  'settings.diagnostics.heading': 'Diagnose',
  'settings.diagnostics.telemetry.label':
    'Anonyme Diagnoseprotokolle an meinen Mail-Server-Betreiber senden',

  // ── Datenschutzeinstellungen ──────────────────────────────────────────
  'settings.privacy.avatarLookup.label':
    'Absenderbilder aus E-Mail-Metadaten abrufen (Gravatar / X-Face / Face)',
  'settings.privacy.avatarLookup.hint':
    'Wenn aktiviert, kontaktiert die Suite Gravatar mit einem Einweg-Hash der E-Mail-Adresse jedes Absenders, um deren Bild abzurufen. Deaktivieren, um alle Absendersuchen lokal zu halten.',
  'settings.privacy.avatarLookup.confirmTitle':
    'Absenderbilder aus dem öffentlichen Web abrufen?',
  'settings.privacy.avatarLookup.confirmBody':
    'Wenn aktiviert, kontaktiert die Suite Gravatar mit einem Einweg-Hash der E-Mail-Adresse jedes Absenders. Der Absender sieht diese Abfrage nicht, aber Gravatars Protokolle schon. Die E-Mail-Adresse des Absenders verlässt Ihr Gerät nie im Klartext. Sie können dies jederzeit deaktivieren.',
  'settings.privacy.avatarLookup.confirmEnable': 'Suche aktivieren',
  'settings.privacy.avatarLookup.confirmCancel': 'Nur lokal bleiben',

  // ── Kontakt-Hovercard (REQ-MAIL-46) ─────────────────────────────────
  'contact.card.add': 'Kontakt hinzufügen',
  'contact.card.edit': 'Kontakt bearbeiten',
  'contact.card.copy': 'E-Mail-Adresse kopieren',
  'contact.card.copied': 'Kopiert',
  'contact.card.sendEmail': 'E-Mail senden',
  'contact.card.chat': 'Chat',
  'contact.card.video': 'Videoanruf starten',
  'contact.card.calendar': 'Termin planen',
  'contact.card.viewDetail': 'Detaillierte Ansicht anzeigen',
  'contact.view.edit': 'Bearbeiten',
  'contact.view.save': 'Speichern',
  'contact.view.cancel': 'Abbrechen',
  'contact.view.name': 'Name',
  'contact.view.email': 'E-Mail',
  'contact.view.saveSuccess': 'Kontakt gespeichert',
  'contact.view.saveError': 'Kontakt konnte nicht gespeichert werden',
  'contact.phone.mobile': 'Mobil',
  'contact.phone.work': 'Arbeit',
  'contact.phone.home': 'Privat',
  'contact.phone.fax': 'Fax',
  'contact.phone.other': 'Sonstige',

  // ── App switcher ────────────────────────────────────────────────────
  'app.mail': 'Mail',
  'app.calendar': 'Kalender',
  'app.contacts': 'Kontakte',
  'app.chat': 'Chat',
  'app.admin': 'Server-Verwaltung',
  'app.switch': 'Suite-Komponente wechseln',
  'shell.profile.menu': 'Konto-Menü',

  // ── Zeit ────────────────────────────────────────────────────────────
  'time.justNow': 'gerade eben',

  // ── Handbuch (re #58) ────────────────────────────────────────────────
  'sidebar.help': 'Hilfe',
  'manual.loading': 'Hilfe wird geladen...',
  'manual.loadError': 'Handbuch konnte nicht geladen werden.',
  'manual.empty': 'Kein Hilfeinhalt verfügbar.',
  'manual.toc.label': 'Inhaltsverzeichnis',
  'manual.onthispage.label': 'Auf dieser Seite',
  'manual.search.placeholder': 'Themen suchen...',
  'manual.search.label': 'Handbuch durchsuchen',
  'manual.search.noResults': 'Keine passenden Themen.',
  'manual.callout.info': 'Hinweis',
  'manual.callout.warning': 'Warnung',
  'manual.callout.caution': 'Vorsicht',
  'manual.req.label': 'Anforderung',
  'manual.code.copy': 'Kopieren',
  'manual.code.copied': 'Kopiert',

  // ── Drei-Punkte-Menü ────────────────────────────────────────────────
  // Wird vom ActionOverflowMenu in der Konversations-Symbolleiste
  // (re #60) und im Nachrichtenkopf (re #98) verwendet.
  'actions.moreActions': 'Weitere Aktionen',

  // ── Common ──────────────────────────────────────────────────────────
  'common.cancel': 'Abbrechen',
  'common.confirm': 'Bestätigen',
  'common.save': 'Speichern',
  'common.saving': 'Wird gespeichert...',
  'common.loading': 'Wird geladen...',
  'common.retry': 'Erneut versuchen',
  'common.revert': 'Zurücksetzen',
  'common.remove': 'Entfernen',
  'common.create': 'Erstellen',
  'common.copy': 'Kopieren',
  'common.copied': 'Kopiert',
  'common.close': 'Schließen',
  'common.back': 'Zurück',

  // ── Einstellungen: Bereichsüberschriften (re #97) ────────────────────
  'settings.about': 'Über',
  'settings.notifications': 'Benachrichtigungen',
  'settings.apiKeys': 'API-Schlüssel',
  'settings.privacy.heading': 'Datenschutz',
  'settings.categories.heading': 'Kategorien',
  'settings.filters.heading': 'Filter',

  // ── Einstellungen: Konto-Bereich (re #97) ────────────────────────────
  'settings.account.signedInAs': 'Angemeldet als',
  'settings.account.signOut': 'Abmelden',
  'settings.account.identitiesHeading': 'Identitäten & Signaturen',
  'settings.account.noIdentities': 'Noch keine Identitäten geladen.',
  'settings.account.authFailedTitle':
    'Authentifizierung fehlgeschlagen — klicken zum erneuten Anmelden',
  'settings.account.unreachableTitle':
    'Externer Server nicht erreichbar — klicken zum Überprüfen der Konfiguration',
  'settings.account.authFailedBadge': 'Anmeldung fehlgeschlagen',
  'settings.account.unreachableBadge': 'Nicht erreichbar',
  'settings.account.externalBadge': 'Extern',
  'settings.account.externalBadgeTitle':
    'Externer SMTP-Versand konfiguriert — klicken zum Bearbeiten',
  'settings.account.configureExternal': 'Externen SMTP einrichten',
  'settings.account.configureExternalTitle': 'Externen SMTP-Versand einrichten',
  'settings.account.extSubHint':
    'Externer SMTP-Versand (z. B. Gmail oder Microsoft 365) ist auf diesem Server nicht aktiviert. Um ausgehende Mail über einen externen Anbieter zu leiten, kann ein Operator dies in {systemToml} aktivieren — siehe {docPath}.',

  // ── Einstellungen: Darstellung (re #97) ──────────────────────────────
  'settings.appearance.themeHint':
    'System folgt der Systemeinstellung und passt sich live an, wenn Sie sie umschalten.',

  // ── Einstellungen: Mail-Bereich (re #97) ─────────────────────────────
  'settings.mail.undoSendWindow': 'Senden-rückgängig-Zeitfenster',
  'settings.mail.undoSendOff': 'Aus (sofortiger Versand)',
  'settings.mail.undoSendValue': '{seconds} s',
  'settings.mail.undoSendAria': 'Sekunden vor dem Versand',
  'settings.mail.undoSendHint':
    'Wenn aktiv, werden Nachrichten serverseitig zurückgehalten; der Toast-Knopf "Rückgängig" bricht den Versand ab.',
  'settings.mail.swipeLeft': 'Aktion bei Wischen nach links',
  'settings.mail.swipeRight': 'Aktion bei Wischen nach rechts',
  'settings.mail.swipeTouch': '(Touch)',
  'settings.mail.swipe.archive': 'Archivieren',
  'settings.mail.swipe.snooze': 'Zurückstellen',
  'settings.mail.swipe.delete': 'Löschen',
  'settings.mail.swipe.markRead': 'Als gelesen markieren',
  'settings.mail.swipe.label': 'Label…',
  'settings.mail.swipe.none': 'Keine',
  'settings.mail.vacationHeading': 'Abwesenheits-Auto-Antwort',
  'settings.mail.sieveHeading': 'Sieve-Filter',
  'settings.mail.spamHeading': 'Spam-Klassifizierer',
  'settings.mail.spamHint':
    'Der Prompt, der zur Klassifizierung Ihrer eingehenden Nachrichten als Spam verwendet wird. Ihre Nachrichten werden zusammen mit diesem Prompt an den konfigurierten Klassifizierungs-Endpunkt von Herold gesendet.',
  'settings.mail.spamLoading': 'Wird geladen…',
  'settings.mail.spamLoadError': 'Konnte nicht geladen werden',
  'settings.mail.spamModelLabel': 'Modell',
  'settings.mail.spamNoPrompt': 'Kein Spam-Prompt konfiguriert.',
  'settings.mail.coachHeading': 'Tastaturkürzel-Hilfe',
  'settings.mail.coachLabel': 'Hinweise einblenden',

  // ── Einstellungen: Benachrichtigungen (re #97) ───────────────────────
  'settings.notifications.soundsLabel': 'Benachrichtigungstöne',
  'settings.notifications.soundsHint':
    'Einen Ton abspielen, wenn eine neue Nachricht oder ein Anruf eingeht und dieser Tab geöffnet ist.',
  'settings.notifications.soundsAria': 'Benachrichtigungstöne',
  'settings.notifications.pushLabel': 'Push-Benachrichtigungen',
  'settings.notifications.pushDeniedHint':
    'Benachrichtigungen sind aus. Sie können sie in den Browser-Einstellungen wieder aktivieren.',
  'settings.notifications.pushForget': 'Entscheidung verwerfen',
  'settings.notifications.pushOnHint': 'Benachrichtigungen sind an.',
  'settings.notifications.pushUpdating': 'Wird aktualisiert…',
  'settings.notifications.pushDisable': 'Benachrichtigungen deaktivieren',
  'settings.notifications.pushOffHint':
    'Erhalten Sie Benachrichtigungen über neue Nachrichten und Anrufe, auch wenn dieser Tab geschlossen ist.',
  'settings.notifications.pushEnabling': 'Wird aktiviert…',
  'settings.notifications.pushEnable': 'Benachrichtigungen aktivieren',
  'settings.notifications.forgetAllLabel': 'Alle Abonnements verwerfen',
  'settings.notifications.forgetAllHint':
    'Entfernt alle Benachrichtigungs-Abonnements für Ihr Konto. Nützlich beim Außerbetriebnehmen eines Geräts.',
  'settings.notifications.forgetAllButton': 'Alle Benachrichtigungs-Abonnements verwerfen',
  'settings.notifications.unavailable':
    'Push-Benachrichtigungen sind auf diesem Server nicht verfügbar.',

  // ── Einstellungen: Datenschutz (re #97) ──────────────────────────────
  'settings.privacy.externalImagesLabel': 'Externe Bilder',
  'settings.privacy.externalImagesAria': 'Laden externer Bilder',
  'settings.privacy.externalImages.never': 'Nie',
  'settings.privacy.externalImages.perSender': 'Pro Absender',
  'settings.privacy.externalImages.always': 'Immer',
  'settings.privacy.externalImagesHint':
    'Externe Bilder können als Lesebestätigungen dienen. {neverEm} blockiert sie standardmäßig; {perSenderEm} lädt nur von Absendern, die Sie freigegeben haben.',
  'settings.privacy.allowedSendersHeading': 'Freigegebene Absender',
  'settings.privacy.allowedSendersEmpty':
    'Noch keine Absender freigegeben. Verwenden Sie "Immer von <Absender>" im Lesebereich, um einen hinzuzufügen.',
  'settings.privacy.autocompleteHeading': 'Autovervollständigungs-Verlauf',
  'settings.privacy.seenAddressesLabel': 'Kürzlich verwendete Adressen merken',
  'settings.privacy.seenAddressesHint':
    'Herold merkt sich pro Konto die Adressen, mit denen Sie korrespondiert haben, um die Empfänger-Autovervollständigung zu ergänzen. Wenn Sie dies deaktivieren, wird der Verlauf sofort gelöscht und es werden keine neuen Einträge mehr hinzugefügt.',
  // Status der Bildverarbeitung (REQ-EXTIMG-BG-32).
  'settings.privacy.imageProcessing.heading': 'Bildverarbeitung',
  'settings.privacy.imageProcessing.pending':
    '{count} Nachricht wartet auf die Internalisierung externer Bilder.',
  'settings.privacy.imageProcessing.pending.other':
    '{count} Nachrichten warten auf die Internalisierung externer Bilder.',
  'settings.privacy.imageProcessing.asOf': 'Stand: {time}.',

  // ── Einstellungen: Über (re #97) ─────────────────────────────────────
  'settings.about.heroldVersion': 'Herold-Version',
  'settings.about.jmapApiUrl': 'JMAP-API-URL',
  'settings.about.eventSourceUrl': 'EventSource-URL',
  'settings.about.sessionState': 'Sitzungsstatus',
  'settings.about.capabilitiesHeading': 'Server-Funktionen',
  'settings.about.noSession': 'Keine Sitzung.',

  // ── Einstellungen: Navigation (re #97) ───────────────────────────────
  'settings.sectionsAria': 'Einstellungs-Bereiche',

  // ── Identitäts-Bearbeitungsformulare (re #97) ────────────────────────
  'settings.identity.signatureLabel': 'Signatur (reiner Text)',
  'settings.identity.signatureSaved': 'Signatur gespeichert',
  'settings.identity.signatureNoAccount': 'Kein Mail-Konto in dieser Sitzung',

  // ── Abwesenheits-Formular (re #97) ───────────────────────────────────
  'settings.vacation.loadFailed': 'Abwesenheits-Antwort konnte nicht geladen werden',
  'settings.vacation.noAccount': 'Kein Mail-Konto in dieser Sitzung',
  'settings.vacation.saveFailed': 'Speichern fehlgeschlagen',
  'settings.vacation.saveFailedReason': 'Speichern fehlgeschlagen: {reason}',
  'settings.vacation.enabled': 'Abwesenheits-Auto-Antwort aktiviert',
  'settings.vacation.disabled': 'Abwesenheits-Auto-Antwort deaktiviert',
  'settings.vacation.autoReply': 'Auto-Antwort',
  'settings.vacation.activeFrom': 'Aktiv ab',
  'settings.vacation.activeFromHint': 'Leer lassen, um sofort beim Aktivieren zu starten.',
  'settings.vacation.activeUntil': 'Aktiv bis',
  'settings.vacation.activeUntilHint': 'Leer lassen für kein Ablaufdatum.',
  'settings.vacation.subject': 'Betreff',
  'settings.vacation.subjectPlaceholder': 'Außer Haus',
  'settings.vacation.body': 'Nachricht',

  // ── Sicherheits-Formular (re #97) ────────────────────────────────────
  'settings.security.loadingSession': 'Sitzung wird geladen...',
  'settings.security.loadingAria': 'Sicherheits-Einstellungen werden geladen',
  'settings.security.intro':
    'Auf dieser Seite verwalten Sie die Anmeldedaten und die Zwei-Faktor-Einstellungen Ihres Kontos. Verwenden Sie sie, um Ihr Passwort zu ändern oder eine TOTP-Authenticator-App für die Zwei-Faktor-Authentifizierung einzurichten.',
  'settings.security.introHint':
    'Die Zwei-Faktor-Authentifizierung fügt beim Anmelden einen zweiten Verifikationsschritt hinzu. Wenn aktiviert, wird zusätzlich zum Passwort die Authenticator-App benötigt. Zum Deaktivieren ist das aktuelle Passwort erforderlich.',
  'settings.security.changePassword': 'Passwort ändern',
  'settings.security.currentPassword': 'Aktuelles Passwort',
  'settings.security.newPassword': 'Neues Passwort',
  'settings.security.confirmNewPassword': 'Neues Passwort bestätigen',
  'settings.security.changePwSubmit': 'Passwort ändern',
  'settings.security.passwordMismatch': 'Die neuen Passwörter stimmen nicht überein.',
  'settings.security.passwordTooShort': 'Das neue Passwort muss mindestens 12 Zeichen lang sein.',
  'settings.security.sessionNotReady': 'Sitzung noch nicht bereit. Bitte neu laden.',
  'settings.security.passwordChanged': 'Passwort geändert.',
  'settings.security.currentPasswordWrong': 'Das aktuelle Passwort ist falsch.',
  'settings.security.twoFactorHeading': 'Zwei-Faktor-Authentifizierung',
  'settings.security.twoFactorEnabled': 'Zwei-Faktor-Authentifizierung ist aktiviert.',
  'settings.security.twoFactorDisabled': 'Zwei-Faktor-Authentifizierung ist nicht aktiviert.',
  'settings.security.disable2faLabel': 'Aktuelles Passwort zum Deaktivieren von 2FA',
  'settings.security.disabling': 'Wird deaktiviert...',
  'settings.security.disable2fa': '2FA deaktivieren',
  'settings.security.disable2faTitle': 'Zwei-Faktor-Authentifizierung deaktivieren?',
  'settings.security.disable2faMessage': 'Dies verringert die Sicherheit Ihres Kontos.',
  'settings.security.disable2faConfirm': 'Deaktivieren',
  'settings.security.twoFactorDisabledToast': 'Zwei-Faktor-Authentifizierung deaktiviert.',
  'settings.security.starting': 'Wird gestartet...',
  'settings.security.enable2fa': 'Zwei-Faktor-Authentifizierung aktivieren',
  'settings.security.scanHint':
    'Scannen Sie den QR-Code mit Ihrer Authenticator-App und geben Sie dann den 6-stelligen Code zur Bestätigung ein.',
  'settings.security.qrAriaLabel': 'TOTP-QR-Code',
  'settings.security.manualEntryKey': 'Schlüssel zur manuellen Eingabe',
  'settings.security.totpSecretAria': 'TOTP-Geheimnis',
  'settings.security.provisioningUri': 'Provisioning-URI',
  'settings.security.provisioningUriAria': 'TOTP-Provisioning-URI',
  'settings.security.codePlaceholder': '6-stelliger Code',
  'settings.security.codeAria': 'Authenticator-Code',
  'settings.security.confirming': 'Wird bestätigt...',
  'settings.security.confirmEnroll': 'Bestätigen',
  'settings.security.twoFactorEnabledToast': 'Zwei-Faktor-Authentifizierung aktiviert.',

  // ── API-Schlüssel-Formular (re #97) ──────────────────────────────────
  'settings.apiKeys.intro1':
    'Mit API-Schlüsseln können sich Skripte und externe Programme über {bearer} bei diesem Konto authentifizieren. Sie sind optional -- nichts in der Web-Suite benötigt einen. Erstellen Sie nur dann einen Schlüssel, wenn Sie JMAP oder die REST-API von außerhalb des Browsers ansprechen möchten.',
  'settings.apiKeys.intro2':
    'Jeder Schlüssel hat einen festen Berechtigungsumfang: Beschränken Sie ihn auf die kleinste Menge an Berechtigungen, die das Skript tatsächlich benötigt (z. B. {mailSend} für einen ausgehenden Bot). Schlüssel werden nur ein einziges Mal beim Erstellen angezeigt -- kopieren Sie sie dann; sie können später nicht abgerufen werden.',
  'settings.apiKeys.copyNow': 'Kopieren Sie diesen Schlüssel jetzt. Er wird nicht erneut angezeigt.',
  'settings.apiKeys.newKeyAria': 'Neuer API-Schlüssel',
  'settings.apiKeys.savedKey': 'Ich habe diesen Schlüssel gespeichert',
  'settings.apiKeys.heading.new': 'Neuer API-Schlüssel',
  'settings.apiKeys.label': 'Bezeichnung',
  'settings.apiKeys.labelPlaceholder': 'z. B. mein-skript',
  'settings.apiKeys.scopes': 'Berechtigungen',
  'settings.apiKeys.scopesHint':
    'Wählen Sie die Berechtigungen aus, die dieser Schlüssel gewährt. Leer lassen für nur mail.send (Standard).',
  'settings.apiKeys.creating': 'Wird erstellt...',
  'settings.apiKeys.create': 'Schlüssel erstellen',
  'settings.apiKeys.createNew': 'Neuen Schlüssel erstellen',
  'settings.apiKeys.loadingAria': 'API-Schlüssel werden geladen',
  'settings.apiKeys.empty': 'Noch keine API-Schlüssel.',
  'settings.apiKeys.createdAt': 'Erstellt {date}',
  'settings.apiKeys.lastUsed': 'Zuletzt verwendet {date}',
  'settings.apiKeys.revoke': 'Widerrufen',
  'settings.apiKeys.revokeTitle': 'Diesen API-Schlüssel widerrufen?',
  'settings.apiKeys.revokeMessage':
    'Anwendungen, die ihn verwenden, funktionieren danach sofort nicht mehr.',
  'settings.apiKeys.revoked': 'API-Schlüssel widerrufen.',
  'settings.apiKeys.scope.endUser': 'Endbenutzer',
  'settings.apiKeys.scope.mailSend': 'Mail: senden',
  'settings.apiKeys.scope.mailReceive': 'Mail: empfangen',
  'settings.apiKeys.scope.chatRead': 'Chat: lesen',
  'settings.apiKeys.scope.chatWrite': 'Chat: schreiben',
  'settings.apiKeys.scope.calRead': 'Kalender: lesen',
  'settings.apiKeys.scope.calWrite': 'Kalender: schreiben',
  'settings.apiKeys.scope.contactsRead': 'Kontakte: lesen',
  'settings.apiKeys.scope.contactsWrite': 'Kontakte: schreiben',

  // ── Diagnose-Formular (re #97) ───────────────────────────────────────
  'settings.diagnostics.saveError': 'Einstellung konnte nicht gespeichert werden.',

  // ── Anmeldung (re #97) ───────────────────────────────────────────────
  'login.email': 'E-Mail-Adresse',
  'login.password': 'Passwort',
  'login.totpCode': 'Authenticator-Code',
  'login.totpPlaceholder': '6-stelliger Code',
  'login.signingIn': 'Anmeldung läuft...',
  'login.signIn': 'Anmelden',
  'login.signInFailed': 'Anmeldung fehlgeschlagen.',

  // ── Chat-Ansicht (re #97) ────────────────────────────────────────────
  'chat.unavailable': 'Chat ist auf diesem Server nicht konfiguriert',
  'chat.startVideoCall': 'Videoanruf starten',
  'chat.callButton': 'Anrufen',
  'chat.unknownCaller': 'Unbekannter Anrufer',
  'chat.selectConversation': 'Wählen Sie eine Unterhaltung, um zu chatten',

  // ── Chat-Oberfläche (re #97) ─────────────────────────────────────────
  // ChatCompose
  'chat.composeAria': 'Chat verfassen',
  'chat.composeEditorAria': 'Nachrichteneditor',
  'chat.send': 'Nachricht senden',
  // MessageList
  'chat.reactions': 'Reaktionen',
  'chat.addReaction': 'Reaktion hinzufügen',
  'chat.newMessages': 'Neue Nachrichten',
  // VideoCall
  'chat.videoCall': 'Videoanruf',
  'chat.remoteVideo': 'Gegenseite',
  'chat.yourCamera': 'Ihre Kamera',
  'chat.callControls': 'Anrufsteuerung',
  'chat.hangUp': 'Auflegen',
  // IncomingCall
  'chat.incomingVideoCall': 'Eingehender Videoanruf',
  'chat.acceptCall': 'Anruf annehmen',
  'chat.declineCall': 'Anruf ablehnen',
  // ChatOverlayHost
  'chat.windows': 'Chat-Fenster',
  // SidebarChats
  'chat.loadingConversations': 'Unterhaltungen werden geladen',
  'chat.discardChatWith': 'Chat mit {name} verwerfen',

  // ── Kontakt-Ansicht (re #97) ─────────────────────────────────────────
  'contact.view.loading': 'Wird geladen…',
  'contact.view.couldNotLoad': 'Kontakt konnte nicht geladen werden.',
  'contact.view.title': 'Kontakt',
  'contact.view.emailHeading': 'E-Mail',
  'contact.view.phoneHeading': 'Telefon',

  // ── Einstellungen → Adress-Tags (re #30, REQ-SET-TAG-01..21) ───────
  'settings.taggedAddresses': 'Adress-Tags',
  'settings.taggedAddresses.heading': 'Adress-Tags',
  'settings.taggedAddresses.intro':
    'Mit Adress-Tags können Sie Varianten Ihrer Adresse vergeben – z. B. alice+amazon@example.local – und Antworten automatisch in Ordner einsortieren lassen. Beim Öffnen einer Nachricht an eine getaggte Variante bietet die Suite an, einen Filter einzurichten.',
  'settings.taggedAddresses.unavailable':
    'Dieser Server unterstützt die Adress-Tag-Funktion nicht.',
  'settings.taggedAddresses.loading': 'Filter werden geladen…',
  'settings.taggedAddresses.refresh': 'Aktualisieren',
  'settings.taggedAddresses.filtersHeading': 'Filter',
  'settings.taggedAddresses.empty':
    'Noch keine Adress-Tag-Filter. Öffnen Sie eine Nachricht an eine getaggte Variante Ihrer Adresse, um einen Filter einzurichten.',
  'settings.taggedAddresses.addFilter': 'Filter hinzufügen',
  'settings.taggedAddresses.col.address': 'Adresse',
  'settings.taggedAddresses.col.action': 'Aktion',
  'settings.taggedAddresses.col.label': 'Label',
  'settings.taggedAddresses.col.created': 'Angelegt',
  'settings.taggedAddresses.col.dismissedAt': 'Abgelehnt',
  'settings.taggedAddresses.col.actions': 'Aktionen',
  'settings.taggedAddresses.action.label': 'Als {label} kennzeichnen',
  'settings.taggedAddresses.action.labelArchive': 'Als {label} kennzeichnen + archivieren',
  'settings.taggedAddresses.action.labelArchiveRead':
    'Als {label} kennzeichnen + archivieren + als gelesen markieren',
  'settings.taggedAddresses.row.edit': 'Bearbeiten',
  'settings.taggedAddresses.row.delete': 'Löschen',
  'settings.taggedAddresses.row.convert': 'In Sieve umwandeln',
  'settings.taggedAddresses.row.editAria': 'Filter für {address} bearbeiten',
  'settings.taggedAddresses.row.deleteAria': 'Filter für {address} löschen',
  'settings.taggedAddresses.row.convertAria': 'Filter für {address} in Sieve umwandeln',
  'settings.taggedAddresses.capNotice': '{used} von {cap} Filterplätzen belegt',
  'settings.taggedAddresses.capReached':
    'Sie haben das Konto-Limit von {cap} Filtern erreicht. Bitte einen bestehenden Filter löschen oder in Sieve umwandeln.',
  'settings.taggedAddresses.dismissalsHeading': 'Abgelehnte Suffixe',
  'settings.taggedAddresses.dismissalsIntro':
    'Getaggte Varianten, deren Banner Sie ignoriert haben. Reaktivieren Sie eine Zeile, um beim nächsten Eingang erneut nach einem Filter gefragt zu werden.',
  'settings.taggedAddresses.dismissalsEmpty': 'Keine abgelehnten Suffixe.',
  'settings.taggedAddresses.dismissalsCount': '{count} abgelehnt',
  'settings.taggedAddresses.dismissalsToggle': 'Abgelehnte Suffixe anzeigen',
  'settings.taggedAddresses.dismissalsCapNotice': '{used} von {cap} Ablehnungs-Plätzen belegt',
  'settings.taggedAddresses.row.undismiss': 'Wieder anbieten',
  'settings.taggedAddresses.row.undismissAria': 'Banner für {address} wieder anbieten',
  'settings.taggedAddresses.dialog.titleAdd': 'Adress-Tag-Filter hinzufügen',
  'settings.taggedAddresses.dialog.titleEdit': 'Adress-Tag-Filter bearbeiten',
  'settings.taggedAddresses.dialog.identity': 'Basis-Identität',
  'settings.taggedAddresses.dialog.identityHint':
    'Mail an {address} mit einem +Suffix wird von diesem Filter abgefangen.',
  'settings.taggedAddresses.dialog.suffix': 'Suffix (nach dem +)',
  'settings.taggedAddresses.dialog.suffixHint':
    'Nur Kleinbuchstaben, kein @ und keine Leerzeichen. Der Server normalisiert die Schreibweise beim Speichern.',
  'settings.taggedAddresses.dialog.suffixPlaceholder': 'amazon',
  'settings.taggedAddresses.dialog.action': 'Aktion',
  'settings.taggedAddresses.dialog.actionLabel': 'Label anwenden',
  'settings.taggedAddresses.dialog.actionLabelArchive': 'Label anwenden und archivieren',
  'settings.taggedAddresses.dialog.actionLabelArchiveRead':
    'Label anwenden, archivieren und als gelesen markieren',
  'settings.taggedAddresses.dialog.labelName': 'Label-Name',
  'settings.taggedAddresses.dialog.labelNameHint':
    'Passende Nachrichten landen in dem angegebenen Label.',
  'settings.taggedAddresses.dialog.labelPlaceholder': 'Shopping',
  'settings.taggedAddresses.dialog.cancel': 'Abbrechen',
  'settings.taggedAddresses.dialog.save': 'Filter speichern',
  'settings.taggedAddresses.dialog.create': 'Filter erstellen',
  'settings.taggedAddresses.dialog.saving': 'Wird gespeichert…',
  'settings.taggedAddresses.dialog.noVerifiedIdentities':
    'Sie haben keine verifizierten Identitäten. Bitte verifizieren Sie eine Identität im Konto-Bereich, bevor Sie Adress-Tag-Filter anlegen.',
  'settings.taggedAddresses.confirmDelete.title': 'Filter löschen',
  'settings.taggedAddresses.confirmDelete.body':
    'Mail an {address} nicht mehr automatisch sortieren? Künftige Mails landen ganz normal im Posteingang. Bereits einsortierte Mails bleiben unverändert.',
  'settings.taggedAddresses.confirmDelete.confirm': 'Filter löschen',
  'settings.taggedAddresses.confirmDelete.cancel': 'Filter behalten',
  'settings.taggedAddresses.confirmConvert.title': 'In Sieve umwandeln',
  'settings.taggedAddresses.confirmConvert.body':
    'Den Filter für {address} in ein Sieve-Skriptfragment umwandeln? Dies ist eine Einbahnstraße: die verwaltete Filterzeile verschwindet und eine entsprechende Sieve-Regel wird in Ihr Skript eingefügt.',
  'settings.taggedAddresses.confirmConvert.confirm': 'In Sieve umwandeln',
  'settings.taggedAddresses.confirmConvert.cancel': 'Verwaltet lassen',
  'settings.taggedAddresses.toast.created': 'Filter angelegt.',
  'settings.taggedAddresses.toast.updated': 'Filter aktualisiert.',
  'settings.taggedAddresses.toast.deleted': 'Filter gelöscht.',
  'settings.taggedAddresses.toast.converted':
    'Filter in Ihr Sieve-Skript übernommen. Bearbeiten Sie es unter Einstellungen → Mail → Sieve.',
  'settings.taggedAddresses.toast.undismissed': 'Banner für {address} wieder aktiviert.',
  'settings.taggedAddresses.toast.failed': 'Aktion fehlgeschlagen: {error}',
  'settings.taggedAddresses.error.suffixEmpty': 'Das Suffix darf nicht leer sein.',
  'settings.taggedAddresses.error.suffixTooLong': 'Das Suffix darf höchstens 64 Bytes lang sein.',
  'settings.taggedAddresses.error.suffixWhitespace':
    'Das Suffix darf keine Leerzeichen enthalten.',
  'settings.taggedAddresses.error.suffixForbidden':
    'Das Suffix darf keine Steuerzeichen und kein @ enthalten.',
  'settings.taggedAddresses.error.labelEmpty': 'Der Label-Name darf nicht leer sein.',
  'settings.taggedAddresses.error.labelTooLong':
    'Der Label-Name darf höchstens 255 Bytes lang sein.',
} as const;
