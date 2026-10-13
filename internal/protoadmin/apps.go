package protoadmin

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/apps"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// Admin REST surface for registered apps (REQ-APP-10..14, REQ-APP-30..31).
// Every route is mounted behind authAdmin (admin scope + elevation,
// REQ-AUTH-74); every mutation is audit-logged with subject
// "app:<app_key>" (REQ-ADM-300).

// AppsOptions configures the apps endpoints.
type AppsOptions struct {
	// Client fetches manifests through the outbound guard (REQ-APP-60).
	// Nil builds a default client with no insecure origins.
	Client *apps.Client
	// DataKey seals the signing secrets (REQ-APP-14, REQ-APP-30). Nil
	// makes every apps endpoint answer 503.
	DataKey []byte
}

// appSecretBytes is the CSPRNG length of a signing secret (REQ-APP-30).
const appSecretBytes = 32

// appPrincipalDTO is one entry of an app's explicit principal set.
type appPrincipalDTO struct {
	ID    uint64 `json:"id"`
	Email string `json:"email"`
}

// appDTO is the wire form of an app. Secret is set only in the create
// and rotate-secret responses; Warnings and ManifestChanged only in the
// create and refresh responses.
type appDTO struct {
	ID                    uint64             `json:"id"`
	AppKey                string             `json:"app_key"`
	Origin                string             `json:"origin"`
	State                 string             `json:"state"`
	Name                  apps.LocalizedText `json:"name"`
	AllPrincipals         bool               `json:"all_principals"`
	Principals            []appPrincipalDTO  `json:"principals"`
	Manifest              *apps.Manifest     `json:"manifest"`
	ManifestDigest        string             `json:"manifest_digest"`
	PendingManifest       *apps.Manifest     `json:"pending_manifest"`
	PendingManifestDigest string             `json:"pending_manifest_digest"`
	PendingDiff           *apps.ManifestDiff `json:"pending_diff"`
	CreatedAt             time.Time          `json:"created_at"`
	UpdatedAt             time.Time          `json:"updated_at"`
	LastInvokedAt         *time.Time         `json:"last_invoked_at"`
	Secret                string             `json:"secret,omitempty"`
	Warnings              []string           `json:"warnings,omitempty"`
	ManifestChanged       *bool              `json:"manifest_changed,omitempty"`
}

// appsReady writes 503 and returns false when the data key is absent
// (REQ-APP-14).
func (s *Server) appsReady(w http.ResponseWriter, r *http.Request) bool {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return false
	}
	if len(s.opts.Apps.DataKey) == 0 {
		writeProblem(w, r, http.StatusServiceUnavailable, "apps/data_key_missing",
			"app management requires a data key; set [server.secrets].data_key_ref", "")
		return false
	}
	return true
}

func (s *Server) appsClient() *apps.Client {
	if s.opts.Apps.Client != nil {
		return s.opts.Apps.Client
	}
	return apps.NewClient(apps.ClientOptions{UserAgent: "herold/" + s.opts.ServerVersion})
}

func parseAppID(w http.ResponseWriter, r *http.Request) (store.AppID, bool) {
	raw := r.PathValue("id")
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		writeProblem(w, r, http.StatusBadRequest, "apps/invalid_id",
			"app id must be a positive integer", raw)
		return 0, false
	}
	return store.AppID(n), true
}

func (s *Server) writeAppStoreError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "apps/not_found", "app not found", "")
		return
	}
	s.writeStoreError(w, r, err)
}

// writeManifestError renders a manifest fetch or validation failure
// with the individual problems under "errors" (REQ-APP-20).
func writeManifestError(w http.ResponseWriter, r *http.Request, err error) {
	var me *apps.ManifestError
	if errors.As(err, &me) {
		writeProblemWithExtras(w, r, http.StatusUnprocessableEntity, "apps/invalid_manifest",
			"the app manifest failed validation", "", map[string]any{"errors": me.Problems})
		return
	}
	writeProblemWithExtras(w, r, http.StatusBadGateway, "apps/manifest_fetch_failed",
		"the app manifest could not be fetched", "", map[string]any{"errors": []string{err.Error()}})
}

