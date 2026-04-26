package backup

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

func init() {
	RegisterAdapter(func(s store.Store) (Backend, bool) {
		fs, ok := s.(*fakestore.Store)
		if !ok {
			return nil, false
		}
		return &fakestoreBackend{fs: fs}, true
	})
}

type fakestoreBackend struct {
	fs *fakestore.Store
}

func (b *fakestoreBackend) Kind() string       { return "fakestore" }
func (b *fakestoreBackend) Blobs() store.Blobs { return b.fs.Blobs() }
func (b *fakestoreBackend) SchemaVersion(ctx context.Context) (int, error) {
	return CurrentSchemaVersion, nil
}

func (b *fakestoreBackend) IsEmpty(ctx context.Context) (bool, error) {
	dump := b.fs.DiagSnapshot()
	for _, rows := range dump.Tables {
		if len(rows) > 0 {
			return false, nil
		}
	}
	return len(dump.Blobs) == 0, nil
}

func (b *fakestoreBackend) TruncateAll(ctx context.Context) error {
	b.fs.DiagReset()
	return nil
}

func (b *fakestoreBackend) Snapshot(ctx context.Context) (Source, error) {
	return &fakestoreSource{fs: b.fs, dump: b.fs.DiagSnapshot()}, nil
}

func (b *fakestoreBackend) Restore(ctx context.Context) (Sink, error) {
	return &fakestoreSink{fs: b.fs}, nil
}

type fakestoreSource struct {
	fs   *fakestore.Store
	dump *fakestore.DiagDump
}

func (s *fakestoreSource) CountRows(ctx context.Context, table string) (int64, error) {
	return int64(len(s.dump.Tables[table])), nil
}

