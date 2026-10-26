// Package accounts lists and chooses the saved provider accounts.
package accounts

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/account/importer"
	"dyna-agent/internal/account/service"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/harness/runtime"
	"github.com/spf13/cobra"
)

// Client is the engine call this command makes. The engine owns the account
// store, so a read, a selection, an add and a removal all go through it.
type Client interface {
	Call(ctx context.Context, method string, args []any, out any) error
}

type Options struct {
	Client Client
}

func NewCommand() *cobra.Command { return Command(Options{}) }

func Command(o Options) *cobra.Command {
	if o.Client == nil {
		o.Client = engineclient.New("")
	}
	root := &cobra.Command{
		Use:   "accounts",
		Short: "List and choose the saved provider accounts",
		Long: "List and choose the saved provider accounts. Each account is one provider sign-in, and a harness only runs it. " +
			"An account used globally is the one its provider runs everywhere; an account used in a room overrides the global one for that room's turns.",
	}
	root.AddCommand(o.listCommand(), o.refreshCommand(), o.useCommand(), o.addCommand(), o.removeCommand())
	return root
}

// snapshot asks the engine for the account view of scope, which is a room
// reference or empty for the global selection.
func (o Options) snapshot(c *cobra.Command, scope string, refresh bool) (service.AccountsSnapshot, error) {
	var snap service.AccountsSnapshot
	if err := o.Client.Call(c.Context(), "AccountsList", []any{scope, refresh}, &snap); err != nil {
		return snap, err
	}
	if snap.Message != "" {
		fmt.Fprintln(c.ErrOrStderr(), snap.Message)
	}
	return snap, nil
}

func (o Options) listCommand() *cobra.Command {
	var provider, room string
	var refresh, asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the saved accounts by provider",
		Long: "List the saved accounts grouped by provider, with the sign-in state, the plan and the usage windows. " +
			"The default reads the saved usage readings. --refresh asks each provider for the current usage of every account, " +
			"the active one and the inactive ones. " +
			"--room marks the account that room uses; its choice overrides the global one for that room's turns.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return o.writeAccountsList(c, provider, room, refresh, asJSON)
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "only this provider, for example anthropic")
	cmd.Flags().StringVar(&room, "room", "", "mark the account this room uses")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "ask the providers for the current usage of every account")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// refreshCommand is the short form of list --refresh, because a forced refresh
// is what an owner reaches for when a reading looks stuck (D-148).
func (o Options) refreshCommand() *cobra.Command {
	var provider, room string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Ask every provider for current usage, then list the accounts",
		Long: "Ask each provider for the current usage of every saved account, active or inactive, then list them. " +
			"This is the same as dyna accounts list --refresh.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return o.writeAccountsList(c, provider, room, true, asJSON)
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "only this provider, for example anthropic")
	cmd.Flags().StringVar(&room, "room", "", "mark the account this room uses")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// writeAccountsList reads the account view and prints it.
func (o Options) writeAccountsList(c *cobra.Command, provider, room string, refresh, asJSON bool) error {
	snap, err := o.snapshot(c, room, refresh)
	if err != nil {
		return err
	}
	writeProblems(c, snap)
	rows := filter(snap, provider)
	if asJSON {
		return json.NewEncoder(c.OutOrStdout()).Encode(rows)
	}
	writeAccounts(c.OutOrStdout(), rows, room)
	return nil
}

func (o Options) useCommand() *cobra.Command {
	var room string
	var global bool
	cmd := &cobra.Command{
		Use:   "use <account>",
		Short: "Choose the account its provider uses",
		Long: "Choose the active account of the account's provider. The default changes the global choice. " +
			"--room changes only that room: its turns use the chosen account and every other room keeps its own. " +
			"--global states the default and leaves every room choice in place. " +
			"The account is named by ID, an ID prefix, or a label that names one account only.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if global && room != "" {
				return errors.New("Choose either --room or --global, not both.")
			}
			scope := room
			if global {
				scope = ""
			}
			before, err := o.snapshot(c, scope, false)
			if err != nil {
				return err
			}
			id, row, err := resolve(before, args[0])
			if err != nil {
				return err
			}
			var snap service.AccountsSnapshot
			if err := o.Client.Call(c.Context(), "AccountsUse", []any{id, scope}, &snap); err != nil {
				return err
			}
			if scope == "" {
				fmt.Fprintf(c.OutOrStdout(), "%s now runs everywhere it may.\n", accountName(row))
				return nil
			}
			fmt.Fprintf(c.OutOrStdout(), "%s now runs in room %s.\n", accountName(row), room)
			return nil
		},
	}
	cmd.Flags().StringVar(&room, "room", "", "use this account only in this room")
	cmd.Flags().BoolVar(&global, "global", false, "use this account everywhere (the default)")
	cmd.MarkFlagsMutuallyExclusive("room", "global")
	return cmd
}