// toAppDTO renders a with its principal set. A stored manifest that no
// longer decodes is rendered as null.
func (s *Server) toAppDTO(r *http.Request, a store.App) (appDTO, error) {
	d := appDTO{
		ID:                    uint64(a.ID),
		AppKey:                a.AppKey,
		Origin:                a.Origin,
		State:                 string(a.State),
		AllPrincipals:         a.AllPrincipals,
		Principals:            []appPrincipalDTO{},
		ManifestDigest:        a.ManifestDigest,
		PendingManifestDigest: a.PendingManifestDigest,
		CreatedAt:             a.CreatedAt,
		UpdatedAt:             a.UpdatedAt,
	}
	if !a.LastInvokedAt.IsZero() {
		t := a.LastInvokedAt
		d.LastInvokedAt = &t
	}
	d.Manifest, _ = apps.DecodeStoredManifest(a.ManifestJSON)
	d.PendingManifest, _ = apps.DecodeStoredManifest(a.PendingManifestJSON)
	switch {
	case d.Manifest != nil:
		d.Name = d.Manifest.Name
	case d.PendingManifest != nil:
		d.Name = d.PendingManifest.Name
	}
	if d.PendingManifest != nil {
		diff := apps.DiffManifests(d.Manifest, d.PendingManifest)
		d.PendingDiff = &diff
	}
	pids, err := s.store.Meta().ListAppPrincipals(r.Context(), a.ID)
	if err != nil {
		return appDTO{}, err
	}
	for _, pid := range pids {
		entry := appPrincipalDTO{ID: uint64(pid)}
		if p, perr := s.store.Meta().GetPrincipalByID(r.Context(), pid); perr == nil {
			entry.Email = p.CanonicalEmail
		}
		d.Principals = append(d.Principals, entry)
	}
	return d, nil
}

func (s *Server) writeAppDTO(w http.ResponseWriter, r *http.Request, status int, a store.App, decorate func(*appDTO)) {
	d, err := s.toAppDTO(r, a)
	if err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	if decorate != nil {
		decorate(&d)
	}
	writeJSON(w, status, d)
}

// newAppSecret generates a signing secret and returns its presented
// base64url text and the sealed form (REQ-APP-30).
func (s *Server) newAppSecret() (string, []byte, error) {
	var b [appSecretBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, err
	}
	text := base64.RawURLEncoding.EncodeToString(b[:])
	sealed, err := secrets.Seal(s.opts.Apps.DataKey, []byte(text))
	if err != nil {
		return "", nil, err
	}
	return text, sealed, nil
}

func (s *Server) auditApp(r *http.Request, action string, a store.App, metadata map[string]string) {
	s.appendAuditDomain(r.Context(), action, "app:"+a.AppKey, store.OutcomeSuccess, "", metadata, "")
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w, r) {
		return
	}
	rows, err := s.store.Meta().ListApps(r.Context())
	if err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	items := make([]appDTO, 0, len(rows))
	for _, a := range rows {
		d, err := s.toAppDTO(r, a)
		if err != nil {
			s.writeAppStoreError(w, r, err)
			return
		}
		items = append(items, d)
	}
	writeJSON(w, http.StatusOK, pageDTO[appDTO]{Items: items, Next: nil})
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w, r) {
		return
	}
	id, ok := parseAppID(w, r)
	if !ok {
		return
	}
	a, err := s.store.Meta().GetApp(r.Context(), id)
	if err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	s.writeAppDTO(w, r, http.StatusOK, a, nil)
}

type createAppRequest struct {
	Origin string `json:"origin"`
}

func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w, r) {
		return
	}
	var req createAppRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	client := s.appsClient()
	origin, err := client.CheckOrigin(req.Origin)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "apps/invalid_origin", err.Error(), req.Origin)
		return
	}
	fm, err := client.FetchManifest(r.Context(), origin)
	if err != nil {
		writeManifestError(w, r, err)
		return
	}
	text, sealed, err := s.newAppSecret()
	if err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.apps.secret_failed",
			"activity", observe.ActivityInternal, "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to create the signing secret", "")
		return
	}
	a, err := s.store.Meta().InsertApp(r.Context(), store.App{
		AppKey:                fm.Manifest.AppKey,
		Origin:                origin,
		PendingManifestJSON:   fm.Canonical,
		PendingManifestDigest: fm.Digest,
		SecretSealed:          sealed,
		State:                 store.AppStatePendingApproval,
	})
	if errors.Is(err, store.ErrConflict) {
		writeProblem(w, r, http.StatusConflict, "apps/conflict",
			"an app with this app_key is already registered", fm.Manifest.AppKey)
		return
	}
	if err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	s.auditApp(r, "app.create", a, map[string]string{"origin": origin, "manifest_digest": fm.Digest})
	w.Header().Set("Location", fmt.Sprintf("/api/v1/apps/%d", a.ID))
	s.writeAppDTO(w, r, http.StatusCreated, a, func(d *appDTO) {
		d.Secret = text
		d.Warnings = append([]string{}, fm.Warnings...)
	})
}

