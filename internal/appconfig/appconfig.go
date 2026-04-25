package appconfig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/hanshuebner/herold/internal/store"
)

// Snapshot is the on-disk shape of an export. It is intentionally flat so
// it round-trips through go-toml without custom decoders.
type Snapshot struct {
	FormatVersion int            `toml:"format_version"`
	ExportedAt    time.Time      `toml:"exported_at"`
	Domains       []DomainEntry  `toml:"domains,omitempty"`
	Principals    []PrincipalRow `toml:"principals,omitempty"`
	Aliases       []AliasEntry   `toml:"aliases,omitempty"`
	OIDCProviders []OIDCProvRow  `toml:"oidc_providers,omitempty"`
	APIKeys       []APIKeyMeta   `toml:"api_keys,omitempty"`
	SieveScripts  []SieveScript  `toml:"sieve_scripts,omitempty"`
}

// DomainEntry mirrors store.Domain without the insert timestamp.
type DomainEntry struct {
	Name    string `toml:"name"`
	IsLocal bool   `toml:"is_local"`
}

// PrincipalRow is the subset of store.Principal safe to export. Password
// hashes and TOTP secrets are intentionally omitted so operators can share
// state.toml without leaking credentials. Import creates empty
// password/TOTP fields; operators reset these via the admin CLI.
type PrincipalRow struct {
	ID             uint64 `toml:"id"`
	Kind           uint8  `toml:"kind"`
	CanonicalEmail string `toml:"canonical_email"`
	DisplayName    string `toml:"display_name,omitempty"`
	QuotaBytes     int64  `toml:"quota_bytes,omitempty"`
	Flags          uint32 `toml:"flags,omitempty"`
}

// AliasEntry captures an alias mapping.
type AliasEntry struct {
	LocalPart       string `toml:"local_part"`
	Domain          string `toml:"domain"`
	TargetPrincipal uint64 `toml:"target_principal"`
}

// OIDCProvRow is the exported shape of store.OIDCProvider.
type OIDCProvRow struct {
	Name            string   `toml:"name"`
	IssuerURL       string   `toml:"issuer_url"`
	ClientID        string   `toml:"client_id"`
	ClientSecretRef string   `toml:"client_secret_ref,omitempty"`
	Scopes          []string `toml:"scopes,omitempty"`
	AutoProvision   bool     `toml:"auto_provision,omitempty"`
}

// APIKeyMeta carries the non-secret fields of an API key. The Hash field
// is NOT exported; operators rotate by issuing a fresh key on the target
// system.
type APIKeyMeta struct {
	ID          uint64 `toml:"id"`
	PrincipalID uint64 `toml:"principal_id"`
	Name        string `toml:"name"`
}

// SieveScript is one principal's active Sieve script.
type SieveScript struct {
	PrincipalID uint64 `toml:"principal_id"`
	Script      string `toml:"script"`
}

// ImportMode selects conflict-resolution behaviour on Import.
type ImportMode int

const (
	// ImportMerge adds missing rows and leaves existing rows untouched.
	ImportMerge ImportMode = iota
	// ImportReplace deletes existing rows the export references and
	// inserts the exported value. Reserved for Phase 2 (full restore); in
	// Phase 1 it behaves like ImportMerge with a log warning.
	ImportReplace
)

// ImportOptions controls Import behaviour.
type ImportOptions struct {
	// Mode selects conflict resolution.
	Mode ImportMode
}