func (o Options) addCommand() *cobra.Command {
	var label, importHarness, account string
	var apiKeyStdin, dryRun bool
	cmd := &cobra.Command{
		Use:   "add [provider]",
		Short: "Add an account of a provider, or import a saved sign-in",
		Long: "Add an account of one provider. With --api-key-stdin Dyna reads one API key from standard input and saves it as an account of that provider. " +
			"A key is never read from an argument, so it does not stay in the shell history. " +
			"Without it Dyna prints the sign-in step to run, because a provider sign-in is interactive and the owner completes it. " +
			"With --import Dyna moves every saved sign-in of one harness into the Pi account of its provider and retires the source, so Dyna never keeps two saved copies of that sign-in. " +
			"--dry-run lists what would move and writes nothing. Dyna refuses a harness that an enabled profile or an open session still runs.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if importHarness != "" {
				if len(args) != 0 {
					return errors.New("Name the harness with --import, not as an argument.")
				}
				if label != "" || apiKeyStdin {
					return errors.New("--label and --api-key-stdin do not apply to --import.")
				}
				return o.importAccounts(c, importHarness, account, dryRun)
			}
			if account != "" || dryRun {
				return errors.New("--account and --dry-run need --import.")
			}
			if len(args) != 1 {
				return errors.New("Name the provider to add, or give a harness with --import.")
			}
			if _, ok := runtime.PiProvider(args[0]); !ok {
				return fmt.Errorf("Unknown provider %q. Choose a provider Dyna knows, such as anthropic, deepseek or zai.", args[0])
			}
			if !apiKeyStdin {
				fmt.Fprintf(c.OutOrStdout(), "Sign in to %s with `dyna harness login pi`, and choose %s in Pi's login. Dyna saves one account for each provider you sign in to.\n", args[0], args[0])
				return nil
			}
			key, err := readKey(c.InOrStdin())
			if err != nil {
				return err
			}
			var snap service.AccountsSnapshot
			if err := o.Client.Call(c.Context(), "AccountsAdd", []any{"pi", args[0], label, key}, &snap); err != nil {
				return err
			}
			provider := ""
			if p, ok := runtime.PiProvider(args[0]); ok {
				provider = string(p)
			}
			writeAccounts(c.OutOrStdout(), filter(snap, provider), "")
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "the name this account shows as")
	cmd.Flags().BoolVar(&apiKeyStdin, "api-key-stdin", false, "read one API key from standard input")
	cmd.Flags().StringVar(&importHarness, "import", "", "move the saved sign-ins of this harness into Pi")
	cmd.Flags().StringVar(&account, "account", "", "only this saved account, by ID, ID prefix or label")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would move and write nothing")
	return cmd
}

// importAccounts asks the engine to move one harness's saved sign-ins and
// renders its typed answer. A refusal names the enabled profiles and open
// sessions that still run the harness.
func (o Options) importAccounts(c *cobra.Command, harness, account string, dryRun bool) error {
	var result importer.Result
	if err := o.Client.Call(c.Context(), "AccountsImport", []any{harness, account, dryRun}, &result); err != nil {
		return err
	}
	return writeImport(c.OutOrStdout(), c.ErrOrStderr(), result)
}

// writeImport prints the moved accounts and the refused ones.
func writeImport(out, errOut io.Writer, result importer.Result) error {
	for _, moved := range result.Moved {
		name := moved.Label
		if name == "" {
			name = moved.Source
		}
		if result.DryRun {
			fmt.Fprintf(out, "Would move %s to a Pi %s account (%s).\n", name, moved.Provider, moved.Kind)
			continue
		}
		fmt.Fprintf(out, "Moved %s to Pi account %s (%s, %s).\n", name, moved.Account, moved.Provider, moved.Kind)
	}
	for _, refused := range result.Refused {
		fmt.Fprintln(errOut, refused.Describe(result.Harness))
	}
	if len(result.Moved) == 0 {
		return errors.New("Nothing was moved. The saved sign-ins stay where they are.")
	}
	return nil
}

func (o Options) removeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <account>",
		Short: "Remove a saved account",
		Long: "Remove a saved account and delete its private homes. " +
			"The account is named by ID, an ID prefix, or a label that names one account only. " +
			"Dyna refuses while a turn of the account runs.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			before, err := o.snapshot(c, "", false)
			if err != nil {
				return err
			}
			id, row, err := resolve(before, args[0])
			if err != nil {
				return err
			}
			var snap service.AccountsSnapshot
			if err := o.Client.Call(c.Context(), "AccountsRemove", []any{id}, &snap); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Removed %s.\n", accountName(row))
			return nil
		},
	}
}

// readKey reads one API key line from in. The key is never echoed or logged.
func readKey(in io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(in, 8<<10)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("The API key could not be read from standard input.")
	}
	key := strings.TrimRight(line, "\r\n")
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", errors.New("Enter the API key on one line.")
	}
	return key, nil
}