type patchAppRequest struct {
	AllPrincipals *bool              `json:"all_principals,omitempty"`
	Principals    *[]json.RawMessage `json:"principals,omitempty"`
}

// resolvePrincipalRefs maps each reference (a numeric principal id or
// an email address string) to a principal id.
func (s *Server) resolvePrincipalRefs(r *http.Request, refs []json.RawMessage) ([]store.PrincipalID, error) {
	out := make([]store.PrincipalID, 0, len(refs))
	for _, raw := range refs {
		raw = bytes.TrimSpace(raw)
		var n uint64
		if err := json.Unmarshal(raw, &n); err == nil {
			if n == 0 {
				return nil, errors.New("principal id must be positive")
			}
			if _, err := s.store.Meta().GetPrincipalByID(r.Context(), store.PrincipalID(n)); err != nil {
				return nil, fmt.Errorf("principal %d: %w", n, err)
			}
			out = append(out, store.PrincipalID(n))
			continue
		}
		var email string
		if err := json.Unmarshal(raw, &email); err != nil || strings.TrimSpace(email) == "" {
			return nil, fmt.Errorf("principal reference %s is neither an id nor an email address", raw)
		}
		p, err := s.store.Meta().GetPrincipalByEmail(r.Context(), strings.TrimSpace(email))
		if err != nil {
			return nil, fmt.Errorf("principal %q: %w", email, err)
		}
		out = append(out, p.ID)
	}
	return out, nil
}

func (s *Server) handlePatchApp(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w, r) {
		return
	}
	id, ok := parseAppID(w, r)
	if !ok {
		return
	}
	var req patchAppRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	a, err := s.store.Meta().GetApp(r.Context(), id)
	if err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	meta := map[string]string{}
	if req.Principals != nil {
		pids, err := s.resolvePrincipalRefs(r, *req.Principals)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "apps/invalid_principal", err.Error(), "")
			return
		}
		if err := s.store.Meta().SetAppPrincipals(r.Context(), id, pids); err != nil {
			s.writeAppStoreError(w, r, err)
			return
		}
		meta["principals"] = strconv.Itoa(len(pids))
	}
	if req.AllPrincipals != nil && *req.AllPrincipals != a.AllPrincipals {
		a.AllPrincipals = *req.AllPrincipals
		if a, err = s.store.Meta().UpdateApp(r.Context(), a); err != nil {
			s.writeAppStoreError(w, r, err)
			return
		}
		meta["all_principals"] = strconv.FormatBool(a.AllPrincipals)
	}
	if len(meta) > 0 {
		s.auditApp(r, "app.update", a, meta)
	}
	if a, err = s.store.Meta().GetApp(r.Context(), id); err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	s.writeAppDTO(w, r, http.StatusOK, a, nil)
}

type approveAppRequest struct {
	ManifestDigest string `json:"manifest_digest"`
}

func (s *Server) handleApproveApp(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w, r) {
		return
	}
	id, ok := parseAppID(w, r)
	if !ok {
		return
	}
	var req approveAppRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.ManifestDigest == "" {
		writeProblem(w, r, http.StatusBadRequest, "apps/validation_failed", "manifest_digest is required", "")
		return
	}
	a, err := s.store.Meta().ApproveAppManifest(r.Context(), id, req.ManifestDigest)
	if errors.Is(err, store.ErrConflict) {
		// REQ-APP-11: the reviewed digest no longer matches what is
		// pending; the administrator must review again.
		writeProblem(w, r, http.StatusConflict, "apps/manifest_changed",
			"the pending manifest differs from the reviewed digest", req.ManifestDigest)
		return
	}
	if err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	s.auditApp(r, "app.approve", a, map[string]string{"manifest_digest": a.ManifestDigest})
	s.writeAppDTO(w, r, http.StatusOK, a, nil)
}

