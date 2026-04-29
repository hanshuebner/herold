package admin

// format.go — human-readable output for structured CLI responses.
//
// Each writeXxxHuman function receives the decoded map[string]any (or
// slice) from the admin REST API and writes a scannable table or key/value
// block to w.  The raw map is always available via --json / non-TTY path;
// these functions only run when the operator is at an interactive terminal
// without --json.
//
// Conventions:
//   - List endpoints return {"items":[...], "next":"cursor|null"}.
//   - Single-record endpoints return the object directly.
//   - Timestamps come as RFC3339 strings; we reformat to local short form.
//   - Null/empty fields are suppressed in human view; JSON carries them.

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/hanshuebner/herold/internal/cliout"
)

// ---- helpers ---------------------------------------------------------------

// itemsFrom extracts the "items" array from a page envelope returned by
// list endpoints.  Returns nil if the key is missing or the wrong type.
func itemsFrom(out map[string]any) []map[string]any {
	raw, ok := out["items"]
	if !ok {
		return nil
	}
	// The JSON decoder gives us []any, not []map[string]any; re-encode and
	// re-decode to get a typed slice without reimplementing the type
	// assertion machinery.
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var items []map[string]any
	if err := json.Unmarshal(b, &items); err != nil {
		return nil
	}
	return items
}

// sval returns a string field from a map, or "" when missing/not-string.
func sval(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// fval returns a float64 field (JSON numbers) as a formatted string, or
// "" when missing.
func fval(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case float64:
		return cliout.FloatStr(v)
	case int64:
		return fmt.Sprintf("%d", v)
	case int:
		return fmt.Sprintf("%d", v)
	case string:
		return v
	}
	return ""
}

// bval returns a bool field as "yes"/"no", or "" when missing.
func bval(m map[string]any, key string) string {
	v, ok := m[key].(bool)
	if !ok {
		return ""
	}
	return cliout.BoolYesNo(v)
}

// tval returns an RFC3339 timestamp field formatted as local short time,
// or "" when missing/empty.
func tval(m map[string]any, key string) string {
	return cliout.FormatTime(sval(m, key))
}

// ssval returns a []string field as a comma-joined list, or "(none)".
func ssval(m map[string]any, key string) string {
	raw, _ := m[key].([]any)
	ss := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			ss = append(ss, s)
		}
	}
	return cliout.StringsJoin(ss)
}

// ---- queue -----------------------------------------------------------------

// writeQueueListHuman renders the queue list page as a table.
func writeQueueListHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(queue is empty)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("ID", "STATE", "RCPT-TO", "ATTEMPTS", "LAST-ATTEMPT", "LAST-ERROR")
	for _, it := range items {
		t.Row(
			fval(it, "id"),
			sval(it, "state"),
			sval(it, "rcpt_to"),
			fval(it, "attempts"),
			cliout.FormatTime(sval(it, "last_attempt_at")),
			cliout.Trunc(sval(it, "last_error"), cliout.MaxErrorLen),
		)
	}
	if next := sval(out, "next"); next != "" {
		defer fmt.Fprintf(w, "(more: --after %s)\n", next)
	}
	return t.Flush()
}

// writeQueueItemHuman renders a single queue item as key/value pairs.
func writeQueueItemHuman(w io.Writer, out map[string]any) error {
	cliout.KV(w, [][2]string{
		{"id", fval(out, "id")},
		{"state", sval(out, "state")},
		{"mail_from", sval(out, "mail_from")},
		{"rcpt_to", sval(out, "rcpt_to")},
		{"attempts", fval(out, "attempts")},
		{"created_at", tval(out, "created_at")},
		{"last_attempt_at", tval(out, "last_attempt_at")},
		{"next_attempt_at", tval(out, "next_attempt_at")},
		{"last_error", sval(out, "last_error")},
		{"envelope_id", sval(out, "envelope_id")},
	})
	return nil
}

// writeQueueStatsHuman renders the queue counts by state.
func writeQueueStatsHuman(w io.Writer, out map[string]any) error {
	counts, _ := out["counts"].(map[string]any)
	if counts == nil {
		fmt.Fprintln(w, "(no stats)")
		return nil
	}
	for _, state := range []string{"queued", "deferred", "inflight", "done", "failed", "held"} {
		v := counts[state]
		fmt.Fprintf(w, "%-10s  %s\n", state, cliout.FloatStr(toFloat64(v)))
	}
	return nil
}

// writeQueueFlushHuman renders the flush result.
func writeQueueFlushHuman(w io.Writer, out map[string]any) error {
	flushed := fval(out, "flushed")
	if flushed == "" {
		flushed = "0"
	}
	fmt.Fprintf(w, "flushed %s queue item(s) to retry now\n", flushed)
	return nil
}