func (s *fakestoreSource) EnumerateRows(ctx context.Context, table string, fn func(row any) error) error {
	// state_changes carries a per-principal Seq but a global PK; the
	// fakestore stores the per-principal Seq only, so we synthesise a
	// global running ID when emitting the JSONL so the SQL backends'
	// AUTOINCREMENT primary key column does not collide on insert.
	var stateChangeID int64
	for _, raw := range s.dump.Tables[table] {
		row, err := convertFakeToRow(table, raw)
		if err != nil {
			return err
		}
		if table == "state_changes" {
			stateChangeID++
			row.(*StateChangeRow).ID = stateChangeID
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakestoreSource) EnumerateBlobHashes(ctx context.Context, fn func(hash string, size int64) error) error {
	hashes := make([]string, 0, len(s.dump.Blobs))
	for h := range s.dump.Blobs {
		hashes = append(hashes, h)
	}
	// Deterministic order.
	for _, h := range sortStrings(hashes) {
		if err := fn(h, s.dump.Blobs[h]); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakestoreSource) Close() error { return nil }

type fakestoreSink struct {
	fs *fakestore.Store
}

func (s *fakestoreSink) Insert(ctx context.Context, table string, row any) error {
	native, err := convertRowToFake(table, row)
	if err != nil {
		return err
	}
	return s.fs.DiagInsert(table, native)
}

func (s *fakestoreSink) Commit(ctx context.Context) error   { return nil }
func (s *fakestoreSink) Rollback(ctx context.Context) error { return nil }

// convertFakeToRow maps a fakestore native struct to the wire-form
// Row struct used by the JSONL bundle.
func convertFakeToRow(table string, raw any) (any, error) {
	switch table {
	case "principals":
		p := raw.(store.Principal)
		return &PrincipalRow{
			ID:             int64(p.ID),
			Kind:           int64(p.Kind),
			CanonicalEmail: p.CanonicalEmail,
			DisplayName:    p.DisplayName,
			PasswordHash:   p.PasswordHash,
			TOTPSecret:     p.TOTPSecret,
			QuotaBytes:     p.QuotaBytes,
			Flags:          int64(p.Flags),
			CreatedAtUs:    micros(p.CreatedAt),
			UpdatedAtUs:    micros(p.UpdatedAt),
		}, nil
	case "domains":
		d := raw.(store.Domain)
		return &DomainRow{Name: d.Name, IsLocal: d.IsLocal, CreatedAtUs: micros(d.CreatedAt)}, nil
	case "oidc_providers":
		p := raw.(store.OIDCProvider)
		return &OIDCProviderRow{
			Name: p.Name, IssuerURL: p.IssuerURL, ClientID: p.ClientID,
			ClientSecretRef: p.ClientSecretRef,
			ScopesCSV:       strings.Join(p.Scopes, ","),
			AutoProvision:   p.AutoProvision, CreatedAtUs: micros(p.CreatedAt),
		}, nil
	case "oidc_links":
		l := raw.(store.OIDCLink)
		return &OIDCLinkRow{
			PrincipalID: int64(l.PrincipalID), ProviderName: l.ProviderName,
			Subject: l.Subject, EmailAtProvider: l.EmailAtProvider,
			LinkedAtUs: micros(l.LinkedAt),
		}, nil
	case "api_keys":
		k := raw.(store.APIKey)
		addrJSON := encodeJSON(k.AllowedFromAddresses)
		domJSON := encodeJSON(k.AllowedFromDomains)
		if addrJSON == "null" {
			addrJSON = "[]"
		}
		if domJSON == "null" {
			domJSON = "[]"
		}
		return &APIKeyRow{
			ID: int64(k.ID), PrincipalID: int64(k.PrincipalID),
			Hash: k.Hash, Name: k.Name,
			CreatedAtUs: micros(k.CreatedAt), LastUsedAtUs: micros(k.LastUsedAt),
			ScopeJSON:                k.ScopeJSON,
			AllowedFromAddressesJSON: addrJSON,
			AllowedFromDomainsJSON:   domJSON,
		}, nil
	case "aliases":
		a := raw.(store.Alias)
		r := &AliasRow{
			ID: int64(a.ID), LocalPart: a.LocalPart, Domain: a.Domain,
			TargetPrincipal: int64(a.TargetPrincipal), CreatedAtUs: micros(a.CreatedAt),
		}
		if a.ExpiresAt != nil {
			us := a.ExpiresAt.UnixMicro()
			r.ExpiresAtUs = &us
		}
		return r, nil
	case "sieve_scripts":
		e := raw.(fakestore.SieveEntry)
		return &SieveScriptRow{PrincipalID: int64(e.PrincipalID), Script: e.Script}, nil
	case "mailboxes":
		mb := raw.(store.Mailbox)
		return &MailboxRow{
			ID: int64(mb.ID), PrincipalID: int64(mb.PrincipalID), ParentID: int64(mb.ParentID),
			Name: mb.Name, Attributes: int64(mb.Attributes), UIDValidity: int64(mb.UIDValidity),
			UIDNext: int64(mb.UIDNext), HighestModSeq: int64(mb.HighestModSeq),
			CreatedAtUs: micros(mb.CreatedAt), UpdatedAtUs: micros(mb.UpdatedAt),
		}, nil
	case "messages":
		m := raw.(store.Message)
		r := &MessageRow{
			ID: int64(m.ID), MailboxID: int64(m.MailboxID), UID: int64(m.UID),
			ModSeq: int64(m.ModSeq), Flags: int64(m.Flags),
			KeywordsCSV:    strings.Join(m.Keywords, ","),
			InternalDateUs: micros(m.InternalDate), ReceivedAtUs: micros(m.ReceivedAt),
			Size: m.Size, BlobHash: m.Blob.Hash, BlobSize: m.Blob.Size,
			ThreadID:   int64(m.ThreadID),
			EnvSubject: m.Envelope.Subject, EnvFrom: m.Envelope.From,
			EnvTo: m.Envelope.To, EnvCc: m.Envelope.Cc, EnvBcc: m.Envelope.Bcc,
			EnvReplyTo: m.Envelope.ReplyTo, EnvMessageID: m.Envelope.MessageID,
			EnvInReplyTo: m.Envelope.InReplyTo, EnvDateUs: micros(m.Envelope.Date),
		}
		if m.SnoozedUntil != nil {
			us := m.SnoozedUntil.UnixMicro()
			r.SnoozedUntilUs = &us
		}
		return r, nil
	case "state_changes":
		c := raw.(store.StateChange)
		return &StateChangeRow{
			ID: int64(c.Seq), PrincipalID: int64(c.PrincipalID), Seq: int64(c.Seq),
			EntityKind: string(c.Kind), EntityID: int64(c.EntityID),
			ParentEntityID: int64(c.ParentEntityID), Op: int64(c.Op),
			ProducedAtUs: micros(c.ProducedAt),
		}, nil
	case "audit_log":
		e := raw.(store.AuditLogEntry)
		md := ""
		if len(e.Metadata) > 0 {
			md = encodeJSON(e.Metadata)
		}
		return &AuditLogRow{
			ID: int64(e.ID), AtUs: micros(e.At), ActorKind: int64(e.ActorKind),
			ActorID: e.ActorID, Action: e.Action, Subject: e.Subject,
			RemoteAddr: e.RemoteAddr, Outcome: int64(e.Outcome), Message: e.Message,
			MetadataJSON: md,
		}, nil
	case "cursors":
		c := raw.(fakestore.CursorEntry)
		return &CursorRow{Key: c.Key, Seq: int64(c.Seq)}, nil
	case "queue":
		q := raw.(store.QueueItem)
		r := &QueueRow{
			ID: int64(q.ID), MailFrom: q.MailFrom, RcptTo: q.RcptTo,
			EnvelopeID: string(q.EnvelopeID), BodyBlobHash: q.BodyBlobHash,
			HeadersBlobHash: q.HeadersBlobHash, State: int64(q.State),
			Attempts:        int64(q.Attempts),
			LastAttemptAtUs: micros(q.LastAttemptAt), NextAttemptAtUs: micros(q.NextAttemptAt),
			LastError: q.LastError, DSNNotifyFlags: int64(q.DSNNotify),
			DSNRet: int64(q.DSNRet), DSNEnvID: q.DSNEnvID, DSNOrcpt: q.DSNOrcpt,
			CreatedAtUs: micros(q.CreatedAt),
		}
		if q.PrincipalID != 0 {
			pid := int64(q.PrincipalID)
			r.PrincipalID = &pid
		}
		if q.IdempotencyKey != "" {
			s := q.IdempotencyKey
			r.IdempotencyKey = &s
		}
		return r, nil
	case "dkim_keys":
		k := raw.(store.DKIMKey)
		return &DKIMKeyRow{
			ID: int64(k.ID), Domain: k.Domain, Selector: k.Selector,
			Algorithm: int64(k.Algorithm), PrivateKeyPEM: k.PrivateKeyPEM,
			PublicKeyB64: k.PublicKeyB64, Status: int64(k.Status),
			CreatedAtUs: micros(k.CreatedAt), RotatedAtUs: micros(k.RotatedAt),
		}, nil
	case "acme_accounts":
		a := raw.(store.ACMEAccount)
		return &ACMEAccountRow{
			ID: int64(a.ID), DirectoryURL: a.DirectoryURL,
			ContactEmail: a.ContactEmail, AccountKeyPEM: a.AccountKeyPEM,
			KID: a.KID, CreatedAtUs: micros(a.CreatedAt),
		}, nil
	case "acme_orders":
		o := raw.(store.ACMEOrder)
		hjson := encodeJSON(o.Hostnames)
		return &ACMEOrderRow{
			ID: int64(o.ID), AccountID: int64(o.AccountID), HostnamesJSON: hjson,
			Status: int64(o.Status), OrderURL: o.OrderURL,
			FinalizeURL: o.FinalizeURL, CertificateURL: o.CertificateURL,
			ChallengeType: int64(o.ChallengeType), UpdatedAtUs: micros(o.UpdatedAt),
			Error: o.Error,
		}, nil
	case "acme_certs":
		c := raw.(store.ACMECert)
		r := &ACMECertRow{
			Hostname: c.Hostname, ChainPEM: c.ChainPEM, PrivateKeyPEM: c.PrivateKeyPEM,
			NotBeforeUs: micros(c.NotBefore), NotAfterUs: micros(c.NotAfter),
			Issuer: c.Issuer,
		}
		if c.OrderID != 0 {
			oid := int64(c.OrderID)
			r.OrderID = &oid
		}
		return r, nil
	case "webhooks":
		w := raw.(store.Webhook)
		var policy string
		if w.RetryPolicy != (store.RetryPolicy{}) {
			policy = encodeJSON(w.RetryPolicy)
		}
		return &WebhookRow{
			ID: int64(w.ID), OwnerKind: int64(w.OwnerKind), OwnerID: w.OwnerID,
			TargetURL: w.TargetURL, HMACSecret: w.HMACSecret,
			DeliveryMode: int64(w.DeliveryMode), RetryPolicyJSON: policy,
			Active: w.Active, CreatedAtUs: micros(w.CreatedAt), UpdatedAtUs: micros(w.UpdatedAt),
			TargetKind:            int64(w.TargetKind),
			BodyMode:              int64(w.BodyMode),
			ExtractedTextMaxBytes: w.ExtractedTextMaxBytes,
			TextRequired:          w.TextRequired,
		}, nil
	case "dmarc_reports_raw":
		d := raw.(store.DMARCReport)
		return &DMARCReportRow{
			ID: int64(d.ID), ReceivedAtUs: micros(d.ReceivedAt),
			ReporterEmail: d.ReporterEmail, ReporterOrg: d.ReporterOrg,
			ReportID: d.ReportID, Domain: d.Domain,
			DateBeginUs: micros(d.DateBegin), DateEndUs: micros(d.DateEnd),
			XMLBlobHash: d.XMLBlobHash, ParsedOK: d.ParsedOK, ParseError: d.ParseError,
		}, nil
	case "dmarc_rows":
		r := raw.(store.DMARCRow)
		return &DMARCRowRow{
			ID: int64(r.ID), ReportID: int64(r.ReportID),
			SourceIP: r.SourceIP, Count: r.Count, Disposition: int64(r.Disposition),
			SPFAligned: r.SPFAligned, DKIMAligned: r.DKIMAligned,
			SPFResult: r.SPFResult, DKIMResult: r.DKIMResult,
			HeaderFrom: r.HeaderFrom, EnvelopeFrom: r.EnvelopeFrom, EnvelopeTo: r.EnvelopeTo,
		}, nil
	case "mailbox_acl":
		a := raw.(store.MailboxACL)
		row := &MailboxACLRow{
			ID: int64(a.ID), MailboxID: int64(a.MailboxID),
			RightsMask: int64(a.Rights), GrantedBy: int64(a.GrantedBy),
			CreatedAtUs: micros(a.CreatedAt),
		}
		if a.PrincipalID != nil {
			pid := int64(*a.PrincipalID)
			row.PrincipalID = &pid
		}
		return row, nil
	case "jmap_states":
		j := raw.(store.JMAPStates)
		return &JMAPStateRow{
			PrincipalID:  int64(j.PrincipalID),
			MailboxState: j.Mailbox, EmailState: j.Email, ThreadState: j.Thread,
			IdentityState: j.Identity, EmailSubmissionState: j.EmailSubmission,
			VacationResponseState: j.VacationResponse, UpdatedAtUs: micros(j.UpdatedAt),
			ShortcutCoachState: j.ShortcutCoach,
		}, nil
	case "jmap_email_submissions":
		e := raw.(store.EmailSubmissionRow)
		return &JMAPEmailSubmissionRow{
			ID: e.ID, EnvelopeID: string(e.EnvelopeID),
			PrincipalID: int64(e.PrincipalID), IdentityID: e.IdentityID,
			EmailID: int64(e.EmailID), ThreadID: e.ThreadID,
			SendAtUs: e.SendAtUs, CreatedAtUs: e.CreatedAtUs,
			UndoStatus: e.UndoStatus, Properties: e.Properties,
		}, nil
	case "jmap_identities":
		i := raw.(store.JMAPIdentity)
		return &JMAPIdentityRow{
			ID: i.ID, PrincipalID: int64(i.PrincipalID),
			Name: i.Name, Email: i.Email,
			ReplyToJSON: i.ReplyToJSON, BccJSON: i.BccJSON,
			TextSignature: i.TextSignature, HTMLSignature: i.HTMLSignature,
			MayDelete: i.MayDelete, CreatedAtUs: i.CreatedAtUs, UpdatedAtUs: i.UpdatedAtUs,
		}, nil
	case "tlsrpt_failures":
		t := raw.(store.TLSRPTFailure)
		return &TLSRPTFailureRow{
			ID: int64(t.ID), RecordedAtUs: micros(t.RecordedAt),
			PolicyDomain: t.PolicyDomain, ReceivingMTAHostname: t.ReceivingMTAHostname,
			FailureType: int64(t.FailureType), FailureCode: t.FailureCode,
			FailureDetailJSON: t.FailureDetailJSON,
		}, nil
	case "push_subscription":
		ps := raw.(store.PushSubscription)
		r := &PushSubscriptionRow{
			ID:                     int64(ps.ID),
			PrincipalID:            int64(ps.PrincipalID),
			DeviceClientID:         ps.DeviceClientID,
			URL:                    ps.URL,
			P256DH:                 ps.P256DH,
			Auth:                   ps.Auth,
			TypesCSV:               strings.Join(ps.Types, ","),
			VerificationCode:       ps.VerificationCode,
			Verified:               ps.Verified,
			VAPIDKeyAtRegistration: ps.VAPIDKeyAtRegistration,
			NotificationRulesJSON:  ps.NotificationRulesJSON,
			QuietHoursTZ:           ps.QuietHoursTZ,
			CreatedAtUs:            micros(ps.CreatedAt),
			UpdatedAtUs:            micros(ps.UpdatedAt),
		}
		if ps.Expires != nil {
			us := ps.Expires.UnixMicro()
			r.ExpiresAtUs = &us
		}
		if ps.QuietHoursStartLocal != nil {
			v := int64(*ps.QuietHoursStartLocal)
			r.QuietHoursStartLocal = &v
		}
		if ps.QuietHoursEndLocal != nil {
			v := int64(*ps.QuietHoursEndLocal)
			r.QuietHoursEndLocal = &v
		}
		return r, nil
	case "email_reactions":
		rdr := raw.(fakestore.ReactionDiagRow)
		return &EmailReactionRow{
			EmailID:     int64(rdr.EmailID),
			Emoji:       rdr.Emoji,
			PrincipalID: int64(rdr.PrincipalID),
			CreatedAtUs: micros(rdr.CreatedAt),
		}, nil
	case "coach_events":
		ev := raw.(store.CoachEvent)
		return &CoachEventRow{
			ID:          ev.ID,
			PrincipalID: int64(ev.PrincipalID),
			Action:      ev.Action,
			InputMethod: string(ev.Method),
			EventCount:  int64(ev.Count),
			OccurredAt:  micros(ev.OccurredAt),
			RecordedAt:  micros(ev.RecordedAt),
		}, nil
	case "coach_dismiss":
		d := raw.(store.CoachDismiss)
		r := &CoachDismissRow{
			PrincipalID:  int64(d.PrincipalID),
			Action:       d.Action,
			DismissCount: int64(d.DismissCount),
			UpdatedAt:    micros(d.UpdatedAt),
		}
		if d.DismissUntil != nil {
			us := d.DismissUntil.UnixMicro()
			r.DismissUntil = &us
		}
		return r, nil
	case "ses_seen_messages":
		// The fakestore emits an empty slice for this table; this branch
		// is only reached during tests that populate a SQLite source and
		// restore to a fakestore destination.
		return nil, fmt.Errorf("fakestore: ses_seen_messages: unexpected call to convertFakeToRow with %T", raw)
	case "blob_refs":
		b := raw.(fakestore.BlobRefEntry)
		return &BlobRefRow{Hash: b.Hash, Size: b.Size, RefCount: b.RefCount}, nil
	}
	return nil, fmt.Errorf("fakestore: unknown table %q", table)
}

// convertRowToFake is the inverse: a JSONL row turns back into the
// fakestore native struct.
func convertRowToFake(table string, row any) (any, error) {
	switch table {
	case "principals":
		r := row.(*PrincipalRow)
		return store.Principal{
			ID: store.PrincipalID(r.ID), Kind: store.PrincipalKind(r.Kind),
			CanonicalEmail: r.CanonicalEmail, DisplayName: r.DisplayName,
			PasswordHash: r.PasswordHash, TOTPSecret: r.TOTPSecret,
			QuotaBytes: r.QuotaBytes, Flags: store.PrincipalFlags(r.Flags),
			CreatedAt: fromMicros(r.CreatedAtUs), UpdatedAt: fromMicros(r.UpdatedAtUs),
		}, nil
	case "domains":
		r := row.(*DomainRow)
		return store.Domain{Name: r.Name, IsLocal: r.IsLocal, CreatedAt: fromMicros(r.CreatedAtUs)}, nil
	case "oidc_providers":
		r := row.(*OIDCProviderRow)
		return store.OIDCProvider{
			Name: r.Name, IssuerURL: r.IssuerURL, ClientID: r.ClientID,
			ClientSecretRef: r.ClientSecretRef,
			Scopes:          splitCSV(r.ScopesCSV),
			AutoProvision:   r.AutoProvision, CreatedAt: fromMicros(r.CreatedAtUs),
		}, nil
	case "oidc_links":
		r := row.(*OIDCLinkRow)
		return store.OIDCLink{
			PrincipalID: store.PrincipalID(r.PrincipalID), ProviderName: r.ProviderName,
			Subject: r.Subject, EmailAtProvider: r.EmailAtProvider,
			LinkedAt: fromMicros(r.LinkedAtUs),
		}, nil
	case "api_keys":
		r := row.(*APIKeyRow)
		var addrs, domains []string
		if r.AllowedFromAddressesJSON != "" && r.AllowedFromAddressesJSON != "[]" {
			_ = decodeJSON(r.AllowedFromAddressesJSON, &addrs)
		}
		if r.AllowedFromDomainsJSON != "" && r.AllowedFromDomainsJSON != "[]" {
			_ = decodeJSON(r.AllowedFromDomainsJSON, &domains)
		}
		return store.APIKey{
			ID: store.APIKeyID(r.ID), PrincipalID: store.PrincipalID(r.PrincipalID),
			Hash: r.Hash, Name: r.Name,
			CreatedAt: fromMicros(r.CreatedAtUs), LastUsedAt: fromMicros(r.LastUsedAtUs),
			ScopeJSON:            r.ScopeJSON,
			AllowedFromAddresses: addrs,
			AllowedFromDomains:   domains,
		}, nil
	case "aliases":
		r := row.(*AliasRow)
		a := store.Alias{
			ID: store.AliasID(r.ID), LocalPart: r.LocalPart, Domain: r.Domain,
			TargetPrincipal: store.PrincipalID(r.TargetPrincipal),
			CreatedAt:       fromMicros(r.CreatedAtUs),
		}
		if r.ExpiresAtUs != nil {
			t := fromMicros(*r.ExpiresAtUs)
			a.ExpiresAt = &t
		}
		return a, nil
	case "sieve_scripts":
		r := row.(*SieveScriptRow)
		return fakestore.SieveEntry{
			PrincipalID: store.PrincipalID(r.PrincipalID), Script: r.Script,
		}, nil
	case "mailboxes":
		r := row.(*MailboxRow)
		return store.Mailbox{
			ID: store.MailboxID(r.ID), PrincipalID: store.PrincipalID(r.PrincipalID),
			ParentID: store.MailboxID(r.ParentID), Name: r.Name,
			Attributes:  store.MailboxAttributes(r.Attributes),
			UIDValidity: store.UIDValidity(r.UIDValidity),
			UIDNext:     store.UID(r.UIDNext), HighestModSeq: store.ModSeq(r.HighestModSeq),
			CreatedAt: fromMicros(r.CreatedAtUs), UpdatedAt: fromMicros(r.UpdatedAtUs),
		}, nil
	case "messages":
		r := row.(*MessageRow)
		m := store.Message{
			ID: store.MessageID(r.ID), MailboxID: store.MailboxID(r.MailboxID),
			UID: store.UID(r.UID), ModSeq: store.ModSeq(r.ModSeq),
			Flags:        store.MessageFlags(r.Flags),
			Keywords:     splitCSV(r.KeywordsCSV),
			InternalDate: fromMicros(r.InternalDateUs),
			ReceivedAt:   fromMicros(r.ReceivedAtUs),
			Size:         r.Size,
			Blob:         store.BlobRef{Hash: r.BlobHash, Size: r.BlobSize},
			ThreadID:     uint64(r.ThreadID),
			Envelope: store.Envelope{
				Subject: r.EnvSubject, From: r.EnvFrom, To: r.EnvTo,
				Cc: r.EnvCc, Bcc: r.EnvBcc, ReplyTo: r.EnvReplyTo,
				MessageID: r.EnvMessageID, InReplyTo: r.EnvInReplyTo,
				Date: fromMicros(r.EnvDateUs),
			},
		}
		if r.SnoozedUntilUs != nil {
			t := fromMicros(*r.SnoozedUntilUs)
			m.SnoozedUntil = &t
		}
		return m, nil
	case "state_changes":
		r := row.(*StateChangeRow)
		return store.StateChange{
			Seq: store.ChangeSeq(r.Seq), PrincipalID: store.PrincipalID(r.PrincipalID),
			Kind:     store.EntityKind(r.EntityKind),
			EntityID: uint64(r.EntityID), ParentEntityID: uint64(r.ParentEntityID),
			Op: store.ChangeOp(r.Op), ProducedAt: fromMicros(r.ProducedAtUs),
		}, nil
	case "audit_log":
		r := row.(*AuditLogRow)
		entry := store.AuditLogEntry{
			ID: store.AuditLogID(r.ID), At: fromMicros(r.AtUs),
			ActorKind: store.ActorKind(r.ActorKind), ActorID: r.ActorID,
			Action: r.Action, Subject: r.Subject, RemoteAddr: r.RemoteAddr,
			Outcome: store.AuditOutcome(r.Outcome), Message: r.Message,
		}
		if r.MetadataJSON != "" {
			entry.Metadata = decodeMetadataJSON(r.MetadataJSON)
		}
		return entry, nil
	case "cursors":
		r := row.(*CursorRow)
		return fakestore.CursorEntry{Key: r.Key, Seq: uint64(r.Seq)}, nil
	case "queue":
		r := row.(*QueueRow)
		q := store.QueueItem{
			ID: store.QueueItemID(r.ID), MailFrom: r.MailFrom, RcptTo: r.RcptTo,
			EnvelopeID:   store.EnvelopeID(r.EnvelopeID),
			BodyBlobHash: r.BodyBlobHash, HeadersBlobHash: r.HeadersBlobHash,
			State: store.QueueState(r.State), Attempts: int32(r.Attempts),
			LastAttemptAt: fromMicros(r.LastAttemptAtUs),
			NextAttemptAt: fromMicros(r.NextAttemptAtUs),
			LastError:     r.LastError,
			DSNNotify:     store.DSNNotifyFlags(r.DSNNotifyFlags),
			DSNRet:        store.DSNRet(r.DSNRet),
			DSNEnvID:      r.DSNEnvID, DSNOrcpt: r.DSNOrcpt,
			CreatedAt: fromMicros(r.CreatedAtUs),
		}
		if r.PrincipalID != nil {
			q.PrincipalID = store.PrincipalID(*r.PrincipalID)
		}
		if r.IdempotencyKey != nil {
			q.IdempotencyKey = *r.IdempotencyKey
		}
		return q, nil
	case "dkim_keys":
		r := row.(*DKIMKeyRow)
		return store.DKIMKey{
			ID: store.DKIMKeyID(r.ID), Domain: r.Domain, Selector: r.Selector,
			Algorithm:     store.DKIMAlgorithm(r.Algorithm),
			PrivateKeyPEM: r.PrivateKeyPEM, PublicKeyB64: r.PublicKeyB64,
			Status:    store.DKIMKeyStatus(r.Status),
			CreatedAt: fromMicros(r.CreatedAtUs), RotatedAt: fromMicros(r.RotatedAtUs),
		}, nil
	case "acme_accounts":
		r := row.(*ACMEAccountRow)
		return store.ACMEAccount{
			ID: store.ACMEAccountID(r.ID), DirectoryURL: r.DirectoryURL,
			ContactEmail: r.ContactEmail, AccountKeyPEM: r.AccountKeyPEM,
			KID: r.KID, CreatedAt: fromMicros(r.CreatedAtUs),
		}, nil
	case "acme_orders":
		r := row.(*ACMEOrderRow)
		var hosts []string
		if r.HostnamesJSON != "" {
			_ = decodeJSON(r.HostnamesJSON, &hosts)
		}
		return store.ACMEOrder{
			ID: store.ACMEOrderID(r.ID), AccountID: store.ACMEAccountID(r.AccountID),
			Hostnames: hosts, Status: store.ACMEOrderStatus(r.Status),
			OrderURL: r.OrderURL, FinalizeURL: r.FinalizeURL,
			CertificateURL: r.CertificateURL,
			ChallengeType:  store.ChallengeType(r.ChallengeType),
			UpdatedAt:      fromMicros(r.UpdatedAtUs), Error: r.Error,
		}, nil
	case "acme_certs":
		r := row.(*ACMECertRow)
		c := store.ACMECert{
			Hostname: r.Hostname, ChainPEM: r.ChainPEM, PrivateKeyPEM: r.PrivateKeyPEM,
			NotBefore: fromMicros(r.NotBeforeUs), NotAfter: fromMicros(r.NotAfterUs),
			Issuer: r.Issuer,
		}
		if r.OrderID != nil {
			c.OrderID = store.ACMEOrderID(*r.OrderID)
		}
		return c, nil
	case "webhooks":
		r := row.(*WebhookRow)
		w := store.Webhook{
			ID: store.WebhookID(r.ID), OwnerKind: store.WebhookOwnerKind(r.OwnerKind),
			OwnerID: r.OwnerID, TargetURL: r.TargetURL, HMACSecret: r.HMACSecret,
			DeliveryMode: store.DeliveryMode(r.DeliveryMode),
			Active:       r.Active,
			CreatedAt:    fromMicros(r.CreatedAtUs), UpdatedAt: fromMicros(r.UpdatedAtUs),
			TargetKind:            store.WebhookTargetKind(r.TargetKind),
			BodyMode:              store.WebhookBodyMode(r.BodyMode),
			ExtractedTextMaxBytes: r.ExtractedTextMaxBytes,
			TextRequired:          r.TextRequired,
		}
		if r.RetryPolicyJSON != "" {
			_ = decodeJSON(r.RetryPolicyJSON, &w.RetryPolicy)
		}
		return w, nil
	case "dmarc_reports_raw":
		r := row.(*DMARCReportRow)
		return store.DMARCReport{
			ID: store.DMARCReportID(r.ID), ReceivedAt: fromMicros(r.ReceivedAtUs),
			ReporterEmail: r.ReporterEmail, ReporterOrg: r.ReporterOrg,
			ReportID: r.ReportID, Domain: r.Domain,
			DateBegin: fromMicros(r.DateBeginUs), DateEnd: fromMicros(r.DateEndUs),
			XMLBlobHash: r.XMLBlobHash, ParsedOK: r.ParsedOK, ParseError: r.ParseError,
		}, nil
	case "dmarc_rows":
		r := row.(*DMARCRowRow)
		return store.DMARCRow{
			ID: store.DMARCRowID(r.ID), ReportID: store.DMARCReportID(r.ReportID),
			SourceIP: r.SourceIP, Count: r.Count, Disposition: int32(r.Disposition),
			SPFAligned: r.SPFAligned, DKIMAligned: r.DKIMAligned,
			SPFResult: r.SPFResult, DKIMResult: r.DKIMResult,
			HeaderFrom: r.HeaderFrom, EnvelopeFrom: r.EnvelopeFrom, EnvelopeTo: r.EnvelopeTo,
		}, nil
	case "mailbox_acl":
		r := row.(*MailboxACLRow)
		a := store.MailboxACL{
			ID: store.MailboxACLID(r.ID), MailboxID: store.MailboxID(r.MailboxID),
			Rights: store.ACLRights(r.RightsMask), GrantedBy: store.PrincipalID(r.GrantedBy),
			CreatedAt: fromMicros(r.CreatedAtUs),
		}
		if r.PrincipalID != nil {
			pid := store.PrincipalID(*r.PrincipalID)
			a.PrincipalID = &pid
		}
		return a, nil
	case "jmap_states":
		r := row.(*JMAPStateRow)
		return store.JMAPStates{
			PrincipalID: store.PrincipalID(r.PrincipalID),
			Mailbox:     r.MailboxState, Email: r.EmailState, Thread: r.ThreadState,
			Identity: r.IdentityState, EmailSubmission: r.EmailSubmissionState,
			VacationResponse: r.VacationResponseState, UpdatedAt: fromMicros(r.UpdatedAtUs),
			ShortcutCoach: r.ShortcutCoachState,
		}, nil
	case "jmap_email_submissions":
		r := row.(*JMAPEmailSubmissionRow)
		return store.EmailSubmissionRow{
			ID: r.ID, EnvelopeID: store.EnvelopeID(r.EnvelopeID),
			PrincipalID: store.PrincipalID(r.PrincipalID),
			IdentityID:  r.IdentityID, EmailID: store.MessageID(r.EmailID),
			ThreadID: r.ThreadID, SendAtUs: r.SendAtUs, CreatedAtUs: r.CreatedAtUs,
			UndoStatus: r.UndoStatus, Properties: r.Properties,
		}, nil
	case "jmap_identities":
		r := row.(*JMAPIdentityRow)
		return store.JMAPIdentity{
			ID: r.ID, PrincipalID: store.PrincipalID(r.PrincipalID),
			Name: r.Name, Email: r.Email,
			ReplyToJSON: r.ReplyToJSON, BccJSON: r.BccJSON,
			TextSignature: r.TextSignature, HTMLSignature: r.HTMLSignature,
			MayDelete:   r.MayDelete,
			CreatedAtUs: r.CreatedAtUs, UpdatedAtUs: r.UpdatedAtUs,
		}, nil
	case "tlsrpt_failures":
		r := row.(*TLSRPTFailureRow)
		return store.TLSRPTFailure{
			ID: store.TLSRPTFailureID(r.ID), RecordedAt: fromMicros(r.RecordedAtUs),
			PolicyDomain: r.PolicyDomain, ReceivingMTAHostname: r.ReceivingMTAHostname,
			FailureType: store.TLSRPTFailureType(r.FailureType),
			FailureCode: r.FailureCode, FailureDetailJSON: r.FailureDetailJSON,
		}, nil
	case "push_subscription":
		r := row.(*PushSubscriptionRow)
		ps := store.PushSubscription{
			ID:                     store.PushSubscriptionID(r.ID),
			PrincipalID:            store.PrincipalID(r.PrincipalID),
			DeviceClientID:         r.DeviceClientID,
			URL:                    r.URL,
			P256DH:                 r.P256DH,
			Auth:                   r.Auth,
			Types:                  splitCSV(r.TypesCSV),
			VerificationCode:       r.VerificationCode,
			Verified:               r.Verified,
			VAPIDKeyAtRegistration: r.VAPIDKeyAtRegistration,
			NotificationRulesJSON:  r.NotificationRulesJSON,
			QuietHoursTZ:           r.QuietHoursTZ,
			CreatedAt:              fromMicros(r.CreatedAtUs),
			UpdatedAt:              fromMicros(r.UpdatedAtUs),
		}
		if r.ExpiresAtUs != nil {
			t := fromMicros(*r.ExpiresAtUs)
			ps.Expires = &t
		}
		if r.QuietHoursStartLocal != nil {
			v := int(*r.QuietHoursStartLocal)
			ps.QuietHoursStartLocal = &v
		}
		if r.QuietHoursEndLocal != nil {
			v := int(*r.QuietHoursEndLocal)
			ps.QuietHoursEndLocal = &v
		}
		return ps, nil
	case "email_reactions":
		r := row.(*EmailReactionRow)
		return fakestore.ReactionDiagRow{
			EmailID:     store.MessageID(r.EmailID),
			Emoji:       r.Emoji,
			PrincipalID: store.PrincipalID(r.PrincipalID),
			CreatedAt:   fromMicros(r.CreatedAtUs),
		}, nil
	case "coach_events":
		r := row.(*CoachEventRow)
		return store.CoachEvent{
			ID:          r.ID,
			PrincipalID: store.PrincipalID(r.PrincipalID),
			Action:      r.Action,
			Method:      store.CoachInputMethod(r.InputMethod),
			Count:       int(r.EventCount),
			OccurredAt:  fromMicros(r.OccurredAt),
			RecordedAt:  fromMicros(r.RecordedAt),
		}, nil
	case "coach_dismiss":
		r := row.(*CoachDismissRow)
		d := store.CoachDismiss{
			PrincipalID:  store.PrincipalID(r.PrincipalID),
			Action:       r.Action,
			DismissCount: int(r.DismissCount),
			UpdatedAt:    fromMicros(r.UpdatedAt),
		}
		if r.DismissUntil != nil {
			t := fromMicros(*r.DismissUntil)
			d.DismissUntil = &t
		}
		return d, nil
	case "ses_seen_messages":
		// The fakestore DiagInsert default branch ignores unknown tables,
		// so we simply return the SESSeenMessageRow and the fakestore
		// will silently discard it (SES dedupe is a best-effort cache).
		r := row.(*SESSeenMessageRow)
		return r, nil
	case "blob_refs":
		r := row.(*BlobRefRow)
		return fakestore.BlobRefEntry{Hash: r.Hash, Size: r.Size, RefCount: r.RefCount}, nil
	}
	return nil, fmt.Errorf("fakestore: unknown table %q", table)
}
