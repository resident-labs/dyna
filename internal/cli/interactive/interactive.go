package interactive

import (
	"embed"
	"fmt"
	"io"
	"os"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/computers"
	"dyna-agent/internal/guidance"
	"dyna-agent/internal/mode"

	"github.com/charmbracelet/glamour"
	"github.com/spf13/cobra"
)

//go:embed guide/*.md
var guideFiles embed.FS

// harnessFiles holds one watcher recipe per harness flag of
// external-orchestration, plus common.md, which every recipe ends with.
//
//go:embed harness/*.md
var harnessFiles embed.FS

var guideHarnesses = []string{"claude", "codex", "muse", "other"}

// guideIndex lists the topics. Root, which has no session stamp, sees the
// root block first; the internal root (root mode) sees its own setup there. A
// Dyna session already has its room, gets posts delivered and gets rules
// injected, so it never sees the root block or the external-orchestration
// topic, and neither does the internal root.
func guideIndex() string {
	_, stamped := mode.CurrentSession()
	internal := mode.Current() == mode.Root
	var out strings.Builder
	if !stamped {
		out.WriteString(guidance.RootBlock(internal))
	}
	for _, topic := range guidance.Topics {
		if topic.Name != guidance.RootTopic || !stamped && !internal {
			fmt.Fprintf(&out, "%s: %s\n", topic.Name, topic.Trigger)
		}
	}
	out.WriteString("\n" + authority.Guide())
	return out.String()
}

func guideDocument(name string) (string, error) {
	for _, topic := range guidance.Topics {
		if topic.Name == name {
			doc, err := guideFiles.ReadFile("guide/" + name + ".md")
			if err != nil {
				return "", err
			}
			if name == "computers" {
				return string(doc) + "\n```text\n" + computers.ActionHelp() + "\n```\n", nil
			}
			return string(doc), nil
		}
	}
	var names []string
	for _, topic := range guidance.Topics {
		names = append(names, topic.Name)
	}
	return "", fmt.Errorf("unknown guide topic %q (topics: %s)", name, strings.Join(names, ", "))
}

// harnessDocument is one harness's watcher recipe followed by the shared
// rules and history steps.
func harnessDocument(harness string) string {
	recipe, _ := harnessFiles.ReadFile("harness/" + harness + ".md")
	common, _ := harnessFiles.ReadFile("harness/common.md")
	return string(recipe) + string(common)
}

func NewGuideCommand() *cobra.Command {
	var plain bool
	harness := map[string]*bool{}
	cmd := &cobra.Command{
		Use:   "guide [topic]",
		Short: "List guide topics, or read one when its trigger applies",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			doc := guideIndex()
			if len(args) == 0 {
				_, err := fmt.Fprint(c.OutOrStdout(), doc)
				return err
			}
			var chosen []string
			for _, name := range guideHarnesses {
				if *harness[name] {
					chosen = append(chosen, name)
				}
			}
			if len(chosen) > 0 && args[0] != guidance.RootTopic {
				return fmt.Errorf("--%s applies only to %s", chosen[0], guidance.RootTopic)
			}
			if len(chosen) > 1 {
				return fmt.Errorf("choose one harness flag, not %s", strings.Join(chosen, " and "))
			}
			var err error
			doc, err = guideDocument(args[0])
			if err != nil {
				return err
			}
			if len(chosen) == 1 {
				doc = harnessDocument(chosen[0])
			}
			if plain || !isTTY(c.OutOrStdout()) {
				fmt.Fprint(c.OutOrStdout(), doc)
				return nil
			}
			out, err := glamour.Render(doc, "auto")
			if err != nil {
				fmt.Fprint(c.OutOrStdout(), doc)
				return nil
			}
			fmt.Fprint(c.OutOrStdout(), out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&plain, "plain", false, "raw markdown (default when piped)")
	for _, name := range guideHarnesses {
		harness[name] = cmd.Flags().Bool(name, false, "external-orchestration: the watcher recipe for "+name)
	}
	if mode.InWorkflow() {
		// Refuse every form, including help and invalid flags, before parsing.
		cmd.DisableFlagParsing = true
		cmd.Args = nil
		cmd.SilenceUsage = true
		cmd.RunE = func(*cobra.Command, []string) error {
			return fmt.Errorf("dyna guide is not available inside a workflow worker; complete the task yourself")
		}
	}
	return cmd
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
