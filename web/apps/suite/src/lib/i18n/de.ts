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

  // ── Message-scope strings ────────────────────────────────────────────
  // Siehe re #98: die per-Nachricht-Symbolleiste wurde entfernt; Antworten
  // / Weiterleiten leben in der festen Antwortleiste; muteThread /
  // reportSpam / reportPhishing / blockSender sind über die Konversations-
  // -Symbolleiste erreichbar; restore / filterLike / viewOriginal bleiben
  // im Drei-Punkte-Menü im Nachrichtenkopf.
  'msg.reply': 'Antworten',
  'msg.replyAll': 'Allen antworten',
  'msg.forward': 'Weiterleiten',
  'msg.react': 'Reagieren',
  'msg.restore': 'Aus Papierkorb wiederherstellen',
  'msg.muteThread': 'Konversation stummschalten',
  'msg.unmuteThread': 'Stummschaltung aufheben',
  'msg.reportSpam': 'Spam melden',
  'msg.reportPhishing': 'Phishing melden',
  'msg.blockSender': 'Absender blockieren',
  'msg.filterLike': 'Filter für ähnliche Nachrichten',
  'msg.viewOriginal': 'Original anzeigen',
  'msg.imagesBlocked': 'Externe Bilder werden blockiert.',
  'msg.loadImages': 'Bilder laden',
  'msg.alwaysFrom': 'Immer von {sender}',
  'msg.noBody': '(kein Inhalt)',
  'msg.noSender': '(kein Absender)',
  'msg.recipientsTo': 'an {first}',
  'msg.recipientsToMany': 'an {first} und {others} weitere Person',
  'msg.recipientsToManyOther': 'an {first} und {others} weitere Personen',

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

  // ── Kontakt-Ansicht (re #97) ─────────────────────────────────────────
  'contact.view.loading': 'Wird geladen…',
  'contact.view.couldNotLoad': 'Kontakt konnte nicht geladen werden.',
  'contact.view.title': 'Kontakt',
  'contact.view.emailHeading': 'E-Mail',
  'contact.view.phoneHeading': 'Telefon',
} as const;
