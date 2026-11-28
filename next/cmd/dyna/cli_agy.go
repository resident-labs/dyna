package main

import (
	"fmt"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func agyCommand(getClient func() client) *cobra.Command {
	agy := &cobra.Command{Use: "agy"}
	seed := &cobra.Command{Use: "seed HOST", Short: "Copy the signed-in agy account to a host", Long: "Copy the signed-in agy account folder, and the program if the host has none, to HOST from the host the owner signed in on, so agy sessions can run there. The files travel as they are; Dyna never reads or logs them. A host that already has an account folder is refused, and a folder is never replaced in place: to re-seed, close its agy sessions and every agy process using the old folder, remove the folder on that host, and seed again. Refused for the source, an unknown host, an exec-only host and one with agents off. The copy leaves out what agy writes per session (conversations, brain, logs, annotations, and the conversation index) and the account is about 1.5 MB, but the first seed also sends the program (274 MB), so it can take minutes on a slow link: the command waits and has no timeout of its own, so do not wrap it in a short one. If a running agy session changes a file while it is packed, the seed fails with a retry message and the host is left unchanged. The host list marks a host as agy once its next probe finds both.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().HostAgySeed(cmd.Context(), contract.HostAgySeedRequest{Host: contract.HostName(args[0])})
		return err
	}}
	use := &cobra.Command{Use: "use ACCOUNT", Short: "Make an Antigravity account the one every agy session uses", Long: "Switch the Antigravity account that every agy session uses to ACCOUNT, an added account by its label or id. Only the owner does this, and only by this command or the Settings button: it is never offered in a quota notice and never happens by itself. It refuses before changing anything when the account is unknown, a seed or an import is running, no account is active yet (run `dyna agy import` first), or any agy host does not answer. Then no agy session takes a turn until it ends: it waits up to 5 minutes for running agy turns to end (if any remain it releases and says how many), swaps the sign-in host by host, hermeslab first, and ends every agy process on each host, so work agy left running there is ended and the next turn starts a new process on the same conversation. It prints one line per host. If a host fails the switch is left unfinished and agy sessions stay held; run the same command again (a host that already has the account's token is skipped). Nothing else is accepted while it is held and there is no cancel: finish it, then switch back if you want. A host where an agy process cannot be ended fails. It can take minutes, and the command waits with no timeout of its own, so do not wrap it in a short one. The sign-in is moved as a file and never read or logged.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		out, err := getClient().AgyUse(cmd.Context(), contract.AgyUseRequest{Account: args[0]})
		for _, h := range out.Hosts {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", h.Host, h.Result)
		}
		return err
	}}
	var file string
	var active bool
	import_ := &cobra.Command{Use: "import LABEL", Short: "Take an existing Antigravity sign-in as an account", Long: "Take an Antigravity sign-in made outside Dyna as an account with the label you choose. --file NAME is required: a token file, or a sign-in folder such as accounts/second (its antigravity-acp/antigravity-oauth-token is taken), in the engine host's agy accounts folder (a plain name there, not a path and not main; no link on the way; boxes do not mount that folder). It is copied whole and never read, parsed or logged, and Dyna never reads main. --active records the account as the one signed in now, on your word, without checking; it is allowed only while no account is active. Only the owner, and only on the engine host.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		out, err := getClient().AgyImport(cmd.Context(), contract.AgyImportRequest{Label: args[0], File: file, Active: active})
		if err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", out.Account)
		}
		return err
	}}
	import_.Flags().StringVar(&file, "file", "", "a token file or a sign-in folder (such as second) in the agy accounts folder (required)")
	import_.Flags().BoolVar(&active, "active", false, "record it as the account signed in now, on your word (only while none is active)")
	_ = import_.MarkFlagRequired("file")
	agy.AddCommand(seed, use, import_)
	return agy
}
