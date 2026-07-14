package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newListCmd registers the `herold list ...` mailing-list admin
// sub-command tree (epic #183, REQ-MLIST-41), mirroring the REST surface
// under /api/v1/lists (REQ-MLIST-40a). <list-id> throughout is the
// numeric id `list list` / `list show` prints; there is no by-address
// lookup on the REST surface yet, matching `alias delete <id>`'s
// numeric-only convention.
func newListCmd() *cobra.Command {
	c := &cobra.Command{
		Use: "list",
		Short: "mailing list management (create, delete, list, show, rename, set, member-add, member-set, " +
			"member-approve, member-reject, member-remove, member-summary, members, held, held-show, held-raw, " +
			"held-approve, held-reject, held-discard, moderators, moderator-add, moderator-remove)",
	}

	createCmd := &cobra.Command{
		Use:   "create <posting-address> <display-name>",
		Short: "create a mailing list",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			body := map[string]any{
				"posting_address": args[0],
				"display_name":    args[1],
			}
			if owner, _ := cmd.Flags().GetString("owner"); owner != "" {
				pid, err := resolvePrincipalID(cmd.Context(), client, owner)
				if err != nil {
					return fmt.Errorf("list create: owner: %w", err)
				}
				body["owner_principal_id"] = mustParseUint(pid)
			}
			if tag, _ := cmd.Flags().GetString("subject-tag"); tag != "" {
				body["subject_tag"] = tag
			}
			if cmd.Flags().Changed("arc-seal") {
				v, _ := cmd.Flags().GetBool("arc-seal")
				body["arc_seal"] = v
			}
			if raw, _ := cmd.Flags().GetString("max-size"); raw != "" {
				n, err := parseHumanBytes(raw)
				if err != nil {
					return fmt.Errorf("list create: max-size: %w", err)
				}
				body["max_message_size_bytes"] = n
			}
			if cmd.Flags().Changed("unsubscribe-enabled") {
				v, _ := cmd.Flags().GetBool("unsubscribe-enabled")
				body["unsubscribe_enabled"] = v
			}
			if sp, _ := cmd.Flags().GetString("subscribe-policy"); sp != "" {
				body["subscribe_policy"] = sp
			}
			if pp, _ := cmd.Flags().GetString("posting-policy"); pp != "" {
				body["posting_policy"] = pp
			}
			if cmd.Flags().Changed("archive-enabled") {
				v, _ := cmd.Flags().GetBool("archive-enabled")
				body["archive_enabled"] = v
			}
			if cmd.Flags().Changed("archive-retention-days") {
				v, _ := cmd.Flags().GetInt64("archive-retention-days")
				body["archive_retention_days"] = v
			}
			if cmd.Flags().Changed("archive-retention-max-messages") {
				v, _ := cmd.Flags().GetInt64("archive-retention-max-messages")
				body["archive_retention_max_messages"] = v
			}
			if bp := bouncePolicyBodyFromFlags(cmd); bp != nil {
				body["bounce_policy"] = bp
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "POST", "/api/v1/lists", body, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	createCmd.Flags().String("owner", "", "owner principal (email or id); defaults to the caller")
	createCmd.Flags().String("subject-tag", "", "subject tag to prepend on fan-out (default: unset)")
	createCmd.Flags().Bool("arc-seal", true, "ARC-seal fanned-out copies")
	createCmd.Flags().String("max-size", "", "per-post size ceiling (accepts K/M/G/T suffixes; default: deployment default)")
	createCmd.Flags().Bool("unsubscribe-enabled", true, "emit List-Unsubscribe / RFC 8058 one-click headers and the self-service management page (REQ-MLIST-56..58)")
	createCmd.Flags().String("subscribe-policy", "", "self-subscription policy: closed, request-approval, open (default: closed, REQ-MLIST-60)")
	createCmd.Flags().String("posting-policy", "", "posting policy: open, members-only, announce-only, moderated (default: open, REQ-MLIST-80)")
	createCmd.Flags().Bool("archive-enabled", false, "create the list's archive mailbox (REQ-MLIST-70)")
	createCmd.Flags().Int64("archive-retention-days", 0, "archive retention age bound in days; 0 means unbounded (REQ-MLIST-74)")
	createCmd.Flags().Int64("archive-retention-max-messages", 0, "archive retention count bound; 0 means unbounded (REQ-MLIST-74)")
	addBouncePolicyFlags(createCmd)
	c.AddCommand(createCmd)

	c.AddCommand(&cobra.Command{
		Use:   "delete <list-id>",
		Short: "delete a mailing list (cascades its roster; the backing group principal is left in place)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			if err := client.do(cmd.Context(), "DELETE", "/api/v1/lists/"+args[0], nil, nil); err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "list deleted: "+args[0])
			return nil
		},
	})

	listListCmd := &cobra.Command{
		Use:   "list",
		Short: "list mailing lists (optionally domain-scoped via --domain)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			path := "/api/v1/lists"
			sep := "?"
			if d, _ := cmd.Flags().GetString("domain"); d != "" {
				path += sep + "domain=" + d
				sep = "&"
			}
			if after, _ := cmd.Flags().GetString("after"); after != "" {
				path += sep + "after=" + after
				sep = "&"
			}
			if limit, _ := cmd.Flags().GetInt("limit"); limit > 0 {
				path += sep + fmt.Sprintf("limit=%d", limit)
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", path, nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	listListCmd.Flags().String("domain", "", "restrict to lists on this domain")
	listListCmd.Flags().String("after", "", "pagination cursor")
	listListCmd.Flags().Int("limit", 0, "page size")
	c.AddCommand(listListCmd)

	c.AddCommand(&cobra.Command{
		Use:   "show <list-id>",
		Short: "show one mailing list's fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", "/api/v1/lists/"+args[0], nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "rename <list-id> <new-posting-address>",
		Short: "change a list's posting address",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			err = client.do(cmd.Context(), "PATCH", "/api/v1/lists/"+args[0],
				map[string]any{"posting_address": args[1]}, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	setCmd := &cobra.Command{
		Use: "set <list-id>",
		Short: "update a mailing list's config (display-name, subject-tag, arc-seal, max-size, owner, " +
			"unsubscribe-enabled, subscribe-policy, posting-policy, archive-*)",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			body := map[string]any{}
			if cmd.Flags().Changed("display-name") {
				v, _ := cmd.Flags().GetString("display-name")
				body["display_name"] = v
			}
			if cmd.Flags().Changed("subject-tag") {
				v, _ := cmd.Flags().GetString("subject-tag")
				body["subject_tag"] = v
			}
			if cmd.Flags().Changed("arc-seal") {
				v, _ := cmd.Flags().GetBool("arc-seal")
				body["arc_seal"] = v
			}
			if cmd.Flags().Changed("max-size") {
				raw, _ := cmd.Flags().GetString("max-size")
				n, err := parseHumanBytes(raw)
				if err != nil {
					return fmt.Errorf("list set: max-size: %w", err)
				}
				body["max_message_size_bytes"] = n
			}
			if cmd.Flags().Changed("owner") {
				owner, _ := cmd.Flags().GetString("owner")
				pid, err := resolvePrincipalID(cmd.Context(), client, owner)
				if err != nil {
					return fmt.Errorf("list set: owner: %w", err)
				}
				body["owner_principal_id"] = mustParseUint(pid)
			}
			if cmd.Flags().Changed("unsubscribe-enabled") {
				v, _ := cmd.Flags().GetBool("unsubscribe-enabled")
				body["unsubscribe_enabled"] = v
			}
			if cmd.Flags().Changed("subscribe-policy") {
				v, _ := cmd.Flags().GetString("subscribe-policy")
				body["subscribe_policy"] = v
			}
			if cmd.Flags().Changed("posting-policy") {
				v, _ := cmd.Flags().GetString("posting-policy")
				body["posting_policy"] = v
			}
			if cmd.Flags().Changed("archive-enabled") {
				v, _ := cmd.Flags().GetBool("archive-enabled")
				body["archive_enabled"] = v
			}
			if cmd.Flags().Changed("archive-retention-days") {
				v, _ := cmd.Flags().GetInt64("archive-retention-days")
				body["archive_retention_days"] = v
			}
			if cmd.Flags().Changed("archive-retention-max-messages") {
				v, _ := cmd.Flags().GetInt64("archive-retention-max-messages")
				body["archive_retention_max_messages"] = v
			}
			if bp := bouncePolicyBodyFromFlags(cmd); bp != nil {
				body["bounce_policy"] = bp
			}
			if len(body) == 0 {
				return errors.New("list set: at least one of --display-name, --subject-tag, --arc-seal, --max-size, --owner, " +
					"--unsubscribe-enabled, --subscribe-policy, --posting-policy, --archive-enabled, --archive-retention-days, " +
					"--archive-retention-max-messages, --bounce-* is required")
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "PATCH", "/api/v1/lists/"+args[0], body, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	setCmd.Flags().String("display-name", "", "new display name (List-ID header)")
	setCmd.Flags().String("subject-tag", "", "subject tag to prepend on fan-out (empty clears it)")
	setCmd.Flags().Bool("arc-seal", true, "ARC-seal fanned-out copies")
	setCmd.Flags().String("max-size", "", "per-post size ceiling (K/M/G/T suffixes)")
	setCmd.Flags().String("owner", "", "reassign ownership to this principal (email or id)")
	setCmd.Flags().Bool("unsubscribe-enabled", true, "emit List-Unsubscribe / RFC 8058 one-click headers and the self-service management page (REQ-MLIST-56..58)")
	setCmd.Flags().String("subscribe-policy", "", "self-subscription policy: closed, request-approval, open (REQ-MLIST-60)")
	setCmd.Flags().String("posting-policy", "", "posting policy: open, members-only, announce-only, moderated (REQ-MLIST-80)")
	setCmd.Flags().Bool("archive-enabled", false, "enable (true) or disable (false) the list's archive mailbox (REQ-MLIST-70); disabling detaches read access but keeps the mailbox and its content")
	setCmd.Flags().Int64("archive-retention-days", 0, "archive retention age bound in days; 0 means unbounded (REQ-MLIST-74)")
	setCmd.Flags().Int64("archive-retention-max-messages", 0, "archive retention count bound; 0 means unbounded (REQ-MLIST-74)")
	addBouncePolicyFlags(setCmd)
	c.AddCommand(setCmd)

	memberAddCmd := &cobra.Command{
		Use:   "member-add <list-id> <address-or-principal>",
		Short: "add a member to a list's roster",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			body := map[string]any{}
			switch kind, val, terr := resolveMlistMemberTarget(cmd.Context(), client, args[1]); {
			case terr != nil:
				return fmt.Errorf("list member-add: %w", terr)
			case kind == aliasTargetPrincipal:
				body["principal_id"] = mustParseUint(val)
			default:
				body["external_address"] = val
			}
			if st, _ := cmd.Flags().GetString("state"); st != "" {
				body["state"] = st
			}
			if dm, _ := cmd.Flags().GetString("delivery-mode"); dm != "" {
				body["delivery_mode"] = dm
			}
			var out map[string]any
			err = client.do(cmd.Context(), "POST",
				fmt.Sprintf("/api/v1/lists/%s/members", args[0]), body, &out)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	memberAddCmd.Flags().String("state", "",
		"initial state: active, suspended, unsubscribed, pending, awaiting-approval (default: active)")
	memberAddCmd.Flags().String("delivery-mode", "",
		"delivery mode: each, nomail (default: each; nomail requires an internal principal, REQ-MLIST-04)")
	c.AddCommand(memberAddCmd)

	memberSetCmd := &cobra.Command{
		Use: "member-set <list-id> <member-id>",
		Short: "update a roster member's state or delivery mode (--state active reactivates a suspended member or " +
			"approves an awaiting-approval member, resetting its bounce score; see also member-approve/member-reject)",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			body := map[string]any{}
			if cmd.Flags().Changed("state") {
				v, _ := cmd.Flags().GetString("state")
				body["state"] = v
			}
			if cmd.Flags().Changed("delivery-mode") {
				v, _ := cmd.Flags().GetString("delivery-mode")
				body["delivery_mode"] = v
			}
			if len(body) == 0 {
				return errors.New("list member-set: at least one of --state, --delivery-mode is required")
			}
			var out map[string]any
			path := fmt.Sprintf("/api/v1/lists/%s/members/%s", args[0], args[1])
			if err := client.do(cmd.Context(), "PATCH", path, body, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	memberSetCmd.Flags().String("state", "",
		"new state (active, suspended, unsubscribed, pending, awaiting-approval); active reactivates and resets bounce_score (REQ-MLIST-55)")
	memberSetCmd.Flags().String("delivery-mode", "",
		"new delivery mode (each, nomail); nomail requires an internal principal (REQ-MLIST-04)")
	c.AddCommand(memberSetCmd)

	c.AddCommand(&cobra.Command{
		Use: "member-approve <list-id> <member-id>",
		Short: "approve an awaiting-approval member on a request-approval list, moving it to active " +
			"(REQ-MLIST-62; shorthand for member-set --state active)",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			path := fmt.Sprintf("/api/v1/lists/%s/members/%s", args[0], args[1])
			if err := client.do(cmd.Context(), "PATCH", path, map[string]any{"state": "active"}, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	c.AddCommand(&cobra.Command{
		Use: "member-reject <list-id> <member-id>",
		Short: "reject an awaiting-approval member on a request-approval list, moving it to unsubscribed " +
			"(REQ-MLIST-62; shorthand for member-set --state unsubscribed)",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			path := fmt.Sprintf("/api/v1/lists/%s/members/%s", args[0], args[1])
			if err := client.do(cmd.Context(), "PATCH", path, map[string]any{"state": "unsubscribed"}, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "member-summary <list-id>",
		Short: "show the roster member count by state (active, suspended, unsubscribed, pending, awaiting-approval)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			path := fmt.Sprintf("/api/v1/lists/%s/members/summary", args[0])
			if err := client.do(cmd.Context(), "GET", path, nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "member-remove <list-id> <member-id>",
		Short: "remove a member from a list's roster (use 'list members' to find the member id)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			path := fmt.Sprintf("/api/v1/lists/%s/members/%s", args[0], args[1])
			if err := client.do(cmd.Context(), "DELETE", path, nil, nil); err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "member removed: "+args[1])
			return nil
		},
	})

	membersCmd := &cobra.Command{
		Use:   "members <list-id>",
		Short: "list, export, or bulk-import a list's roster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			if importFile, _ := cmd.Flags().GetString("import"); importFile != "" {
				return runMlistMembersImport(cmd, client, args[0], importFile)
			}
			export, _ := cmd.Flags().GetBool("export")
			path := fmt.Sprintf("/api/v1/lists/%s/members", args[0])
			if export {
				path += "/export"
			}
			sep := "?"
			if st, _ := cmd.Flags().GetString("state"); st != "" {
				path += sep + "state=" + st
				sep = "&"
			}
			if dm, _ := cmd.Flags().GetString("delivery-mode"); dm != "" {
				path += sep + "delivery_mode=" + dm
				sep = "&"
			}
			if after, _ := cmd.Flags().GetString("after"); after != "" {
				path += sep + "after=" + after
				sep = "&"
			}
			if limit, _ := cmd.Flags().GetInt("limit"); limit > 0 {
				path += sep + fmt.Sprintf("limit=%d", limit)
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", path, nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			if outFile, _ := cmd.Flags().GetString("out"); outFile != "" {
				if err := writeJSONFile(outFile, out); err != nil {
					return fmt.Errorf("list members: %w", err)
				}
				writeLine(cmd.OutOrStdout(), g, "wrote "+outFile)
				return nil
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	membersCmd.Flags().String("state", "", "filter by state (active, suspended, unsubscribed, pending, awaiting-approval)")
	membersCmd.Flags().String("delivery-mode", "", "filter by delivery mode (each, nomail)")
	membersCmd.Flags().String("after", "", "pagination cursor")
	membersCmd.Flags().Int("limit", 0, "page size")
	membersCmd.Flags().Bool("export", false, "fetch the full roster export instead of one default-size page")
	membersCmd.Flags().String("out", "", "write the JSON response to this file instead of stdout (pairs with --export for bulk backup)")
	membersCmd.Flags().String("import", "", "bulk-import members from a JSON file (accepts the shape 'members --export --out' writes)")
	c.AddCommand(membersCmd)

	// Moderation (v2 milestone, issue #189, REQ-MLIST-80/REQ-AC-41): the
	// held-post queue and approve/reject/discard, plus assigning the
	// list:moderator grant. Mirrors /api/v1/lists/{id}/held and
	// /api/v1/lists/{id}/moderators.
	heldCmd := &cobra.Command{
		Use:   "held <list-id>",
		Short: "list held posts awaiting moderation (optionally filtered by --status)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			path := fmt.Sprintf("/api/v1/lists/%s/held", args[0])
			sep := "?"
			if st, _ := cmd.Flags().GetString("status"); st != "" {
				path += sep + "status=" + st
				sep = "&"
			}
			if after, _ := cmd.Flags().GetString("after"); after != "" {
				path += sep + "after=" + after
				sep = "&"
			}
			if limit, _ := cmd.Flags().GetInt("limit"); limit > 0 {
				path += sep + fmt.Sprintf("limit=%d", limit)
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", path, nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	heldCmd.Flags().String("status", "", "filter by status (pending, approved, rejected, discarded); default: all")
	heldCmd.Flags().String("after", "", "pagination cursor")
	heldCmd.Flags().Int("limit", 0, "page size")
	c.AddCommand(heldCmd)

	c.AddCommand(&cobra.Command{
		Use:   "held-show <list-id> <held-post-id>",
		Short: "show one held post's metadata (from, subject, status, reason)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			path := fmt.Sprintf("/api/v1/lists/%s/held/%s", args[0], args[1])
			if err := client.do(cmd.Context(), "GET", path, nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	heldRawCmd := &cobra.Command{
		Use:   "held-raw <list-id> <held-post-id>",
		Short: "write a held post's raw message/rfc822 bytes to --out (or stdout)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			path := fmt.Sprintf("/api/v1/lists/%s/held/%s/raw", args[0], args[1])
			raw, err := client.doRaw(cmd.Context(), "GET", path, nil)
			if err != nil {
				return wrapPendingRESTError(err)
			}
			if outFile, _ := cmd.Flags().GetString("out"); outFile != "" {
				if err := os.WriteFile(outFile, raw, 0o600); err != nil {
					return fmt.Errorf("list held-raw: %w", err)
				}
				writeLine(cmd.OutOrStdout(), g, "wrote "+outFile)
				return nil
			}
			_, err = cmd.OutOrStdout().Write(raw)
			return err
		},
	}
	heldRawCmd.Flags().String("out", "", "write to this file instead of stdout")
	c.AddCommand(heldRawCmd)

	c.AddCommand(&cobra.Command{
		Use:   "held-approve <list-id> <held-post-id>",
		Short: "approve a held post: fans out through the normal path (REQ-MLIST-80)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			path := fmt.Sprintf("/api/v1/lists/%s/held/%s/approve", args[0], args[1])
			if err := client.do(cmd.Context(), "POST", path, nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	heldRejectCmd := &cobra.Command{
		Use:   "held-reject <list-id> <held-post-id>",
		Short: "reject a held post: never fanned out (REQ-MLIST-80)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			body := map[string]any{}
			if note, _ := cmd.Flags().GetString("note"); note != "" {
				body["note"] = note
			}
			var out map[string]any
			path := fmt.Sprintf("/api/v1/lists/%s/held/%s/reject", args[0], args[1])
			if err := client.do(cmd.Context(), "POST", path, body, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	heldRejectCmd.Flags().String("note", "", "optional moderator note recorded on the decision")
	c.AddCommand(heldRejectCmd)

	heldDiscardCmd := &cobra.Command{
		Use:   "held-discard <list-id> <held-post-id>",
		Short: "discard a held post: never fanned out (REQ-MLIST-80)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			body := map[string]any{}
			if note, _ := cmd.Flags().GetString("note"); note != "" {
				body["note"] = note
			}
			var out map[string]any
			path := fmt.Sprintf("/api/v1/lists/%s/held/%s/discard", args[0], args[1])
			if err := client.do(cmd.Context(), "POST", path, body, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	}
	heldDiscardCmd.Flags().String("note", "", "optional moderator note recorded on the decision")
	c.AddCommand(heldDiscardCmd)

	c.AddCommand(&cobra.Command{
		Use:   "moderators <list-id>",
		Short: "show the list:moderator grants on a list (REQ-AC-41)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			var out map[string]any
			if err := client.do(cmd.Context(), "GET", fmt.Sprintf("/api/v1/lists/%s/moderators", args[0]), nil, &out); err != nil {
				return wrapPendingRESTError(err)
			}
			return writeResult(cmd.OutOrStdout(), g, out)
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "moderator-add <list-id> <principal>",
		Short: "grant list:moderator (approve/reject/discard held posts, read the roster) to a principal (REQ-AC-41)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			pid, err := resolvePrincipalID(cmd.Context(), client, args[1])
			if err != nil {
				return fmt.Errorf("list moderator-add: %w", err)
			}
			path := fmt.Sprintf("/api/v1/lists/%s/moderators", args[0])
			if err := client.do(cmd.Context(), "POST", path, map[string]any{"principal_id": mustParseUint(pid)}, nil); err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "moderator granted: "+args[1])
			return nil
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "moderator-remove <list-id> <principal-id>",
		Short: "revoke a principal's list:moderator grant",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			client, err := clientFromGlobals(g)
			if err != nil {
				return err
			}
			path := fmt.Sprintf("/api/v1/lists/%s/moderators/%s", args[0], args[1])
			if err := client.do(cmd.Context(), "DELETE", path, nil, nil); err != nil {
				return wrapPendingRESTError(err)
			}
			writeLine(cmd.OutOrStdout(), g, "moderator revoked: "+args[1])
			return nil
		},
	})

	return c
}

// addBouncePolicyFlags registers the REQ-MLIST-53 bounce-scoring-policy
// flags shared by `list create` and `list set`. Every flag is optional
// (zero value on the wire means "use the deployment default"), so a
// command that sets none of them sends no bounce_policy at all.
func addBouncePolicyFlags(cmd *cobra.Command) {
	cmd.Flags().Float64("bounce-hard-weight", 0, "bounce score added by one hard bounce (default: deployment default)")
	cmd.Flags().Float64("bounce-soft-weight", 0, "bounce score added by one soft bounce (default: deployment default)")
	cmd.Flags().Duration("bounce-decay-window", 0, "how long an accumulated bounce score survives a gap with no bounce, e.g. 168h (default: deployment default)")
	cmd.Flags().Float64("bounce-suspend-threshold", 0, "bounce score at or above which a member is auto-suspended (default: deployment default)")
}

// bouncePolicyBodyFromFlags reads addBouncePolicyFlags's flags off cmd
// and returns the bounce_policy JSON body fragment, or nil if the caller
// set none of them (so the request omits bounce_policy entirely rather
// than sending an all-defaults override).
func bouncePolicyBodyFromFlags(cmd *cobra.Command) map[string]any {
	body := map[string]any{}
	if cmd.Flags().Changed("bounce-hard-weight") {
		v, _ := cmd.Flags().GetFloat64("bounce-hard-weight")
		body["hard_weight"] = v
	}
	if cmd.Flags().Changed("bounce-soft-weight") {
		v, _ := cmd.Flags().GetFloat64("bounce-soft-weight")
		body["soft_weight"] = v
	}
	if cmd.Flags().Changed("bounce-decay-window") {
		v, _ := cmd.Flags().GetDuration("bounce-decay-window")
		body["decay_window_seconds"] = int64(v / time.Second)
	}
	if cmd.Flags().Changed("bounce-suspend-threshold") {
		v, _ := cmd.Flags().GetFloat64("bounce-suspend-threshold")
		body["suspend_threshold"] = v
	}
	if len(body) == 0 {
		return nil
	}
	return body
}

// resolveMlistMemberTarget classifies <target> for `herold list
// member-add`: a numeric principal id or a local principal's canonical
// email resolves to aliasTargetPrincipal; any other well-formed email
// address is an external roster member (aliasTargetExternal). Unlike
// resolveAliasTarget (cmd_alias.go), a mailing-list member is never
// rejected for coinciding with an unregistered address on a locally
// hosted domain — membership is just an address to copy, not a routing
// rule.
func resolveMlistMemberTarget(ctx context.Context, client *Client, ref string) (aliasTargetKind, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, "", errors.New("member address or principal required")
	}
	if _, err := strconv.ParseUint(ref, 10, 64); err == nil {
		return aliasTargetPrincipal, ref, nil
	}
	var out struct {
		Items []struct {
			ID             uint64 `json:"id"`
			CanonicalEmail string `json:"canonical_email"`
		} `json:"items"`
	}
	if err := client.do(ctx, "GET", "/api/v1/principals?limit=1000", nil, &out); err == nil {
		lower := strings.ToLower(ref)
		for _, p := range out.Items {
			if strings.EqualFold(p.CanonicalEmail, lower) {
				return aliasTargetPrincipal, strconv.FormatUint(p.ID, 10), nil
			}
		}
	}
	addr, err := mail.ParseAddress(ref)
	if err != nil {
		return 0, "", fmt.Errorf("%q is neither a known principal (id or email) nor a valid email address", ref)
	}
	return aliasTargetExternal, strings.ToLower(addr.Address), nil
}

// runMlistMembersImport reads path (either the {"items":[...]} shape
// `list members --export` writes or a bare {"members":[...]} request
// body) and re-POSTs the recognised fields (principal_id,
// external_address, state, delivery_mode) to the bulk import endpoint,
// so an operator can round-trip a roster export back into (the same or
// a different) list without hand-editing the shape.
func runMlistMembersImport(cmd *cobra.Command, client *Client, listRef, path string) error {
	g := globals(cmd.Context())
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("list members --import: read %s: %w", path, err)
	}
	var doc struct {
		Items   []map[string]any `json:"items"`
		Members []map[string]any `json:"members"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("list members --import: parse %s: %w", path, err)
	}
	entries := doc.Members
	if len(entries) == 0 {
		entries = doc.Items
	}
	members := make([]map[string]any, 0, len(entries))
	for _, it := range entries {
		m := map[string]any{}
		for _, k := range []string{"principal_id", "external_address", "state", "delivery_mode"} {
			if v, ok := it[k]; ok {
				m[k] = v
			}
		}
		members = append(members, m)
	}
	body := map[string]any{"members": members}
	var out map[string]any
	err = client.do(cmd.Context(), "POST",
		fmt.Sprintf("/api/v1/lists/%s/members/import", listRef), body, &out)
	if err != nil {
		return wrapPendingRESTError(err)
	}
	return writeResult(cmd.OutOrStdout(), g, out)
}