// toFloat64 coerces a JSON-decoded value to float64 (JSON numbers decode as
// float64 via map[string]any).
func toFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}

// ---- principals ------------------------------------------------------------

// writePrincipalListHuman renders the principal list page as a table.
func writePrincipalListHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(no principals)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("ID", "EMAIL", "QUOTA", "FLAGS", "TOTP", "CREATED")
	for _, it := range items {
		quota := ""
		if v, ok := it["quota_bytes"].(float64); ok && v > 0 {
			quota = cliout.HumanBytes(int64(v))
		}
		t.Row(
			fval(it, "id"),
			sval(it, "canonical_email"),
			quota,
			ssval(it, "flags"),
			bval(it, "totp_enabled"),
			tval(it, "created_at"),
		)
	}
	if next := sval(out, "next"); next != "" {
		defer fmt.Fprintf(w, "(more: --after %s)\n", next)
	}
	return t.Flush()
}

// writePrincipalHuman renders a single principal record.
func writePrincipalHuman(w io.Writer, out map[string]any) error {
	quota := ""
	if v, ok := out["quota_bytes"].(float64); ok && v > 0 {
		quota = cliout.HumanBytes(int64(v))
	}
	cliout.KV(w, [][2]string{
		{"id", fval(out, "id")},
		{"email", sval(out, "canonical_email")},
		{"display_name", sval(out, "display_name")},
		{"quota", quota},
		{"flags", ssval(out, "flags")},
		{"totp_enabled", bval(out, "totp_enabled")},
		{"created_at", tval(out, "created_at")},
		{"updated_at", tval(out, "updated_at")},
	})
	return nil
}

// ---- domains ---------------------------------------------------------------

// writeDomainListHuman renders the domain list page as a table.
func writeDomainListHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(no domains)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("DOMAIN", "LOCAL", "CREATED")
	for _, it := range items {
		t.Row(
			sval(it, "name"),
			bval(it, "local"),
			tval(it, "created_at"),
		)
	}
	return t.Flush()
}

// ---- aliases ---------------------------------------------------------------

// writeAliasListHuman renders the alias list page as a table.
func writeAliasListHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(no aliases)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("ID", "ADDRESS", "TARGET-PRINCIPAL-ID", "CREATED")
	for _, it := range items {
		addr := sval(it, "local") + "@" + sval(it, "domain")
		t.Row(
			fval(it, "id"),
			addr,
			fval(it, "target_principal_id"),
			tval(it, "created_at"),
		)
	}
	return t.Flush()
}

// writeAliasHuman renders a single alias record.
func writeAliasHuman(w io.Writer, out map[string]any) error {
	addr := sval(out, "local") + "@" + sval(out, "domain")
	cliout.KV(w, [][2]string{
		{"id", fval(out, "id")},
		{"address", addr},
		{"target_principal_id", fval(out, "target_principal_id")},
		{"created_at", tval(out, "created_at")},
	})
	return nil
}

// ---- API keys --------------------------------------------------------------

// writeAPIKeyListHuman renders the API key list as a table.
func writeAPIKeyListHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(no API keys)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("ID", "PRINCIPAL-ID", "LABEL", "CREATED", "LAST-USED")
	for _, it := range items {
		t.Row(
			fval(it, "id"),
			fval(it, "principal_id"),
			sval(it, "label"),
			tval(it, "created_at"),
			tval(it, "last_used_at"),
		)
	}
	return t.Flush()
}

// writeAPIKeyHuman renders a single API key record. The plaintext key is
// shown when present (only on creation); subsequent reads omit it.
func writeAPIKeyHuman(w io.Writer, out map[string]any) error {
	cliout.KV(w, [][2]string{
		{"id", fval(out, "id")},
		{"principal_id", fval(out, "principal_id")},
		{"label", sval(out, "label")},
		{"scope", ssval(out, "scope")},
		{"key", sval(out, "key")},
		{"created_at", tval(out, "created_at")},
		{"last_used_at", tval(out, "last_used_at")},
	})
	return nil
}

// ---- audit log -------------------------------------------------------------

// writeAuditListHuman renders the audit log page as a table.
func writeAuditListHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(no audit entries)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("ID", "AT", "ACTOR", "ACTION", "SUBJECT", "OUTCOME")
	for _, it := range items {
		actor := sval(it, "actor_kind")
		if aid := sval(it, "actor_id"); aid != "" {
			actor += ":" + aid
		}
		var at string
		switch v := it["at"].(type) {
		case string:
			at = cliout.FormatTime(v)
		}
		t.Row(
			fval(it, "id"),
			at,
			actor,
			sval(it, "action"),
			cliout.Trunc(sval(it, "subject"), 40),
			sval(it, "outcome"),
		)
	}
	if next := sval(out, "next"); next != "" {
		defer fmt.Fprintf(w, "(more: --after %s)\n", next)
	}
	return t.Flush()
}

