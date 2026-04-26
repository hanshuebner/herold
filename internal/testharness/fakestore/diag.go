package fakestore

import (
	"context"
	"sort"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// DiagSnapshot returns a deep snapshot of every row the in-memory
// store currently holds, mapped to the row shapes used by the
// internal/diag/backup pipeline. The snapshot is taken under the
// write lock so the caller observes a consistent view; subsequent
// mutations are not reflected.
//
// The returned tables map mirrors backup.TableNames; the function is
// the fakestore equivalent of "SELECT * FROM <table>" for every
// covered table. Phase 2 tables are produced when the in-memory
// state has been touched (lazy ensurePhase2 path); empty otherwise.
//
// Exported solely for the diag tooling; STANDARDS §1 rule 3
// (storage-centric) does not apply to a tests-only backend.
func (s *Store) DiagSnapshot() *DiagDump {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dd := &DiagDump{
		Tables: map[string][]any{},
		Blobs:  map[string]int64{},
	}

	// Iterate maps via sorted keys so callers see deterministic order.
	pids := sortedPrincipalIDs(s.principals)
	for _, pid := range pids {
		p := s.principals[pid]
		dd.Tables["principals"] = append(dd.Tables["principals"], p)
	}
	dnames := make([]string, 0, len(s.domains))
	for n := range s.domains {
		dnames = append(dnames, n)
	}
	sort.Strings(dnames)
	for _, n := range dnames {
		dd.Tables["domains"] = append(dd.Tables["domains"], s.domains[n])
	}
	pnames := make([]string, 0, len(s.providers))
	for n := range s.providers {
		pnames = append(pnames, n)
	}
	sort.Strings(pnames)
	for _, n := range pnames {
		dd.Tables["oidc_providers"] = append(dd.Tables["oidc_providers"], s.providers[n])
	}
	lkeys := make([]string, 0, len(s.oidcLinks))
	for k := range s.oidcLinks {
		lkeys = append(lkeys, k)
	}
	sort.Strings(lkeys)
	for _, k := range lkeys {
		dd.Tables["oidc_links"] = append(dd.Tables["oidc_links"], s.oidcLinks[k])
	}
	akids := make([]store.APIKeyID, 0, len(s.apiKeys))
	for id := range s.apiKeys {
		akids = append(akids, id)
	}
	sort.Slice(akids, func(i, j int) bool { return akids[i] < akids[j] })
	for _, id := range akids {
		dd.Tables["api_keys"] = append(dd.Tables["api_keys"], s.apiKeys[id])
	}
	aids := make([]store.AliasID, 0, len(s.aliases))
	for id := range s.aliases {
		aids = append(aids, id)
	}
	sort.Slice(aids, func(i, j int) bool { return aids[i] < aids[j] })
	for _, id := range aids {
		dd.Tables["aliases"] = append(dd.Tables["aliases"], s.aliases[id])
	}
	scriptPids := make([]store.PrincipalID, 0, len(s.sieveScripts))
	for pid := range s.sieveScripts {
		scriptPids = append(scriptPids, pid)
	}
	sort.Slice(scriptPids, func(i, j int) bool { return scriptPids[i] < scriptPids[j] })
	for _, pid := range scriptPids {
		dd.Tables["sieve_scripts"] = append(dd.Tables["sieve_scripts"],
			SieveEntry{PrincipalID: pid, Script: s.sieveScripts[pid]})
	}
	mbids := make([]store.MailboxID, 0, len(s.mailboxes))
	for id := range s.mailboxes {
		mbids = append(mbids, id)
	}
	sort.Slice(mbids, func(i, j int) bool { return mbids[i] < mbids[j] })
	for _, id := range mbids {
		dd.Tables["mailboxes"] = append(dd.Tables["mailboxes"], s.mailboxes[id])
	}
	mids := make([]store.MessageID, 0, len(s.messages))
	for id := range s.messages {
		mids = append(mids, id)
	}
	sort.Slice(mids, func(i, j int) bool { return mids[i] < mids[j] })
	for _, id := range mids {
		dd.Tables["messages"] = append(dd.Tables["messages"], s.messages[id])
	}
	for _, pid := range pids {
		for _, c := range s.stateChanges[pid] {
			dd.Tables["state_changes"] = append(dd.Tables["state_changes"], c)
		}
	}
	dd.Tables["audit_log"] = make([]any, 0, len(s.auditLog))
	for _, e := range s.auditLog {
		dd.Tables["audit_log"] = append(dd.Tables["audit_log"], e)
	}
	ckeys := make([]string, 0, len(s.cursors))
	for k := range s.cursors {
		ckeys = append(ckeys, k)
	}
	sort.Strings(ckeys)
	for _, k := range ckeys {
		dd.Tables["cursors"] = append(dd.Tables["cursors"], CursorEntry{Key: k, Seq: s.cursors[k]})
	}

	if s.phase2 != nil {
		qids := make([]store.QueueItemID, 0, len(s.phase2.queue))
		for id := range s.phase2.queue {
			qids = append(qids, id)
		}
		sort.Slice(qids, func(i, j int) bool { return qids[i] < qids[j] })
		for _, id := range qids {
			dd.Tables["queue"] = append(dd.Tables["queue"], s.phase2.queue[id])
		}
		dkimIDs := make([]store.DKIMKeyID, 0, len(s.phase2.dkimKeys))
		for id := range s.phase2.dkimKeys {
			dkimIDs = append(dkimIDs, id)
		}
		sort.Slice(dkimIDs, func(i, j int) bool { return dkimIDs[i] < dkimIDs[j] })
		for _, id := range dkimIDs {
			dd.Tables["dkim_keys"] = append(dd.Tables["dkim_keys"], s.phase2.dkimKeys[id])
		}
		acmeAcctIDs := make([]store.ACMEAccountID, 0, len(s.phase2.acmeAccts))
		for id := range s.phase2.acmeAccts {
			acmeAcctIDs = append(acmeAcctIDs, id)
		}
		sort.Slice(acmeAcctIDs, func(i, j int) bool { return acmeAcctIDs[i] < acmeAcctIDs[j] })
		for _, id := range acmeAcctIDs {
			dd.Tables["acme_accounts"] = append(dd.Tables["acme_accounts"], s.phase2.acmeAccts[id])
		}
		acmeOrdIDs := make([]store.ACMEOrderID, 0, len(s.phase2.acmeOrders))
		for id := range s.phase2.acmeOrders {
			acmeOrdIDs = append(acmeOrdIDs, id)
		}
		sort.Slice(acmeOrdIDs, func(i, j int) bool { return acmeOrdIDs[i] < acmeOrdIDs[j] })
		for _, id := range acmeOrdIDs {
			dd.Tables["acme_orders"] = append(dd.Tables["acme_orders"], s.phase2.acmeOrders[id])
		}
		certHosts := make([]string, 0, len(s.phase2.acmeCerts))
		for h := range s.phase2.acmeCerts {
			certHosts = append(certHosts, h)
		}
		sort.Strings(certHosts)
		for _, h := range certHosts {
			dd.Tables["acme_certs"] = append(dd.Tables["acme_certs"], s.phase2.acmeCerts[h])
		}
		whIDs := make([]store.WebhookID, 0, len(s.phase2.webhooks))
		for id := range s.phase2.webhooks {
			whIDs = append(whIDs, id)
		}
		sort.Slice(whIDs, func(i, j int) bool { return whIDs[i] < whIDs[j] })
		for _, id := range whIDs {
			dd.Tables["webhooks"] = append(dd.Tables["webhooks"], s.phase2.webhooks[id])
		}
		drIDs := make([]store.DMARCReportID, 0, len(s.phase2.dmarcReports))
		for id := range s.phase2.dmarcReports {
			drIDs = append(drIDs, id)
		}
		sort.Slice(drIDs, func(i, j int) bool { return drIDs[i] < drIDs[j] })
		for _, id := range drIDs {
			dd.Tables["dmarc_reports_raw"] = append(dd.Tables["dmarc_reports_raw"], s.phase2.dmarcReports[id])
		}
		drowIDs := make([]store.DMARCRowID, 0, len(s.phase2.dmarcRows))
		for id := range s.phase2.dmarcRows {
			drowIDs = append(drowIDs, id)
		}
		sort.Slice(drowIDs, func(i, j int) bool { return drowIDs[i] < drowIDs[j] })
		for _, id := range drowIDs {
			dd.Tables["dmarc_rows"] = append(dd.Tables["dmarc_rows"], s.phase2.dmarcRows[id])
		}
		mbAclIDs := make([]store.MailboxACLID, 0, len(s.phase2.mailboxACL))
		for id := range s.phase2.mailboxACL {
			mbAclIDs = append(mbAclIDs, id)
		}
		sort.Slice(mbAclIDs, func(i, j int) bool { return mbAclIDs[i] < mbAclIDs[j] })
		for _, id := range mbAclIDs {
			dd.Tables["mailbox_acl"] = append(dd.Tables["mailbox_acl"], s.phase2.mailboxACL[id])
		}
		jmapPids := make([]store.PrincipalID, 0, len(s.phase2.jmapStates))
		for pid := range s.phase2.jmapStates {
			jmapPids = append(jmapPids, pid)
		}
		sort.Slice(jmapPids, func(i, j int) bool { return jmapPids[i] < jmapPids[j] })
		for _, pid := range jmapPids {
			dd.Tables["jmap_states"] = append(dd.Tables["jmap_states"], s.phase2.jmapStates[pid])
		}
		esIDs := make([]string, 0, len(s.phase2.emailSubmissions))
		for id := range s.phase2.emailSubmissions {
			esIDs = append(esIDs, id)
		}
		sort.Strings(esIDs)
		for _, id := range esIDs {
			dd.Tables["jmap_email_submissions"] = append(dd.Tables["jmap_email_submissions"],
				s.phase2.emailSubmissions[id])
		}
		identIDs := make([]string, 0, len(s.phase2.jmapIdentities))
		for id := range s.phase2.jmapIdentities {
			identIDs = append(identIDs, id)
		}
		sort.Strings(identIDs)
		for _, id := range identIDs {
			dd.Tables["jmap_identities"] = append(dd.Tables["jmap_identities"],
				s.phase2.jmapIdentities[id])
		}
		tlsIDs := make([]store.TLSRPTFailureID, 0, len(s.phase2.tlsrpt))
		for id := range s.phase2.tlsrpt {
			tlsIDs = append(tlsIDs, id)
		}
		sort.Slice(tlsIDs, func(i, j int) bool { return tlsIDs[i] < tlsIDs[j] })
		for _, id := range tlsIDs {
			dd.Tables["tlsrpt_failures"] = append(dd.Tables["tlsrpt_failures"], s.phase2.tlsrpt[id])
		}
		psIDs := make([]store.PushSubscriptionID, 0, len(s.phase2.pushSubscriptions))
		for id := range s.phase2.pushSubscriptions {
			psIDs = append(psIDs, id)
		}
		sort.Slice(psIDs, func(i, j int) bool { return psIDs[i] < psIDs[j] })
		for _, id := range psIDs {
			dd.Tables["push_subscription"] = append(dd.Tables["push_subscription"],
				s.phase2.pushSubscriptions[id])
		}
	}

	// ses_seen_messages: fakestore never persists SES dedupe rows, so
	// we always emit an empty slice to satisfy the TableNames iteration.
	dd.Tables["ses_seen_messages"] = []any{}

	// email_reactions: fakestore stores reactions in an in-memory map
	// keyed by (emailID, emoji, principalID). Emit them in deterministic
	// order for the diag round-trip.
	if s.reactions != nil {
		type rkey struct {
			emailID     store.MessageID
			emoji       string
			principalID store.PrincipalID
		}
		keys := make([]rkey, 0, len(s.reactions.rows))
		for k := range s.reactions.rows {
			keys = append(keys, rkey{k.emailID, k.emoji, k.principalID})
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].emailID != keys[j].emailID {
				return keys[i].emailID < keys[j].emailID
			}
			if keys[i].emoji != keys[j].emoji {
				return keys[i].emoji < keys[j].emoji
			}
			return keys[i].principalID < keys[j].principalID
		})
		for _, k := range keys {
			dd.Tables["email_reactions"] = append(dd.Tables["email_reactions"],
				ReactionDiagRow{
					EmailID:     k.emailID,
					Emoji:       k.emoji,
					PrincipalID: k.principalID,
					CreatedAt:   s.reactions.rows[reactionKey{k.emailID, k.emoji, k.principalID}],
				})
		}
	}
	if dd.Tables["email_reactions"] == nil {
		dd.Tables["email_reactions"] = []any{}
	}

	// coach_events and coach_dismiss: fakestore stores these in the
	// coachData struct. Emit them sorted for determinism.
	if s.coach != nil {
		// Collect all events sorted by ID (already monotone from
		// nextID).
		var evs []store.CoachEvent
		for _, evList := range s.coach.events {
			evs = append(evs, evList...)
		}
		sort.Slice(evs, func(i, j int) bool { return evs[i].ID < evs[j].ID })
		for _, ev := range evs {
			dd.Tables["coach_events"] = append(dd.Tables["coach_events"], ev)
		}

		// Collect all dismiss rows sorted by (principal_id, action).
		type dkey struct {
			pid    store.PrincipalID
			action string
		}
		dkeys := make([]dkey, 0, len(s.coach.dismiss))
		for k := range s.coach.dismiss {
			dkeys = append(dkeys, dkey{k.Principal, k.Action})
		}
		sort.Slice(dkeys, func(i, j int) bool {
			if dkeys[i].pid != dkeys[j].pid {
				return dkeys[i].pid < dkeys[j].pid
			}
			return dkeys[i].action < dkeys[j].action
		})
		for _, dk := range dkeys {
			dd.Tables["coach_dismiss"] = append(dd.Tables["coach_dismiss"],
				s.coach.dismiss[coachKey{dk.pid, dk.action}])
		}
	}
	if dd.Tables["coach_events"] == nil {
		dd.Tables["coach_events"] = []any{}
	}
	if dd.Tables["coach_dismiss"] == nil {
		dd.Tables["coach_dismiss"] = []any{}
	}

	// Blobs: include refcount=1 for every present blob; the caller's
	// consumer treats this as the "blob_refs" table input.
	bhashes := make([]string, 0, len(s.blobSize))
	for h := range s.blobSize {
		bhashes = append(bhashes, h)
	}
	sort.Strings(bhashes)
	for _, h := range bhashes {
		dd.Blobs[h] = s.blobSize[h]
		dd.Tables["blob_refs"] = append(dd.Tables["blob_refs"], BlobRefEntry{
			Hash: h, Size: s.blobSize[h], RefCount: int64(s.blobRefs[h]),
		})
	}
	return dd
}

