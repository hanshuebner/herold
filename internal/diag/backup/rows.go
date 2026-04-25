package backup

// Row types describing one record per backed-up table. The shape
// mirrors the SQL columns in internal/storesqlite/migrations and
// internal/storepg/migrations exactly — the migrations are
// intentionally isomorphic so dump and restore can move rows
// row-for-row across backends without translation.
//
// JSON tags use snake_case to match the SQL column names so an
// operator inspecting the JSONL with jq sees the same field names as
// in the schema.
//
// Every nullable column is modelled with a pointer so the JSONL
// distinguishes "absent" from "zero-valued" — important for
// expires_at_us, idempotency_key, principal_id (mailbox_acl), and
// order_id (acme_certs). Boolean columns are stored as bool;
// callers translate to/from the backend's native representation.
//
// BLOB / BYTEA columns serialise as base64 strings; encoding/json's
// default []byte handling does this automatically.

type DomainRow struct {
	Name        string `json:"name"`
	IsLocal     bool   `json:"is_local"`
	CreatedAtUs int64  `json:"created_at_us"`
}

type PrincipalRow struct {
	ID             int64  `json:"id"`
	Kind           int64  `json:"kind"`
	CanonicalEmail string `json:"canonical_email"`
	DisplayName    string `json:"display_name"`
	PasswordHash   string `json:"password_hash"`
	TOTPSecret     []byte `json:"totp_secret,omitempty"`
	QuotaBytes     int64  `json:"quota_bytes"`
	Flags          int64  `json:"flags"`
	UsedBytes      int64  `json:"used_bytes"`
	CreatedAtUs    int64  `json:"created_at_us"`
	UpdatedAtUs    int64  `json:"updated_at_us"`
}

type OIDCProviderRow struct {
	Name            string `json:"name"`
	IssuerURL       string `json:"issuer_url"`
	ClientID        string `json:"client_id"`
	ClientSecretRef string `json:"client_secret_ref"`
	ScopesCSV       string `json:"scopes_csv"`
	AutoProvision   bool   `json:"auto_provision"`
	CreatedAtUs     int64  `json:"created_at_us"`
}

type OIDCLinkRow struct {
	PrincipalID     int64  `json:"principal_id"`
	ProviderName    string `json:"provider_name"`
	Subject         string `json:"subject"`
	EmailAtProvider string `json:"email_at_provider"`
	LinkedAtUs      int64  `json:"linked_at_us"`
}

type APIKeyRow struct {
	ID           int64  `json:"id"`
	PrincipalID  int64  `json:"principal_id"`
	Hash         string `json:"hash"`
	Name         string `json:"name"`
	CreatedAtUs  int64  `json:"created_at_us"`
	LastUsedAtUs int64  `json:"last_used_at_us"`
}

type AliasRow struct {
	ID              int64  `json:"id"`
	LocalPart       string `json:"local_part"`
	Domain          string `json:"domain"`
	TargetPrincipal int64  `json:"target_principal"`
	ExpiresAtUs     *int64 `json:"expires_at_us,omitempty"`
	CreatedAtUs     int64  `json:"created_at_us"`
}

type SieveScriptRow struct {
	PrincipalID int64  `json:"principal_id"`
	Script      string `json:"script"`
	UpdatedAtUs int64  `json:"updated_at_us"`
}

// CategorisationConfigRow mirrors store.CategorisationConfig and the
// jmap_categorisation_config table introduced in migration 0009
// (REQ-FILT-200..221). Endpoint, Model, and APIKeyEnv are nullable in
// SQL so each can be modelled with a pointer; the categoriser falls
// back to operator defaults when these are unset.
type CategorisationConfigRow struct {
	PrincipalID     int64   `json:"principal_id"`
	Prompt          string  `json:"prompt"`
	CategorySetJSON []byte  `json:"category_set_json,omitempty"`
	EndpointURL     *string `json:"endpoint_url,omitempty"`
	Model           *string `json:"model,omitempty"`
	APIKeyEnv       *string `json:"api_key_env,omitempty"`
	TimeoutSec      int64   `json:"timeout_sec"`
	Enabled         bool    `json:"enabled"`
	UpdatedAtUs     int64   `json:"updated_at_us"`
}

