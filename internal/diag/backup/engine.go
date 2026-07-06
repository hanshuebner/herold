package backup

// engine.go — reflection-based generic dump + restore engine.
//
// DESIGN OVERVIEW
//
// Every backed-up table has a typed *Row struct in rows.go. Each struct
// field carries a "json" tag whose name is exactly the SQL column name
// (the two namespaces are intentionally isomorphic so operators reading
// JSONL with jq see the same field names as in the schema). The engine
// exploits this identity to generate SELECT and INSERT statements, and
// to bind scan targets and insert values, without a per-table switch.
//
// STRUCT TAGS CONSUMED BY THE ENGINE
//
//   json:"col_name[,omitempty]"
//     Column name. Mandatory on every field. Fields with json:"-" are
//     skipped (none of the Row structs use this today; noted for safety).
//
//   nullable:"true"
//     Marks a []byte field that should be stored as SQL NULL when the Go
//     slice is nil or empty, rather than as an empty BLOB. Used for
//     optional JSON blob columns (e.g. notification_rules_json,
//     reactions_json, credential_ct — all NULLable in the schema). Without
//     this tag, a nil []byte is passed to the driver as nil (which also
//     becomes NULL for NULLable columns), so the tag is only needed for
//     fields where an *empty* non-nil slice should still produce NULL.
//     In practice, these fields are never populated with []byte{} by the
//     application layer, so the tag is a documentation signal and a
//     correctness guard.
//
//   default:"value"
//     Used on string fields that must never be empty in the DB even when
//     absent from an older backup bundle. The engine substitutes "value"
//     on INSERT when the field is the empty string. Currently:
//       - api_keys.scope_json          default:'["admin"]'
//       - api_keys.allowed_from_addresses_json   default:"[]"
//       - api_keys.allowed_from_domains_json     default:"[]"
//       - state_changes.cause          default:"user"
//
// TYPE HANDLING
//
// Go -> SQL (INSERT / bind):
//   bool      — converted to int64 (0 or 1) for SQLite; the engine
//               detects bool via reflection.
//   *bool     — not used in Row structs; reserved.
//   *int64    — nil → driver nil (SQL NULL); non-nil → dereferenced value.
//   *string   — nil → driver nil (SQL NULL); non-nil → dereferenced value.
//   *float64  — nil → driver nil (SQL NULL); non-nil → dereferenced value.
//   []byte    — passed as-is (nil → SQL NULL for NULLable cols). When the
//               nullable:"true" tag is present, an empty non-nil slice is
//               also passed as nil.
//   Everything else (int64, string, float64) — passed directly.
//
// SQL -> Go (SELECT / scan):
//   bool fields      — scanned into a local int64, then stored as != 0.
//   *int64 fields    — scanned into sql.NullInt64; stored as pointer when Valid.
//   *string fields   — scanned into sql.NullString; stored as pointer when Valid.
//   *float64 fields  — scanned into sql.NullFloat64; stored as pointer when Valid.
//   []byte fields    — scanned directly (nil for NULL, slice for non-NULL).
//   int64 / string / float64 — scanned directly into the field pointer.
//
// REGISTRY
//
// tableReg maps table name to tableDesc. A tableDesc carries:
//   - the zero-value Row prototype (used by rowsForTable and as scan target
//     template),
//   - the ORDER BY clause for deterministic dumps,
//   - an optional insertHook for tables with non-standard insert behaviour
//     (currently none; the default tag handles all existing cases).
//
// LIMITATIONS / NON-GOALS
//
// The engine targets SQLite only (this file is used by adapter_sqlite.go).
// The postgres adapter, when it lands, will re-use the same Row structs and
// the same engine with a different placeholder style ($1/$2... vs ?/?...).

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// tableDesc describes one backed-up table in the registry.
type tableDesc struct {
	// proto is a pointer to a zero-value Row struct. Reflection reads its
	// type to discover field names/types and to allocate fresh instances.
	proto any

	// orderBy is the ORDER BY clause appended to the SELECT statement,
	// without the "ORDER BY" keyword. Must produce a stable, deterministic
	// ordering for snapshot consistency and deterministic test diffs.
	orderBy string
}

