package protoadmin

// tagged_address_convert.go implements the one-way migration from a
// managed tagged-address filter row to a hand-written Sieve fragment
// (REQ-TAG-50..51).
//
// Endpoint:
//   - POST /api/v1/tagged-address-filters/{id}/convert-to-sieve
//     Self-only. The caller must own the named filter row.
//
// Behaviour (REQ-TAG-50):
//   1. Load the filter row owned by the caller. 404 when no row matches
//      either the id or the (id, caller) ownership check.
//   2. Load the base Identity row to recover the local-part needed for
//      the generated envelope test.
//   3. Generate the Sieve fragment per REQ-TAG-30's action -> Sieve
//      mapping. Merge any pre-existing `require [...]` declaration in
//      the caller's user script so the resulting top-of-file `require`
//      remains a single statement.
//   4. Validate the merged script with the existing Sieve parser /
//      validator (REQ-TAG-50 step 4). On failure: abort the operation,
//      do NOT delete the filter row, surface a 422 with the parser's
//      diagnostic.
//   5. On success: persist the merged user script AND delete the filter
//      row (which also drops any matching dismissal row per
//      REQ-TAG-61). The two writes are NOT a single store transaction;
//      they are sequenced (user-script write first, filter delete
//      second) so a failure mid-flow leaves the filter intact and the
//      Sieve fragment in place — semantically identical to the
//      pre-conversion state (the fragment matches the same recipients
//      the filter row did, so the second-stage delete is purely
//      bookkeeping).
//   6. Emit an audit-log entry tagged tagged_address_filter.convert_to_sieve
//      (REQ-TAG-90).
//
// Conversion is one-way (REQ-TAG-51). The handler does NOT preserve
// the filter row when validation passes — the user must hand-edit the
// generated Sieve to undo the conversion.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
)

// convertResponse is the wire-form result of a successful conversion.
// The SPA mirrors this onto its local cache: the filter row vanishes
// (Identity/changes will eventually refresh the JMAP view) and the
// user's Sieve script grows the fragment text. Returning the fragment
// + the merged script lets the SPA show a diff or roll-up without
// another round trip.
type convertResponse struct {
	// Fragment is the Sieve text the handler prepended. Includes the
	// auto-merged `require` line.
	Fragment string `json:"fragment"`
	// Script is the new user-Sieve script the store now holds. The
	// caller can stamp this into the ManageSieve editor without a
	// follow-up read.
	Script string `json:"script"`
}

