package protoadmin

import (
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// principalDTO is the wire representation of a Principal. Sensitive
// fields (PasswordHash, TOTPSecret) are never serialised.
type principalDTO struct {
	ID             uint64    `json:"id"`
	Kind           string    `json:"kind"`
	CanonicalEmail string    `json:"canonical_email"`
	DisplayName    string    `json:"display_name,omitempty"`
	QuotaBytes     int64     `json:"quota_bytes"`
	Flags          []string  `json:"flags"`
	TOTPEnabled    bool      `json:"totp_enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func toPrincipalDTO(p store.Principal) principalDTO {
	return principalDTO{
		ID:             uint64(p.ID),
		Kind:           principalKindString(p.Kind),
		CanonicalEmail: p.CanonicalEmail,
		DisplayName:    p.DisplayName,
		QuotaBytes:     p.QuotaBytes,
		Flags:          principalFlagsToStrings(p.Flags),
		TOTPEnabled:    p.Flags.Has(store.PrincipalFlagTOTPEnabled),
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
}

func principalKindString(k store.PrincipalKind) string {
	switch k {
	case store.PrincipalKindUser:
		return "user"
	case store.PrincipalKindGroup:
		return "group"
	case store.PrincipalKindService:
		return "service"
	default:
		return "unknown"
	}
}

func principalFlagsToStrings(f store.PrincipalFlags) []string {
	out := []string{}
	if f.Has(store.PrincipalFlagDisabled) {
		out = append(out, "disabled")
	}
	if f.Has(store.PrincipalFlagIgnoreDownloadLimits) {
		out = append(out, "ignore_download_limits")
	}
	if f.Has(store.PrincipalFlagAdmin) {
		out = append(out, "admin")
	}
	if f.Has(store.PrincipalFlagTOTPEnabled) {
		out = append(out, "totp_enabled")
	}
	return out
}

func principalFlagsFromStrings(in []string) (store.PrincipalFlags, bool) {
	var f store.PrincipalFlags
	for _, s := range in {
		switch s {
		case "disabled":
			f |= store.PrincipalFlagDisabled
		case "ignore_download_limits":
			f |= store.PrincipalFlagIgnoreDownloadLimits
		case "admin":
			f |= store.PrincipalFlagAdmin
		case "totp_enabled":
			// Clients may not set totp_enabled directly; it is toggled by
			// the TOTP confirm/disable endpoints.
			continue
		default:
			return 0, false
		}
	}
	return f, true
}

// aliasDTO is the wire representation of an Alias row.
type aliasDTO struct {
	ID                uint64     `json:"id"`
	LocalPart         string     `json:"local"`
	Domain            string     `json:"domain"`
	TargetPrincipalID uint64     `json:"target_principal_id"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

func toAliasDTO(a store.Alias) aliasDTO {
	return aliasDTO{
		ID:                uint64(a.ID),
		LocalPart:         a.LocalPart,
		Domain:            a.Domain,
		TargetPrincipalID: uint64(a.TargetPrincipal),
		ExpiresAt:         a.ExpiresAt,
		CreatedAt:         a.CreatedAt,
	}
}

// domainDTO is the wire representation of a Domain.
type domainDTO struct {
	Name      string    `json:"name"`
	Local     bool      `json:"local"`
	CreatedAt time.Time `json:"created_at"`
}

func toDomainDTO(d store.Domain) domainDTO {
	return domainDTO{Name: d.Name, Local: d.IsLocal, CreatedAt: d.CreatedAt}
}

// oidcProviderDTO is the wire representation of an OIDC provider row
// minus secret material.
type oidcProviderDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	IssuerURL string    `json:"issuer"`
	ClientID  string    `json:"client_id"`
	Scopes    []string  `json:"scopes"`
	CreatedAt time.Time `json:"created_at"`
}

func toOIDCProviderDTO(p store.OIDCProvider) oidcProviderDTO {
	return oidcProviderDTO{
		ID:        p.Name,
		Name:      p.Name,
		IssuerURL: p.IssuerURL,
		ClientID:  p.ClientID,
		Scopes:    append([]string(nil), p.Scopes...),
		CreatedAt: p.CreatedAt,
	}
}

// oidcLinkDTO is the wire representation of an OIDC link row.
type oidcLinkDTO struct {
	PrincipalID     uint64    `json:"principal_id"`
	ProviderID      string    `json:"provider_id"`
	Subject         string    `json:"subject"`
	EmailAtProvider string    `json:"email_at_provider,omitempty"`
	LinkedAt        time.Time `json:"linked_at"`
}

func toOIDCLinkDTO(l store.OIDCLink) oidcLinkDTO {
	return oidcLinkDTO{
		PrincipalID:     uint64(l.PrincipalID),
		ProviderID:      l.ProviderName,
		Subject:         l.Subject,
		EmailAtProvider: l.EmailAtProvider,
		LinkedAt:        l.LinkedAt,
	}
}

// apiKeyDTO is the wire representation of an APIKey row. The plaintext
// key is only returned once, on creation; subsequent GETs expose
// neither plaintext nor hash.
type apiKeyDTO struct {
	ID          uint64    `json:"id"`
	PrincipalID uint64    `json:"principal_id"`
	Label       string    `json:"label"`
	CreatedAt   time.Time `json:"created_at"`
	LastUsedAt  time.Time `json:"last_used_at,omitempty"`
}

func toAPIKeyDTO(k store.APIKey) apiKeyDTO {
	dto := apiKeyDTO{
		ID:          uint64(k.ID),
		PrincipalID: uint64(k.PrincipalID),
		Label:       k.Name,
		CreatedAt:   k.CreatedAt,
	}
	if !k.LastUsedAt.IsZero() {
		dto.LastUsedAt = k.LastUsedAt
	}
	return dto
}

// dkimKeyDTO is the wire representation of a DKIM key row. The private key
// material is never serialised; the TXTRecord field carries the public DNS
// payload that belongs at <selector>._domainkey.<domain>.
// REQ-ADM-310: the DKIM TXT record body is operator-visible.
type dkimKeyDTO struct {
	Selector  string    `json:"selector"`
	Algorithm string    `json:"algorithm"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	RotatedAt time.Time `json:"rotated_at,omitempty"`
	TXTRecord string    `json:"txt_record"`
}

func toDKIMKeyDTO(k store.DKIMKey, txt string) dkimKeyDTO {
	return dkimKeyDTO{
		Selector:  k.Selector,
		Algorithm: k.Algorithm.String(),
		IsActive:  k.Status == store.DKIMKeyStatusActive,
		CreatedAt: k.CreatedAt,
		RotatedAt: k.RotatedAt,
		TXTRecord: txt,
	}
}

// pageDTO is the envelope used by keyset-paginated list endpoints.
// We keep it minimal: items + next (as a string cursor, or null when
// the caller has reached the end).
type pageDTO[T any] struct {
	Items []T     `json:"items"`
	Next  *string `json:"next"`
}