// tableReg is the single source of truth that backs rowsForTable,
// EnumerateRows, and Insert. Adding a table requires one entry here and
// in manifest.TableNames (plus the Row struct in rows.go).
var tableReg = map[string]tableDesc{
	"domains":                    {&DomainRow{}, "name"},
	"principals":                 {&PrincipalRow{}, "id"},
	"principal_managed_domains":  {&PrincipalManagedDomainRow{}, "principal_id, domain"},
	"push_subscription":          {&PushSubscriptionRow{}, "id"},
	"oidc_providers":             {&OIDCProviderRow{}, "name"},
	"oidc_links":                 {&OIDCLinkRow{}, "provider_name, subject"},
	"api_keys":                   {&APIKeyRow{}, "id"},
	"aliases":                    {&AliasRow{}, "id"},
	"sieve_scripts":              {&SieveScriptRow{}, "principal_id"},
	"sieve_named_scripts":        {&SieveNamedScriptRow{}, "principal_id, name"},
	"managed_rules":              {&ManagedRuleRow{}, "id"},
	"jmap_categorisation_config": {&CategorisationConfigRow{}, "principal_id"},
	"chat_account_settings":      {&ChatAccountSettingsRow{}, "principal_id"},
	"coach_events":               {&CoachEventRow{}, "id"},
	"coach_dismiss":              {&CoachDismissRow{}, "principal_id, action"},
	"mailboxes":                  {&MailboxRow{}, "id"},
	"messages":                   {&MessageRow{}, "id"},
	"message_mailboxes":          {&MessageMailboxRow{}, "message_id, mailbox_id"},
	"email_pretrash_mailboxes":   {&EmailPretrashMailboxRow{}, "email_id, mailbox_id"},
	"email_reactions":            {&EmailReactionRow{}, "email_id, emoji, principal_id"},
	"mailbox_acl":                {&MailboxACLRow{}, "id"},
	"state_changes":              {&StateChangeRow{}, "id"},
	"audit_log":                  {&AuditLogRow{}, "id"},
	"cursors":                    {&CursorRow{}, "key"},
	"queue":                      {&QueueRow{}, "id"},
	"dkim_keys":                  {&DKIMKeyRow{}, "id"},
	"acme_accounts":              {&ACMEAccountRow{}, "id"},
	"acme_orders":                {&ACMEOrderRow{}, "id"},
	"acme_certs":                 {&ACMECertRow{}, "hostname"},
	"webhooks":                   {&WebhookRow{}, "id"},
	"dmarc_reports_raw":          {&DMARCReportRow{}, "id"},
	"dmarc_rows":                 {&DMARCRowRow{}, "id"},
	"jmap_states":                {&JMAPStateRow{}, "principal_id"},
	"jmap_email_submissions":     {&JMAPEmailSubmissionRow{}, "id"},
	"jmap_identities":            {&JMAPIdentityRow{}, "id"},
	"identity_submission":        {&IdentitySubmissionRow{}, "identity_id"},
	"tlsrpt_failures":            {&TLSRPTFailureRow{}, "id"},
	"address_books":              {&AddressBookRow{}, "id"},
	"contacts":                   {&ContactRow{}, "id"},
	"calendars":                  {&CalendarRow{}, "id"},
	"calendar_events":            {&CalendarEventRow{}, "id"},
	"chat_conversations":         {&ChatConversationRow{}, "id"},
	"chat_memberships":           {&ChatMembershipRow{}, "id"},
	"chat_messages":              {&ChatMessageRow{}, "id"},
	"chat_blocks":                {&ChatBlockRow{}, "blocker_principal_id, blocked_principal_id"},
	"chat_dm_pairs":              {&ChatDMPairRow{}, "pid_lo, pid_hi"},
	"ses_seen_messages":          {&SESSeenMessageRow{}, "message_id"},
	"llm_classifications":        {&LLMClassificationRow{}, "message_id"},
	"seen_addresses":             {&SeenAddressRow{}, "id"},
	"inbound_attpol_domain":      {&InboundAttpolDomainRow{}, "domain"},
	"inbound_attpol_recipient":   {&InboundAttpolRecipientRow{}, "address"},
	"tagged_address_filters":     {&TaggedAddressFilterRow{}, "id"},
	"tagged_address_dismissals":  {&TaggedAddressDismissalRow{}, "principal_id, base_identity_id, suffix"},
	"file_shares":                {&FileShareRow{}, "id"},
	"blob_refs":                  {&BlobRefRow{}, "hash"},
	"imapimport_account":         {&IMAPImportAccountRow{}, "id"},
	"imapimport_folder_map":      {&IMAPImportFolderMapRow{}, "account_id, upstream_folder"},
	"imapimport_folder_cursor":   {&IMAPImportFolderCursorRow{}, "account_id, upstream_folder"},
	"imapimport_message_state":   {&IMAPImportMessageStateRow{}, "account_id, upstream_folder, upstream_uid"},
	"sessions":                   {&SessionRow{}, "session_id"},
	"session_elevations":         {&SessionElevationRow{}, "session_id"},
	"clientlog":                  {&ClientLogRow{}, "id"},
}