// handleConvertTaggedAddressFilterToSieve implements POST
// /api/v1/tagged-address-filters/{id}/convert-to-sieve (REQ-TAG-50).
// Self-only by ownership check inside the handler (the filter row's
// PrincipalID must equal the caller's id; admin impersonation is not
// supported, consistent with the rest of the user-state surfaces).
func (s *Server) handleConvertTaggedAddressFilterToSieve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caller, ok := principalFrom(ctx)
	if !ok {
		writeProblem(w, r, http.StatusUnauthorized, "unauthenticated",
			"session required", "")
		return
	}
	filterID := r.PathValue("id")
	if filterID == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id",
			"filter id is required", "")
		return
	}

	// Locate the filter row by listing the caller's filters and
	// matching the id. ListTaggedAddressFiltersForPrincipal is the
	// only available read path scoped to a principal; the store has
	// no GetByID method (and adding one expands the store-interface
	// surface that has to land on both sqlite and postgres for one
	// caller). The list cap of 100 (REQ-TAG-11) bounds the scan.
	rows, err := s.store.Meta().ListTaggedAddressFiltersForPrincipal(ctx, caller.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	var row store.TaggedAddressFilter
	found := false
	for _, candidate := range rows {
		if candidate.ID == filterID {
			row = candidate
			found = true
			break
		}
	}
	if !found {
		writeProblem(w, r, http.StatusNotFound, "not_found",
			"filter not found", filterID)
		return
	}

	// Load the base Identity to recover the local-part. The generated
	// envelope test uses :localpart matching so the conversion is
	// independent of the domain.
	ident, err := s.store.Meta().GetJMAPIdentity(ctx, row.BaseIdentityID)
	if err != nil {
		// A dangling base_identity_id would be a store-side
		// inconsistency (the FK should prevent it). Surface it as a
		// store error rather than 404; the operator needs to see this.
		s.writeStoreError(w, r, err)
		return
	}
	localPart := identityLocalPart(ident.Email)
	if localPart == "" {
		writeProblem(w, r, http.StatusUnprocessableEntity, "invalid_identity",
			"base identity has no usable local part", row.BaseIdentityID)
		return
	}

	fragment, err := generateSieveFragmentForFilter(localPart, row)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "fragment_generate_failed",
			err.Error(), "")
		return
	}

	// Load the user-written Sieve half. The managed-rules preamble
	// (compiled from store.ManagedRule) is NOT touched: the spec
	// targets the user-written script. EffectiveScript will splice
	// the preamble + the new user script after this write.
	existing, err := s.store.Meta().GetUserSieveScript(ctx, caller.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	merged := mergeRequireAndPrepend(existing, fragment)

	// Validate the merged user script before persisting. The Sieve
	// parser + validator accept multi-statement scripts including a
	// leading `require`; a failure means the operation aborts and the
	// filter row stays intact (REQ-TAG-50 step 4 rollback).
	parsed, perr := sieve.Parse([]byte(merged))
	if perr != nil {
		s.appendAudit(ctx, "tagged_address_filter.convert_to_sieve",
			row.ID, store.OutcomeFailure,
			fmt.Sprintf("parse failed: %s", perr.Error()),
			map[string]string{
				"base_identity_id": row.BaseIdentityID,
				"suffix":           row.Suffix,
				"action":           row.Action,
			})
		writeProblem(w, r, http.StatusUnprocessableEntity, "sieve_parse_failed",
			perr.Error(), "")
		return
	}
	if verr := sieve.Validate(parsed); verr != nil {
		s.appendAudit(ctx, "tagged_address_filter.convert_to_sieve",
			row.ID, store.OutcomeFailure,
			fmt.Sprintf("validate failed: %s", verr.Error()),
			map[string]string{
				"base_identity_id": row.BaseIdentityID,
				"suffix":           row.Suffix,
				"action":           row.Action,
			})
		writeProblem(w, r, http.StatusUnprocessableEntity, "sieve_validate_failed",
			verr.Error(), "")
		return
	}

	// Persist the user script first, then drop the filter row. The
	// store interface does not expose a single transaction over both
	// tables; sequencing the writes user-first ensures the active
	// fragment is in place by the time the filter row vanishes. The
	// effective-script recompile (managed preamble + user script)
	// happens transparently on the next managed-rule mutation; for
	// this write path we touch only the user half.
	if err := s.store.Meta().SetUserSieveScript(ctx, caller.ID, merged); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	// The runtime sieve_scripts slot reads from GetSieveScript, which
	// returns the effective (preamble + user) script. Recompose it
	// now so the next inbound message sees the freshly-merged user
	// half — without this the delivery path would still hit the old
	// effective script until the next managed-rule mutation triggers
	// a recompile.
	if err := s.recomposeEffectiveScript(ctx, caller.ID, merged); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if _, _, _, err := s.store.Meta().DeleteTaggedAddressFilter(ctx, row.ID); err != nil {
		// If the delete fails after the user script has been written,
		// the user sees the Sieve fragment AND the structured filter
		// still pointing at the same (identity, suffix). Both apply
		// the same filing action so behaviour is preserved; we still
		// surface the error so the operator can investigate. The
		// SPA's next reload will reconcile.
		s.writeStoreError(w, r, err)
		return
	}

	s.appendAudit(ctx, "tagged_address_filter.convert_to_sieve",
		row.ID, store.OutcomeSuccess, "",
		map[string]string{
			"base_identity_id": row.BaseIdentityID,
			"suffix":           row.Suffix,
			"action":           row.Action,
		})
	writeJSON(w, http.StatusOK, convertResponse{
		Fragment: fragment,
		Script:   merged,
	})
}

// identityLocalPart returns the local-part of an Identity email
// (everything before the first '@'). Returns empty when the address is
// malformed.
func identityLocalPart(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return ""
	}
	return email[:at]
}

