package admin

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// notImplementedPendingProtoadmin is the shared sentinel returned by
// admin subcommands whose REST surface has not yet landed. It keeps the
// CLI self-describing — operators see one message they can grep for.
var notImplementedPendingProtoadmin = errors.New("admin CLI command not yet implemented in Wave 3 (waiting on protoadmin merge)")

func newPrincipalCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "principal",
		Short: "principal management (create, delete, list, set-password)",
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
	return c
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
