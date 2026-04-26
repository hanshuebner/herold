package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protocall"
	"github.com/hanshuebner/herold/internal/protochat"
	"github.com/hanshuebner/herold/internal/store"
)

// callBroadcasterAdapter bridges protocall.Broadcaster (decoupled
// envelope shape) to protochat.Broadcaster.Emit (the in-process
// chat-WebSocket fanout). protocall does not import protochat
// directly so we pivot through a JSON-encoded payload here.
type callBroadcasterAdapter struct {
	bc *protochat.Broadcaster
}

func newCallBroadcasterAdapter(bc *protochat.Broadcaster) *callBroadcasterAdapter {
	return &callBroadcasterAdapter{bc: bc}
}

// Emit serialises env.Payload to JSON and forwards it as a chat
// ServerFrame. A nil broadcaster (chat disabled) collapses to a no-op
// so test deployments without /chat/ws still serve the credential
// mint endpoint.
func (a *callBroadcasterAdapter) Emit(_ context.Context, to store.PrincipalID, env protocall.ServerEnvelope) error {
	if a == nil || a.bc == nil {
		return errors.New("admin: chat broadcaster unavailable")
	}
	body, err := json.Marshal(env.Payload)
	if err != nil {
		return err
	}
	a.bc.Emit(to, protochat.ServerFrame{
		Type:    env.Type,
		Payload: body,
	})
	return nil
}

// callPresenceAdapter implements protocall.PresenceLookup atop the
// chat broadcaster's connection registry. A principal is "online" if
// the broadcaster has at least one live WebSocket session for them.
type callPresenceAdapter struct {
	bc *protochat.Broadcaster
}

func newCallPresenceAdapter(bc *protochat.Broadcaster) *callPresenceAdapter {
	return &callPresenceAdapter{bc: bc}
}

func (a *callPresenceAdapter) IsOnline(p store.PrincipalID) bool {
	if a == nil || a.bc == nil {
		return false
	}
	return a.bc.HasConnection(p)
}

// callMembersAdapter resolves conversation members against the chat
// store. The conversation id arrives on the wire as a decimal uint64
// string (matches protojmap/chat's encoding); a non-numeric or zero
// id is treated as not-found so signaling cannot be probed for ids
// that aren't real.
type callMembersAdapter struct {
	st store.Store
}

func newCallMembersAdapter(st store.Store) *callMembersAdapter {
	return &callMembersAdapter{st: st}
}

func (a *callMembersAdapter) ConversationMembers(ctx context.Context, conv string) ([]store.PrincipalID, error) {
	cid, ok := parseConversationID(conv)
	if !ok {
		return nil, store.ErrNotFound
	}
	rows, err := a.st.Meta().ListChatMembershipsByConversation(ctx, cid)
	if err != nil {
		return nil, err
	}
	out := make([]store.PrincipalID, 0, len(rows))
	for _, m := range rows {
		out = append(out, m.PrincipalID)
	}
	return out, nil
}

// callSysmsgsAdapter writes call.started / call.ended audit rows via
// store.Metadata.InsertChatMessage with kind=system. The "sender" is
// the principal protocall reports (caller for call.started, hangup
// initiator for call.ended); the body is the JSON metadata payload.
type callSysmsgsAdapter struct {
	st store.Store
}

func newCallSysmsgsAdapter(st store.Store) *callSysmsgsAdapter {
	return &callSysmsgsAdapter{st: st}
}

func (a *callSysmsgsAdapter) InsertChatSystemMessage(ctx context.Context, conv string, sender store.PrincipalID, payload []byte) error {
	cid, ok := parseConversationID(conv)
	if !ok {
		return store.ErrNotFound
	}
	msg := store.ChatMessage{
		ConversationID: cid,
		IsSystem:       true,
		BodyFormat:     "text",
		MetadataJSON:   payload,
	}
	if sender != 0 {
		s := sender
		msg.SenderPrincipalID = &s
	}
	_, err := a.st.Meta().InsertChatMessage(ctx, msg)
	return err
}

// callChatMembershipResolver bridges protochat's MembershipResolver
// to store.GetChatMembership. A non-numeric or zero conv id is
// reported as "not a member" rather than an error so the dispatcher
// stays quiet for malformed input.
func callChatMembershipResolver(st store.Store) protochat.MembershipResolver {
	return func(ctx context.Context, conv string, pid store.PrincipalID) (bool, error) {
		cid, ok := parseConversationID(conv)
		if !ok {
			return false, nil
		}
		_, err := st.Meta().GetChatMembership(ctx, cid, pid)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil
	}
}

