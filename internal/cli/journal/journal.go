package journal

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/goal"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/runstore"
)

// Goals is how a persistent session reaches its goal journal.
type Goals struct {
	// InSession reports whether the caller is a persistent session.
	InSession func() bool
	// OpenHeld lists the IDs of the open goals the session holds.
	OpenHeld func() ([]string, error)
	// Note appends text to one goal's journal.
	Note func(ctx context.Context, goal, text string) error
}

func defaultGoals() Goals {
	return Goals{
		InSession: func() bool { _, ok := mode.CurrentSession(); return ok },
		OpenHeld:  func() ([]string, error) { return goal.OpenHeldGoals(goal.Options{}) },
		Note: func(ctx context.Context, id, text string) error {
			_, err := goal.Note(ctx, goal.Options{}, "", id, text)
			return err
		},
	}
}

func NewCommand() *cobra.Command { return Command(defaultGoals()) }

func Command(g Goals) *cobra.Command {
	var kind, next string
	cmd := &cobra.Command{
		Use:   "journal <message>",
		Short: "Append a progress entry to this worker's journal, or to your goal's journal",
		Long: `Append a progress entry to this worker's journal.

A workflow worker writes to its own journal. A persistent session has no worker
journal. If it holds exactly one open goal, the entry goes to that goal's journal,
like "dyna goal note <goal> <text>"; the kind and next step become part of the
note text. With no open goal or with several, use "dyna goal note <goal> "<text>"".`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			entry := runstore.AgentJournalEntry{Kind: kind, Message: args[0], Next: next}
			if strings.TrimSpace(os.Getenv(runstore.AgentJournalEnv)) != "" || !g.InSession() {
				return runstore.AppendAgentJournalEntryFromEnv(entry)
			}
			ids, err := g.OpenHeld()
			if err != nil {
				return err
			}
			if len(ids) != 1 {
				return fmt.Errorf("dyna journal needs exactly one open goal, and you hold %d; use dyna goal note <goal> \"<text>\"", len(ids))
			}
			return g.Note(c.Context(), ids[0], goalNoteText(entry))
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "update", "entry kind (for example: update, finding, decision, verification, blocker)")
	cmd.Flags().StringVar(&next, "next", "", "concise next step")
	return cmd
}

// goalNoteText puts the entry kind and next step into plain note text, because
// a goal journal keeps only text.
func goalNoteText(e runstore.AgentJournalEntry) string {
	text := strings.TrimSpace(e.Message)
	if e.Kind != "" && e.Kind != "update" {
		text = e.Kind + ": " + text
	}
	if next := strings.TrimSpace(e.Next); next != "" {
		text += "\nNext: " + next
	}
	return text
}