func (s *Server) handleRefreshApp(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w, r) {
		return
	}
	id, ok := parseAppID(w, r)
	if !ok {
		return
	}
	a, err := s.store.Meta().GetApp(r.Context(), id)
	if err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	fm, err := s.appsClient().FetchManifest(r.Context(), a.Origin)
	if err != nil {
		writeManifestError(w, r, err)
		return
	}
	if fm.Manifest.AppKey != a.AppKey {
		writeManifestError(w, r, &apps.ManifestError{Problems: []string{
			fmt.Sprintf("app_key: %q differs from the registered %q", fm.Manifest.AppKey, a.AppKey)}})
		return
	}
	changed := false
	switch {
	case fm.Digest == a.PendingManifestDigest:
		// Already pending: nothing to store (REQ-APP-12).
	case fm.Digest == a.ManifestDigest:
		// The app serves its approved manifest again; a stale pending
		// update no longer applies.
		if a.PendingManifestDigest != "" {
			a.PendingManifestJSON, a.PendingManifestDigest = nil, ""
			changed = true
		}
	default:
		a.PendingManifestJSON, a.PendingManifestDigest = fm.Canonical, fm.Digest
		changed = true
	}
	if changed {
		if a, err = s.store.Meta().UpdateApp(r.Context(), a); err != nil {
			s.writeAppStoreError(w, r, err)
			return
		}
		s.auditApp(r, "app.refresh", a, map[string]string{"pending_manifest_digest": a.PendingManifestDigest})
	}
	s.writeAppDTO(w, r, http.StatusOK, a, func(d *appDTO) {
		d.Warnings = append([]string{}, fm.Warnings...)
		d.ManifestChanged = &changed
	})
}

func (s *Server) handleRotateAppSecret(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w, r) {
		return
	}
	id, ok := parseAppID(w, r)
	if !ok {
		return
	}
	a, err := s.store.Meta().GetApp(r.Context(), id)
	if err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	text, sealed, err := s.newAppSecret()
	if err != nil {
		s.loggerFrom(r.Context()).Error("protoadmin.apps.secret_failed",
			"activity", observe.ActivityInternal, "err", err)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to create the signing secret", "")
		return
	}
	a.SecretSealed = sealed
	if a, err = s.store.Meta().UpdateApp(r.Context(), a); err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	s.auditApp(r, "app.rotate_secret", a, nil)
	s.writeAppDTO(w, r, http.StatusOK, a, func(d *appDTO) { d.Secret = text })
}

// setAppState moves an app between active and disabled (REQ-APP-11).
func (s *Server) setAppState(w http.ResponseWriter, r *http.Request, from, to store.AppState, action string) {
	if !s.appsReady(w, r) {
		return
	}
	id, ok := parseAppID(w, r)
	if !ok {
		return
	}
	a, err := s.store.Meta().GetApp(r.Context(), id)
	if err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	if a.State != to {
		if a.State != from {
			writeProblem(w, r, http.StatusConflict, "apps/invalid_state",
				fmt.Sprintf("app is %s; only a %s app can be moved to %s", a.State, from, to), "")
			return
		}
		a.State = to
		if a, err = s.store.Meta().UpdateApp(r.Context(), a); err != nil {
			s.writeAppStoreError(w, r, err)
			return
		}
		s.auditApp(r, action, a, nil)
	}
	s.writeAppDTO(w, r, http.StatusOK, a, nil)
}

func (s *Server) handleDisableApp(w http.ResponseWriter, r *http.Request) {
	s.setAppState(w, r, store.AppStateActive, store.AppStateDisabled, "app.disable")
}

func (s *Server) handleEnableApp(w http.ResponseWriter, r *http.Request) {
	s.setAppState(w, r, store.AppStateDisabled, store.AppStateActive, "app.enable")
}

func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w, r) {
		return
	}
	id, ok := parseAppID(w, r)
	if !ok {
		return
	}
	a, err := s.store.Meta().GetApp(r.Context(), id)
	if err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	if err := s.store.Meta().DeleteApp(r.Context(), id); err != nil {
		s.writeAppStoreError(w, r, err)
		return
	}
	s.auditApp(r, "app.delete", a, nil)
	w.WriteHeader(http.StatusNoContent)
}
