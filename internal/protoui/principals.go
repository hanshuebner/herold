package protoui

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/store"
)

// principalsListData is the body payload for templates/principals_list.html.
type principalsListData struct {
	Items   []store.Principal
	Search  string
	Cursor  uint64
	HasNext bool
	NextCur uint64
}

func (s *Server) handlePrincipalsList(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required to list principals.")
		return
	}
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("q"))
	limit := 25
	after := uint64(0)
	if raw := q.Get("after"); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
			after = n
		}
	}
	rows, err := s.store.Meta().ListPrincipals(r.Context(), store.PrincipalID(after), limit)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "List principals failed: "+err.Error())
		return
	}
	// In-memory substring filter for the search box. Acceptable for the
	// admin UI's expected scale (thousands of principals); a SQL ILIKE
	// path lands when we hit a deployment that needs it.
	if search != "" {
		filtered := make([]store.Principal, 0, len(rows))
		needle := strings.ToLower(search)
		for _, p := range rows {
			if strings.Contains(strings.ToLower(p.CanonicalEmail), needle) ||
				strings.Contains(strings.ToLower(p.DisplayName), needle) {
				filtered = append(filtered, p)
			}
		}
		rows = filtered
	}
	body := principalsListData{
		Items:  rows,
		Search: search,
		Cursor: after,
	}
	if len(rows) == limit {
		body.HasNext = true
		body.NextCur = uint64(rows[len(rows)-1].ID)
	}
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "Principals",
		Active:   "principals",
		BodyTmpl: "principals_list_body",
		Body:     body,
	})
}

// principalDetailData is the body payload for templates/principals_detail.html.
type principalDetailData struct {
	Principal     store.Principal
	APIKeys       []store.APIKey
	OIDCLinks     []store.OIDCLink
	OIDCProviders []store.OIDCProvider
	NewAPIKey     string // plaintext, set only after a fresh mint
	TOTPProvision *totpProvision
}

type totpProvision struct {
	Secret string
	URI    string
	// QRPNG is a fully-formed `data:image/png;base64,...` URL. The
	// html/template auto-escaper rejects data: URLs in src=
	// attributes by default (the ZgotmplZ guard); template.URL
	// signals the value has been audited and is safe to embed in
	// a URL context. This is the only template.URL site in the
	// package — the value is server-generated from a local QR
	// encoder, never from user input.
	QRPNG template.URL
}

func (s *Server) handlePrincipalsDetail(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFromCtx(r.Context())
	if !canActOn(caller, pid) {
		s.renderError(w, r, http.StatusForbidden, "Insufficient privileges.")
		return
	}
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "Principal not found.")
		return
	}
	body := principalDetailData{Principal: p}
	if keys, err := s.store.Meta().ListAPIKeysByPrincipal(r.Context(), pid); err == nil {
		body.APIKeys = keys
	}
	if links, err := s.store.Meta().ListOIDCLinksByPrincipal(r.Context(), pid); err == nil {
		body.OIDCLinks = links
	}
	if provs, err := s.store.Meta().ListOIDCProviders(r.Context()); err == nil {
		body.OIDCProviders = provs
	}
	body.NewAPIKey = r.URL.Query().Get("new_api_key")

	flash := flashFromQuery(r)
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "Principal " + p.CanonicalEmail,
		Active:   "principals",
		Flash:    flash,
		BodyTmpl: "principals_detail_body",
		Body:     body,
	})
}

func (s *Server) handlePrincipalsNewForm(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "New principal",
		Active:   "principals",
		BodyTmpl: "principals_new_body",
		Body:     map[string]string{},
	})
}

func (s *Server) handlePrincipalsCreate(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	email := strings.TrimSpace(r.PostForm.Get("email"))
	password := r.PostForm.Get("password")
	displayName := strings.TrimSpace(r.PostForm.Get("display_name"))
	if email == "" || password == "" {
		s.renderPage(w, r, http.StatusBadRequest, &pageData{
			Title:    "New principal",
			Active:   "principals",
			Flash:    &flashMessage{Kind: "error", Body: "Email and password are required."},
			BodyTmpl: "principals_new_body",
			Body:     map[string]string{"Email": email, "DisplayName": displayName},
		})
		return
	}
	pid, err := s.dir.CreatePrincipal(r.Context(), email, password)
	if err != nil {
		s.renderPage(w, r, http.StatusBadRequest, &pageData{
			Title:    "New principal",
			Active:   "principals",
			Flash:    &flashMessage{Kind: "error", Body: humanDirError(err)},
			BodyTmpl: "principals_new_body",
			Body:     map[string]string{"Email": email, "DisplayName": displayName},
		})
		return
	}
	if displayName != "" {
		if p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid); err == nil {
			p.DisplayName = displayName
			_ = s.store.Meta().UpdatePrincipal(r.Context(), p)
		}
	}
	http.Redirect(w, r, fmt.Sprintf("%s/principals/%d?flash=created", s.pathPrefix, pid), http.StatusSeeOther)
}