type MailboxRow struct {
	ID            int64  `json:"id"`
	PrincipalID   int64  `json:"principal_id"`
	ParentID      int64  `json:"parent_id"`
	Name          string `json:"name"`
	Attributes    int64  `json:"attributes"`
	UIDValidity   int64  `json:"uidvalidity"`
	UIDNext       int64  `json:"uidnext"`
	HighestModSeq int64  `json:"highest_modseq"`
	CreatedAtUs   int64  `json:"created_at_us"`
	UpdatedAtUs   int64  `json:"updated_at_us"`
}

type MessageRow struct {
	ID             int64  `json:"id"`
	MailboxID      int64  `json:"mailbox_id"`
	UID            int64  `json:"uid"`
	ModSeq         int64  `json:"modseq"`
	Flags          int64  `json:"flags"`
	KeywordsCSV    string `json:"keywords_csv"`
	InternalDateUs int64  `json:"internal_date_us"`
	ReceivedAtUs   int64  `json:"received_at_us"`
	Size           int64  `json:"size"`
	BlobHash       string `json:"blob_hash"`
	BlobSize       int64  `json:"blob_size"`
	ThreadID       int64  `json:"thread_id"`
	EnvSubject     string `json:"env_subject"`
	EnvFrom        string `json:"env_from"`
	EnvTo          string `json:"env_to"`
	EnvCc          string `json:"env_cc"`
	EnvBcc         string `json:"env_bcc"`
	EnvReplyTo     string `json:"env_reply_to"`
	EnvMessageID   string `json:"env_message_id"`
	EnvInReplyTo   string `json:"env_in_reply_to"`
	EnvDateUs      int64  `json:"env_date_us"`
	SnoozedUntilUs *int64 `json:"snoozed_until_us,omitempty"`
}

type MailboxACLRow struct {
	ID          int64  `json:"id"`
	MailboxID   int64  `json:"mailbox_id"`
	PrincipalID *int64 `json:"principal_id,omitempty"`
	RightsMask  int64  `json:"rights_mask"`
	GrantedBy   int64  `json:"granted_by"`
	CreatedAtUs int64  `json:"created_at_us"`
}

type StateChangeRow struct {
	ID             int64  `json:"id"`
	PrincipalID    int64  `json:"principal_id"`
	Seq            int64  `json:"seq"`
	EntityKind     string `json:"entity_kind"`
	EntityID       int64  `json:"entity_id"`
	ParentEntityID int64  `json:"parent_entity_id"`
	Op             int64  `json:"op"`
	ProducedAtUs   int64  `json:"produced_at_us"`
}

type AuditLogRow struct {
	ID           int64  `json:"id"`
	AtUs         int64  `json:"at_us"`
	ActorKind    int64  `json:"actor_kind"`
	ActorID      string `json:"actor_id"`
	Action       string `json:"action"`
	Subject      string `json:"subject"`
	RemoteAddr   string `json:"remote_addr"`
	Outcome      int64  `json:"outcome"`
	Message      string `json:"message"`
	MetadataJSON string `json:"metadata_json"`
	PrincipalID  int64  `json:"principal_id"`
}

type CursorRow struct {
	Key string `json:"key"`
	Seq int64  `json:"seq"`
}

type QueueRow struct {
	ID              int64   `json:"id"`
	PrincipalID     *int64  `json:"principal_id,omitempty"`
	MailFrom        string  `json:"mail_from"`
	RcptTo          string  `json:"rcpt_to"`
	EnvelopeID      string  `json:"envelope_id"`
	BodyBlobHash    string  `json:"body_blob_hash"`
	HeadersBlobHash string  `json:"headers_blob_hash"`
	State           int64   `json:"state"`
	Attempts        int64   `json:"attempts"`
	LastAttemptAtUs int64   `json:"last_attempt_at_us"`
	NextAttemptAtUs int64   `json:"next_attempt_at_us"`
	LastError       string  `json:"last_error"`
	DSNNotifyFlags  int64   `json:"dsn_notify_flags"`
	DSNRet          int64   `json:"dsn_ret"`
	DSNEnvID        string  `json:"dsn_envid"`
	DSNOrcpt        string  `json:"dsn_orcpt"`
	IdempotencyKey  *string `json:"idempotency_key,omitempty"`
	CreatedAtUs     int64   `json:"created_at_us"`
}