// DiagDump is the snapshot returned by DiagSnapshot. Fields are
// exported so the diag adapter can read them from outside this
// package without reflection.
type DiagDump struct {
	Tables map[string][]any
	Blobs  map[string]int64
}

// SieveEntry represents one row of the sieve_scripts table in the
// fakestore snapshot. The fakestore stores scripts as a plain
// principal->text map; the diag adapter materialises the table row
// shape via this helper.
type SieveEntry struct {
	PrincipalID store.PrincipalID
	Script      string
}

// CursorEntry mirrors one row of the cursors table for the diag
// adapter.
type CursorEntry struct {
	Key string
	Seq uint64
}

// BlobRefEntry mirrors one row of the blob_refs table for the diag
// adapter (the fakestore stores refcounts in a separate map).
type BlobRefEntry struct {
	Hash     string
	Size     int64
	RefCount int64
}

// ReactionDiagRow is the fakestore-native representation of one row
// from the email_reactions table. Exported for the diag adapter in
// internal/diag/backup which converts between this and EmailReactionRow.
type ReactionDiagRow struct {
	EmailID     store.MessageID
	Emoji       string
	PrincipalID store.PrincipalID
	CreatedAt   time.Time
}

// DiagReset wipes every row from the in-memory store and resets the
// monotonic counters. Used by the diag/restore ModeReplace path.
func (s *Store) DiagReset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.principals = map[store.PrincipalID]store.Principal{}
	s.byEmail = map[string]store.PrincipalID{}
	s.mailboxes = map[store.MailboxID]store.Mailbox{}
	s.messages = map[store.MessageID]store.Message{}
	s.aliases = map[store.AliasID]store.Alias{}
	s.aliasBy = map[string]store.AliasID{}
	s.domains = map[string]store.Domain{}
	s.providers = map[string]store.OIDCProvider{}
	s.oidcLinks = map[string]store.OIDCLink{}
	s.apiKeys = map[store.APIKeyID]store.APIKey{}
	s.keyByHash = map[string]store.APIKeyID{}
	s.blobSize = map[string]int64{}
	s.blobRefs = map[string]int{}
	s.stateChanges = map[store.PrincipalID][]store.StateChange{}
	s.changeSeq = map[store.PrincipalID]store.ChangeSeq{}
	s.ftsChanges = nil
	s.ftsSeq = 0
	s.cursors = map[string]uint64{}
	s.auditLog = nil
	s.nextAuditLogID = 1
	s.ftsDocs = map[store.MessageID]ftsDoc{}
	s.sieveScripts = map[store.PrincipalID]string{}
	s.phase2 = nil
	s.reactions = nil
	s.coach = nil
	s.nextPrincipalID = 1
	s.nextMailboxID = 1
	s.nextMessageID = 1
	s.nextAliasID = 1
	s.nextAPIKeyID = 1
	s.nextUIDValidity = 1
}