func (s *Server) handlePrincipalsUpdate(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFromCtx(r.Context())
	if !canActOn(caller, pid) {
		s.renderError(w, r, http.StatusForbidden, "Insufficient privileges.")
		return
	}
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "Principal not found.")
		return
	}
	p.DisplayName = strings.TrimSpace(r.PostForm.Get("display_name"))
	if caller.Flags.Has(store.PrincipalFlagAdmin) {
		if raw := strings.TrimSpace(r.PostForm.Get("quota_bytes")); raw != "" {
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 0 {
				p.QuotaBytes = n
			}
		}
		preserved := p.Flags & store.PrincipalFlagTOTPEnabled
		var f store.PrincipalFlags
		if r.PostForm.Get("flag_admin") == "on" {
			f |= store.PrincipalFlagAdmin
		}
		if r.PostForm.Get("flag_disabled") == "on" {
			f |= store.PrincipalFlagDisabled
		}
		if r.PostForm.Get("flag_ignore_download_limits") == "on" {
			f |= store.PrincipalFlagIgnoreDownloadLimits
		}
		p.Flags = f | preserved
	}
	if err := s.store.Meta().UpdatePrincipal(r.Context(), p); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Update failed: "+err.Error())
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s/principals/%d?flash=updated", s.pathPrefix, pid), http.StatusSeeOther)
}

func (s *Server) handlePrincipalsDelete(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	if r.PostForm.Get("confirm") != "DELETE" {
		s.renderError(w, r, http.StatusBadRequest, "Type DELETE to confirm.")
		return
	}
	if err := s.dir.DeletePrincipal(r.Context(), pid); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Delete failed: "+err.Error())
		return
	}
	http.Redirect(w, r, s.pathPrefix+"/principals?flash=deleted", http.StatusSeeOther)
}

