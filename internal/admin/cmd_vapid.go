package admin

import (
	"crypto/rand"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/vapid"
)

// newVAPIDCmd returns the `herold vapid` command tree. v1 ships one
// subcommand: `generate`, which mints a fresh P-256 ECDSA key pair
// and prints the PEM private key + base64url-encoded public key on
// stdout. The operator stashes the private key in their secrets
// store (env var or file:/path per STANDARDS §9), points
// [server.push].vapid_private_key_env / _file at it, and the public
// key flows back to clients via the JMAP session capability
// descriptor (REQ-PROTO-122).
func newVAPIDCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "vapid",
		Short: "VAPID (RFC 8292) key management for Web Push",
	}
	c.AddCommand(newVAPIDGenerateCmd())
	return c
}

func newVAPIDGenerateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "generate",
		Short: "generate a fresh P-256 VAPID key pair (REQ-PROTO-122)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kp, err := vapid.Generate(rand.Reader)
			if err != nil {
				return fmt.Errorf("vapid generate: %w", err)
			}
			pemStr, err := vapid.EncodePrivatePEM(kp.Private)
			if err != nil {
				return fmt.Errorf("vapid encode: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "# VAPID private key (PEM PKCS#8 P-256). Store this in a secret")
			fmt.Fprintln(out, "# manager and reference it via:")
			fmt.Fprintln(out, "#   [server.push]")
			fmt.Fprintln(out, "#   vapid_private_key_env = \"$HEROLD_VAPID_PRIVATE_KEY\"")
			fmt.Fprintln(out, "# or")
			fmt.Fprintln(out, "#   vapid_private_key_file = \"/etc/herold/secrets/vapid.pem\"")
			fmt.Fprint(out, pemStr)
			fmt.Fprintln(out)
			fmt.Fprintln(out, "# VAPID public key (base64url, uncompressed P-256). Tabard reads this")
			fmt.Fprintln(out, "# from the JMAP session capability descriptor and passes it to")
			fmt.Fprintln(out, "# pushManager.subscribe({applicationServerKey: ...}).")
			fmt.Fprintln(out, kp.PublicKeyB64URL)
			return nil
		},
	}
}