// filter selects the groups of one provider. An empty provider keeps every
// group.
func filter(snap service.AccountsSnapshot, provider string) service.AccountsSnapshot {
	if provider == "" {
		return snap
	}
	out := snap
	out.Providers = []service.ProviderAccounts{}
	for _, group := range snap.Providers {
		if strings.EqualFold(string(group.Provider), provider) {
			out.Providers = append(out.Providers, group)
		}
	}
	return out
}

// resolve returns the ID of the account a reference names and its row. The
// reference is an exact ID, an ID prefix, or a label that names one account.
func resolve(snap service.AccountsSnapshot, ref string) (string, service.ScopeAccount, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", service.ScopeAccount{}, errors.New("Name an account by ID, ID prefix or label.")
	}
	var prefix, labels []service.ScopeAccount
	for _, group := range snap.Providers {
		for _, a := range group.Accounts {
			if strings.EqualFold(a.ID, ref) {
				return a.ID, a, nil
			}
			if strings.HasPrefix(a.ID, ref) {
				prefix = append(prefix, a)
			}
			if strings.EqualFold(a.Label, ref) {
				labels = append(labels, a)
			}
		}
	}
	switch {
	case len(prefix) == 1:
		return prefix[0].ID, prefix[0], nil
	case len(prefix) > 1:
		return "", service.ScopeAccount{}, fmt.Errorf("%s starts %d account IDs. Give more characters of the ID.", ref, len(prefix))
	case len(labels) == 1:
		return labels[0].ID, labels[0], nil
	case len(labels) > 1:
		return "", service.ScopeAccount{}, fmt.Errorf("%d accounts are called %q. Use an ID prefix.", len(labels), ref)
	}
	return "", service.ScopeAccount{}, fmt.Errorf("No saved account matches %q.", ref)
}

// accountName names an account in a result line.
func accountName(a service.ScopeAccount) string {
	if a.Label != "" {
		return a.Label + " (" + string(a.Provider) + ")"
	}
	return a.ID
}

// writeAccounts prints the groups as a text table.
func writeAccounts(out io.Writer, snap service.AccountsSnapshot, room string) {
	if len(snap.Providers) == 0 {
		fmt.Fprintln(out, "No saved accounts.")
		return
	}
	for _, group := range snap.Providers {
		provider := string(group.Provider)
		if provider == "" {
			provider = "no provider"
		}
		fmt.Fprintln(out, provider)
		for _, a := range group.Accounts {
			mark := " "
			if a.ActiveHere {
				mark = "*"
			}
			fmt.Fprintf(out, "%s %s  %s  %s  %s%s%s\n", mark, label(a.Account), kind(a.Account), a.Status, usage(a.Account), reading(a.Account), elsewhere(a, room))
		}
	}
}

// elsewhere marks the global account when the shown scope uses another one.
func elsewhere(a service.ScopeAccount, room string) string {
	if room != "" && a.Active && !a.ActiveHere {
		return "  (global)"
	}
	return ""
}

// reading shows how old the saved usage reading is and marks a stale one, so an
// old reading is never shown as current.
func reading(a service.Account) string {
	out := ""
	if !a.ObservedAt.IsZero() {
		out = fmt.Sprintf("  (observed %s ago)", max(time.Duration(0), time.Since(a.ObservedAt)).Truncate(time.Second))
	}
	if a.Stale {
		out += "  (stale)"
	}
	return out
}

func label(a service.Account) string {
	if a.Label != "" {
		return a.Label
	}
	return a.ID
}

func kind(a service.Account) string {
	switch a.Kind {
	case store.KindAPIKey:
		return "api key"
	case store.KindOAuth:
		return "sign-in"
	}
	return "unknown kind"
}

// usage is the account's usage summary, or that its usage is unavailable and
// why. A provider that publishes no quota, such as one signed in with an API
// key, gets the second form.
func usage(a service.Account) string {
	if len(a.Windows) == 0 {
		reason := a.UsageError
		if reason == "" {
			reason = "This provider does not report usage for this account."
		}
		return "usage unavailable: " + strings.TrimSuffix(reason, ".")
	}
	return service.WindowsSummary(a)
}

// writeProblems names each saved row the list skipped, so a bad row is visible
// without failing the list.
func writeProblems(c *cobra.Command, snap service.AccountsSnapshot) {
	if len(snap.Problems) == 0 {
		return
	}
	sort.SliceStable(snap.Problems, func(i, j int) bool { return snap.Problems[i].ID < snap.Problems[j].ID })
	for _, p := range snap.Problems {
		name := p.Label
		if name == "" {
			name = p.ID
		}
		fmt.Fprintf(c.ErrOrStderr(), "Skipped saved row %s: %s.\n", name, problemReason(p.Reason))
	}
}

func problemReason(reason runtime.AccountProblemReason) string {
	switch reason {
	case runtime.AccountUnknownHarness:
		return "its harness is not part of this release"
	case runtime.AccountUnsafeHome:
		return "its home path is unsafe or gone"
	case runtime.AccountMissingHome:
		return "it names no saved home"
	}
	return "it could not be read"
}
