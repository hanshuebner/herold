package admin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// notImplementedPendingProtoadmin is the shared sentinel returned by
// admin subcommands whose REST surface has not yet landed. It keeps the
// CLI self-describing — operators see one message they can grep for.
var notImplementedPendingProtoadmin = errors.New("admin CLI command not yet implemented in Wave 3 (waiting on protoadmin merge)")

func newPrincipalCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "principal",
		Short: "principal management (create, show, delete, list, set-password, disable/enable, quota, grant-admin, totp)",
	}
	c.AddCommand(&cobra.Command{
		Use:   "create <email>",
		Short: "create a new principal via the admin REST API",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			body := map[string]any{"email": args[0]}
			if pw, _ := cmd.Flags().GetString("password"); pw != "" {
				body["password"] = pw
			}
			if rp, _ := cmd.Flags().GetBool("random-password"); rp {
				body["random_password"] = true
			}
			var out map[string]any
			err = client.do(cmd.Context(), "POST", "/api/v1/principals", body, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})
	c.Commands()[len(c.Commands())-1].Flags().String("password", "", "explicit password (omit to let the server generate one)")
	c.Commands()[len(c.Commands())-1].Flags().Bool("random-password", false, "force a server-generated random password")

	c.AddCommand(&cobra.Command{
		Use:   "delete <email-or-id>",
		Short: "delete a principal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			err = client.do(cmd.Context(), "DELETE", "/api/v1/principals/"+args[0], nil, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "deleted "+args[0])
			return nil
		},
	})

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "list principals",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			after, _ := cmd.Flags().GetString("after")
			limit, _ := cmd.Flags().GetInt("limit")
			path := "/api/v1/principals"
			sep := "?"
			if after != "" {
				path += sep + "after=" + after
				sep = "&"
			}
			if limit > 0 {
				path += sep + fmt.Sprintf("limit=%d", limit)
			}
			var out map[string]any
			err = client.do(cmd.Context(), "GET", path, nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	listCmd.Flags().String("after", "", "pagination cursor")
	listCmd.Flags().Int("limit", 0, "page size")
	c.AddCommand(listCmd)

	// show: GET /api/v1/principals/{pid}. Accepts email or numeric id;
	// emails resolve client-side via list-and-filter (REST has no
	// by-email lookup endpoint as of Wave 3.5c).
	c.AddCommand(&cobra.Command{
		Use:   "show <email-or-id>",
		Short: "show one principal's fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			pid, err := resolvePrincipalID(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(cmd.Context(), "GET", "/api/v1/principals/"+pid, nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	// disable / enable: read-modify-write on the principal's flags array.
	c.AddCommand(&cobra.Command{
		Use:   "disable <email-or-id>",
		Short: "set the disabled flag on a principal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPrincipalDisabled(cmd, args[0], true)
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "enable <email-or-id>",
		Short: "clear the disabled flag on a principal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPrincipalDisabled(cmd, args[0], false)
		},
	})

	// quota: PATCH /api/v1/principals/{pid} with quota_bytes. Accepts
	// human-readable suffixes (5G, 200M, 1024). REQ-ADM-10 hints at a
	// dedicated /quota subresource; the wired REST surface today is
	// PATCH-on-parent. Switch is internal if the dedicated subresource
	// lands later.
	c.AddCommand(&cobra.Command{
		Use:   "quota <email-or-id> <bytes>",
		Short: "set storage quota in bytes (accepts suffixes K, M, G, T)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			n, err := parseHumanBytes(args[1])
			if err != nil {
				return fmt.Errorf("quota: parse %q: %w", args[1], err)
			}
			pid, err := resolvePrincipalID(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"quota_bytes": n}
			var out map[string]any
			err = client.do(cmd.Context(), "PATCH", "/api/v1/principals/"+pid, body, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, fmt.Sprintf("quota set: %s -> %d bytes", args[0], n))
			return nil
		},
	})

	// grant-admin / revoke-admin: read-modify-write on the principal's
	// flags array. Server gates via PrincipalFlagAdmin.
	c.AddCommand(&cobra.Command{
		Use:   "grant-admin <email-or-id>",
		Short: "add the admin flag to a principal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPrincipalAdmin(cmd, args[0], true)
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "revoke-admin <email-or-id>",
		Short: "remove the admin flag from a principal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPrincipalAdmin(cmd, args[0], false)
		},
	})

	setPwCmd := &cobra.Command{
		Use:   "set-password <email-or-id>",
		Short: "change a principal's password (prompts unless --password is given)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			password, _ := cmd.Flags().GetString("password")
			if password == "" {
				// Interactive prompt. We intentionally use the stdlib so
				// the CLI does not pull a terminal-handling dep.
				fmt.Fprint(cmd.OutOrStdout(), "new password: ")
				_, err := fmt.Fscanln(cmd.InOrStdin(), &password)
				if err != nil {
					return fmt.Errorf("read password: %w", err)
				}
				if password == "" {
					return errors.New("empty password rejected")
				}
			}
			err = client.do(cmd.Context(), "POST", "/api/v1/principals/"+args[0]+"/password",
				map[string]string{"password": password}, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "password updated")
			return nil
		},
	}
	setPwCmd.Flags().String("password", "", "new password (omit to be prompted)")
	c.AddCommand(setPwCmd)

	// totp subcommand group — enroll / disable. Confirm is a self-service
	// flow and not exposed as an operator verb (the operator cannot read
	// the TOTP code off the user's authenticator).
	c.AddCommand(newPrincipalTOTPCmd())
	return c
}

func newPrincipalTOTPCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "totp",
		Short: "TOTP management (enroll, disable)",
	}
	c.AddCommand(&cobra.Command{
		Use:   "enroll <email-or-id>",
		Short: "begin TOTP enrolment; returns the provisioning URI for QR rendering",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			pid, err := resolvePrincipalID(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(cmd.Context(), "POST", "/api/v1/principals/"+pid+"/totp/enroll", nil, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})
	disableCmd := &cobra.Command{
		Use:   "disable <email-or-id>",
		Short: "remove TOTP from a principal (requires --current-password)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			pw, _ := cmd.Flags().GetString("current-password")
			if pw == "" {
				return errors.New("totp disable: --current-password is required")
			}
			pid, err := resolvePrincipalID(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			body := map[string]string{"current_password": pw}
			err = client.do(cmd.Context(), "DELETE", "/api/v1/principals/"+pid+"/totp", body, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "totp removed for "+args[0])
			return nil
		},
	}
	disableCmd.Flags().String("current-password", "",
		"current password of the principal (required by REST surface; "+
			"admins still need the password as a deliberate footgun guard)")
	c.AddCommand(disableCmd)
	return c
}

// setPrincipalDisabled toggles the "disabled" flag in the principal's
// flags array via PATCH /api/v1/principals/{pid}. The handler preserves
// totp_enabled internally; we round-trip the flag set otherwise.
func setPrincipalDisabled(cmd *cobra.Command, ref string, on bool) error {
	g := globals(cmd.Context())
	client, err := clientFromGlobals(g)
	if err != nil {
		return err
	}
	pid, err := resolvePrincipalID(cmd.Context(), client, ref)
	if err != nil {
		return err
	}
	flags, err := fetchFlags(cmd.Context(), client, pid)
	if err != nil {
		return err
	}
	flags = setStringFlag(flags, "disabled", on)
	body := map[string]any{"flags": flags}
	err = client.do(cmd.Context(), "PATCH", "/api/v1/principals/"+pid, body, nil)
	if err != nil {
		return wrapPendingRESTError(err)
	}
	verb := "enabled"
	if on {
		verb = "disabled"
	}
	writeLine(cmd.OutOrStdout(), g, fmt.Sprintf("principal %s %s", ref, verb))
	return nil
}

// setPrincipalAdmin toggles the "admin" flag on a principal.
func setPrincipalAdmin(cmd *cobra.Command, ref string, on bool) error {
	g := globals(cmd.Context())
	client, err := clientFromGlobals(g)
	if err != nil {
		return err
	}
	pid, err := resolvePrincipalID(cmd.Context(), client, ref)
	if err != nil {
		return err
	}
	flags, err := fetchFlags(cmd.Context(), client, pid)
	if err != nil {
		return err
	}
	flags = setStringFlag(flags, "admin", on)
	body := map[string]any{"flags": flags}
	err = client.do(cmd.Context(), "PATCH", "/api/v1/principals/"+pid, body, nil)
	if err != nil {
		return wrapPendingRESTError(err)
	}
	verb := "revoked"
	if on {
		verb = "granted"
	}
	writeLine(cmd.OutOrStdout(), g, fmt.Sprintf("admin flag %s for %s", verb, ref))
	return nil
}

// fetchFlags reads the flags array from GET /api/v1/principals/{pid}.
// totp_enabled is filtered out: the PATCH handler refuses caller-supplied
// totp_enabled and round-tripping it forces a server-side 400.
func fetchFlags(ctx context.Context, client *Client, pid string) ([]string, error) {
	var got struct {
		Flags []string `json:"flags"`
	}
	if err := client.do(ctx, "GET", "/api/v1/principals/"+pid, nil, &got); err != nil {
		return nil, fmt.Errorf("fetch principal: %w", err)
	}
	out := make([]string, 0, len(got.Flags))
	for _, f := range got.Flags {
		if f == "totp_enabled" {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// setStringFlag adds or removes flag from set, preserving order otherwise.
func setStringFlag(set []string, flag string, on bool) []string {
	out := make([]string, 0, len(set)+1)
	found := false
	for _, f := range set {
		if f == flag {
			found = true
			if on {
				out = append(out, f)
			}
			continue
		}
		out = append(out, f)
	}
	if on && !found {
		out = append(out, flag)
	}
	return out
}

// parseHumanBytes accepts a decimal byte count or a value with a single
// suffix (K, M, G, T, case-insensitive; binary multipliers — 1K = 1024).
// Returns the byte count or an error explaining what went wrong.
func parseHumanBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty value")
	}
	mul := int64(1)
	last := s[len(s)-1]
	switch last {
	case 'k', 'K':
		mul = 1 << 10
		s = s[:len(s)-1]
	case 'm', 'M':
		mul = 1 << 20
		s = s[:len(s)-1]
	case 'g', 'G':
		mul = 1 << 30
		s = s[:len(s)-1]
	case 't', 'T':
		mul = 1 << 40
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("not an integer: %w", err)
	}
	if n < 0 {
		return 0, errors.New("negative byte count rejected")
	}
	return n * mul, nil
}

// wrapPendingRESTError annotates errors from the placeholder admin
// handler so operators know to expect a fuller surface once protoadmin
// lands.
func wrapPendingRESTError(err error) error {
	if err == nil {
		return nil
	}
	var pd *ProblemDetails
	if errors.As(err, &pd) && pd.Status == 501 {
		return fmt.Errorf("%s: %w", notImplementedPendingProtoadmin.Error(), err)
	}
	return err
}
