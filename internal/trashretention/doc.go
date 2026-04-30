// Package trashretention implements the periodic Trash-mailbox sweep that
// hard-deletes email messages whose InternalDate is older than the operator-
// configured retention window (REQ-STORE-90). The worker follows the same
// pattern as internal/chatretention: a Run(ctx) loop that calls Tick(ctx) on
// each sweep interval, pages through all principals, locates each one's Trash
// mailbox (MailboxAttrTrash), and calls ExpungeMessages on batches of aged-out
// rows. Default retention is 30 days; the sweep interval defaults to 1 hour.
package trashretention
