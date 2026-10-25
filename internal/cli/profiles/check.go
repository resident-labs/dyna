package profiles

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/harness"
	harnesshealth "dyna-agent/internal/harness/health"
	"dyna-agent/internal/harness/path"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/profilecheck"
	"dyna-agent/internal/workeroutput"
)

const probeReply = profile.HarnessProbeReply

func newCheckCommand() *cobra.Command {
	var live bool
	cmd := &cobra.Command{
		Use:   "check [name...]",
		Short: "Check enabled profiles, installed binaries, output capabilities, and accounts",
		Long:  "Check enabled profiles, including subagent-only profiles, and report each managed profile's private sign-in state. --live starts each harness and can incur provider charges.",
		RunE: func(c *cobra.Command, names []string) error {
			store, err := loadStore()
			if err != nil {
				return err
			}
			selected, err := selectCheckProfiles(store, names)
			if err != nil {
				return err
			}
			if len(selected) == 0 {
				fmt.Fprintln(c.OutOrStdout(), "No enabled profiles to check.")
				return nil
			}
			failed := 0
			docs := accountDoctors(c.Context(), selected)
			for _, p := range selected {
				problem := false
				dialect := p.EffectiveDialect()
				caps := workeroutput.Capabilities(dialect)
				binary, binaryErr := checkBinary(p)
				fmt.Fprintf(c.OutOrStdout(), "%s\n  harness: %s  effective dialect: %s\n", p.Name, p.Harness, orDash(dialect))
				if binaryErr != nil {
					fmt.Fprintf(c.OutOrStdout(), "  binary: missing (%v)\n", binaryErr)
					problem = true
				} else {
					fmt.Fprintf(c.OutOrStdout(), "  binary: installed (%s)\n", binary)
				}
				if doc, managed := docs[p.Name]; managed && !printAccount(c.OutOrStdout(), doc) {
					problem = true
				}
				fmt.Fprintf(c.OutOrStdout(), "  Capabilities: transcript=%t tool-events=%t safe-points=%t usage=%s session-resume=%t\n", caps.Transcript, caps.ToolEvents, caps.SafePoints, caps.Usage, caps.SessionResume)
				if caps.UsageReason != "" {
					fmt.Fprintf(c.OutOrStdout(), "  usage: %s\n", caps.UsageReason)
				}
				if binaryErr == nil {
					version := harnesshealth.VersionDetector(c.Context(), p)
					health := harnesshealth.New("").View(p, version.Version)
					fmt.Fprintf(c.OutOrStdout(), "  version: %s  observed health: %s (%s)\n", orDash(version.Version), health.Status, health.Reason)
					if live {
						fmt.Fprintln(c.OutOrStdout(), "  live: running (timeout: 2 minutes)")
						if err := runProfileProbe(c.Context(), p); err != nil {
							problem = true
							fmt.Fprintf(c.OutOrStdout(), "  live: FAIL (%v)\n", err)
						} else {
							fmt.Fprintln(c.OutOrStdout(), "  live: PASS (text, supported tool events, and supported usage confirmed)")
						}
					}
				}
				if problem {
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d profile check(s) failed", failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&live, "live", false, "run a tiny shell command and reply through each harness (owner or root only; may incur charges)")
	return cmd
}

func selectCheckProfiles(store *profile.Store, names []string) ([]profile.Profile, error) {
	if len(names) == 0 {
		var selected []profile.Profile
		for _, p := range store.Profiles {
			if !p.Disabled {
				selected = append(selected, p)
			}
		}
		return selected, nil
	}
	selected := make([]profile.Profile, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		p, ok := store.Get(name)
		if !ok {
			return nil, fmt.Errorf("no profile named %q", name)
		}
		if p.Disabled {
			return nil, fmt.Errorf("profile %q is disabled; enable it before checking", name)
		}
		if !seen[name] {
			selected = append(selected, *p)
			seen[name] = true
		}
	}
	return selected, nil
}

// accountDoctors reports the private sign-in state each managed profile needs
// before its first turn. The mock harness runs no executable, so Dyna checks
// no account for it.
func accountDoctors(ctx context.Context, selected []profile.Profile) map[string]harnessruntime.Doctor {
	managed := []profile.Profile{}
	for _, p := range selected {
		if p.DynaManaged() {
			managed = append(managed, p)
		}
	}
	docs := make(map[string]harnessruntime.Doctor, len(managed))
	if len(managed) == 0 {
		return docs
	}
	for _, doc := range harnessruntime.New().Doctors(ctx, managed) {
		docs[doc.Profile] = doc
	}
	return docs
}

// printAccount writes the account state a managed profile starts with. It
// returns false when a turn cannot start, and names the command that fixes it.
func printAccount(w io.Writer, doc harnessruntime.Doctor) bool {
	fmt.Fprintf(w, "  account: %s", doc.Status)
	if doc.Reason != "" {
		fmt.Fprintf(w, " (%s)", doc.Reason)
	}
	fmt.Fprintln(w)
	switch doc.Status {
	case "blocked", "not signed in":
		fmt.Fprintf(w, "  next: dyna harness login %s\n", doc.Harness)
		return false
	}
	return true
}

func checkBinary(p profile.Profile) (string, error) {
	if p.Harness == profile.HarnessMock {
		return "built-in mock", nil
	}
	definition, ok := profile.HarnessDefinitionFor(p.Harness)
	if !ok {
		return "", fmt.Errorf("unknown harness %q", p.Harness)
	}
	binary := definition.Binary
	env := os.Environ()
	for key, value := range p.Env {
		env = append(env, key+"="+value)
	}
	return path.Resolve(binary, env)
}

func runProfileProbe(ctx context.Context, p profile.Profile) error {
	var result configsvc.ProfileCheck
	if err := engineclient.New("").Call(ctx, "ConfigCheckProfile", []any{p.Name, true}, &result); err != nil {
		return fmt.Errorf("profile %q was not checked: %w", p.Name, err)
	}
	if result.ProbeError != "" {
		return fmt.Errorf("profile %q probe failed: %s", p.Name, result.ProbeError)
	}
	return nil
}

func validateProbe(dialect string, result harness.Result, data []byte) error {
	return profilecheck.Validate(dialect, result, data)
}