// callChatMembersResolver bridges protochat's MembersResolver to
// store.ListChatMembershipsByConversation. Used for fanout (typing,
// presence, read).
func callChatMembersResolver(st store.Store) protochat.MembersResolver {
	return func(ctx context.Context, conv string) ([]store.PrincipalID, error) {
		cid, ok := parseConversationID(conv)
		if !ok {
			return nil, nil
		}
		rows, err := st.Meta().ListChatMembershipsByConversation(ctx, cid)
		if err != nil {
			return nil, err
		}
		out := make([]store.PrincipalID, 0, len(rows))
		for _, m := range rows {
			out = append(out, m.PrincipalID)
		}
		return out, nil
	}
}

// callChatPeersResolver bridges protochat's PeersResolver to the
// chat-store path: list every conversation the publisher belongs to,
// then for each conversation gather its members. The publisher
// themself is excluded from the result; principals who share more
// than one conversation with the publisher are deduped. An empty
// publisher membership set returns an empty (non-nil) slice so the
// caller can distinguish "no peers" from a transient error.
//
// Wired into protochat.Options.PeersResolver by composeAdminAndUI so
// presence broadcast is no longer a silent no-op in production.
func callChatPeersResolver(st store.Store) protochat.PeersResolver {
	return func(ctx context.Context, publisher store.PrincipalID) ([]store.PrincipalID, error) {
		mine, err := st.Meta().ListChatMembershipsByPrincipal(ctx, publisher)
		if err != nil {
			return nil, fmt.Errorf("admin: list publisher memberships: %w", err)
		}
		if len(mine) == 0 {
			return []store.PrincipalID{}, nil
		}
		seen := make(map[store.PrincipalID]struct{})
		for _, mb := range mine {
			rows, err := st.Meta().ListChatMembershipsByConversation(ctx, mb.ConversationID)
			if err != nil {
				return nil, fmt.Errorf("admin: list conversation members: %w", err)
			}
			for _, peer := range rows {
				if peer.PrincipalID == publisher {
					continue
				}
				seen[peer.PrincipalID] = struct{}{}
			}
		}
		out := make([]store.PrincipalID, 0, len(seen))
		for pid := range seen {
			out = append(out, pid)
		}
		return out, nil
	}
}

// parseConversationID parses the wire-form conversation id. The chat
// JMAP datatype encodes ConversationID as a decimal uint64 string;
// the WebSocket frames carry the same shape.
func parseConversationID(s string) (store.ConversationID, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.ConversationID(v), true
}

// callSignalForwarder wraps protocall.Server.HandleSignal in a
// protochat.FrameHandler. The protocall package's local ClientFrame
// has the same shape as protochat.ClientFrame's (Type + Payload), so
// the bridge is a no-op rename.
func callSignalForwarder(call *protocall.Server) protochat.FrameHandler {
	return func(ctx context.Context, from store.PrincipalID, frame protochat.ClientFrame) {
		call.HandleSignal(ctx, from, protocall.ClientFrame{
			Type:    frame.Type,
			Payload: frame.Payload,
		})
	}
}

// newCallAuthn returns the resolver protocall's HTTP handler uses to
// authenticate credential-mint callers. Two paths are accepted:
//
//   - Suite session cookie (the same one /ui issues); typical browser
//     client.
//   - protoadmin Bearer API key (hk_<...>); for non-browser callers
//     that can't carry the cookie.
//
// Disabled principals are rejected through both paths.
func newCallAuthn(st store.Store, sessionResolver func(*http.Request) (store.PrincipalID, bool)) func(*http.Request) (store.PrincipalID, bool) {
	return func(r *http.Request) (store.PrincipalID, bool) {
		if sessionResolver != nil {
			if pid, ok := sessionResolver(r); ok {
				return pid, true
			}
		}
		// Fall back to Bearer API key.
		h := r.Header.Get("Authorization")
		const bearer = "Bearer "
		if !strings.HasPrefix(h, bearer) {
			return 0, false
		}
		token := strings.TrimSpace(h[len(bearer):])
		if !strings.HasPrefix(token, protoadmin.APIKeyPrefix) {
			return 0, false
		}
		hashed := protoadmin.HashAPIKey(token)
		key, err := st.Meta().GetAPIKeyByHash(r.Context(), hashed)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(key.Hash), []byte(hashed)) != 1 {
			return 0, false
		}
		p, err := st.Meta().GetPrincipalByID(r.Context(), key.PrincipalID)
		if err != nil {
			return 0, false
		}
		if p.Flags.Has(store.PrincipalFlagDisabled) {
			return 0, false
		}
		return p.ID, true
	}
}
