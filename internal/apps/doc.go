// Package apps implements the server side of registered apps
// (docs/design/server/requirements/29-apps.md): the v1 manifest parser
// and validator, the guarded HTTP client that reaches an app's origin,
// manifest fetches, the signed invocation request and the validation of
// the app's response. Persistence lives in internal/store, the admin
// REST surface in internal/protoadmin and the JMAP method in
// internal/protojmap/apps; this package holds the wire contract they
// share.
package apps