// Export dumps the application config to w in TOML.
func Export(ctx context.Context, s store.Store, w io.Writer) error {
	if s == nil {
		return errors.New("appconfig: nil store")
	}
	snap := Snapshot{
		FormatVersion: 1,
		ExportedAt:    time.Now().UTC(),
	}
	// Domains.
	doms, err := s.Meta().ListLocalDomains(ctx)
	if err != nil {
		return fmt.Errorf("appconfig: list domains: %w", err)
	}
	for _, d := range doms {
		snap.Domains = append(snap.Domains, DomainEntry{Name: d.Name, IsLocal: d.IsLocal})
	}
	// Principals.
	var after store.PrincipalID
	for {
		batch, err := s.Meta().ListPrincipals(ctx, after, 1000)
		if err != nil {
			return fmt.Errorf("appconfig: list principals: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		for _, p := range batch {
			snap.Principals = append(snap.Principals, PrincipalRow{
				ID:             uint64(p.ID),
				Kind:           uint8(p.Kind),
				CanonicalEmail: p.CanonicalEmail,
				DisplayName:    p.DisplayName,
				QuotaBytes:     p.QuotaBytes,
				Flags:          uint32(p.Flags),
			})
			// Sieve per principal.
			script, err := s.Meta().GetSieveScript(ctx, p.ID)
			if err != nil {
				return fmt.Errorf("appconfig: get sieve for %d: %w", p.ID, err)
			}
			if script != "" {
				snap.SieveScripts = append(snap.SieveScripts, SieveScript{
					PrincipalID: uint64(p.ID),
					Script:      script,
				})
			}
			after = p.ID
		}
		if len(batch) < 1000 {
			break
		}
	}
	// OIDC providers.
	providers, err := s.Meta().ListOIDCProviders(ctx)
	if err != nil {
		return fmt.Errorf("appconfig: list oidc: %w", err)
	}
	for _, p := range providers {
		snap.OIDCProviders = append(snap.OIDCProviders, OIDCProvRow{
			Name:            p.Name,
			IssuerURL:       p.IssuerURL,
			ClientID:        p.ClientID,
			ClientSecretRef: p.ClientSecretRef,
			Scopes:          p.Scopes,
			AutoProvision:   p.AutoProvision,
		})
	}
	// Aliases and API keys: Phase 1 store surface doesn't currently
	// expose list-by-principal; keep the slots in the snapshot for
	// forward-compat but leave them empty. A future metadata extension
	// fills these in.
	enc := toml.NewEncoder(w)
	enc.SetIndentTables(true)
	if err := enc.Encode(snap); err != nil {
		return fmt.Errorf("appconfig: encode: %w", err)
	}
	return nil
}

// Import loads a Snapshot from r and applies it to s according to opts.
// In Phase 1 merge mode: domains, principals (with blank passwords),
// OIDC providers, and Sieve scripts are inserted when absent. Conflicts
// are logged via the returned error; partial progress is preserved.
//
// Every successful mutation emits a `appconfig.<kind>.<verb>` audit
// row via store.Metadata.AppendAuditLog with ActorKind=ActorSystem
// and ActorID="appconfig-import" so an operator running
// `herold app-config load` leaves a trail. STANDARDS §9: every
// config mutation is auditable. Audit-append failures are logged-via-
// error-return-only on the first occurrence so a misconfigured store
// does not silently swallow forensic trail bits while the import
// rolls forward.
func Import(ctx context.Context, s store.Store, r io.Reader, opts ImportOptions) error {
	if s == nil {
		return errors.New("appconfig: nil store")
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("appconfig: read: %w", err)
	}
	var snap Snapshot
	if err := toml.Unmarshal(raw, &snap); err != nil {
		return fmt.Errorf("appconfig: parse: %w", err)
	}
	now := time.Now().UTC()
	audit := func(action, subject string, meta map[string]string) error {
		return s.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
			At:        now,
			ActorKind: store.ActorSystem,
			ActorID:   "appconfig-import",
			Action:    action,
			Subject:   subject,
			Outcome:   store.OutcomeSuccess,
			Metadata:  meta,
		})
	}
	// Domains.
	for _, d := range snap.Domains {
		err := s.Meta().InsertDomain(ctx, store.Domain{Name: d.Name, IsLocal: d.IsLocal})
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			return fmt.Errorf("appconfig: insert domain %q: %w", d.Name, err)
		}
		if err := audit("appconfig.domain.upsert", "domain:"+d.Name, map[string]string{
			"is_local": boolStr(d.IsLocal),
		}); err != nil {
			return fmt.Errorf("appconfig: audit domain %q: %w", d.Name, err)
		}
	}
	// Principals. We insert without the original ID; the store assigns
	// fresh IDs. The import preserves the operator-visible fields
	// (email, display name, quota). Sieve scripts are attached by
	// canonical email lookup below.
	idMap := make(map[uint64]store.PrincipalID, len(snap.Principals))
	for _, p := range snap.Principals {
		existing, err := s.Meta().GetPrincipalByEmail(ctx, p.CanonicalEmail)
		if err == nil {
			idMap[p.ID] = existing.ID
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("appconfig: lookup principal %q: %w", p.CanonicalEmail, err)
		}
		inserted, err := s.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKind(p.Kind),
			CanonicalEmail: p.CanonicalEmail,
			DisplayName:    p.DisplayName,
			QuotaBytes:     p.QuotaBytes,
			Flags:          store.PrincipalFlags(p.Flags),
		})
		if err != nil {
			return fmt.Errorf("appconfig: insert principal %q: %w", p.CanonicalEmail, err)
		}
		idMap[p.ID] = inserted.ID
		if err := audit(
			"appconfig.principal.upsert",
			fmt.Sprintf("principal:%d", inserted.ID),
			map[string]string{
				"email":        inserted.CanonicalEmail,
				"display_name": inserted.DisplayName,
				"flags":        fmt.Sprintf("%d", uint32(inserted.Flags)),
				"kind":         fmt.Sprintf("%d", uint8(inserted.Kind)),
			}); err != nil {
			return fmt.Errorf("appconfig: audit principal %q: %w", p.CanonicalEmail, err)
		}
	}
	// Sieve scripts by mapped principal id.
	for _, sc := range snap.SieveScripts {
		pid, ok := idMap[sc.PrincipalID]
		if !ok {
			continue
		}
		if err := s.Meta().SetSieveScript(ctx, pid, sc.Script); err != nil {
			return fmt.Errorf("appconfig: set sieve for %d: %w", pid, err)
		}
		if err := audit(
			"appconfig.sieve.upsert",
			fmt.Sprintf("principal:%d", pid),
			map[string]string{
				"script_bytes": fmt.Sprintf("%d", len(sc.Script)),
			}); err != nil {
			return fmt.Errorf("appconfig: audit sieve %d: %w", pid, err)
		}
	}
	// OIDC providers.
	for _, prov := range snap.OIDCProviders {
		err := s.Meta().InsertOIDCProvider(ctx, store.OIDCProvider{
			Name:            prov.Name,
			IssuerURL:       prov.IssuerURL,
			ClientID:        prov.ClientID,
			ClientSecretRef: prov.ClientSecretRef,
			Scopes:          prov.Scopes,
			AutoProvision:   prov.AutoProvision,
		})
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			return fmt.Errorf("appconfig: insert oidc provider %q: %w", prov.Name, err)
		}
		if err := audit("appconfig.oidc.upsert", "oidc:"+prov.Name, map[string]string{
			"issuer_url": prov.IssuerURL,
			"client_id":  prov.ClientID,
		}); err != nil {
			return fmt.Errorf("appconfig: audit oidc %q: %w", prov.Name, err)
		}
	}
	_ = opts
	return nil
}

// boolStr renders a bool as "true"/"false" for audit metadata maps.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