func (s *Server) handlePrincipalsPassword(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFromCtx(r.Context())
	if !canActOn(caller, pid) {
		s.renderError(w, r, http.StatusForbidden, "Insufficient privileges.")
		return
	}
	newPass := r.PostForm.Get("new_password")
	currentPass := r.PostForm.Get("current_password")
	if newPass == "" {
		s.renderError(w, r, http.StatusBadRequest, "New password required.")
		return
	}
	isSelf := caller.ID == pid
	switch {
	case isSelf:
		if err := s.dir.UpdatePassword(r.Context(), pid, currentPass, newPass); err != nil {
			s.renderError(w, r, http.StatusBadRequest, humanDirError(err))
			return
		}
	case caller.Flags.Has(store.PrincipalFlagAdmin):
		// Admin override path. Mirrors protoadmin's adminSetPassword.
		// Two-caller duplication justified per the protosend pattern;
		// a third caller earns a shared directory.AdminSetPassword.
		if err := s.adminSetPassword(r, pid, newPass); err != nil {
			s.renderError(w, r, http.StatusBadRequest, humanDirError(err))
			return
		}
	default:
		s.renderError(w, r, http.StatusForbidden, "Insufficient privileges.")
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s/principals/%d?flash=password", s.pathPrefix, pid), http.StatusSeeOther)
}

func (s *Server) handleTOTPEnroll(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFromCtx(r.Context())
	if !canActOn(caller, pid) {
		s.renderError(w, r, http.StatusForbidden, "Insufficient privileges.")
		return
	}
	secret, uri, err := s.dir.EnrollTOTP(r.Context(), pid)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, humanDirError(err))
		return
	}
	pngBytes, err := renderQRPNG(uri, 192)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "QR render failed: "+err.Error())
		return
	}
	dataURL := template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes))

	p, _ := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	body := principalDetailData{Principal: p}
	if keys, err := s.store.Meta().ListAPIKeysByPrincipal(r.Context(), pid); err == nil {
		body.APIKeys = keys
	}
	body.TOTPProvision = &totpProvision{Secret: secret, URI: uri, QRPNG: dataURL}
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "Principal " + p.CanonicalEmail,
		Active:   "principals",
		Flash:    &flashMessage{Kind: "ok", Body: "Scan the QR with your authenticator, then enter a code to confirm."},
		BodyTmpl: "principals_detail_body",
		Body:     body,
	})
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFromCtx(r.Context())
	if !canActOn(caller, pid) {
		s.renderError(w, r, http.StatusForbidden, "Insufficient privileges.")
		return
	}
	code := strings.TrimSpace(r.PostForm.Get("code"))
	if code == "" {
		s.renderError(w, r, http.StatusBadRequest, "TOTP code required.")
		return
	}
	if err := s.dir.ConfirmTOTP(r.Context(), pid, code); err != nil {
		s.renderError(w, r, http.StatusBadRequest, humanDirError(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s/principals/%d?flash=totp_confirmed", s.pathPrefix, pid), http.StatusSeeOther)
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFromCtx(r.Context())
	if !canActOn(caller, pid) {
		s.renderError(w, r, http.StatusForbidden, "Insufficient privileges.")
		return
	}
	currentPass := r.PostForm.Get("current_password")
	if currentPass == "" {
		s.renderError(w, r, http.StatusBadRequest, "Current password required.")
		return
	}
	if err := s.dir.DisableTOTP(r.Context(), pid, currentPass); err != nil {
		s.renderError(w, r, http.StatusBadRequest, humanDirError(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s/principals/%d?flash=totp_disabled", s.pathPrefix, pid), http.StatusSeeOther)
}

func (s *Server) handleAPIKeyNew(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFromCtx(r.Context())
	if !canActOn(caller, pid) {
		s.renderError(w, r, http.StatusForbidden, "Insufficient privileges.")
		return
	}
	label := strings.TrimSpace(r.PostForm.Get("label"))
	if label == "" {
		label = "ui-issued"
	}
	plaintext, hashed, err := generateAPIKey()
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Key generation failed.")
		return
	}
	if _, err := s.store.Meta().InsertAPIKey(r.Context(), store.APIKey{
		PrincipalID: pid,
		Hash:        hashed,
		Name:        label,
	}); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Key insert failed: "+err.Error())
		return
	}
	target := fmt.Sprintf("%s/principals/%d?new_api_key=%s&flash=apikey_new",
		s.pathPrefix, pid, url.QueryEscape(plaintext))
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) handleAPIKeyRevoke(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFromCtx(r.Context())
	if !canActOn(caller, pid) {
		s.renderError(w, r, http.StatusForbidden, "Insufficient privileges.")
		return
	}
	idRaw := r.PathValue("id")
	id, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id == 0 {
		s.renderError(w, r, http.StatusBadRequest, "Invalid key id.")
		return
	}
	if err := s.store.Meta().DeleteAPIKey(r.Context(), store.APIKeyID(id)); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Revoke failed: "+err.Error())
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s/principals/%d?flash=apikey_revoked", s.pathPrefix, pid), http.StatusSeeOther)
}

func (s *Server) handleOIDCUnlink(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFromCtx(r.Context())
	if !canActOn(caller, pid) {
		s.renderError(w, r, http.StatusForbidden, "Insufficient privileges.")
		return
	}
	if s.rp == nil {
		s.renderError(w, r, http.StatusBadRequest, "OIDC not configured on this server.")
		return
	}
	providerID := r.PathValue("provider_id")
	if err := s.rp.Unlink(r.Context(), pid, directoryoidc.ProviderID(providerID)); err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s/principals/%d?flash=oidc_unlinked", s.pathPrefix, pid), http.StatusSeeOther)
}

func (s *Server) handleOIDCLinkBegin(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.parsePID(w, r)
	if !ok {
		return
	}
	caller, _ := principalFromCtx(r.Context())
	if !canActOn(caller, pid) {
		s.renderError(w, r, http.StatusForbidden, "Insufficient privileges.")
		return
	}
	if s.rp == nil {
		s.renderError(w, r, http.StatusBadRequest, "OIDC not configured on this server.")
		return
	}
	providerID := r.PathValue("provider_id")
	authURL, _, err := s.rp.BeginLink(r.Context(), pid, directoryoidc.ProviderID(providerID))
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// parsePID reads the {pid} path parameter.
func (s *Server) parsePID(w http.ResponseWriter, r *http.Request) (store.PrincipalID, bool) {
	raw := r.PathValue("pid")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		s.renderError(w, r, http.StatusBadRequest, "Invalid principal id.")
		return 0, false
	}
	return store.PrincipalID(n), true
}

// canActOn reports whether the caller may act on the target principal.
func canActOn(caller store.Principal, target store.PrincipalID) bool {
	return caller.ID == target || caller.Flags.Has(store.PrincipalFlagAdmin)
}

// humanDirError maps a directory error to a one-line user-facing
// message. Internal errors collapse to a generic phrase so backend
// implementation details do not leak to the operator UI.
func humanDirError(err error) string {
	switch {
	case errors.Is(err, directory.ErrUnauthorized):
		return "Current password is incorrect."
	case errors.Is(err, directory.ErrInvalidEmail):
		return "Email address is not valid."
	case errors.Is(err, directory.ErrWeakPassword):
		return "Password too weak (minimum 12 characters)."
	case errors.Is(err, directory.ErrConflict):
		return "An entry with this identifier already exists."
	case errors.Is(err, directory.ErrTOTPAlreadyEnabled):
		return "Two-factor authentication is already enabled."
	case errors.Is(err, directory.ErrTOTPNotEnrolled):
		return "Two-factor authentication is not enrolled."
	case errors.Is(err, directory.ErrRateLimited):
		return "Too many recent attempts; try again shortly."
	default:
		return "Operation failed."
	}
}

// flashFromQuery converts a `?flash=...` argument into a flashMessage.
func flashFromQuery(r *http.Request) *flashMessage {
	switch r.URL.Query().Get("flash") {
	case "":
		return nil
	case "created":
		return &flashMessage{Kind: "ok", Body: "Principal created."}
	case "updated":
		return &flashMessage{Kind: "ok", Body: "Principal updated."}
	case "deleted":
		return &flashMessage{Kind: "ok", Body: "Principal deleted."}
	case "password":
		return &flashMessage{Kind: "ok", Body: "Password changed."}
	case "totp_confirmed":
		return &flashMessage{Kind: "ok", Body: "Two-factor authentication enabled."}
	case "totp_disabled":
		return &flashMessage{Kind: "ok", Body: "Two-factor authentication disabled."}
	case "apikey_new":
		return &flashMessage{Kind: "ok", Body: "API key created — copy it now, it will not be shown again."}
	case "apikey_revoked":
		return &flashMessage{Kind: "ok", Body: "API key revoked."}
	case "oidc_unlinked":
		return &flashMessage{Kind: "ok", Body: "OIDC link removed."}
	case "domain_created":
		return &flashMessage{Kind: "ok", Body: "Domain added."}
	case "domain_deleted":
		return &flashMessage{Kind: "ok", Body: "Domain removed."}
	case "alias_created":
		return &flashMessage{Kind: "ok", Body: "Alias added."}
	case "alias_deleted":
		return &flashMessage{Kind: "ok", Body: "Alias removed."}
	case "queue_action":
		return &flashMessage{Kind: "ok", Body: "Queue action applied."}
	default:
		return nil
	}
}

// generateAPIKey returns a freshly minted plaintext key (with the
// admin "hk_" prefix) and its sha256 hash. Mirrors the protoadmin
// helper of the same shape; duplicated under the protosend rule.
func generateAPIKey() (plaintext, hashed string, err error) {
	var b [24]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", "", err
	}
	plaintext = "hk_" + base64.RawURLEncoding.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(plaintext))
	hashed = hex.EncodeToString(sum[:])
	return plaintext, hashed, nil
}

// renderQRPNG is a tiny wrapper around boombuler/barcode/qr that
// outputs a PNG byte slice for inline rendering as a data URL.
func renderQRPNG(text string, size int) ([]byte, error) {
	code, err := qr.Encode(text, qr.M, qr.Auto)
	if err != nil {
		return nil, err
	}
	scaled, err := barcode.Scale(code, size, size)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// adminSetPassword bypasses the current-password verification. Mirrors
// protoadmin.Server.adminSetPassword exactly, copied here under the
// protosend duplication-justification rule. When a third caller
// arrives, all three should converge on directory.AdminSetPassword.
func (s *Server) adminSetPassword(r *http.Request, pid store.PrincipalID, newPassword string) error {
	if len(newPassword) < directory.MinPasswordLength {
		return directory.ErrWeakPassword
	}
	p, err := s.store.Meta().GetPrincipalByID(r.Context(), pid)
	if err != nil {
		return err
	}
	hashed, err := hashPasswordArgon2id(newPassword)
	if err != nil {
		return fmt.Errorf("hash: %w", err)
	}
	p.PasswordHash = hashed
	return s.store.Meta().UpdatePrincipal(r.Context(), p)
}