// DiagInsert mirrors one row from the diag/restore pipeline into the
// in-memory store. The row's concrete type names the destination map.
// Used solely by the diag/backup adapter for fakestore-based tests
// and migrations.
//
// Counters are bumped to one past the largest inserted ID so a
// subsequent normal insert does not collide.
func (s *Store) DiagInsert(table string, row any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	switch table {
	case "principals":
		p := row.(store.Principal)
		s.principals[p.ID] = p
		s.byEmail[p.CanonicalEmail] = p.ID
		if p.ID >= s.nextPrincipalID {
			s.nextPrincipalID = p.ID + 1
		}
	case "domains":
		d := row.(store.Domain)
		s.domains[d.Name] = d
	case "oidc_providers":
		p := row.(store.OIDCProvider)
		s.providers[p.Name] = p
	case "oidc_links":
		l := row.(store.OIDCLink)
		s.oidcLinks[oidcLinkKey(l.ProviderName, l.Subject)] = l
	case "api_keys":
		k := row.(store.APIKey)
		s.apiKeys[k.ID] = k
		s.keyByHash[k.Hash] = k.ID
		if k.ID >= s.nextAPIKeyID {
			s.nextAPIKeyID = k.ID + 1
		}
	case "aliases":
		a := row.(store.Alias)
		s.aliases[a.ID] = a
		s.aliasBy[aliasKey(a.LocalPart, a.Domain)] = a.ID
		if a.ID >= s.nextAliasID {
			s.nextAliasID = a.ID + 1
		}
	case "sieve_scripts":
		e := row.(SieveEntry)
		s.sieveScripts[e.PrincipalID] = e.Script
	case "mailboxes":
		mb := row.(store.Mailbox)
		s.mailboxes[mb.ID] = mb
		if mb.ID >= s.nextMailboxID {
			s.nextMailboxID = mb.ID + 1
		}
		if mb.UIDValidity >= s.nextUIDValidity {
			s.nextUIDValidity = mb.UIDValidity + 1
		}
	case "messages":
		m := row.(store.Message)
		s.messages[m.ID] = m
		if m.ID >= s.nextMessageID {
			s.nextMessageID = m.ID + 1
		}
		// Refcount accounting: bump for the blob this message
		// references so subsequent expunge does not underflow.
		if m.Blob.Hash != "" {
			s.blobRefs[m.Blob.Hash]++
		}
	case "state_changes":
		c := row.(store.StateChange)
		s.stateChanges[c.PrincipalID] = append(s.stateChanges[c.PrincipalID], c)
		if c.Seq > s.changeSeq[c.PrincipalID] {
			s.changeSeq[c.PrincipalID] = c.Seq
		}
	case "audit_log":
		e := row.(store.AuditLogEntry)
		s.auditLog = append(s.auditLog, e)
		if e.ID >= s.nextAuditLogID {
			s.nextAuditLogID = e.ID + 1
		}
	case "cursors":
		c := row.(CursorEntry)
		s.cursors[c.Key] = c.Seq
	case "queue":
		q := row.(store.QueueItem)
		s.phase2.queue[q.ID] = q
		if q.ID >= s.phase2.nextQueueID {
			s.phase2.nextQueueID = q.ID + 1
		}
	case "dkim_keys":
		k := row.(store.DKIMKey)
		s.phase2.dkimKeys[k.ID] = k
		if k.ID >= s.phase2.nextDKIMID {
			s.phase2.nextDKIMID = k.ID + 1
		}
	case "acme_accounts":
		a := row.(store.ACMEAccount)
		s.phase2.acmeAccts[a.ID] = a
		if a.ID >= s.phase2.nextACMEAcc {
			s.phase2.nextACMEAcc = a.ID + 1
		}
	case "acme_orders":
		o := row.(store.ACMEOrder)
		s.phase2.acmeOrders[o.ID] = o
		if o.ID >= s.phase2.nextACMEOrd {
			s.phase2.nextACMEOrd = o.ID + 1
		}
	case "acme_certs":
		c := row.(store.ACMECert)
		s.phase2.acmeCerts[c.Hostname] = c
	case "webhooks":
		w := row.(store.Webhook)
		s.phase2.webhooks[w.ID] = w
		if w.ID >= s.phase2.nextWebhook {
			s.phase2.nextWebhook = w.ID + 1
		}
	case "dmarc_reports_raw":
		d := row.(store.DMARCReport)
		s.phase2.dmarcReports[d.ID] = d
		if d.ID >= s.phase2.nextReportID {
			s.phase2.nextReportID = d.ID + 1
		}
	case "dmarc_rows":
		d := row.(store.DMARCRow)
		s.phase2.dmarcRows[d.ID] = d
		if d.ID >= s.phase2.nextRowID {
			s.phase2.nextRowID = d.ID + 1
		}
	case "mailbox_acl":
		a := row.(store.MailboxACL)
		s.phase2.mailboxACL[a.ID] = a
		if a.ID >= s.phase2.nextACLID {
			s.phase2.nextACLID = a.ID + 1
		}
	case "jmap_states":
		j := row.(store.JMAPStates)
		s.phase2.jmapStates[j.PrincipalID] = j
	case "jmap_email_submissions":
		e := row.(store.EmailSubmissionRow)
		s.phase2.emailSubmissions[e.ID] = e
	case "jmap_identities":
		i := row.(store.JMAPIdentity)
		s.phase2.jmapIdentities[i.ID] = i
	case "tlsrpt_failures":
		t := row.(store.TLSRPTFailure)
		s.phase2.tlsrpt[t.ID] = t
		if t.ID >= s.phase2.nextTLSRPT {
			s.phase2.nextTLSRPT = t.ID + 1
		}
	case "push_subscription":
		ps := row.(store.PushSubscription)
		s.phase2.pushSubscriptions[ps.ID] = ps
		if ps.ID >= s.phase2.nextPushSubscription {
			s.phase2.nextPushSubscription = ps.ID + 1
		}
	case "email_reactions":
		rdr := row.(ReactionDiagRow)
		s.reactionStore().rows[reactionKey{
			emailID:     rdr.EmailID,
			emoji:       rdr.Emoji,
			principalID: rdr.PrincipalID,
		}] = rdr.CreatedAt
	case "coach_events":
		ev := row.(store.CoachEvent)
		s.ensureCoach()
		k := coachKey{Principal: ev.PrincipalID, Action: ev.Action}
		s.coach.events[k] = append(s.coach.events[k], ev)
		if ev.ID >= s.coach.nextID {
			s.coach.nextID = ev.ID + 1
		}
	case "coach_dismiss":
		d := row.(store.CoachDismiss)
		s.ensureCoach()
		var dup store.CoachDismiss
		dup = d
		if d.DismissUntil != nil {
			t := *d.DismissUntil
			dup.DismissUntil = &t
		}
		s.coach.dismiss[coachKey{Principal: d.PrincipalID, Action: d.Action}] = dup
	case "blob_refs":
		b := row.(BlobRefEntry)
		s.blobSize[b.Hash] = b.Size
		s.blobRefs[b.Hash] = int(b.RefCount)
	default:
		_ = ctxUnused
	}
	return nil
}

// ctxUnused silences a linter complaint about an unused branch.
var ctxUnused context.Context = context.TODO()

func sortedPrincipalIDs(m map[store.PrincipalID]store.Principal) []store.PrincipalID {
	out := make([]store.PrincipalID, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
