package admin

// ses_adapters.go provides the glue between the admin server's subsystems
// and the sesinbound package (Phase 3 Wave 3.2, REQ-HOOK-SES-01..07).

import (
	"context"
	"fmt"
	"time"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sesinbound"
	"github.com/hanshuebner/herold/internal/store"
)

// sesPipelineAdapter implements sesinbound.Pipeline using
// protosmtp.Server.IngestBytes.  It resolves envelope recipients to local
// principal IDs before passing the request to the pipeline.
type sesPipelineAdapter struct {
	smtp *protosmtp.Server
	meta store.Metadata
}

// Ingest implements sesinbound.Pipeline.
func (a *sesPipelineAdapter) Ingest(ctx context.Context, req sesinbound.IngestMsg) error {
	var rcpts []protosmtp.IngestRecipient
	for _, addr := range req.EnvelopeTo {
		localPart, domain := splitEmailAddr(addr)
		if localPart == "" {
			continue
		}
		pid, err := a.meta.ResolveAlias(ctx, localPart, domain)
		if err == nil {
			rcpts = append(rcpts, protosmtp.IngestRecipient{
				Addr:        addr,
				PrincipalID: pid,
			})
			continue
		}
		p, err := a.meta.GetPrincipalByEmail(ctx, addr)
		if err == nil {
			rcpts = append(rcpts, protosmtp.IngestRecipient{
				Addr:        addr,
				PrincipalID: p.ID,
			})
		}
		// Non-local addresses are silently skipped; SES delivers only
		// messages for domains this herold instance owns.
	}
	if len(rcpts) == 0 {
		return fmt.Errorf("ses_inbound: no local recipients found in %v", req.EnvelopeTo)
	}
	return a.smtp.IngestBytes(ctx, protosmtp.IngestRequest{
		Body:         req.Body,
		MailFrom:     req.MailFrom,
		SourceIP:     req.SourceIP,
		Recipients:   rcpts,
		IngestSource: "ses_inbound",
	})
}

var _ sesinbound.Pipeline = (*sesPipelineAdapter)(nil)

// sesSeenStore adapts store.Metadata to sesinbound.SeenStore.
type sesSeenStore struct {
	meta store.Metadata
}

func (s *sesSeenStore) IsSESSeen(ctx context.Context, messageID string) (bool, error) {
	return s.meta.IsSESSeen(ctx, messageID)
}

func (s *sesSeenStore) InsertSESSeen(ctx context.Context, messageID string, seenAt time.Time) error {
	return s.meta.InsertSESSeen(ctx, messageID, seenAt)
}

func (s *sesSeenStore) GCOldSESSeen(ctx context.Context, cutoff time.Time) error {
	return s.meta.GCOldSESSeen(ctx, cutoff)
}

var _ sesinbound.SeenStore = (*sesSeenStore)(nil)

// splitEmailAddr splits "local@domain" into (localPart, domain).
// Returns ("", "") on a malformed address.
func splitEmailAddr(addr string) (localPart, domain string) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == '@' {
			return addr[:i], addr[i+1:]
		}
	}
	return "", ""
}
