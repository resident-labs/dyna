package main

import (
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

// profileCommand is dyna profile. The profiles are the owner's: a session's call is refused.
func profileCommand(getClient func() client) *cobra.Command {
	profile := &cobra.Command{Use: "profile", Short: "Model profiles: list, show, set and remove (owner only)"}
	stored := func(cmd *cobra.Command, name string) (contract.ProfileEntry, error) {
		result, err := getClient().ProfileList(cmd.Context(), contract.ProfileListRequest{})
		if err != nil {
			return contract.ProfileEntry{}, err
		}
		for _, entry := range result.Profiles {
			if string(entry.Profile.Name) == name {
				return entry, nil
			}
		}
		return contract.ProfileEntry{}, fmt.Errorf("no profile %q", name)
	}
	list := &cobra.Command{Use: "list", Short: "List profiles, one line each", Long: "List profiles, one line each: name, provider and model, thinking level, images on or off, and how many open sessions use it.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := getClient().ProfileList(cmd.Context(), contract.ProfileListRequest{})
		if err != nil {
			return err
		}
		for _, e := range result.Profiles {
			p := e.Profile
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s %s\t%s\timages %s\t%d in use\n", p.Name, p.Provider, p.Model, p.Thinking, onOff(p.Images == nil || *p.Images), e.InUse)
		}
		return nil
	}}
	show := &cobra.Command{Use: "show NAME", Short: "Print every stored field of a profile", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		e, err := stored(cmd, args[0])
		if err != nil {
			return err
		}
		p := e.Profile
		fmt.Fprintf(cmd.OutOrStdout(), "name: %s\ndescription: %s\nprovider: %s\nmodel: %s\nallowed-models: %s\nthinking: %s\nimages: %s\ntaste: %d\ndiagnostics: %d\nintelligence: %d\ncost: %d\ncompact-tokens: %d\nmax-concurrent: %d\nin-use: %d\n",
			p.Name, p.Description, p.Provider, p.Model, strings.Join(p.AllowedModels, ","), p.Thinking, onOff(p.Images == nil || *p.Images), p.Taste, p.Diagnostics, p.Intelligence, p.Cost, p.CompactTokens, p.MaxConcurrent, e.InUse)
		return nil
	}}
	var (
		description, provider, model, thinking, images string
		allowed                                        []string
		taste, diagnostics, intelligence, cost         int
		compactTokens, maxConcurrent                   int
		create                                         bool
	)
	set := &cobra.Command{Use: "set NAME [--FIELD VALUE]...", Short: "Change a profile's fields; a field left out stays", Long: "Change a profile. It reads the stored profile, changes only the fields whose flags are given and writes the whole profile back, so a one-flag change drops nothing. Sessions on the profile move to the new model at their next turn.\n\n" +
		"--create makes a new profile instead; the fields not given start empty, and the engine refuses one that is not valid (a provider, a model among the allowed models and a thinking level are needed). --thinking is off, minimal, low, medium, high or xhigh. --images on|off says whether the models take images; --allowed-models lists the models a session may call, and --model must be one of them. --taste, --diagnostics, --intelligence and --cost are ratings 0..10, and 0 is unrated. --max-concurrent limits crew workers on the profile, 0 for no limit; --compact-tokens is the compaction trigger, 0 for Pi's default.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var p contract.Profile
		if create {
			p = contract.Profile{Name: contract.ProfileName(args[0])}
		} else {
			e, err := stored(cmd, args[0])
			if err != nil {
				return err
			}
			p = e.Profile
		}
		flags := cmd.Flags()
		changed := flags.NFlag()
		if create {
			changed--
		}
		if flags.Changed("description") {
			p.Description = description
		}
		if flags.Changed("provider") {
			p.Provider = contract.ModelProvider(provider)
		}
		if flags.Changed("model") {
			p.Model = model
		}
		if flags.Changed("allowed-models") {
			p.AllowedModels = allowed
		}
		if flags.Changed("thinking") {
			p.Thinking = contract.ThinkingLevel(thinking)
		}
		if flags.Changed("images") {
			on, err := hostSwitch("images", images)
			if err != nil {
				return err
			}
			p.Images = on
		}
		if flags.Changed("taste") {
			p.Taste = taste
		}
		if flags.Changed("diagnostics") {
			p.Diagnostics = diagnostics
		}
		if flags.Changed("intelligence") {
			p.Intelligence = intelligence
		}
		if flags.Changed("cost") {
			p.Cost = cost
		}
		if flags.Changed("compact-tokens") {
			p.CompactTokens = compactTokens
		}
		if flags.Changed("max-concurrent") {
			p.MaxConcurrent = maxConcurrent
		}
		if changed == 0 && !create {
			return fmt.Errorf("give at least one field to change, such as --thinking")
		}
		_, err := getClient().ProfileSet(cmd.Context(), contract.ProfileSetRequest{Create: create, Profile: p})
		return err
	}}
	set.Flags().BoolVar(&create, "create", false, "make a new profile")
	set.Flags().StringVar(&description, "description", "", "what the profile is for")
	set.Flags().StringVar(&provider, "provider", "", "anthropic or openai")
	set.Flags().StringVar(&model, "model", "", "the model it runs")
	set.Flags().StringSliceVar(&allowed, "allowed-models", nil, "every model a session on it may call (MODEL[,MODEL...])")
	set.Flags().StringVar(&thinking, "thinking", "", "off, minimal, low, medium, high or xhigh")
	set.Flags().StringVar(&images, "images", "", "on or off")
	set.Flags().IntVar(&taste, "taste", 0, "rating 0..10")
	set.Flags().IntVar(&diagnostics, "diagnostics", 0, "rating 0..10")
	set.Flags().IntVar(&intelligence, "intelligence", 0, "rating 0..10")
	set.Flags().IntVar(&cost, "cost", 0, "rating 0..10")
	set.Flags().IntVar(&compactTokens, "compact-tokens", 0, "compaction trigger in tokens; 0 is Pi's default")
	set.Flags().IntVar(&maxConcurrent, "max-concurrent", 0, "most crew workers at once; 0 is unlimited")
	var replacement string
	remove := &cobra.Command{Use: "remove NAME [--replacement NAME]", Short: "Remove a profile", Long: "Remove a profile. One that open sessions use is refused, unless --replacement names another profile, which takes those sessions first.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().ProfileRemove(cmd.Context(), contract.ProfileRemoveRequest{Name: contract.ProfileName(args[0]), Replacement: contract.ProfileName(replacement)})
		return err
	}}
	remove.Flags().StringVar(&replacement, "replacement", "", "the profile that takes the open sessions")
	profile.AddCommand(list, show, set, remove)
	return profile
}