// colName extracts the SQL column name from a struct field's json tag.
// Returns ("", false) when the field should be skipped (json:"-").
func colName(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("json")
	if tag == "" {
		// No json tag — use the lower-cased field name as fallback (should not
		// happen with the current Row structs, but is safe).
		return strings.ToLower(f.Name), true
	}
	name := strings.SplitN(tag, ",", 2)[0]
	if name == "-" {
		return "", false
	}
	return name, true
}

// fieldKind classifies a struct field into the type bucket the engine uses
// to decide how to scan and bind.
type fieldKind int

const (
	fkInt64       fieldKind = iota // int64, stored directly
	fkString                       // string, stored directly (non-nullable)
	fkFloat64                      // float64, stored directly
	fkBytes                        // []byte, stored directly (nil = NULL)
	fkBool                         // bool, stored as 0/1 integer in SQLite
	fkNullInt64                    // *int64, NULL when nil
	fkNullString                   // *string, NULL when nil
	fkNullFloat64                  // *float64, NULL when nil
	fkNullStringZ                  // string with nullable:"true" — DB NULL↔Go ""
)

// classifyField returns the fieldKind for a struct field, or -1 if the field
// is of an unsupported type (caller should panic on registration).
// The nullable tag on a string field promotes it to fkNullStringZ (empty = NULL).
func classifyField(f reflect.StructField) fieldKind {
	t := f.Type
	switch t.Kind() {
	case reflect.Bool:
		return fkBool
	case reflect.Int64:
		return fkInt64
	case reflect.Float64:
		return fkFloat64
	case reflect.String:
		if f.Tag.Get("nullable") == "true" {
			return fkNullStringZ
		}
		return fkString
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return fkBytes
		}
	case reflect.Ptr:
		switch t.Elem().Kind() {
		case reflect.Int64:
			return fkNullInt64
		case reflect.String:
			return fkNullString
		case reflect.Float64:
			return fkNullFloat64
		}
	}
	return -1 // unsupported
}

// fieldMeta is the pre-computed metadata for one struct field.
type fieldMeta struct {
	colName  string    // SQL column name (from json tag)
	kind     fieldKind // type classification
	idx      int       // field index in the struct
	nullable bool      // json:"nullable" — []byte: nil/empty → SQL NULL
	defVal   string    // "default" tag value for empty strings on INSERT
}

