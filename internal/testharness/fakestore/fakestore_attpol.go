package fakestore

import (
	"context"
	"errors"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// -- Phase 3 Wave 3.5c inbound attachment policy (REQ-FLOW-ATTPOL-01) --

// GetInboundAttachmentPolicy resolves the per-recipient / per-domain
// policy with explicit > inherited > default precedence.
func (m *metaFace) GetInboundAttachmentPolicy(
	ctx context.Context,
	address string,
) (store.InboundAttachmentPolicyRow, error) {
	if err := ctx.Err(); err != nil {
		return store.InboundAttachmentPolicyRow{}, err
	}
	address = strings.TrimSpace(strings.ToLower(address))
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return store.InboundAttachmentPolicyRow{Policy: store.AttPolicyAccept}, nil
	}
	domain := address[at+1:]
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if row, ok := s.attpolRecipient[address]; ok {
		if row.Policy == store.AttPolicyUnset {
			row.Policy = store.AttPolicyAccept
		}
		return row, nil
	}
	if row, ok := s.attpolDomain[domain]; ok {
		if row.Policy == store.AttPolicyUnset {
			row.Policy = store.AttPolicyAccept
		}
		return row, nil
	}
	return store.InboundAttachmentPolicyRow{Policy: store.AttPolicyAccept}, nil
}

// SetInboundAttachmentPolicyRecipient upserts (or deletes on
// AttPolicyUnset) the per-recipient row.
func (m *metaFace) SetInboundAttachmentPolicyRecipient(
	ctx context.Context,
	address string,
	row store.InboundAttachmentPolicyRow,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	address = strings.TrimSpace(strings.ToLower(address))
	if address == "" {
		return errors.New("fakestore: empty address")
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if row.Policy == store.AttPolicyUnset {
		delete(s.attpolRecipient, address)
		return nil
	}
	s.attpolRecipient[address] = row
	return nil
}

// SetInboundAttachmentPolicyDomain upserts (or deletes on
// AttPolicyUnset) the per-domain row.
func (m *metaFace) SetInboundAttachmentPolicyDomain(
	ctx context.Context,
	domain string,
	row store.InboundAttachmentPolicyRow,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	domain = strings.TrimSpace(strings.ToLower(domain))
	if domain == "" {
		return errors.New("fakestore: empty domain")
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if row.Policy == store.AttPolicyUnset {
		delete(s.attpolDomain, domain)
		return nil
	}
	s.attpolDomain[domain] = row
	return nil
}