// generateSieveFragmentForFilter produces the Sieve text equivalent of
// one TaggedAddressFilter row, per REQ-TAG-30:
//
//   - label:               fileinto "<label>";              (no stop)
//   - label_archive:       fileinto "<label>"; stop;
//   - label_archive_read:  setflag "\\Seen"; fileinto "<label>"; stop;
//
// The test is `envelope :matches :localpart "to" "<local>+<suffix>"`
// which matches the canonical recipient irrespective of the trailing
// domain. The fragment includes its own `require [...]` line for the
// extensions it actually uses; mergeRequireAndPrepend folds it into
// any pre-existing require declaration.
func generateSieveFragmentForFilter(localPart string, f store.TaggedAddressFilter) (string, error) {
	exts := []string{"envelope", "fileinto"}
	var action string
	switch f.Action {
	case store.TaggedAddressActionLabel:
		action = fmt.Sprintf("    fileinto %s;\n", sieveStringLiteral(f.LabelName))
	case store.TaggedAddressActionLabelArchive:
		action = fmt.Sprintf("    fileinto %s;\n    stop;\n", sieveStringLiteral(f.LabelName))
	case store.TaggedAddressActionLabelArchiveRead:
		exts = append(exts, "imap4flags")
		action = fmt.Sprintf("    setflag \"\\\\Seen\";\n    fileinto %s;\n    stop;\n",
			sieveStringLiteral(f.LabelName))
	default:
		return "", fmt.Errorf("unsupported action %q", f.Action)
	}
	envelopeMatch := fmt.Sprintf("%s+%s", localPart, f.Suffix)
	var sb strings.Builder
	sb.WriteString("require [")
	for i, ext := range exts {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(sieveStringLiteral(ext))
	}
	sb.WriteString("];\n")
	fmt.Fprintf(&sb, "# tagged-address: %s+%s -> %s (%s)\n",
		localPart, f.Suffix, f.LabelName, f.Action)
	fmt.Fprintf(&sb, "if envelope :matches :localpart \"to\" %s {\n",
		sieveStringLiteral(envelopeMatch))
	sb.WriteString(action)
	sb.WriteString("}\n")
	return sb.String(), nil
}

