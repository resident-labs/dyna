package profiles

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
)

// engineClient lets failure tests use a private socket without a restart wait.
var engineClient = func() *engineclient.Client { return engineclient.New("") }

// loadRegistry is the one management read: every saved profile with its typed
// status, so a profile this release cannot run still lists and can be repaired.
func loadRegistry() (profile.Registry, error) {
	return profile.ReadConfig(profile.DefaultPath())
}

// ---------- profiles ----------

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profiles",
		Short: "Manage worker profiles (registered models + stats)",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}

	var asJSON, all, stats bool
	var statsDays int
	list := &cobra.Command{
		Use:   "list",
		Short: "List registered worker profiles with descriptions and stats",
		RunE: func(c *cobra.Command, _ []string) error {
			if c.Flags().Changed("days") && !stats {
				return fmt.Errorf("--days requires --stats")
			}
			if stats && (statsDays < 1 || statsDays > 365) {
				return fmt.Errorf("--days must be between 1 and 365")
			}
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			s := reg.Store
			visible := s.Profiles
			if !all {
				visible = s.EnabledProfiles()
			}
			measured := subagent.ProfileUsageSnapshot{Profiles: []subagent.ProfileUsageStats{}}
			if stats {
				now := time.Now().UTC()
				today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
				from := today.AddDate(0, 0, 1-statsDays)
				if err := engineClient().Call(c.Context(), "ProfileUsage", []any{from.Format(time.RFC3339), now.Format(time.RFC3339)}, &measured); err != nil {
					return err
				}
			}
			byName := map[string]subagent.ProfileUsageStats{}
			for _, row := range measured.Profiles {
				byName[row.Profile] = row
			}
			if asJSON {
				rows := make([]profileListRow, 0, len(visible))
				for _, p := range visible {
					row := newProfileListRow(p)
					row.Problem = reg.Reports[p.Name].Status
					if stats {
						value := byName[p.Name]
						value.Profile = p.Name
						row.Stats = &value
					}
					rows = append(rows, row)
				}
				if stats {
					b, _ := json.MarshalIndent(profileListSnapshot{From: measured.From, To: measured.To, Profiles: rows}, "", "  ")
					fmt.Fprintln(c.OutOrStdout(), string(b))
				} else {
					b, _ := json.MarshalIndent(rows, "", "  ")
					fmt.Fprintln(c.OutOrStdout(), string(b))
				}
				return nil
			}
			if len(visible) == 0 && len(s.Profiles) > 0 {
				fmt.Fprintln(c.OutOrStdout(), "No enabled profiles. Use dyna profiles list --all to include disabled profiles.")
				return nil
			}
			if len(visible) == 0 {
				fmt.Fprintln(c.OutOrStdout(), "No profiles registered. Add one:\n  dyna profiles add --name opus-4.8 --harness claude-code --model opus --taste 5 --intelligence 4 --cost 2 --desc \"...\"")
				return nil
			}
			if stats {
				fmt.Fprintf(c.OutOrStdout(), "Measured activity: %s to %s (UTC). Cost per turn uses reported turns; ≥ marks a lower bound.\n\n", measured.From, measured.To)
			}
			for _, p := range visible {
				def := ""
				if p.Default {
					def = "  (default)"
				}
				if p.Disabled {
					def += "  (DISABLED)"
				}
				if report := reg.Reports[p.Name]; report.Status != profile.StatusOK {
					def += "  (NEEDS REPAIR: " + string(report.Status) + ")"
				}
				if len(p.Uses) > 0 {
					def += "  (" + strings.Join(p.Uses, ", ") + " only)"
				}
				limits := ""
				if p.MaxConcurrent > 0 {
					limits += fmt.Sprintf("  max-concurrent: %d", p.MaxConcurrent)
				}
				if p.MaxCallsPerRun > 0 {
					limits += fmt.Sprintf("  max-calls/run: %d", p.MaxCallsPerRun)
				}
				if models := p.EffectiveModels(); len(models) > 1 {
					limits += "  models: " + strings.Join(models, " -> ")
				}
				if p.Fallback != "" {
					limits += "  fallback: " + p.Fallback
				}
				fmt.Fprintf(c.OutOrStdout(), "%s%s\n  harness: %s  model: %s  effective dialect: %s%s\n  taste: %d/10  diagnostics: %d/10  intelligence: %d/10  cost-efficiency: %d/10\n",
					p.Name, def, p.Harness, orDash(p.Model), orDash(p.EffectiveDialect()), limits, p.Taste, p.EffectiveDiagnostics(), p.Intelligence, p.Cost)
				if stats {
					value := byName[p.Name]
					if value.Turns == 0 {
						fmt.Fprintln(c.OutOrStdout(), "  measured: no turns")
					} else {
						cache := "unknown"
						if value.CacheReadShare != nil {
							cache = fmt.Sprintf("%s (%d paired turns)", measuredNumber(*value.CacheReadShare*100, "%"), value.CachePaired.Turns)
						}
						fmt.Fprintf(c.OutOrStdout(), "  measured: %d turns  median turn %s  cache read %s\n", value.Turns, profileDurationText(value.MedianWallMs), cache)
						known := value.Cost.CompleteTurns + value.Cost.PartialTurns
						fmt.Fprintf(c.OutOrStdout(), "  measured cost: %s/reported turn  %s spend  (%d of %d turns report cost)\n",
							profileCostPerTurnText(value.CostPerTurnUSD, value.Cost, 4), profileCostText(value.Cost.USD, value.Cost, 2), known, value.Turns)
					}
				}
				fmt.Fprintf(c.OutOrStdout(), "  %s\n\n", p.Description)
			}
			return nil
		},
	}
	list.Flags().BoolVar(&all, "all", false, "include disabled profiles")
	list.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	list.Flags().BoolVar(&stats, "stats", false, "include measured activity beside authored ratings")
	list.Flags().IntVar(&statsDays, "days", 7, "days of activity to include with --stats (1-365)")

	show := &cobra.Command{
		Use:   "show <name>",
		Short: "Show one profile (JSON)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			p, ok := reg.Store.Get(a[0])
			if !ok {
				return fmt.Errorf("no profile named %q", a[0])
			}
			row := newProfileListRow(*p)
			row.Problem = reg.Reports[a[0]].Status
			b, _ := json.MarshalIndent(row, "", "  ")
			fmt.Fprintln(c.OutOrStdout(), string(b))
			return nil
		},
	}

	var p profile.Profile
	var outputDialect string
	add := &cobra.Command{
		Use:   "add",
		Short: "Register or update a worker profile",
		RunE: func(c *cobra.Command, _ []string) error {
			if c.Flags().Changed("models") {
				p.Model, p.Models = profile.NormalizeModels("", p.Models)
			}
			if strings.TrimSpace(outputDialect) != "" {
				p.Output = &workeroutput.Declaration{Dialect: strings.TrimSpace(outputDialect)}
			}
			if err := engineClient().Call(c.Context(), "UpsertProfile", []any{p}, nil); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "saved profile %q (%s)\n", p.Name, p.Harness)
			return nil
		},
	}
	f := add.Flags()
	f.StringVar(&p.Name, "name", "", "unique profile name (required)")
	f.StringVar(&p.Description, "desc", "", "personality/strengths description shown to agents")
	f.StringVar(&p.Harness, "harness", profile.HarnessClaudeCode, "one of: "+strings.Join(profile.Harnesses, ", "))
	f.StringVar(&p.RuntimeVersion, "runtime-version", "", "exact installed private bundle version (default selected)")
	f.StringVar(&p.Model, "model", "", "model id passed to the harness CLI")
	f.StringSliceVar(&p.Models, "models", nil, "ordered model fallback list (comma-separated or repeatable)")
	f.StringVar(&p.Fallback, "fallback", "", "profile that continues a session when this harness hits a usage limit; set that profile's own fallback for a chain")
	f.StringVar(&outputDialect, "output-dialect", "", "override the harness default stdout dialect: "+strings.Join(workeroutput.Dialects(), ", "))
	f.IntVar(&p.Taste, "taste", 5, "1..10, higher better")
	f.IntVar(&p.Intelligence, "intelligence", 5, "1..10, higher better")
	f.IntVar(&p.Cost, "cost", 5, "cost efficiency 1..10, higher better (10 = very cheap)")
	f.IntVar(&p.Diagnostics, "diagnostics", 5, "finding what is wrong in a given solution 1..10, higher better")
	f.StringArrayVar(&p.ExtraArgs, "extra-arg", nil, "extra CLI arg for the harness (repeatable)")
	f.IntVar(&p.TimeoutSec, "timeout", 0, "per-call timeout in seconds (default: 5 hours; explicit values have a 30-minute minimum)")
	f.BoolVar(&p.Default, "default", false, "make this the default profile")
	f.BoolVar(&p.SafeMode, "safe-mode", false, "keep the harness's own permission prompts/sandbox (default: bypassed)")
	f.StringSliceVar(&p.Uses, "use", nil, useHelp)
	f.IntVar(&p.MaxConcurrent, "limit-concurrent", 0, "max simultaneous workers of this profile per run (0 = unlimited)")
	f.IntVar(&p.MaxCallsPerRun, "limit-calls", 0, "max total calls to this profile per run (0 = unlimited)")
	add.MarkFlagRequired("name")

	rm := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			return engineClient().Call(c.Context(), "RemoveProfile", []any{a[0]}, nil)
		},
	}

	toggle := func(use, short string, disabled bool) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <name>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, a []string) error {
				if err := engineClient().Call(c.Context(), "SetProfileDisabled", []any{a[0], disabled}, nil); err != nil {
					return err
				}
				fmt.Fprintln(c.OutOrStdout(), use+"d "+a[0])
				return nil
			},
		}
	}
	enable := toggle("enable", "Re-enable a disabled profile", false)
	disable := toggle("disable", "Disable a profile (stats/description kept, workflows can't use it)", true)

	var uses []string
	set := &cobra.Command{
		Use:   "set <name> --use workflows,agents,services",
		Short: "Change who may use a profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			var effective []string
			if err := engineClient().Call(c.Context(), "SetProfileUses", []any{a[0], uses}, &effective); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "profile %q uses: %s\n", a[0], strings.Join(effective, ", "))
			return nil
		},
	}
	set.Flags().StringSliceVar(&uses, "use", nil, useHelp)
	set.MarkFlagRequired("use")

	setDefault := &cobra.Command{
		Use:   "set-default <name>",
		Short: "Mark a profile as the default worker",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			return engineClient().Call(c.Context(), "SetDefaultProfile", []any{a[0]}, nil)
		},
	}

	cmd.AddCommand(newCheckCommand(), list, show, add, set, rm, enable, disable, setDefault)
	return cmd
}

