package pr

import (
	"context"
	"encoding/json"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/pr/watch"
	"github.com/spf13/cobra"
)

type mergeController interface {
	MergeControl(context.Context, string, string, []string, []string) error
	MergeControlStatus(string) (watch.MergeControlStatus, error)
}

// NewMergeCommand exposes root and steward controls for automatic merging.
func NewMergeCommand() *cobra.Command { return MergeCommand(Options{}) }

// MergeCommand uses the same service and room resolution as PR merge settings.
func MergeCommand(o Options) *cobra.Command {
	if o.Service == nil {
		o.Service = watch.New
	}
	return mergeCommand(func(ref string) (mergeController, string, error) {
		s := o.Service()
		id, err := mergeRoomFor(s, ref)
		return engineMergeController{s}, id, err
	})
}

// engineMergeController reads the engine queue, including its fairness counters.
type engineMergeController struct{ *watch.Service }

func (engineMergeController) MergeControlStatus(roomID string) (watch.MergeControlStatus, error) {
	var result watch.MergeControlStatus
	err := engineclient.New("").Call(context.Background(), "MergeControlStatus", []any{roomID}, &result)
	return result, err
}

func (engineMergeController) MergeControl(ctx context.Context, roomID, action string, args, without []string) error {
	return engineclient.New("").Call(ctx, "MergeControl", []any{roomID, action, args, without}, nil)
}

func mergeRoomFor(s *watch.Service, ref string) (string, error) {
	rec, err := s.Notify.Rooms.ResolveContext(ref, false)
	return rec.ID, err
}

func mergeCommand(resolve func(string) (mergeController, string, error)) *cobra.Command {
	root := &cobra.Command{Use: "merge", Short: "Inspect automatic merging and control failed batches", Args: cobra.NoArgs}
	room := root.PersistentFlags().String("room", "", "room name or ID (default: current stamped room)")
	status := &cobra.Command{Use: "status", Short: "Show the queue, current failed batch and pause state as JSON", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, id, err := resolve(*room)
		if err != nil {
			return err
		}
		result, err := s.MergeControlStatus(id)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(c.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}}
	root.AddCommand(status)
	for _, spec := range []struct {
		use, short string
		count      int
	}{
		{"pause", "Pause automatic merging", 0},
		{"resume", "Resume automatic merging", 0},
		{"cancel", "Cancel the current batch", 0},
		{"return <pr> <reason>", "Return a PR to its author with a reason", 2},
		{"retry", "Retry the failed batch, optionally excluding PRs", 0},
		{"bisect", "Start prefix trials for the failed batch on demand", 0},
		{"publish <trial>", "Publish a successful trial", 1},
		{"note <line>", "Record a quiet merge decision for root", 1},
	} {
		cmd := &cobra.Command{Use: spec.use, Short: spec.short, Args: cobra.ExactArgs(spec.count)}
		var without []string
		if cmd.Name() == "retry" {
			cmd.Flags().StringArrayVar(&without, "without", nil, "PR to exclude from the retry (repeatable)")
		}
		cmd.RunE = func(c *cobra.Command, args []string) error {
			s, id, err := resolve(*room)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				args = nil
			}
			return s.MergeControl(c.Context(), id, c.Name(), args, without)
		}
		root.AddCommand(cmd)
	}
	return root
}
