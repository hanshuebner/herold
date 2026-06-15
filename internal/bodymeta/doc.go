// Package bodymeta provides the background sweep worker that fills in the
// precomputed preview and hasAttachment fields for messages whose
// body_meta_computed flag has not yet been set. The worker drains the
// ListMessagesNeedingBodyMeta backlog newest-first so that inbox messages
// (the user's active concern) are covered before archive.
//
// The worker mirrors the structure of internal/extimg/internalizeworker: a
// context-cancellable Run loop, small batches, idle sleep, slog output, and
// wiring into the admin/server.go lifecycle errgroup.
package bodymeta