// useHelp explains the --use values.
const useHelp = "who may use it, any of: workflows (workflow agent() calls), agents (dyna subagent sessions) and services (Dyna's PR reviewers and librarian); comma-separated, default all three"

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// profileListRow adds the resolved dialect and ratings without changing stored
// values. An unset rating (0) shows its neutral 5.
type profileListRow struct {
	profile.Profile
	Diagnostics      int                         `json:"diagnostics"`
	EffectiveDialect string                      `json:"effectiveDialect"`
	Stats            *subagent.ProfileUsageStats `json:"stats,omitempty"`
	// Problem is the typed reason this profile cannot run; empty means launchable.
	Problem profile.Status `json:"problem,omitempty"`
}

// profileListSnapshot preserves the selected window in machine-readable output.
type profileListSnapshot struct {
	From     string           `json:"from"`
	To       string           `json:"to"`
	Profiles []profileListRow `json:"profiles"`
}

func profileCostText(value *float64, coverage subagent.UsageCost, precision int) string {
	return priceText(value, coverage.PartialTurns > 0 || coverage.UnknownTurns > 0, precision)
}

// profileCostPerTurnText averages reported turns only. Missing reports do not
// make that average a lower bound; partial reports among them do.
func profileCostPerTurnText(value *float64, coverage subagent.UsageCost, precision int) string {
	return priceText(value, coverage.PartialTurns > 0, precision)
}

func priceText(value *float64, lowerBound bool, precision int) string {
	if value == nil {
		return "unknown"
	}
	prefix := ""
	if lowerBound {
		prefix = "≥ "
	}
	return fmt.Sprintf("%s$%.*f", prefix, precision, *value)
}

func measuredNumber(value float64, unit string) string {
	text := strconv.FormatFloat(value, 'f', 1, 64)
	return strings.TrimSuffix(text, ".0") + unit
}

func profileDurationText(wallMs *int64) string {
	if wallMs == nil {
		return "unknown"
	}
	duration := time.Duration(*wallMs) * time.Millisecond
	switch {
	case duration < time.Minute:
		return measuredNumber(duration.Seconds(), "s")
	case duration < time.Hour:
		return measuredNumber(duration.Minutes(), "m")
	default:
		return measuredNumber(duration.Hours(), "h")
	}
}

func newProfileListRow(p profile.Profile) profileListRow {
	return profileListRow{Profile: p, Diagnostics: p.EffectiveDiagnostics(), EffectiveDialect: p.EffectiveDialect()}
}
