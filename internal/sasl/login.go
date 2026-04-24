package sasl

import (
	"context"
	"fmt"
)

// NewLOGIN constructs a SASL LOGIN mechanism. LOGIN is not in any RFC
// but is widely deployed: the server prompts "Username:" (base64), the
// client returns the username, the server prompts "Password:", and the
// client returns the password. Plain-text: TLS required.
func NewLOGIN(auth Authenticator) Mechanism { return &loginMech{auth: auth} }

type loginMech struct {
	auth     Authenticator
	state    int // 0=not started, 1=awaiting username, 2=awaiting password, 3=done
	username string
	pid      PrincipalID
}

func (m *loginMech) Name() string { return "LOGIN" }

const (
	loginPromptUser = "Username:"
	loginPromptPass = "Password:"
)

func (m *loginMech) Start(ctx context.Context, ir []byte) ([]byte, bool, error) {
	if m.state != 0 {
		return nil, false, ErrProtocolError
	}
	if !tlsPresent(ctx) {
		return nil, false, ErrTLSRequired
	}
	if len(ir) == 0 {
		m.state = 1
		return []byte(loginPromptUser), false, nil
	}
	// Some clients send the username as IR.
	m.username = string(ir)
	m.state = 2
	return []byte(loginPromptPass), false, nil
}

func (m *loginMech) Next(ctx context.Context, resp []byte) ([]byte, bool, error) {
	switch m.state {
	case 1:
		m.username = string(resp)
		m.state = 2
		return []byte(loginPromptPass), false, nil
	case 2:
		pid, err := m.auth.Authenticate(ctx, m.username, string(resp))
		if err != nil {
			return nil, false, mapAuthErr(err)
		}
		m.pid = pid
		m.state = 3
		return nil, true, nil
	default:
		return nil, false, fmt.Errorf("LOGIN state %d: %w", m.state, ErrProtocolError)
	}
}

func (m *loginMech) Principal() (PrincipalID, error) {
	if m.state != 3 {
		return 0, ErrAuthFailed
	}
	return m.pid, nil
}