type DKIMKeyRow struct {
	ID            int64  `json:"id"`
	Domain        string `json:"domain"`
	Selector      string `json:"selector"`
	Algorithm     int64  `json:"algorithm"`
	PrivateKeyPEM string `json:"private_key_pem"`
	PublicKeyB64  string `json:"public_key_b64"`
	Status        int64  `json:"status"`
	CreatedAtUs   int64  `json:"created_at_us"`
	RotatedAtUs   int64  `json:"rotated_at_us"`
}

type ACMEAccountRow struct {
	ID            int64  `json:"id"`
	DirectoryURL  string `json:"directory_url"`
	ContactEmail  string `json:"contact_email"`
	AccountKeyPEM string `json:"account_key_pem"`
	KID           string `json:"kid"`
	CreatedAtUs   int64  `json:"created_at_us"`
}

type ACMEOrderRow struct {
	ID             int64  `json:"id"`
	AccountID      int64  `json:"account_id"`
	HostnamesJSON  string `json:"hostnames_json"`
	Status         int64  `json:"status"`
	OrderURL       string `json:"order_url"`
	FinalizeURL    string `json:"finalize_url"`
	CertificateURL string `json:"certificate_url"`
	ChallengeType  int64  `json:"challenge_type"`
	UpdatedAtUs    int64  `json:"updated_at_us"`
	Error          string `json:"error"`
}

type ACMECertRow struct {
	Hostname      string `json:"hostname"`
	ChainPEM      string `json:"chain_pem"`
	PrivateKeyPEM string `json:"private_key_pem"`
	NotBeforeUs   int64  `json:"not_before_us"`
	NotAfterUs    int64  `json:"not_after_us"`
	Issuer        string `json:"issuer"`
	OrderID       *int64 `json:"order_id,omitempty"`
}

type WebhookRow struct {
	ID              int64  `json:"id"`
	OwnerKind       int64  `json:"owner_kind"`
	OwnerID         string `json:"owner_id"`
	TargetURL       string `json:"target_url"`
	HMACSecret      []byte `json:"hmac_secret,omitempty"`
	DeliveryMode    int64  `json:"delivery_mode"`
	RetryPolicyJSON string `json:"retry_policy_json"`
	Active          bool   `json:"active"`
	CreatedAtUs     int64  `json:"created_at_us"`
	UpdatedAtUs     int64  `json:"updated_at_us"`
}

type DMARCReportRow struct {
	ID            int64  `json:"id"`
	ReceivedAtUs  int64  `json:"received_at_us"`
	ReporterEmail string `json:"reporter_email"`
	ReporterOrg   string `json:"reporter_org"`
	ReportID      string `json:"report_id"`
	Domain        string `json:"domain"`
	DateBeginUs   int64  `json:"date_begin_us"`
	DateEndUs     int64  `json:"date_end_us"`
	XMLBlobHash   string `json:"xml_blob_hash"`
	ParsedOK      bool   `json:"parsed_ok"`
	ParseError    string `json:"parse_error"`
}

type DMARCRowRow struct {
	ID           int64  `json:"id"`
	ReportID     int64  `json:"report_id"`
	SourceIP     string `json:"source_ip"`
	Count        int64  `json:"count"`
	Disposition  int64  `json:"disposition"`
	SPFAligned   bool   `json:"spf_aligned"`
	DKIMAligned  bool   `json:"dkim_aligned"`
	SPFResult    string `json:"spf_result"`
	DKIMResult   string `json:"dkim_result"`
	HeaderFrom   string `json:"header_from"`
	EnvelopeFrom string `json:"envelope_from"`
	EnvelopeTo   string `json:"envelope_to"`
}

