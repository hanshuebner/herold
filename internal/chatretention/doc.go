// Package chatretention implements the chat retention sweeper
// (REQ-CHAT-92). One goroutine periodically scans
// chat_conversations whose retention_seconds is positive plus
// chat_account_settings whose default_retention_seconds is positive,
// and hard-deletes chat_messages older than the resolved window.
//
// Distinct from REQ-CHAT-21 soft-delete (the user-driven path that
// keeps the row for read-receipt anchor stability): retention
// hard-deletes the row entirely and decrements blob refcounts for
// any attachments. Only non-system messages are eligible — system
// messages (joins/leaves, call.started, etc.) are retained per
// REQ-CHAT-92 so the conversation history remains intelligible.
//
// Lifecycle: construct with NewWorker; call Run(ctx) in a single
// goroutine owned by the server lifecycle (admin/server.go's
// errgroup); cancel ctx to drain and return.
package chatretention
