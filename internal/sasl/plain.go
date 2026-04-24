package sasl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// NewPLAIN constructs a SASL PLAIN mechanism (RFC 4616) backed by
// auth. Plain-text: refuses to run without TLS.
func NewPLAIN(auth Authenticator) Mechanism { return &plainMech{auth: auth} }

type plainMech struct {
	auth    Authenticator
	started bool
	done    bool
	pid     PrincipalID
}

func (m *plainMech) Name() string { return "PLAIN" }

func (m *plainMech) Start(ctx context.Context, ir []byte) ([]byte, bool, error) {
	if m.started {
		return nil, false, ErrProtocolError
	}
	m.started = true
	if !tlsPresent(ctx) {
		return nil, false, ErrTLSRequired
	}
	if len(ir) == 0 {
		// Client did not supply IR; server responds with an empty
		// challenge and waits.
		return []byte{}, false, nil
	}
	return m.consume(ctx, ir)
}

func (m *plainMech) Next(ctx context.Context, resp []byte) ([]byte, bool, error) {
	if !m.started || m.done {
		return nil, false, ErrProtocolError
	}
	return m.consume(ctx, resp)
}

func (m *plainMech) Principal() (PrincipalID, error) {
	if !m.done {
		return 0, ErrAuthFailed
	}
	return m.pid, nil
}

// RFC 4616 message: authzid \0 authcid \0 passwd. authzid is typically
// empty for end-user auth; when present and non-empty we require it to
// equal authcid (we do not support SASL proxying in Wave 1).
func (m *plainMech) consume(ctx context.Context, msg []byte) ([]byte, bool, error) {
	parts := bytes.SplitN(msg, []byte{0}, 3)
	if len(parts) != 3 {
		return nil, false, fmt.Errorf("PLAIN: %w", ErrInvalidMessage)
	}
	authzid, authcid, passwd := string(parts[0]), string(parts[1]), string(parts[2])
	if authzid != "" && authzid != authcid {
		return nil, false, fmt.Errorf("PLAIN proxy auth rejected: %w", ErrAuthFailed)
	}
	pid, err := m.auth.Authenticate(ctx, authcid, passwd)
	if err != nil {
		return nil, false, mapAuthErr(err)
	}
	m.pid = pid
	m.done = true
	return nil, true, nil
}

// mapAuthErr folds directory errors into the SASL error vocabulary.
// We do not leak whether the username is unknown vs the password is
// wrong; all credential failures surface as ErrAuthFailed.
func mapAuthErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrAuthFailed
}