// buildFieldMeta reflects on a Row struct pointer and returns ordered metadata
// for all exported fields that have a json tag and are not json:"-".
// Panics if an unsupported field type is encountered (catches authoring errors
// at program startup, not at test time).
func buildFieldMeta(proto any) []fieldMeta {
	t := reflect.TypeOf(proto)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	var metas []fieldMeta
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		col, ok := colName(f)
		if !ok {
			continue // json:"-"
		}
		k := classifyField(f)
		if k < 0 {
			panic(fmt.Sprintf("engine: unsupported field type %v in %T.%s", f.Type, proto, f.Name))
		}
		nullable := f.Tag.Get("nullable") == "true"
		defVal := f.Tag.Get("default")
		metas = append(metas, fieldMeta{
			colName:  col,
			kind:     k,
			idx:      i,
			nullable: nullable,
			defVal:   defVal,
		})
	}
	return metas
}

// metaCache caches pre-computed field metadata per table name. Access
// is serialised by metaCacheMu so concurrent tests can call getFieldMeta
// from parallel goroutines without a data race.
var (
	metaCache   = make(map[string][]fieldMeta, len(tableReg))
	metaCacheMu sync.RWMutex
)

func getFieldMeta(table string) []fieldMeta {
	metaCacheMu.RLock()
	m, ok := metaCache[table]
	metaCacheMu.RUnlock()
	if ok {
		return m
	}

	// Not cached yet — compute outside the lock, then store.
	desc, ok2 := tableReg[table]
	if !ok2 {
		return nil
	}
	computed := buildFieldMeta(desc.proto)

	metaCacheMu.Lock()
	// Check again under write-lock: another goroutine may have filled it.
	if existing, ok3 := metaCache[table]; ok3 {
		metaCacheMu.Unlock()
		return existing
	}
	metaCache[table] = computed
	metaCacheMu.Unlock()
	return computed
}

// genericEnumerate performs a SELECT * (columns derived from struct fields)
// against table, scans each row into a freshly-allocated Row value, and calls
// fn for each row.
func genericEnumerate(ctx context.Context, tx *sql.Tx, table string, fn func(any) error) error {
	desc, ok := tableReg[table]
	if !ok {
		return fmt.Errorf("engine: unknown table %q", table)
	}
	metas := getFieldMeta(table)
	if len(metas) == 0 {
		return fmt.Errorf("engine: no fields for table %q", table)
	}

	// Build SELECT.
	cols := make([]string, len(metas))
	for i, m := range metas {
		cols[i] = m.colName
	}
	query := fmt.Sprintf("SELECT %s FROM %s ORDER BY %s",
		strings.Join(cols, ", "), table, desc.orderBy)

	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("engine: query %s: %w", table, err)
	}
	defer rows.Close()

	rowType := reflect.TypeOf(desc.proto).Elem() // dereference pointer to struct type

	for rows.Next() {
		// Allocate a fresh row struct.
		rowVal := reflect.New(rowType) // *RowType
		structVal := rowVal.Elem()     // RowType

		// Allocate scan targets. We cannot scan directly into all field
		// pointers because bool fields need int64 intermediaries, and
		// nullable pointer fields need sql.NullX intermediaries.
		scanTargets := make([]any, len(metas))
		// Nullable intermediaries: we keep them as typed values (not interface)
		// to avoid allocation during reflection. Using a parallel array of
		// values indexed by meta position.
		type nullContainer struct {
			ni64 sql.NullInt64
			ns   sql.NullString
			nf64 sql.NullFloat64
			bi   int64 // for bool fields
		}
		containers := make([]nullContainer, len(metas))

		for i, m := range metas {
			fv := structVal.Field(m.idx)
			switch m.kind {
			case fkBool:
				scanTargets[i] = &containers[i].bi
			case fkNullInt64:
				scanTargets[i] = &containers[i].ni64
			case fkNullString:
				scanTargets[i] = &containers[i].ns
			case fkNullFloat64:
				scanTargets[i] = &containers[i].nf64
			case fkNullStringZ:
				// nullable string (empty string = NULL): scan via NullString.
				scanTargets[i] = &containers[i].ns
			default:
				// int64, string, float64, []byte: scan directly into field.
				scanTargets[i] = fv.Addr().Interface()
			}
		}

		if err := rows.Scan(scanTargets...); err != nil {
			return fmt.Errorf("engine: scan %s: %w", table, err)
		}

		// Copy intermediary values into struct fields.
		for i, m := range metas {
			fv := structVal.Field(m.idx)
			c := containers[i]
			switch m.kind {
			case fkBool:
				fv.SetBool(c.bi != 0)
			case fkNullInt64:
				if c.ni64.Valid {
					v := c.ni64.Int64
					fv.Set(reflect.ValueOf(&v))
				}
				// else: field stays nil (zero value of *int64)
			case fkNullString:
				if c.ns.Valid {
					v := c.ns.String
					fv.Set(reflect.ValueOf(&v))
				}
			case fkNullFloat64:
				if c.nf64.Valid {
					v := c.nf64.Float64
					fv.Set(reflect.ValueOf(&v))
				}
			case fkNullStringZ:
				// NULL in DB → "" in Go (zero value stays).
				if c.ns.Valid {
					fv.SetString(c.ns.String)
				}
			case fkBytes:
				// The SQLite driver returns nil (not []byte{}) for an empty BLOB.
				// For non-nullable BLOB columns (no nullable:"true"), nil must not
				// be re-inserted as SQL NULL — replace with []byte{} to satisfy
				// the NOT NULL constraint on round-trip.
				if !m.nullable && fv.IsNil() {
					fv.Set(reflect.ValueOf([]byte{}))
				}
				// fkInt64, fkString, fkFloat64: already in-place.
			}
		}

		if err := fn(rowVal.Interface()); err != nil {
			return err
		}
	}
	return rows.Err()
}