type JMAPStateRow struct {
	PrincipalID           int64 `json:"principal_id"`
	MailboxState          int64 `json:"mailbox_state"`
	EmailState            int64 `json:"email_state"`
	ThreadState           int64 `json:"thread_state"`
	IdentityState         int64 `json:"identity_state"`
	EmailSubmissionState  int64 `json:"email_submission_state"`
	VacationResponseState int64 `json:"vacation_response_state"`
	UpdatedAtUs           int64 `json:"updated_at_us"`
}

type JMAPEmailSubmissionRow struct {
	ID          string `json:"id"`
	EnvelopeID  string `json:"envelope_id"`
	PrincipalID int64  `json:"principal_id"`
	IdentityID  string `json:"identity_id"`
	EmailID     int64  `json:"email_id"`
	ThreadID    string `json:"thread_id"`
	SendAtUs    int64  `json:"send_at_us"`
	CreatedAtUs int64  `json:"created_at_us"`
	UndoStatus  string `json:"undo_status"`
	Properties  []byte `json:"properties,omitempty"`
}

type JMAPIdentityRow struct {
	ID            string `json:"id"`
	PrincipalID   int64  `json:"principal_id"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	ReplyToJSON   []byte `json:"reply_to_json,omitempty"`
	BccJSON       []byte `json:"bcc_json,omitempty"`
	TextSignature string `json:"text_signature"`
	HTMLSignature string `json:"html_signature"`
	MayDelete     bool   `json:"may_delete"`
	CreatedAtUs   int64  `json:"created_at_us"`
	UpdatedAtUs   int64  `json:"updated_at_us"`
}

type TLSRPTFailureRow struct {
	ID                   int64  `json:"id"`
	RecordedAtUs         int64  `json:"recorded_at_us"`
	PolicyDomain         string `json:"policy_domain"`
	ReceivingMTAHostname string `json:"receiving_mta_hostname"`
	FailureType          int64  `json:"failure_type"`
	FailureCode          string `json:"failure_code"`
	FailureDetailJSON    string `json:"failure_detail_json"`
}

type BlobRefRow struct {
	Hash         string `json:"hash"`
	Size         int64  `json:"size"`
	RefCount     int64  `json:"ref_count"`
	LastChangeUs int64  `json:"last_change_us"`
}

type AddressBookRow struct {
	ID           int64   `json:"id"`
	PrincipalID  int64   `json:"principal_id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	ColorHex     *string `json:"color_hex,omitempty"`
	SortOrder    int64   `json:"sort_order"`
	IsSubscribed bool    `json:"is_subscribed"`
	IsDefault    bool    `json:"is_default"`
	RightsMask   int64   `json:"rights_mask"`
	CreatedAtUs  int64   `json:"created_at_us"`
	UpdatedAtUs  int64   `json:"updated_at_us"`
	ModSeq       int64   `json:"modseq"`
}

type ContactRow struct {
	ID            int64  `json:"id"`
	AddressBookID int64  `json:"address_book_id"`
	PrincipalID   int64  `json:"principal_id"`
	UID           string `json:"uid"`
	JSContactJSON []byte `json:"jscontact_json,omitempty"`
	DisplayName   string `json:"display_name"`
	GivenName     string `json:"given_name"`
	Surname       string `json:"surname"`
	OrgName       string `json:"org_name"`
	PrimaryEmail  string `json:"primary_email"`
	SearchBlob    string `json:"search_blob"`
	CreatedAtUs   int64  `json:"created_at_us"`
	UpdatedAtUs   int64  `json:"updated_at_us"`
	ModSeq        int64  `json:"modseq"`
}

