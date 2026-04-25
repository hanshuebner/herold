package admin

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func newHookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "hook",
		Short: "webhook subscription management",
	}
	c.AddCommand(newHookListCmd())
	c.AddCommand(newHookShowCmd())
	c.AddCommand(newHookCreateCmd())
	c.AddCommand(newHookUpdateCmd())
	c.AddCommand(newHookDeleteCmd())
	return c
}

func newHookListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list webhook subscriptions, optionally filtered by owner",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			ownerKind, _ := cmd.Flags().GetString("owner-kind")
			ownerID, _ := cmd.Flags().GetString("owner-id")
			vals := url.Values{}
			if ownerKind != "" {
				vals.Set("owner_kind", ownerKind)
			}
			if ownerID != "" {
				vals.Set("owner_id", ownerID)
			}
			path := "/api/v1/webhooks"
			if q := vals.Encode(); q != "" {
				path += "?" + q
			}
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", path, nil, &out); err != nil {
				return err
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	cmd.Flags().String("owner-kind", "", "owner kind: domain | principal")
	cmd.Flags().String("owner-id", "", "owner id (domain name or principal id)")
	return cmd
}

func newHookShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "show one webhook by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", "/api/v1/webhooks/"+args[0], nil, &out); err != nil {
				return err
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
}

func newHookCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "create a webhook subscription; the HMAC secret is printed once on success",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			ownerKind, _ := cmd.Flags().GetString("owner-kind")
			ownerID, _ := cmd.Flags().GetString("owner-id")
			targetURL, _ := cmd.Flags().GetString("target-url")
			mode, _ := cmd.Flags().GetString("mode")
			active, _ := cmd.Flags().GetBool("active")
			if ownerKind == "" || ownerID == "" || targetURL == "" {
				return errors.New("hook create: --owner-kind, --owner-id, --target-url are required")
			}
			body := map[string]any{
				"owner_kind": ownerKind,
				"owner_id":   ownerID,
				"target_url": targetURL,
				"active":     active,
			}
			if mode != "" {
				body["delivery_mode"] = mode
			}
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "POST", "/api/v1/webhooks", body, &out); err != nil {
				return err
			}
			// Highlight the one-shot secret prominently in human mode so
			// operators know they need to copy it now. In JSON mode, the
			// secret is part of the body and the caller will pipe it into a
			// secret manager.
			if !g.jsonOut {
				if secret, ok := out["hmac_secret"].(string); ok && secret != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "hmac_secret: %s  (shown once; store it in your receiver's secret manager)\n", secret)
				}
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	cmd.Flags().String("owner-kind", "", "owner kind: domain | principal")
	cmd.Flags().String("owner-id", "", "owner id (domain name or principal id)")
	cmd.Flags().String("target-url", "", "receiver URL (https:// recommended)")
	cmd.Flags().String("mode", "inline", "delivery mode: inline | fetch_url")
	cmd.Flags().Bool("active", true, "whether the subscription dispatches on creation")
	cmd.Flags().String("label", "", "operator-visible label (currently informational)")
	cmd.Flags().Bool("rotate-secret", false, "force the server to mint a fresh secret")
	return cmd
}

func newHookUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "update one or more mutable webhook fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			body := map[string]any{}
			if cmd.Flags().Changed("target-url") {
				v, _ := cmd.Flags().GetString("target-url")
				body["target_url"] = v
			}
			if cmd.Flags().Changed("mode") {
				v, _ := cmd.Flags().GetString("mode")
				body["delivery_mode"] = v
			}
			if cmd.Flags().Changed("active") {
				v, _ := cmd.Flags().GetBool("active")
				body["active"] = v
			}
			if cmd.Flags().Changed("rotate-secret") {
				v, _ := cmd.Flags().GetBool("rotate-secret")
				body["rotate_secret"] = v
			}
			if len(body) == 0 {
				return errors.New("hook update: at least one of --target-url|--mode|--active|--rotate-secret is required")
			}
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "PATCH", "/api/v1/webhooks/"+args[0], body, &out); err != nil {
				return err
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	cmd.Flags().String("target-url", "", "new receiver URL")
	cmd.Flags().String("mode", "", "delivery mode: inline | fetch_url")
	cmd.Flags().Bool("active", false, "set the active flag")
	cmd.Flags().Bool("rotate-secret", false, "rotate the HMAC secret (returned once in the response)")
	return cmd
}

func newHookDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "delete a webhook subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			if err := client.do(cmd.Context(), "DELETE", "/api/v1/webhooks/"+args[0], nil, nil); err != nil {
				return err
			}
			writeLine(cmd.OutOrStdout(), g, "webhook deleted: "+args[0])
			return nil
		},
	}
}