// genericInsert builds and executes an INSERT for one row of table using SQLite
// ?-style placeholders. The row argument's concrete type must match the Row
// struct registered for table.
func genericInsert(ctx context.Context, tx *sql.Tx, table string, row any) error {
	metas := getFieldMeta(table)
	if len(metas) == 0 {
		return fmt.Errorf("engine: no fields for table %q", table)
	}

	rowVal := reflect.ValueOf(row)
	if rowVal.Kind() == reflect.Ptr {
		rowVal = rowVal.Elem()
	}

	cols := make([]string, len(metas))
	placeholders := make([]string, len(metas))
	args := make([]any, len(metas))

	for i, m := range metas {
		cols[i] = m.colName
		placeholders[i] = "?"
		fv := rowVal.Field(m.idx)

		switch m.kind {
		case fkBool:
			if fv.Bool() {
				args[i] = int64(1)
			} else {
				args[i] = int64(0)
			}
		case fkInt64:
			args[i] = fv.Int()
		case fkFloat64:
			args[i] = fv.Float()
		case fkString:
			s := fv.String()
			if s == "" && m.defVal != "" {
				s = m.defVal
			}
			args[i] = s
		case fkBytes:
			b := fv.Bytes()
			if m.nullable {
				// NULLable BLOB column: nil or empty → SQL NULL.
				if len(b) == 0 {
					args[i] = nil
				} else {
					args[i] = b
				}
			} else {
				// Non-nullable BLOB column: pass as-is.
				// nil []byte → SQL NULL for nullable columns in the DB;
				// []byte{} → empty BLOB (satisfies NOT NULL constraint).
				args[i] = b
			}
		case fkNullInt64:
			if fv.IsNil() {
				args[i] = nil
			} else {
				args[i] = fv.Elem().Int()
			}
		case fkNullString:
			if fv.IsNil() {
				args[i] = nil
			} else {
				args[i] = fv.Elem().String()
			}
		case fkNullFloat64:
			if fv.IsNil() {
				args[i] = nil
			} else {
				args[i] = fv.Elem().Float()
			}
		case fkNullStringZ:
			// empty string → NULL; non-empty → value.
			s := fv.String()
			if s == "" {
				args[i] = nil
			} else {
				args[i] = s
			}
		}
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "))

	_, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("engine: insert %s: %w", table, err)
	}
	return nil
}