type CalendarRow struct {
	ID           int64   `json:"id"`
	PrincipalID  int64   `json:"principal_id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	ColorHex     *string `json:"color_hex,omitempty"`
	SortOrder    int64   `json:"sort_order"`
	IsSubscribed bool    `json:"is_subscribed"`
	IsDefault    bool    `json:"is_default"`
	IsVisible    bool    `json:"is_visible"`
	TimeZoneID   string  `json:"time_zone_id"`
	RightsMask   int64   `json:"rights_mask"`
	CreatedAtUs  int64   `json:"created_at_us"`
	UpdatedAtUs  int64   `json:"updated_at_us"`
	ModSeq       int64   `json:"modseq"`
}

type CalendarEventRow struct {
	ID             int64   `json:"id"`
	CalendarID     int64   `json:"calendar_id"`
	PrincipalID    int64   `json:"principal_id"`
	UID            string  `json:"uid"`
	JSCalendarJSON []byte  `json:"jscalendar_json,omitempty"`
	StartUs        int64   `json:"start_us"`
	EndUs          int64   `json:"end_us"`
	IsRecurring    bool    `json:"is_recurring"`
	RRuleJSON      []byte  `json:"rrule_json,omitempty"`
	Summary        string  `json:"summary"`
	OrganizerEmail *string `json:"organizer_email,omitempty"`
	Status         string  `json:"status"`
	CreatedAtUs    int64   `json:"created_at_us"`
	UpdatedAtUs    int64   `json:"updated_at_us"`
	ModSeq         int64   `json:"modseq"`
}

type ChatConversationRow struct {
	ID                   int64   `json:"id"`
	Kind                 string  `json:"kind"`
	Name                 *string `json:"name,omitempty"`
	Topic                *string `json:"topic,omitempty"`
	CreatedByPrincipalID int64   `json:"created_by_principal_id"`
	CreatedAtUs          int64   `json:"created_at_us"`
	UpdatedAtUs          int64   `json:"updated_at_us"`
	LastMessageAtUs      *int64  `json:"last_message_at_us,omitempty"`
	MessageCount         int64   `json:"message_count"`
	IsArchived           bool    `json:"is_archived"`
	ModSeq               int64   `json:"modseq"`
}

type ChatMembershipRow struct {
	ID                   int64  `json:"id"`
	ConversationID       int64  `json:"conversation_id"`
	PrincipalID          int64  `json:"principal_id"`
	Role                 string `json:"role"`
	JoinedAtUs           int64  `json:"joined_at_us"`
	LastReadMessageID    *int64 `json:"last_read_message_id,omitempty"`
	IsMuted              bool   `json:"is_muted"`
	MuteUntilUs          *int64 `json:"mute_until_us,omitempty"`
	NotificationsSetting string `json:"notifications_setting"`
	ModSeq               int64  `json:"modseq"`
}

type ChatMessageRow struct {
	ID                int64   `json:"id"`
	ConversationID    int64   `json:"conversation_id"`
	SenderPrincipalID *int64  `json:"sender_principal_id,omitempty"`
	IsSystem          bool    `json:"is_system"`
	BodyText          *string `json:"body_text,omitempty"`
	BodyHTML          *string `json:"body_html,omitempty"`
	BodyFormat        string  `json:"body_format"`
	ReplyToMessageID  *int64  `json:"reply_to_message_id,omitempty"`
	ReactionsJSON     []byte  `json:"reactions_json,omitempty"`
	AttachmentsJSON   []byte  `json:"attachments_json,omitempty"`
	MetadataJSON      []byte  `json:"metadata_json,omitempty"`
	EditedAtUs        *int64  `json:"edited_at_us,omitempty"`
	DeletedAtUs       *int64  `json:"deleted_at_us,omitempty"`
	CreatedAtUs       int64   `json:"created_at_us"`
	ModSeq            int64   `json:"modseq"`
}

type ChatBlockRow struct {
	BlockerPrincipalID int64   `json:"blocker_principal_id"`
	BlockedPrincipalID int64   `json:"blocked_principal_id"`
	CreatedAtUs        int64   `json:"created_at_us"`
	Reason             *string `json:"reason,omitempty"`
}