// sieveStringLiteral produces a Sieve-quoted string literal. Mirrors
// internal/sieve/compile_managed.go's unexported sieveQuote; we
// re-implement here to avoid widening the sieve package's public
// surface for one caller. Backslash and double-quote are escaped;
// control characters and DEL are replaced with U+FFFD (which is
// preserved as UTF-8 in the script — Sieve string literals tolerate
// any non-control byte sequence).
func sieveStringLiteral(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, ch := range s {
		switch {
		case ch == '"':
			sb.WriteString(`\"`)
		case ch == '\\':
			sb.WriteString(`\\`)
		case ch < 0x20 || ch == 0x7f:
			sb.WriteRune(0xFFFD)
		default:
			sb.WriteRune(ch)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// mergeRequireAndPrepend prepends fragment to existing, merging any
// leading `require [...];` declarations into a single statement so the
// resulting script keeps the single-require invariant the spec calls
// for (REQ-TAG-50 step 2). The merge is conservative:
//
//   - If existing has no leading require, fragment is prepended raw.
//   - If existing has a leading require, its extension list is unioned
//     with fragment's extension list, the new require line replaces
//     fragment's require, and the rest of fragment is prepended above
//     existing's post-require body.
//
// This implementation parses the require line via the sieve package's
// Parse so the merge does not have to re-implement string escaping or
// whitespace tolerance.
func mergeRequireAndPrepend(existing, fragment string) string {
	fragmentExts, fragmentBody := splitLeadingRequire(fragment)
	if existing == "" {
		// No user script yet: prepend the fragment verbatim. The
		// fragment already has its own require line.
		return fragment
	}
	existingExts, existingBody := splitLeadingRequire(existing)
	if len(existingExts) == 0 && len(fragmentExts) == 0 {
		// Defensive: both halves lack a require statement. The store
		// will accept the merged text but the parser would reject the
		// extension-dependent tests; surface as a plain concatenation
		// and let validation upstream fail loudly.
		return fragment + "\n" + existing
	}
	merged := dedupAppend(existingExts, fragmentExts)
	var sb strings.Builder
	sb.WriteString("require [")
	for i, ext := range merged {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(sieveStringLiteral(ext))
	}
	sb.WriteString("];\n")
	sb.WriteString(fragmentBody)
	if !strings.HasSuffix(fragmentBody, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString(existingBody)
	return sb.String()
}

// splitLeadingRequire returns the extension list from every leading
// `require ...;` statement (if any) and the body that follows them.
// If the script has no leading require, returns (nil, original).
//
// sieve.Parse collapses every leading require declaration into the
// Script.Requires slice — that flat slice is the union of extensions
// to fold into the new merged require line. We walk the source text
// separately to find the byte index of the LAST leading semicolon so
// the returned body strips every consumed require statement (the
// parser does not surface that byte offset itself).
//
// When Parse fails we fall through to "no require found"; the caller's
// validation pass will surface the underlying error to the user.
func splitLeadingRequire(script string) ([]string, string) {
	parsed, err := sieve.Parse([]byte(script))
	if err != nil || parsed == nil || len(parsed.Requires) == 0 {
		return nil, script
	}
	exts := append([]string{}, parsed.Requires...)
	endIdx := indexOfLastLeadingRequireEnd(script)
	if endIdx < 0 {
		return nil, script
	}
	body := script[endIdx+1:]
	body = strings.TrimPrefix(body, "\n")
	return exts, body
}

// indexOfLastLeadingRequireEnd walks the script from the start,
// consuming whitespace, line comments (#...\n), block comments
// (/* ... */), and `require ...;` statements, until it hits the first
// non-require statement. Returns the byte index of the ';' that ended
// the LAST consumed require statement, or -1 when no leading require
// was found.
func indexOfLastLeadingRequireEnd(s string) int {
	i := 0
	lastEnd := -1
	for i < len(s) {
		// Skip whitespace.
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
		// Skip line comment.
		if i < len(s) && s[i] == '#' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		// Skip block comment.
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			}
			continue
		}
		// Try to match the "require" identifier (case-insensitive per
		// RFC 5228 §2.10).
		if !hasPrefixFold(s[i:], "require") {
			return lastEnd
		}
		// Make sure the next char is not an identifier continuation
		// (so we don't match "requiresomething").
		next := i + len("require")
		if next < len(s) && isIdentByte(s[next]) {
			return lastEnd
		}
		// Find the terminator ';' starting at next.
		semi := indexOfStatementEnd(s, next)
		if semi < 0 {
			return lastEnd
		}
		lastEnd = semi
		i = semi + 1
	}
	return lastEnd
}

// hasPrefixFold reports whether s begins with prefix under ASCII
// case-folding. Sieve identifiers are case-insensitive per RFC 5228
// §2.10.
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a, b := s[i], prefix[i]
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

// isIdentByte reports whether c is a valid Sieve identifier
// continuation byte. Identifiers are ASCII letters / digits /
// underscore per RFC 5228 §2.10.
func isIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '_'
}

// indexOfStatementEnd scans forward from i for the ';' that ends the
// current statement, skipping double-quoted strings (their embedded
// ';' bytes are not statement terminators). Returns -1 when no
// terminator is found before end-of-input.
func indexOfStatementEnd(s string, i int) int {
	inString := false
	escape := false
	for ; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if inString {
			switch c {
			case '\\':
				escape = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case ';':
			return i
		}
	}
	return -1
}

// dedupAppend returns the union of a and b preserving the order of a
// followed by any new entries from b in their first-seen order.
func dedupAppend(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// recomposeEffectiveScript re-renders the effective script (managed
// preamble + user script) and writes it to the runtime sieve_scripts
// slot. The managed preamble is fetched from store.ManagedRule rows
// (the JMAP ManagedRule surface owns this path); when the principal
// has no managed rules the preamble is empty and EffectiveScript
// returns the user script verbatim.
func (s *Server) recomposeEffectiveScript(ctx context.Context, pid store.PrincipalID, userScript string) error {
	rules, err := s.store.Meta().ListManagedRules(ctx, pid, store.ManagedRuleFilter{})
	if err != nil {
		return fmt.Errorf("convert-to-sieve: list managed rules: %w", err)
	}
	preamble, err := sieve.CompileRules(rules)
	if err != nil {
		return fmt.Errorf("convert-to-sieve: compile managed rules: %w", err)
	}
	effective := sieve.EffectiveScript(preamble, userScript)
	if err := s.store.Meta().SetSieveScript(ctx, pid, effective); err != nil {
		return fmt.Errorf("convert-to-sieve: persist effective script: %w", err)
	}
	// Recompose is best-effort; a failure here means the runtime path
	// keeps the old combined script, but the user-script half persists
	// correctly so the next managed-rule mutation re-syncs. We log
	// nothing extra; writeStoreError handles the surface response.
	_ = observe.ActivityInternal
	return nil
}

// Compile-time sanity: handleConvertTaggedAddressFilterToSieve must
// satisfy the (w, r) handler shape so registerRoutes can bind it.
var _ http.HandlerFunc = (*Server)(nil).handleConvertTaggedAddressFilterToSieve
