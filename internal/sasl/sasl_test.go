package sasl_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/sasl"
)

// stubAuth implements sasl.Authenticator over a fixed map.
type stubAuth struct {
	users map[string]string
	pid   sasl.PrincipalID
}

func (s *stubAuth) Authenticate(ctx context.Context, email, password string) (sasl.PrincipalID, error) {
	if p, ok := s.users[email]; ok && p == password {
		return s.pid, nil
	}
	return 0, errors.New("bad creds")
}

type stubTokenVerifier struct {
	valid map[string]sasl.PrincipalID
}

func (s *stubTokenVerifier) VerifyAccessToken(ctx context.Context, token string) (sasl.PrincipalID, error) {
	if p, ok := s.valid[token]; ok {
		return p, nil
	}
	return 0, errors.New("bad token")
}

func TestPLAINRequiresTLS(t *testing.T) {
	m := sasl.NewPLAIN(&stubAuth{users: map[string]string{"a@b": "pw"}, pid: 42})
	_, _, err := m.Start(context.Background(), []byte("\x00a@b\x00pw"))
	if !errors.Is(err, sasl.ErrTLSRequired) {
		t.Fatalf("want ErrTLSRequired, got %v", err)
	}
}

func TestPLAINHappyPath(t *testing.T) {
	ctx := sasl.WithTLS(context.Background(), true)
	m := sasl.NewPLAIN(&stubAuth{users: map[string]string{"a@b": "pw"}, pid: 42})
	chal, done, err := m.Start(ctx, []byte("\x00a@b\x00pw"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !done {
		t.Fatalf("expected done")
	}
	if len(chal) != 0 {
		t.Fatalf("expected empty final challenge, got %q", chal)
	}
	pid, err := m.Principal()
	if err != nil || pid != 42 {
		t.Fatalf("principal: %v %d", err, pid)
	}
}

func TestPLAINBadCreds(t *testing.T) {
	ctx := sasl.WithTLS(context.Background(), true)
	m := sasl.NewPLAIN(&stubAuth{users: map[string]string{"a@b": "pw"}, pid: 42})
	_, _, err := m.Start(ctx, []byte("\x00a@b\x00wrong"))
	if !errors.Is(err, sasl.ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

func TestLOGINRoundTrip(t *testing.T) {
	ctx := sasl.WithTLS(context.Background(), true)
	m := sasl.NewLOGIN(&stubAuth{users: map[string]string{"a@b": "pw"}, pid: 7})
	chal, done, err := m.Start(ctx, nil)
	if err != nil || done || string(chal) != "Username:" {
		t.Fatalf("start: %v done=%v chal=%q", err, done, chal)
	}
	chal, done, err = m.Next(ctx, []byte("a@b"))
	if err != nil || done || string(chal) != "Password:" {
		t.Fatalf("user: %v done=%v chal=%q", err, done, chal)
	}
	_, done, err = m.Next(ctx, []byte("pw"))
	if err != nil || !done {
		t.Fatalf("pass: %v done=%v", err, done)
	}
	pid, _ := m.Principal()
	if pid != 7 {
		t.Fatalf("pid: %d", pid)
	}
}

func TestOAUTHBEARERGood(t *testing.T) {
	ctx := sasl.WithTLS(context.Background(), true)
	tv := &stubTokenVerifier{valid: map[string]sasl.PrincipalID{"tok": 99}}
	m := sasl.NewOAUTHBEARER(tv)
	// Client message: n,a=user@example.com,\x01auth=Bearer tok\x01\x01
	msg := []byte("n,a=user@example.com,\x01auth=Bearer tok\x01\x01")
	_, done, err := m.Start(ctx, msg)
	if err != nil || !done {
		t.Fatalf("start: %v done=%v", err, done)
	}
	pid, _ := m.Principal()
	if pid != 99 {
		t.Fatalf("pid: %d", pid)
	}
}

func TestOAUTHBEARERBadToken(t *testing.T) {
	ctx := sasl.WithTLS(context.Background(), true)
	tv := &stubTokenVerifier{valid: map[string]sasl.PrincipalID{"tok": 99}}
	m := sasl.NewOAUTHBEARER(tv)
	msg := []byte("n,a=user@example.com,\x01auth=Bearer nope\x01\x01")
	chal, done, err := m.Start(ctx, msg)
	if err != nil || done {
		t.Fatalf("start: %v done=%v", err, done)
	}
	if len(chal) == 0 {
		t.Fatalf("expected failure JSON challenge")
	}
	_, done, err = m.Next(ctx, []byte{0x01})
	if !errors.Is(err, sasl.ErrAuthFailed) || !done {
		t.Fatalf("ack: %v done=%v", err, done)
	}
}

func TestXOAUTH2Good(t *testing.T) {
	ctx := sasl.WithTLS(context.Background(), true)
	tv := &stubTokenVerifier{valid: map[string]sasl.PrincipalID{"tok": 7}}
	m := sasl.NewXOAUTH2(tv)
	msg := []byte("user=user@example.com\x01auth=Bearer tok\x01\x01")
	_, done, err := m.Start(ctx, msg)
	if err != nil || !done {
		t.Fatalf("start: %v done=%v", err, done)
	}
	pid, _ := m.Principal()
	if pid != 7 {
		t.Fatalf("pid: %d", pid)
	}
}

func TestOAUTHBEARERRequiresTLS(t *testing.T) {
	tv := &stubTokenVerifier{valid: map[string]sasl.PrincipalID{"tok": 7}}
	m := sasl.NewOAUTHBEARER(tv)
	_, _, err := m.Start(context.Background(), []byte("n,,\x01auth=Bearer tok\x01\x01"))
	if !errors.Is(err, sasl.ErrTLSRequired) {
		t.Fatalf("want ErrTLSRequired, got %v", err)
	}
}