// ---- certs -----------------------------------------------------------------

// writeCertListHuman renders the ACME cert list as a table.
func writeCertListHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(no certificates)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("HOSTNAME", "NOT-BEFORE", "NOT-AFTER", "ISSUER")
	for _, it := range items {
		t.Row(
			sval(it, "hostname"),
			tval(it, "not_before"),
			tval(it, "not_after"),
			sval(it, "issuer"),
		)
	}
	return t.Flush()
}

// writeCertHuman renders a single cert record. The chain PEM is omitted
// in human view (too long); --json exposes it.
func writeCertHuman(w io.Writer, out map[string]any) error {
	cliout.KV(w, [][2]string{
		{"hostname", sval(out, "hostname")},
		{"not_before", tval(out, "not_before")},
		{"not_after", tval(out, "not_after")},
		{"issuer", sval(out, "issuer")},
		{"order_id", fval(out, "order_id")},
	})
	if chain := sval(out, "chain_pem"); chain != "" {
		fmt.Fprintln(w, "\nchain_pem (use --json for machine-readable form):")
		fmt.Fprintln(w, chain)
	}
	return nil
}

// ---- webhooks --------------------------------------------------------------

// writeHookListHuman renders the webhook list as a table.
func writeHookListHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(no webhooks)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("ID", "OWNER", "TARGET-URL", "MODE", "ACTIVE")
	for _, it := range items {
		owner := sval(it, "owner_kind") + ":" + sval(it, "owner_id")
		if tk := sval(it, "target_kind"); tk != "" {
			owner = tk + ":" + sval(it, "owner_id")
		}
		t.Row(
			fval(it, "id"),
			owner,
			cliout.Trunc(sval(it, "target_url"), 50),
			sval(it, "delivery_mode"),
			bval(it, "active"),
		)
	}
	return t.Flush()
}

// writeHookHuman renders a single webhook record.
func writeHookHuman(w io.Writer, out map[string]any) error {
	cliout.KV(w, [][2]string{
		{"id", fval(out, "id")},
		{"owner_kind", sval(out, "owner_kind")},
		{"owner_id", sval(out, "owner_id")},
		{"target_kind", sval(out, "target_kind")},
		{"target_url", sval(out, "target_url")},
		{"delivery_mode", sval(out, "delivery_mode")},
		{"body_mode", sval(out, "body_mode")},
		{"active", bval(out, "active")},
		{"created_at", tval(out, "created_at")},
		{"updated_at", tval(out, "updated_at")},
	})
	if secret := sval(out, "hmac_secret"); secret != "" {
		fmt.Fprintf(w, "\nhmac_secret: %s  (shown once; store it securely)\n", secret)
	}
	return nil
}

// ---- OIDC providers --------------------------------------------------------

// writeOIDCProviderListHuman renders the OIDC provider list as a table.
func writeOIDCProviderListHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(no OIDC providers)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("ID", "NAME", "ISSUER", "CLIENT-ID", "CREATED")
	for _, it := range items {
		t.Row(
			sval(it, "id"),
			sval(it, "name"),
			sval(it, "issuer"),
			sval(it, "client_id"),
			tval(it, "created_at"),
		)
	}
	return t.Flush()
}

// writeOIDCProviderHuman renders a single OIDC provider record.
func writeOIDCProviderHuman(w io.Writer, out map[string]any) error {
	cliout.KV(w, [][2]string{
		{"id", sval(out, "id")},
		{"name", sval(out, "name")},
		{"issuer", sval(out, "issuer")},
		{"client_id", sval(out, "client_id")},
		{"scopes", ssval(out, "scopes")},
		{"created_at", tval(out, "created_at")},
	})
	return nil
}

// writeOIDCLinkListHuman renders the OIDC link list as a table.
func writeOIDCLinkListHuman(w io.Writer, out map[string]any) error {
	items := itemsFrom(out)
	if len(items) == 0 {
		fmt.Fprintln(w, "(no OIDC links)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("PROVIDER", "EXTERNAL-SUB", "LINKED-AT")
	for _, it := range items {
		t.Row(
			sval(it, "provider_name"),
			sval(it, "external_sub"),
			tval(it, "linked_at"),
		)
	}
	return t.Flush()
}

// ---- spam policy -----------------------------------------------------------

// writeSpamPolicyHuman renders the spam policy as key/value pairs.
func writeSpamPolicyHuman(w io.Writer, out map[string]any) error {
	cliout.KV(w, [][2]string{
		{"plugin", sval(out, "plugin_name")},
		{"threshold", fval(out, "threshold")},
		{"model", sval(out, "model")},
	})
	return nil
}
