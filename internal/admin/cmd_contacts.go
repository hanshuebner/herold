package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/cliout"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// newContactsCmd registers the `herold contacts ...` admin sub-command
// tree. The contacts surface gives operators read-only visibility into
// per-principal contact data that is otherwise only reachable via JMAP
// or direct SQL.
//
// There is no admin REST endpoint for contacts (the JMAP surface owns
// the wire protocol for contact mutation). This command opens the store
// directly from the system config, the same approach used by
// `herold diag backup`.
func newContactsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "contacts",
		Short: "contact visibility (list)",
	}
	c.AddCommand(newContactsListCmd())
	return c
}

func newContactsListCmd() *cobra.Command {
	var bookRef string
	var limit int

	cmd := &cobra.Command{
		Use:   "list <email-or-id>",
		Short: "list contacts belonging to a principal",
		Long: "Opens the store from --system-config, resolves the principal " +
			"by email or numeric ID, and prints the contact rows. " +
			"Use --book to scope to one address book; --limit to cap the " +
			"result (server enforces a 1000-row maximum regardless). " +
			"--json emits compact JSON; --full (with --json) includes the " +
			"full jscontact_json blob.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g := globals(cmd.Context())
			cfg, err := requireConfig(g)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			clk := clock.NewReal()
			st, err := openStore(ctx, cfg, discardLogger(), clk)
			if err != nil {
				return fmt.Errorf("contacts list: open store: %w", err)
			}
			defer st.Close()

			// Resolve principal by numeric ID or canonical email.
			ref := strings.TrimSpace(args[0])
			pid, err := resolvePrincipalFromStore(ctx, st, ref)
			if err != nil {
				return err
			}

			// Build the contact filter.
			filter := store.ContactFilter{
				PrincipalID: &pid,
			}
			if bookRef != "" {
				abid, err := resolveAddressBook(ctx, st, pid, bookRef)
				if err != nil {
					return err
				}
				filter.AddressBookID = &abid
			}
			if limit > 0 {
				if limit > 1000 {
					limit = 1000
				}
				filter.Limit = limit
			}

			contacts, err := st.Meta().ListContacts(ctx, filter)
			if err != nil {
				return fmt.Errorf("contacts list: %w", err)
			}

			// Sort by display_name ascending for the default text view.
			sort.Slice(contacts, func(i, j int) bool {
				ni := strings.ToLower(contacts[i].DisplayName)
				nj := strings.ToLower(contacts[j].DisplayName)
				if ni != nj {
					return ni < nj
				}
				return contacts[i].ID < contacts[j].ID
			})

			full, _ := cmd.Flags().GetBool("full")
			if g.jsonOut || !isTerminal(cmd.OutOrStdout()) {
				return writeContactsJSON(cmd.OutOrStdout(), contacts, full)
			}
			return writeContactsTable(cmd.OutOrStdout(), contacts)
		},
	}
	cmd.Flags().StringVar(&bookRef, "book", "", "scope to one address book by name or numeric ID")
	cmd.Flags().IntVar(&limit, "limit", 0, "max contacts to return (capped at 1000)")
	cmd.Flags().Bool("full", false, "include full jscontact_json blob in --json output")
	return cmd
}

// resolvePrincipalFromStore resolves a principal reference (numeric ID
// or canonical email) directly against the store. Returns a clear
// operator-facing error for unknown principals.
func resolvePrincipalFromStore(ctx context.Context, st store.Store, ref string) (store.PrincipalID, error) {
	if n, err := strconv.ParseUint(ref, 10, 64); err == nil {
		p, err := st.Meta().GetPrincipalByID(ctx, store.PrincipalID(n))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return 0, fmt.Errorf("principal %q not found", ref)
			}
			return 0, fmt.Errorf("contacts list: get principal: %w", err)
		}
		return p.ID, nil
	}
	p, err := st.Meta().GetPrincipalByEmail(ctx, strings.ToLower(ref))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, fmt.Errorf("principal %q not found", ref)
		}
		return 0, fmt.Errorf("contacts list: get principal: %w", err)
	}
	return p.ID, nil
}

