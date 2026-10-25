package admin

// cmd_principal_password.go implements the two password verbs of the
// principal command group that need more than a single REST call:
//
//   - set-password on the caller's own principal, which supplies the
//     current password and answers the server's step_up_required with a
//     TOTP step-up (REQ-AUTH-78, re #540);
//   - reset-password --offline, the host-access recovery path that writes
//     a password hash straight into the store (re #540).
//
// Passwords and TOTP codes are only ever held in local variables and sent
// in request bodies; no error or log line carries them.

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// secretPrompter reads secrets from one input without echo on a terminal.
// On non-terminal input it shares a single buffered reader across prompts,
// so several piped lines are consumed in order.
type secretPrompter struct {
	in  io.Reader
	out io.Writer
	br  *bufio.Reader
}

func newSecretPrompter(in io.Reader, out io.Writer) *secretPrompter {
	return &secretPrompter{in: in, out: out}
}

// ask prints label and returns one line of input.
func (p *secretPrompter) ask(label string) (string, error) {
	fmt.Fprint(p.out, label)
	if f, ok := p.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(p.out)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	if p.br == nil {
		p.br = bufio.NewReader(p.in)
	}
	line, err := p.br.ReadString('\n')
	fmt.Fprintln(p.out)
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func runPrincipalSetPassword(cmd *cobra.Command, args []string) error {
	g := globals(cmd.Context())
	client, err := clientFromGlobals(g)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	prompt := newSecretPrompter(cmd.InOrStdin(), cmd.OutOrStdout())

	password, _ := cmd.Flags().GetString("password")
	if password == "" {
		pw, err := prompt.ask("new password: ")
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		if pw == "" {
			return errors.New("empty password rejected")
		}
		password = pw
	}
	pid, err := resolvePrincipalID(ctx, client, args[0])
	if err != nil {
		return err
	}
	self, err := isOwnPrincipal(ctx, client, pid)
	if err != nil {
		return err
	}
	body := map[string]string{"new_password": password}
	if self {
		cur, _ := cmd.Flags().GetString("current-password")
		if cur == "" {
			cur, err = prompt.ask("current password: ")
			if err != nil {
				return fmt.Errorf("read current password: %w", err)
			}
			if cur == "" {
				return errors.New("empty current password rejected")
			}
		}
		body["current_password"] = cur
	}
	path := "/api/v1/principals/" + pid + "/password"
	err = client.do(ctx, "PUT", path, body, nil)
	var pd *ProblemDetails
	if self && errors.As(err, &pd) && pd.Status == 403 && pd.StepUpRequired {
		code, _ := cmd.Flags().GetString("totp-code")
		if code == "" {
			code, err = prompt.ask("TOTP code: ")
			if err != nil {
				return fmt.Errorf("read TOTP code: %w", err)
			}
			if code == "" {
				return errors.New("empty TOTP code rejected")
			}
		}
		if err := client.do(ctx, "POST", "/api/v1/auth/step-up",
			map[string]string{"totp_code": code}, nil); err != nil {
			return fmt.Errorf("step-up: %w", err)
		}
		err = client.do(ctx, "PUT", path, body, nil)
	}
	if err != nil {
		return wrapPendingRESTError(err)
	}
	writeLine(cmd.OutOrStdout(), g, "password updated")
	return nil
}

// isOwnPrincipal reports whether pid is the principal the client's API key
// authenticates as.
func isOwnPrincipal(ctx context.Context, client *Client, pid string) (bool, error) {
	var me struct {
		PrincipalID uint64 `json:"principal_id"`
	}
	if err := client.do(ctx, "GET", "/api/v1/auth/whoami", nil, &me); err != nil {
		return false, fmt.Errorf("identify caller: %w", err)
	}
	return strconv.FormatUint(me.PrincipalID, 10) == pid, nil
}

func runPrincipalResetPassword(cmd *cobra.Command, args []string) error {
	if off, _ := cmd.Flags().GetBool("offline"); !off {
		return errors.New("reset-password writes to the store directly and requires --offline; " +
			"to change a password through the running server use 'principal set-password'")
	}
	g := globals(cmd.Context())
	cfg, err := requireConfig(g)
	if err != nil {
		return err
	}
	prompt := newSecretPrompter(cmd.InOrStdin(), cmd.OutOrStdout())
	password, _ := cmd.Flags().GetString("password")
	if password == "" {
		password, err = prompt.ask("new password: ")
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		confirm, err := prompt.ask("repeat new password: ")
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		if confirm != password {
			return errors.New("passwords do not match")
		}
	}
	if password == "" {
		return errors.New("empty password rejected")
	}
	if serverListening(cfg) {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"warning: the herold server appears to be running; the reset proceeds "+
				"(store writes are transactional), existing sessions and API keys of the principal are not revoked")
	}
	clk := clock.NewReal()
	st, err := openStore(cmd.Context(), cfg, discardLogger(), clk)
	if err != nil {
		return fmt.Errorf("reset-password: open store: %w", err)
	}
	defer st.Close()
	if err := resetPasswordOffline(cmd.Context(), st, clk, args[0], password); err != nil {
		return err
	}
	writeLine(cmd.OutOrStdout(), g, "password reset for "+strings.ToLower(strings.TrimSpace(args[0])))
	return nil
}

// resetPasswordOffline writes a fresh Argon2id hash for the principal with
// the given email and appends an audit-log entry. Both storage backends are
// safe for a second writer process: the SQLite store takes its write lock
// at BEGIN (BEGIN IMMEDIATE under a 30 s busy_timeout, WAL mode), so a
// concurrent server waits instead of corrupting, and Postgres is
// multi-writer. The update rewrites the principal row, so a concurrent
// edit of another field of the same principal can be lost; the window is
// the time between the read and the write below.
func resetPasswordOffline(ctx context.Context, st store.Store, clk clock.Clock, email, password string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(password) < directory.MinPasswordLength {
		return fmt.Errorf("reset-password: %w (minimum %d characters)",
			directory.ErrWeakPassword, directory.MinPasswordLength)
	}
	p, err := st.Meta().GetPrincipalByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("reset-password: principal %q not found", email)
		}
		return fmt.Errorf("reset-password: look up principal: %w", err)
	}
	if p.Kind == store.PrincipalKindSubAccount {
		return fmt.Errorf("reset-password: %q is a sub-account and carries no password", email)
	}
	hash, err := directory.HashPassword(rand.Reader, password)
	if err != nil {
		return fmt.Errorf("reset-password: hash password: %w", err)
	}
	p.PasswordHash = hash
	if err := st.Meta().UpdatePrincipal(ctx, p); err != nil {
		return fmt.Errorf("reset-password: update principal: %w", err)
	}
	if err := st.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
		At:        clk.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "cli-offline",
		Action:    "principal.password.reset",
		Subject:   fmt.Sprintf("principal:%d", p.ID),
		Outcome:   store.OutcomeSuccess,
		Message:   "offline password reset via herold CLI",
	}); err != nil {
		return fmt.Errorf("reset-password: password written but audit append failed: %w", err)
	}
	return nil
}

// serverListening reports whether the configured admin listener accepts a
// TCP connection, which indicates a running server on this host.
func serverListening(cfg *sysconfig.Config) bool {
	url, _, ok := sysconfig.AdminRESTURL(cfg)
	if !ok {
		return false
	}
	hostport := url
	if i := strings.Index(hostport, "://"); i >= 0 {
		hostport = hostport[i+3:]
	}
	hostport = strings.TrimRight(hostport, "/")
	c, err := net.DialTimeout("tcp", hostport, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
