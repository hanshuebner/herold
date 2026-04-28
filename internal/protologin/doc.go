// Package protologin provides a reusable JSON login/logout handler pair
// for any HTTP listener in the Herold server.
//
// The admin REST surface (internal/protoadmin) has its own login handler in
// session_auth.go that wires directly to the protoadmin Server struct. This
// package lifts the same handler shape into a standalone, configurable Server
// so the public listener can expose POST /api/v1/auth/login and
// POST /api/v1/auth/logout without depending on internal/protoadmin.
//
// Phase 3c-i adds the public-listener mount using this package. The admin
// listener continues using internal/protoadmin/session_auth.go unchanged.
// Phase 3c-iii may collapse the duplication if desired.
//
// Both handlers are listener-scoped: the Options.Session field carries the
// cookie name and signing key specific to the listener, and
// Options.Scopes computes the scope set to embed in the issued cookie.
package protologin