// resolveAddressBook looks up an address book for the given principal by
// name (exact match, case-insensitive) or numeric ID. The book must
// belong to the principal. Returns a clear operator-facing error when
// not found.
func resolveAddressBook(ctx context.Context, st store.Store, pid store.PrincipalID, ref string) (store.AddressBookID, error) {
	abs, err := st.Meta().ListAddressBooks(ctx, store.AddressBookFilter{
		PrincipalID: &pid,
	})
	if err != nil {
		return 0, fmt.Errorf("contacts list: list address books: %w", err)
	}
	// Try numeric ID match first.
	if n, err2 := strconv.ParseUint(ref, 10, 64); err2 == nil {
		target := store.AddressBookID(n)
		for _, ab := range abs {
			if ab.ID == target {
				return ab.ID, nil
			}
		}
		return 0, fmt.Errorf("address book %d not found for this principal", n)
	}
	// Name match (case-insensitive).
	lower := strings.ToLower(ref)
	for _, ab := range abs {
		if strings.ToLower(ab.Name) == lower {
			return ab.ID, nil
		}
	}
	return 0, fmt.Errorf("address book %q not found for this principal", ref)
}

// contactJSON is the JSON representation emitted by --json. The full
// jscontact_json blob is omitted by default and included only when
// --full is set.
type contactJSON struct {
	ID            uint64 `json:"id"`
	AddressBookID uint64 `json:"address_book_id"`
	UID           string `json:"uid"`
	DisplayName   string `json:"display_name"`
	PrimaryEmail  string `json:"primary_email"`
	UpdatedAtUs   int64  `json:"updated_at_us"`
	// JSContactJSON is included only when --full is set; the omitempty
	// tag silences it otherwise so the default --json view stays compact.
	JSContactJSON json.RawMessage `json:"jscontact_json,omitempty"`
}

// writeContactsJSON emits the contact slice as a JSON array. When full
// is false the jscontact_json blob is omitted; when true it is embedded
// as-is (raw bytes from the store so the output remains valid JSON even
// if the blob is a JSON object).
func writeContactsJSON(w io.Writer, contacts []store.Contact, full bool) error {
	out := make([]contactJSON, 0, len(contacts))
	for _, c := range contacts {
		cj := contactJSON{
			ID:            uint64(c.ID),
			AddressBookID: uint64(c.AddressBookID),
			UID:           c.UID,
			DisplayName:   c.DisplayName,
			PrimaryEmail:  c.PrimaryEmail,
			UpdatedAtUs:   c.UpdatedAt.UnixMicro(),
		}
		if full && len(c.JSContactJSON) > 0 {
			cj.JSContactJSON = json.RawMessage(c.JSContactJSON)
		}
		out = append(out, cj)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// writeContactsTable renders the contacts as a human-readable table.
// Columns: ID, BOOK, DISPLAY-NAME, PRIMARY-EMAIL, UPDATED.
func writeContactsTable(w io.Writer, contacts []store.Contact) error {
	if len(contacts) == 0 {
		fmt.Fprintln(w, "(no contacts)")
		return nil
	}
	t := cliout.NewTable(w)
	t.Header("ID", "BOOK", "DISPLAY-NAME", "PRIMARY-EMAIL", "UPDATED")
	for _, c := range contacts {
		t.Row(
			strconv.FormatUint(uint64(c.ID), 10),
			strconv.FormatUint(uint64(c.AddressBookID), 10),
			cliout.Trunc(c.DisplayName, 40),
			cliout.Trunc(c.PrimaryEmail, 40),
			cliout.FormatTimeValue(c.UpdatedAt),
		)
	}
	return t.Flush()
}
