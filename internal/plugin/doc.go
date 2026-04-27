// Package plugin hosts the plugin supervisor, the JSON-RPC 2.0 stdio client,
// manifest validation, and lifecycle management.
//
// The supervisor spawns each declared plugin as a child process and speaks
// newline-delimited JSON-RPC 2.0 on stdin/stdout, per
// docs/design/server/architecture/07-plugin-architecture.md. Long-running plugins stay
// resident; on-demand plugins run per-invocation. Plugin stderr is piped into
// the server's slog logger with the plugin name attached as a field.
//
// Public entry points:
//
//   - Manager   — supervises N plugins per server; SIGHUP-reloadable.
//   - Plugin    — handle for one plugin; exposes Call and Notify.
//   - Request, Response, Error — JSON-RPC 2.0 wire types.
//   - Manifest  — plugin self-description returned by initialize.
//
// Ownership: plugin-platform-implementor.
package plugin
